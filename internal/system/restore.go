package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writerServices are the ZimaOS services that write the state directories.
// They are stopped while the state is replaced and started again after,
// so the WebUI answers until the user reboots (measured G1: the box came
// back clean after a reboot with exactly this set stopped).
var writerServices = []string{
	"zimaos.service", "zimaos-user.service", "zimaos-app-management.service", "zimaos-local-storage.service",
	"icewhale-files.service", "icewhale-files-backup.service", "rclone.service", "casaos-installer.service",
}

// Options steer a restore.
type Options struct {
	// Force applies a snapshot whose OS version differs from the running one.
	Force bool
	// ModuleDir is the module's own data directory (/DATA/AppData/zbackup);
	// it is never overwritten by a restore — the running store and the
	// restore staging live there.
	ModuleDir string
}

// Plan is what a restore would do, computed without touching the system.
type Plan struct {
	Manifest       Manifest `json:"manifest"`
	RunningVersion string   `json:"running_version"`
	VersionMatch   bool     `json:"version_match"`
	OverlayFiles   int      `json:"overlay_files"`
	Apps           []string `json:"apps"`
	Databases      []string `json:"databases"`
	HasAppData     bool     `json:"has_appdata"`
	HasData        bool     `json:"has_data"`
	Warnings       []string `json:"warnings,omitempty"`
}

// Report is the outcome of Apply.
type Report struct {
	Plan
	Steps       []string `json:"steps"`
	AppsStarted []string `json:"apps_started"`
	AppsFailed  []string `json:"apps_failed"`
	// containers that ran before the restore and are not started by an
	// app's compose (hand-started, other tooling): started again by id
	ContainersRestarted int  `json:"containers_restarted"`
	ContainersFailed    int  `json:"containers_failed"`
	NeedsReboot         bool `json:"needs_reboot"`
	FilesWritten        int  `json:"files_written"`
}

// ErrVersionMismatch says the snapshot came from another ZimaOS version.
var ErrVersionMismatch = errors.New("snapshot was taken on another ZimaOS version")

// manifestPath is where Prepare put the manifest, relative to the
// restored snapshot root.
func manifestPath(root, moduleDir string) string {
	return filepath.Join(root, moduleDir, StagingName, ManifestName)
}

// MakePlan inspects a restored snapshot root (restic restore --target root)
// and the running system.
func MakePlan(l Layout, root string, opts Options) (Plan, error) {
	var p Plan
	m, err := ReadManifest(manifestPath(root, opts.ModuleDir))
	if err != nil {
		return p, fmt.Errorf("this snapshot is not a system backup: %w", err)
	}
	p.Manifest = m
	p.RunningVersion = readOSRelease(l.Path("/etc/os-release"))["VERSION"]
	p.VersionMatch = m.OSVersion() != "" && m.OSVersion() == p.RunningVersion
	if !p.VersionMatch {
		p.Warnings = append(p.Warnings, fmt.Sprintf("snapshot from ZimaOS %q, running %q", m.OSVersion(), p.RunningVersion))
	}
	upper := filepath.Join(root, OverlayDir, "upper_etc")
	_ = filepath.WalkDir(upper, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			p.OverlayFiles++
		}
		return nil
	})
	if p.OverlayFiles == 0 {
		p.Warnings = append(p.Warnings, "the snapshot holds no /etc overlay")
	}
	p.Apps = listDir(filepath.Join(root, CasaOSDir, "apps"), true)
	for _, rel := range m.Databases {
		if _, err := os.Stat(filepath.Join(root, opts.ModuleDir, StagingName, "db", filepath.Base(rel))); err == nil {
			p.Databases = append(p.Databases, rel)
		}
	}
	p.HasAppData = isDir(filepath.Join(root, AppDataDir))
	p.HasData = m.IncludeData && isDir(filepath.Join(root, DataDir))
	return p, nil
}

// Apply plays a restored snapshot root back onto the running system, the
// way G1/G2 measured it (PLAN-0.3 §8, §9):
//
//  1. stop the writing services and all containers,
//  2. /etc: every file of the snapshot's overlay upper goes to /etc through
//     the merged view; files the running upper has and the snapshot does
//     not fall back to the read-only root's copy or are removed,
//  3. casaos, icewhale, extensions (not the module's own), AppData (not
//     the module's own): rsync --delete from the snapshot,
//  4. the SQLite copies replace the live databases (WAL/SHM removed),
//     each checked with PRAGMA integrity_check,
//  5. optionally the rest of /DATA (additive, never deleting user files),
//  6. every app below casaos/apps: docker compose up -d,
//  7. services started again; a reboot finishes the job.
//
// Every step is written to log. A failure in 2–5 stops the restore with
// the error; a failing app in 6 is reported, not fatal.
func Apply(ctx context.Context, l Layout, run Commander, log func(string), root string, opts Options) (Report, error) {
	plan, err := MakePlan(l, root, opts)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Plan: plan, NeedsReboot: true}
	step := func(format string, a ...any) {
		s := fmt.Sprintf(format, a...)
		rep.Steps = append(rep.Steps, s)
		log(s)
	}
	if !plan.VersionMatch && !opts.Force {
		return rep, fmt.Errorf("%w: snapshot %q, running %q", ErrVersionMismatch, plan.Manifest.OSVersion(), plan.RunningVersion)
	}
	if opts.ModuleDir == "" {
		return rep, errors.New("module directory not set")
	}

	// 1. quiet the writers
	for _, u := range writerServices {
		if out, err := run.Run(ctx, "systemctl", "stop", u); err != nil {
			step("could not stop %s: %s", u, short(err, out))
		}
	}
	step("stopped %d services", len(writerServices))
	var wasRunning []string
	if out, err := run.Run(ctx, "docker", "ps", "-q"); err == nil {
		ids := strings.Fields(string(out))
		wasRunning = ids
		if len(ids) > 0 {
			args := append([]string{"stop", "-t", "30"}, ids...)
			if out, err := run.Run(ctx, "docker", args...); err != nil {
				step("docker stop: %s", short(err, out))
			} else {
				step("stopped %d containers", len(ids))
			}
		}
	}
	defer func() {
		for _, u := range writerServices {
			_, _ = run.Run(ctx, "systemctl", "start", u)
		}
		log("services started again — reboot to finish the restore")
	}()

	// 2. /etc through the merged view
	n, err := restoreOverlay(ctx, l, run, log, filepath.Join(root, OverlayDir, "upper_etc"), opts)
	if err != nil {
		return rep, fmt.Errorf("/etc overlay: %w", err)
	}
	rep.FilesWritten += n
	step("/etc overlay: %d files written", n)

	// 3. state directories
	for _, dir := range []string{CasaOSDir, IceWhaleDir} {
		src := filepath.Join(root, dir)
		if !isDir(src) {
			continue
		}
		if out, err := run.Run(ctx, "rsync", "-a", "--delete", src+"/", l.Path(dir)+"/"); err != nil {
			return rep, fmt.Errorf("rsync %s: %s", dir, short(err, out))
		}
		step("%s restored", dir)
	}
	if src := filepath.Join(root, ExtensionsDir); isDir(src) {
		// the module's own sysext image is left alone: it is the code that is running this restore
		if out, err := run.Run(ctx, "rsync", "-a", "--delete", "--exclude", "zbackup*", src+"/", l.Path(ExtensionsDir)+"/"); err != nil {
			return rep, fmt.Errorf("rsync extensions: %s", short(err, out))
		}
		step("%s restored (module's own image kept)", ExtensionsDir)
	}
	if src := filepath.Join(root, AppDataDir); isDir(src) {
		rel, _ := filepath.Rel(AppDataDir, opts.ModuleDir)
		args := []string{"-a", "--delete", "--exclude", "/" + rel + "/", src + "/", l.Path(AppDataDir) + "/"}
		if out, err := run.Run(ctx, "rsync", args...); err != nil {
			return rep, fmt.Errorf("rsync AppData: %s", short(err, out))
		}
		step("%s restored (module's own folder kept)", AppDataDir)
	}

	// 4. databases
	for _, relDB := range plan.Databases {
		src := filepath.Join(root, opts.ModuleDir, StagingName, "db", filepath.Base(relDB))
		dst := filepath.Join(l.Path(DataDir), relDB)
		for _, side := range []string{dst, dst + "-wal", dst + "-shm"} {
			_ = os.Remove(side)
		}
		if err := copyFile(src, dst); err != nil {
			return rep, fmt.Errorf("database %s: %w", relDB, err)
		}
		out, err := run.Run(ctx, "sqlite3", dst, "PRAGMA integrity_check;")
		if err != nil || strings.TrimSpace(string(out)) != "ok" {
			return rep, fmt.Errorf("database %s: integrity_check: %s", relDB, short(errOr(err), out))
		}
		step("database %s restored, integrity ok", relDB)
	}

	// 5. user data, additively
	if plan.HasData {
		args := []string{"-a", "--exclude", "/.casaos/", "--exclude", "/.icewhale/", "--exclude", "/.extensions/", "--exclude", "/AppData/", "--exclude", "/.media/",
			filepath.Join(root, DataDir) + "/", l.Path(DataDir) + "/"}
		if out, err := run.Run(ctx, "rsync", args...); err != nil {
			return rep, fmt.Errorf("rsync /DATA: %s", short(err, out))
		}
		step("%s restored (additive, nothing deleted)", DataDir)
	}

	// 6. apps: the tile hangs on the container (G2), so every app is started
	for _, app := range plan.Apps {
		dir := filepath.Join(l.Path(CasaOSDir), "apps", app)
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
			continue
		}
		if out, err := run.RunIn(ctx, dir, "docker", "compose", "up", "-d"); err != nil {
			rep.AppsFailed = append(rep.AppsFailed, app)
			step("app %s: compose up failed: %s", app, short(err, out))
			continue
		}
		rep.AppsStarted = append(rep.AppsStarted, app)
	}
	step("apps started: %d, failed: %d", len(rep.AppsStarted), len(rep.AppsFailed))

	// 6b. whatever ran before and is not back yet — containers outside the app store (measured: ten of
	// them on the test box, started by hand) — is started again by id; compose has re-created its own
	if len(wasRunning) > 0 {
		running := map[string]bool{}
		if out, err := run.Run(ctx, "docker", "ps", "-q"); err == nil {
			for _, id := range strings.Fields(string(out)) {
				running[id] = true
			}
		}
		for _, id := range wasRunning {
			if running[id] {
				continue
			}
			if out, err := run.Run(ctx, "docker", "start", id); err != nil {
				rep.ContainersFailed++
				log("container " + id + ": start failed: " + short(err, out))
				continue
			}
			rep.ContainersRestarted++
		}
		step("containers outside the apps started again: %d, failed: %d", rep.ContainersRestarted, rep.ContainersFailed)
	}
	return rep, nil
}

// restoreOverlay writes the snapshot's upper_etc onto /etc through the
// merged view. Files the running upper holds that the snapshot lacks are
// replaced by the read-only root's copy (mounted read-only for the
// duration) or removed. A whiteout in the snapshot (character device
// 0/0) means the file was deleted on the source box: it is removed.
func restoreOverlay(ctx context.Context, l Layout, run Commander, log func(string), snapUpper string, opts Options) (int, error) {
	if !isDir(snapUpper) {
		return 0, nil
	}
	etc := l.Path("/etc")
	written := 0
	err := filepath.WalkDir(snapUpper, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(snapUpper, path)
		if rel == "." {
			return nil
		}
		dst := filepath.Join(etc, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeCharDevice != 0:
			_ = os.RemoveAll(dst)
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
		default:
			if err := copyFile(path, dst); err != nil {
				return err
			}
		}
		written++
		return nil
	})
	if err != nil {
		return written, err
	}

	// files the running upper has and the snapshot does not
	curUpper := filepath.Join(l.Path(OverlayDir), "upper_etc")
	var extra []string
	_ = filepath.WalkDir(curUpper, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(curUpper, path)
		if _, err := os.Lstat(filepath.Join(snapUpper, rel)); err != nil {
			extra = append(extra, rel)
		}
		return nil
	})
	if len(extra) == 0 {
		return written, nil
	}
	sort.Strings(extra)
	lower, unmount, err := mountLower(ctx, l, run, opts)
	if err != nil {
		return written, err
	}
	defer unmount()
	for _, rel := range extra {
		if rel == "machine-id" {
			continue // set at boot from the kernel command line (G4); leave the running one
		}
		dst := filepath.Join(etc, rel)
		if src := filepath.Join(lower, "etc", rel); fileExists(src) {
			if err := copyFile(src, dst); err != nil {
				return written, err
			}
			log("/etc/" + rel + ": read-only root's copy restored")
		} else {
			_ = os.Remove(dst)
			log("/etc/" + rel + ": removed")
		}
	}
	return written, nil
}

// mountLower mounts the read-only root filesystem below the module's
// data dir (/mnt is itself read-only — measured) and returns the mount
// point plus the unmount func.
func mountLower(ctx context.Context, l Layout, run Commander, opts Options) (string, func(), error) {
	out, err := run.Run(ctx, "findmnt", "-no", "SOURCE", l.Path("/"))
	if err != nil {
		return "", nil, fmt.Errorf("findmnt /: %s", short(err, out))
	}
	dev := strings.TrimSpace(string(out))
	mp := filepath.Join(l.Path(opts.ModuleDir), StagingName, "lower")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return "", nil, err
	}
	if out, err := run.Run(ctx, "mount", "-o", "ro", dev, mp); err != nil {
		return "", nil, fmt.Errorf("mount %s: %s", dev, short(err, out))
	}
	return mp, func() { _, _ = run.Run(ctx, "umount", mp); _ = os.Remove(mp) }, nil
}

// copyFile copies a regular file with its mode; the destination is
// replaced atomically-enough (write to temp, rename) so a reader never
// sees a half file.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".zbackup-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, st.Mode().Perm())
	return os.Rename(tmp, dst)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && !st.IsDir()
}

func errOr(err error) error {
	if err != nil {
		return err
	}
	return errors.New("unexpected output")
}
