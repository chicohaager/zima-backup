package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chicohaager/zima-backup/internal/model"
)

// fakeCmd records every call and answers from a table keyed by the
// command name. Unknown commands fail, like a missing binary would.
type fakeCmd struct {
	t     *testing.T
	calls []string
	table map[string]func(dir string, args []string) ([]byte, error)
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return f.RunIn(context.Background(), "", name, args...)
}

func (f *fakeCmd) RunIn(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")+" @"+dir))
	if h, ok := f.table[name]; ok {
		return h(dir, args)
	}
	return []byte("sh: " + name + ": command not found"), errors.New("exit status 127")
}

func (f *fakeCmd) called(prefix string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// sqliteCopy answers `sqlite3 <src> .backup '<dst>'` by copying the file.
func sqliteCopy(dir string, args []string) ([]byte, error) {
	if len(args) == 2 && strings.HasPrefix(args[1], ".backup '") {
		dst := strings.TrimSuffix(strings.TrimPrefix(args[1], ".backup '"), "'")
		return nil, copyFile(args[0], dst)
	}
	if len(args) == 2 && args[1] == "PRAGMA integrity_check;" {
		return []byte("ok\n"), nil
	}
	return nil, errors.New("unexpected sqlite3 call")
}

// fakeRsync mirrors src/ to dst/ the way `rsync -a --delete --exclude X`
// would, well enough for the assertions: excluded names survive on the
// receiver, everything else is replaced.
func fakeRsync(_ string, args []string) ([]byte, error) {
	var excludes []string
	for i := 0; i < len(args)-2; i++ {
		if args[i] == "--exclude" {
			excludes = append(excludes, strings.Trim(args[i+1], "/*"))
		}
	}
	src, dst := args[len(args)-2], args[len(args)-1]
	excluded := func(name string) bool {
		for _, e := range excludes {
			if strings.HasPrefix(name, e) {
				return true
			}
		}
		return false
	}
	if entries, err := os.ReadDir(dst); err == nil {
		for _, e := range entries {
			if !excluded(e.Name()) {
				os.RemoveAll(filepath.Join(dst, e.Name()))
			}
		}
	}
	return nil, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if excluded(strings.Split(rel, "/")[0]) {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeDisk builds the measured layout: overlay with an /etc upper, the
// hidden state dirs on /DATA, AppData with the module's own folder, and
// the six databases.
func fakeDisk(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	l := Layout{Root: root}
	write(t, l.Path("/etc/os-release"), "NAME=ZimaOS\nVERSION=\"v1.7.1\"\nPRETTY_NAME=\"ZimaOS v1.7.1\"\n")
	write(t, l.Path(OverlayDir+"/upper_etc/hostname"), "ZimaOS\n")
	write(t, l.Path(OverlayDir+"/upper_etc/NetworkManager/system-connections/br0.nmconnection"), "[connection]\nid=br0\n")
	write(t, l.Path(CasaOSDir+"/apps/dozzle/docker-compose.yml"), "services: {}\n")
	write(t, l.Path(CasaOSDir+"/apps/immich/docker-compose.yml"), "services: {}\n")
	write(t, l.Path(CasaOSDir+"/rclone.conf"), "[gdrive]\ntoken = SECRET-TOKEN-XYZ\n")
	write(t, l.Path(IceWhaleDir+"/appstore/.run"), "")
	write(t, l.Path(ExtensionsDir+"/zbackup.raw"), "raw")
	write(t, l.Path(ExtensionsDir+"/cron.raw"), "raw")
	write(t, l.Path(AppDataDir+"/zbackup/keys/.keep"), "")
	write(t, l.Path(AppDataDir+"/immich/config.json"), "{}")
	for _, db := range Databases {
		write(t, filepath.Join(l.Path(DataDir), db), "SQLite format 3\x00"+db)
	}
	return l
}

func TestSourcesRefuseANonZimaOSDisk(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if _, err := Sources(l, false); !errors.Is(err, ErrNotZimaOS) {
		t.Fatalf("want ErrNotZimaOS, got %v", err)
	}
}

func TestSourcesAreTheMeasuredStateDirectories(t *testing.T) {
	l := fakeDisk(t)
	got, err := Sources(l, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{OverlayDir, CasaOSDir, IceWhaleDir, ExtensionsDir, AppDataDir}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	got, _ = Sources(l, true)
	if strings.Join(got, ",") != OverlayDir+","+DataDir {
		t.Fatalf("with data: %v", got)
	}
}

func TestPrepareWritesManifestAndConsistentDatabaseCopies(t *testing.T) {
	l := fakeDisk(t)
	cmd := &fakeCmd{t: t, table: map[string]func(string, []string) ([]byte, error){
		"rauc": func(string, []string) ([]byte, error) {
			return []byte("\x1b[1mBooted from:\x1b[0m kernel.1 (B)\n"), nil
		},
		"findmnt": func(string, []string) ([]byte, error) { return []byte("/dev/nvme0n1p7\n"), nil },
		"lsblk":   func(string, []string) ([]byte, error) { return []byte("nvme0n1\n"), nil },
		"sfdisk": func(string, []string) ([]byte, error) {
			return []byte("label: gpt\n/dev/nvme0n1p1 : start=2048, size=65536, name=\"casaos-boot\"\n"), nil
		},
		"docker": func(string, []string) ([]byte, error) {
			return []byte(`{"Repository":"amir20/dozzle","Tag":"v10.7.1","Digest":"sha256:abc","ID":"e4da"}` + "\n" + `{"Repository":"<none>","Tag":"<none>","Digest":"<none>","ID":"dead"}` + "\n"), nil
		},
		"sqlite3": sqliteCopy,
	}}
	staging := filepath.Join(l.Path(AppDataDir), "zbackup", StagingName)
	m, warnings, err := Prepare(context.Background(), l, cmd, staging, []string{OverlayDir}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if m.OSVersion() != "v1.7.1" || m.SystemDisk != "/dev/nvme0n1" || !strings.Contains(m.RaucStatus, "Booted from: kernel.1") || strings.Contains(m.RaucStatus, "\x1b") {
		t.Fatalf("manifest: %+v", m)
	}
	if len(m.Images) != 1 || m.Images[0].Digest != "sha256:abc" {
		t.Fatalf("images: %+v", m.Images)
	}
	if strings.Join(m.Apps, ",") != "dozzle,immich" || strings.Join(m.Extensions, ",") != "cron.raw,zbackup.raw" {
		t.Fatalf("apps %v extensions %v", m.Apps, m.Extensions)
	}
	if len(m.Databases) != len(Databases) {
		t.Fatalf("databases copied: %v", m.Databases)
	}
	for _, db := range Databases {
		if _, err := os.Stat(filepath.Join(staging, "db", filepath.Base(db))); err != nil {
			t.Fatalf("copy of %s missing", db)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(staging, ManifestName))
	if strings.Contains(string(raw), "SECRET-TOKEN-XYZ") {
		t.Fatal("the manifest must not carry the content of rclone.conf")
	}
	if got := cmd.called("sqlite3"); len(got) != len(Databases) || !strings.Contains(got[0], ".backup '") {
		t.Fatalf("sqlite3 calls: %v", got)
	}
}

func TestPrepareSurvivesMissingTools(t *testing.T) {
	l := fakeDisk(t)
	cmd := &fakeCmd{t: t, table: map[string]func(string, []string) ([]byte, error){}}
	staging := filepath.Join(t.TempDir(), StagingName)
	m, warnings, err := Prepare(context.Background(), l, cmd, staging, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) < 3 || len(m.Databases) != 0 {
		t.Fatalf("expected warnings for rauc, docker and every database; got %v, databases %v", warnings, m.Databases)
	}
	if _, err := os.Stat(filepath.Join(staging, ManifestName)); err != nil {
		t.Fatal("manifest must still be written")
	}
}

func TestPrepareJobSetsOneFileSystemAndExcludesTheScratch(t *testing.T) {
	staging := "/DATA/AppData/zbackup/system"
	j := prepareJob(&model.Job{Kind: model.KindSystem, Excludes: []string{"/DATA/AppData/big"}}, []string{OverlayDir, AppDataDir}, staging)
	if !j.OneFileSystem {
		t.Fatal("one file system must be set")
	}
	if strings.Join(j.Sources, ",") != OverlayDir+","+AppDataDir {
		t.Fatalf("sources %v: staging is inside AppData and must not be listed twice", j.Sources)
	}
	if len(j.Excludes) != 3 || j.Excludes[1] != staging+"/restore-*" || j.Excludes[2] != staging+"/lower" {
		t.Fatalf("excludes %v", j.Excludes)
	}
	j = prepareJob(&model.Job{Kind: model.KindSystem}, []string{OverlayDir}, staging)
	if len(j.Sources) != 2 || j.Sources[1] != staging {
		t.Fatalf("staging outside the sources must be added: %v", j.Sources)
	}
	// "also /DATA" with the repository on the system disk: the repository must not back itself up
	j = prepareJob(&model.Job{Kind: model.KindSystem, IncludeData: true, Target: model.Target{Type: model.TargetLocal, Path: "/DATA/Backups/System"}}, []string{OverlayDir, DataDir}, staging)
	if j.Excludes[len(j.Excludes)-1] != "/DATA/Backups/System" {
		t.Fatalf("a local repository inside a source must be excluded: %v", j.Excludes)
	}
	j = prepareJob(&model.Job{Kind: model.KindSystem, Target: model.Target{Type: model.TargetLocal, Path: "/media/sdb/Backups/System"}}, []string{OverlayDir, AppDataDir}, staging)
	if len(j.Excludes) != 2 {
		t.Fatalf("a repository outside the sources needs no exclude: %v", j.Excludes)
	}
}

// snapshotRoot builds what `restic restore --target root` leaves behind for
// the fake disk, with a few differences to the running system.
func snapshotRoot(t *testing.T, l Layout, version string) string {
	t.Helper()
	root := t.TempDir()
	moduleDir := filepath.Join(AppDataDir, "zbackup")
	write(t, filepath.Join(root, OverlayDir, "upper_etc", "hostname"), "old-name\n")
	write(t, filepath.Join(root, OverlayDir, "upper_etc", "hosts"), "127.0.0.1 localhost\n")
	write(t, filepath.Join(root, CasaOSDir, "apps", "dozzle", "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(root, CasaOSDir, "apps", "paperless", "docker-compose.yml"), "services: {}\n")
	write(t, filepath.Join(root, CasaOSDir, "apps", "broken", "README"), "no compose here\n")
	write(t, filepath.Join(root, IceWhaleDir, "appstore", ".run"), "")
	write(t, filepath.Join(root, ExtensionsDir, "cron.raw"), "raw")
	write(t, filepath.Join(root, ExtensionsDir, "zbackup.raw"), "older module image")
	write(t, filepath.Join(root, AppDataDir, "paperless", "data.json"), "{}")
	write(t, filepath.Join(root, moduleDir, "keys", "old"), "must never be restored")
	staging := filepath.Join(root, moduleDir, StagingName)
	for _, db := range Databases {
		write(t, filepath.Join(staging, "db", filepath.Base(db)), "SQLite format 3\x00snapshot "+db)
	}
	write(t, filepath.Join(staging, ManifestName), `{"manifest_version":1,"os_release":{"VERSION":"`+version+`"},"apps":["dozzle","paperless"],"databases":["`+strings.Join(Databases, `","`)+`"],"include_data":false}`)
	return root
}

func applyCmd(t *testing.T, l Layout) *fakeCmd {
	return &fakeCmd{t: t, table: map[string]func(string, []string) ([]byte, error){
		"systemctl": func(string, []string) ([]byte, error) { return nil, nil },
		"docker": func(dir string, args []string) ([]byte, error) {
			if args[0] == "ps" {
				return []byte("c1\nc2\n"), nil
			}
			if args[0] == "compose" && strings.HasSuffix(dir, "/paperless") {
				return []byte("pull access denied"), errors.New("exit status 1")
			}
			return nil, nil
		},
		"rsync":   fakeRsync,
		"sqlite3": sqliteCopy,
		"findmnt": func(string, []string) ([]byte, error) { return []byte("/dev/nvme0n1p5\n"), nil },
		"mount": func(_ string, args []string) ([]byte, error) {
			// the read-only root holds the stock /etc/hostname and nothing else
			write(t, filepath.Join(args[len(args)-1], "etc", "hostname"), "ZimaOS\n")
			return nil, nil
		},
		"umount": func(string, []string) ([]byte, error) { return nil, nil },
	}}
}

func TestApplyRefusesAnotherOSVersionUnlessForced(t *testing.T) {
	l := fakeDisk(t)
	root := snapshotRoot(t, l, "v1.7.0")
	cmd := applyCmd(t, l)
	_, err := Apply(context.Background(), l, cmd, func(string) {}, root, Options{ModuleDir: filepath.Join(AppDataDir, "zbackup")})
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("want ErrVersionMismatch, got %v", err)
	}
	if len(cmd.calls) != 0 {
		t.Fatalf("nothing may run before the version check passes: %v", cmd.calls)
	}
	if _, err := Apply(context.Background(), l, cmd, func(string) {}, root, Options{ModuleDir: filepath.Join(AppDataDir, "zbackup"), Force: true}); err != nil {
		t.Fatalf("forced: %v", err)
	}
}

func TestApplyPlaysTheMeasuredProcedure(t *testing.T) {
	l := fakeDisk(t)
	// the running box has a hostname of its own and an extra NM profile the snapshot lacks
	write(t, l.Path("/etc/hostname"), "zimaos-g1test\n")
	write(t, l.Path(OverlayDir+"/upper_etc/hostname"), "zimaos-g1test\n")
	write(t, l.Path(OverlayDir+"/upper_etc/NetworkManager/system-connections/g1test.nmconnection"), "[connection]\n")
	write(t, l.Path("/etc/NetworkManager/system-connections/g1test.nmconnection"), "[connection]\n")
	write(t, l.Path(OverlayDir+"/upper_etc/machine-id"), "36d5\n")
	write(t, l.Path("/etc/machine-id"), "36d5\n")
	root := snapshotRoot(t, l, "v1.7.1")
	cmd := applyCmd(t, l)
	var logged []string
	rep, err := Apply(context.Background(), l, cmd, func(s string) { logged = append(logged, s) }, root, Options{ModuleDir: filepath.Join(AppDataDir, "zbackup")})
	if err != nil {
		t.Fatal(err)
	}
	// /etc: snapshot files written through the merged view
	if b, _ := os.ReadFile(l.Path("/etc/hosts")); string(b) != "127.0.0.1 localhost\n" {
		t.Fatalf("/etc/hosts = %q", b)
	}
	// the running-only hostname falls back to the read-only root's copy, as measured in G1
	if b, _ := os.ReadFile(l.Path("/etc/hostname")); string(b) != "old-name\n" {
		t.Fatalf("/etc/hostname = %q (snapshot's copy expected)", b)
	}
	if _, err := os.Stat(l.Path("/etc/NetworkManager/system-connections/g1test.nmconnection")); !os.IsNotExist(err) {
		t.Fatal("the extra NM profile must be removed")
	}
	if b, _ := os.ReadFile(l.Path("/etc/machine-id")); string(b) != "36d5\n" {
		t.Fatal("machine-id is set at boot and must be left alone")
	}
	// databases replaced by the copies and checked
	if b, _ := os.ReadFile(filepath.Join(l.Path(DataDir), ".casaos/db/user.db")); !strings.Contains(string(b), "snapshot") {
		t.Fatal("user.db must come from the snapshot copy")
	}
	if got := cmd.called("sqlite3"); len(got) != len(Databases) {
		t.Fatalf("integrity checks: %v", got)
	}
	// rsync: state dirs with --delete, AppData without the module's own folder
	rs := cmd.called("rsync")
	if len(rs) != 4 {
		t.Fatalf("rsync calls: %v", rs)
	}
	if !strings.Contains(rs[3], "--exclude /zbackup/") || !strings.Contains(rs[2], "--exclude zbackup*") {
		t.Fatalf("the module's own folder and image must be excluded: %v", rs[2:])
	}
	// order: services and containers stopped first, started again at the end
	if !strings.HasPrefix(cmd.calls[0], "systemctl stop zimaos.service") {
		t.Fatalf("first call: %s", cmd.calls[0])
	}
	if got := cmd.called("docker stop -t 30 c1 c2"); len(got) != 1 {
		t.Fatalf("containers must be stopped: %v", cmd.calls)
	}
	if last := cmd.calls[len(cmd.calls)-1]; !strings.HasPrefix(last, "systemctl start casaos-installer.service") {
		t.Fatalf("services must be started again last: %s", last)
	}
	// apps: every compose dir started, the failing one reported, the dir without compose skipped
	if strings.Join(rep.AppsStarted, ",") != "dozzle" || strings.Join(rep.AppsFailed, ",") != "paperless" {
		t.Fatalf("apps started %v failed %v", rep.AppsStarted, rep.AppsFailed)
	}
	if _, err := os.Stat(l.Path(AppDataDir + "/zbackup/keys/.keep")); err != nil {
		t.Fatal("the module's own folder must survive the AppData restore")
	}
	if b, _ := os.ReadFile(l.Path(ExtensionsDir + "/zbackup.raw")); string(b) != "raw" {
		t.Fatal("the module's own image must survive the extensions restore")
	}
	if _, err := os.Stat(l.Path(AppDataDir + "/immich/config.json")); !os.IsNotExist(err) {
		t.Fatal("AppData of an app the snapshot lacks must be removed (--delete)")
	}
	if !rep.NeedsReboot || rep.FilesWritten != 2 {
		t.Fatalf("report: %+v", rep)
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "reboot to finish") {
		t.Fatalf("log must end with the reboot hint: %v", logged)
	}
}

func TestMakePlanReportsVersionAndContent(t *testing.T) {
	l := fakeDisk(t)
	root := snapshotRoot(t, l, "v1.7.0")
	p, err := MakePlan(l, root, Options{ModuleDir: filepath.Join(AppDataDir, "zbackup")})
	if err != nil {
		t.Fatal(err)
	}
	if p.VersionMatch || p.RunningVersion != "v1.7.1" || p.OverlayFiles != 2 || len(p.Databases) != len(Databases) || strings.Join(p.Apps, ",") != "broken,dozzle,paperless" {
		t.Fatalf("plan: %+v", p)
	}
	if _, err := MakePlan(l, t.TempDir(), Options{ModuleDir: "x"}); err == nil {
		t.Fatal("a root without manifest is not a system backup")
	}
}
