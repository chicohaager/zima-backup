// Package system knows what makes a ZimaOS box *this* box and how to put
// it back. Measured on ZimaOS v1.7.1 (PLAN-0.3 §1, §8–§10): the OS itself
// is an A/B pair of read-only squashfs slots identical to the release
// image; everything that was ever changed lives in the /etc overlay
// (/mnt/overlay/upper_etc, ~330 KB), in the casaos and icewhale state
// directories, in the sysext modules and in /DATA/AppData. A system job
// backs up exactly that with restic, plus a manifest and consistent
// copies of the SQLite databases; a system restore plays it back the way
// it was measured to survive a reboot.
package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Paths of the state, as they appear on ZimaOS. casaos, icewhale and
// extensions are bind mounts of hidden directories on the data partition
// (findmnt: /var/lib/casaos → /dev/nvme0n1p8[/.casaos]); the /DATA names
// are used so the module's source rule (/DATA, /media, /mnt) holds.
const (
	OverlayDir    = "/mnt/overlay"
	CasaOSDir     = "/DATA/.casaos"
	IceWhaleDir   = "/DATA/.icewhale"
	ExtensionsDir = "/DATA/.extensions"
	AppDataDir    = "/DATA/AppData"
	DataDir       = "/DATA"

	// ManifestName is the file the job writes into its staging dir; it
	// ends up in the snapshot next to the databases.
	ManifestName = "system-manifest.json"
	// StagingName is the directory below the module's data dir that holds
	// manifest and database copies of the last run.
	StagingName = "system"
)

// ErrNotZimaOS says the state directories are not there.
var ErrNotZimaOS = errors.New("this does not look like a ZimaOS system disk: /mnt/overlay or /DATA/.casaos is missing")

// Layout maps the canonical paths onto a root, so tests can build a fake
// system disk in a temp dir. Root "" is the real machine.
type Layout struct {
	Root string
}

// Path returns p below the layout's root.
func (l Layout) Path(p string) string {
	if l.Root == "" {
		return p
	}
	return filepath.Join(l.Root, p)
}

// Commander runs external commands and returns their combined output.
// The real one is os/exec; tests substitute a table.
type Commander interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunIn(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// Exec is the Commander backed by os/exec.
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (Exec) RunIn(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	return c.CombinedOutput()
}

// Sources lists the directories a system job backs up. With includeData
// the whole data partition goes in (AppData and the hidden state dirs are
// inside it); otherwise the state directories and AppData individually.
// Directories that do not exist are left out; the overlay and casaos
// must exist or this is not a ZimaOS disk.
func Sources(l Layout, includeData bool) ([]string, error) {
	for _, must := range []string{OverlayDir, CasaOSDir} {
		if st, err := os.Stat(l.Path(must)); err != nil || !st.IsDir() {
			return nil, ErrNotZimaOS
		}
	}
	var want []string
	if includeData {
		want = []string{OverlayDir, DataDir}
	} else {
		want = []string{OverlayDir, CasaOSDir, IceWhaleDir, ExtensionsDir, AppDataDir}
	}
	var out []string
	for _, p := range want {
		if st, err := os.Stat(l.Path(p)); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	return out, nil
}

// Databases are the SQLite files of the state, relative to the data
// partition. Measured: all six are opened WAL-mode by running services,
// so a plain file copy can be torn; `sqlite3 .backup` is consistent.
var Databases = []string{
	".casaos/db/user.db",
	".casaos/db/zimaos.db",
	".casaos/db/local-storage.db",
	".casaos/db/message-bus.db",
	".casaos/files.db",
	".icewhale/backup.db",
}

// Manifest describes the box a snapshot was taken from. It carries no
// secrets: versions, partition table, image digests, names.
type Manifest struct {
	Version     int               `json:"manifest_version"`
	TakenAt     time.Time         `json:"taken_at"`
	Hostname    string            `json:"hostname"`
	OSRelease   map[string]string `json:"os_release"`
	RaucStatus  string            `json:"rauc_status"`
	SystemDisk  string            `json:"system_disk"`
	Partitions  string            `json:"partitions"` // sfdisk -d
	Images      []Image           `json:"docker_images"`
	Apps        []string          `json:"apps"`       // directories below casaos/apps
	Extensions  []string          `json:"extensions"` // files below /var/lib/extensions
	Sources     []string          `json:"sources"`
	IncludeData bool              `json:"include_data"`
	Databases   []string          `json:"databases"` // the copies that were taken
}

// Image is one docker image with its digest, enough to pull it again.
type Image struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	ID         string `json:"id"`
}

// Prepare writes the manifest and the database copies into staging and
// returns the manifest. Missing tools degrade to empty fields, never to
// an error: a snapshot without `rauc status` is still a snapshot; a
// database that could not be copied consistently is reported in the
// returned warnings so the run's log says so.
func Prepare(ctx context.Context, l Layout, run Commander, staging string, sources []string, includeData bool) (Manifest, []string, error) {
	var warnings []string
	if err := os.MkdirAll(filepath.Join(staging, "db"), 0o700); err != nil {
		return Manifest{}, nil, err
	}
	m := Manifest{Version: 1, TakenAt: time.Now().UTC(), Sources: sources, IncludeData: includeData}
	if h, err := os.Hostname(); err == nil {
		m.Hostname = h
	}
	m.OSRelease = readOSRelease(l.Path("/etc/os-release"))
	if out, err := run.Run(ctx, "rauc", "status"); err == nil {
		m.RaucStatus = strings.TrimSpace(stripANSI(string(out)))
	} else {
		warnings = append(warnings, "rauc status: "+short(err, out))
	}
	if disk := systemDisk(ctx, l, run); disk != "" {
		m.SystemDisk = disk
		if out, err := run.Run(ctx, "sfdisk", "-d", disk); err == nil {
			m.Partitions = strings.TrimSpace(string(out))
		} else {
			warnings = append(warnings, "sfdisk: "+short(err, out))
		}
	}
	if out, err := run.Run(ctx, "docker", "image", "ls", "--digests", "--format", "{{json .}}"); err == nil {
		m.Images = parseImages(out)
	} else {
		warnings = append(warnings, "docker image ls: "+short(err, out))
	}
	m.Apps = listDir(filepath.Join(l.Path(CasaOSDir), "apps"), true)
	m.Extensions = listDir(l.Path(ExtensionsDir), false)
	for _, rel := range Databases {
		src := filepath.Join(l.Path(DataDir), rel)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(staging, "db", filepath.Base(rel))
		_ = os.Remove(dst)
		if out, err := run.Run(ctx, "sqlite3", src, fmt.Sprintf(".backup '%s'", dst)); err != nil {
			warnings = append(warnings, "database copy "+rel+": "+short(err, out))
			continue
		}
		m.Databases = append(m.Databases, rel)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, warnings, err
	}
	if err := os.WriteFile(filepath.Join(staging, ManifestName), b, 0o600); err != nil {
		return m, warnings, err
	}
	return m, warnings, nil
}

// ReadManifest loads a manifest from a restored snapshot root.
func ReadManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest unreadable: %w", err)
	}
	return m, nil
}

// OSVersion is the VERSION of an os-release map ("v1.7.1"), or "".
func (m Manifest) OSVersion() string { return m.OSRelease["VERSION"] }

func readOSRelease(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"`)
	}
	return out
}

// systemDisk finds the disk that holds the overlay partition: findmnt
// gives /dev/nvme0n1p7, lsblk the parent nvme0n1.
func systemDisk(ctx context.Context, l Layout, run Commander) string {
	out, err := run.Run(ctx, "findmnt", "-no", "SOURCE", l.Path(OverlayDir))
	if err != nil {
		return ""
	}
	part := strings.TrimSpace(string(out))
	if i := strings.Index(part, "["); i > 0 {
		part = part[:i]
	}
	if part == "" {
		return ""
	}
	out, err = run.Run(ctx, "lsblk", "-no", "PKNAME", part)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return ""
	}
	return "/dev/" + strings.TrimSpace(string(out))
}

func parseImages(out []byte) []Image {
	var imgs []Image
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var row struct {
			Repository string `json:"Repository"`
			Tag        string `json:"Tag"`
			Digest     string `json:"Digest"`
			ID         string `json:"ID"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil || row.Repository == "" || row.Repository == "<none>" {
			continue
		}
		imgs = append(imgs, Image{Repository: row.Repository, Tag: row.Tag, Digest: row.Digest, ID: row.ID})
	}
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Repository+imgs[i].Tag < imgs[j].Repository+imgs[j].Tag })
	return imgs
}

func listDir(dir string, dirsOnly bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if dirsOnly && !e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func short(err error, out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return err.Error()
	}
	return err.Error() + ": " + s
}

// stripANSI removes colour codes (rauc status prints them even to a pipe).
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
