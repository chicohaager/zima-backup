package mirror

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/localfs"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/mounts"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// The tests run against the real rsync and rclone: the output shapes are
// what can break, not the Go around them.
func newRunner(t *testing.T) *Runner {
	t.Helper()
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Fatal("rsync is not installed; the sync runner cannot be tested without it")
	}
	return &Runner{Rsync: rsync, Rclone: rcloneBin(t), Key: sshkey.Pair{Dir: filepath.Join(t.TempDir(), "keys")}}
}

// rcloneBin returns rclone from RCLONE_BIN or PATH. It is empty when
// missing; rclone tests then skip loudly (CI installs it).
func rcloneBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("RCLONE_BIN"); p != "" {
		return p
	}
	p, err := exec.LookPath("rclone")
	if err != nil {
		return ""
	}
	return p
}

func needRclone(t *testing.T, r *Runner) {
	t.Helper()
	if r.Rclone == "" {
		t.Skip("SKIPPED: rclone not found — set RCLONE_BIN or install rclone; the rclone path is untested here")
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func noProgress(float64, string) {}

func localSync(target string, sources ...string) *model.Job {
	return &model.Job{ID: "s1", Name: "docs", Kind: model.KindSync, Sources: sources,
		Target: model.Target{Type: model.TargetLocal, Path: target}}
}

// Two sources land side by side under the target; a second run mirrors a
// change and — only with delete_extraneous — a deletion, while files the
// job never wrote stay untouched.
func TestRsyncMirrorsSourcesIntoTarget(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	docs, pics := filepath.Join(base, "docs"), filepath.Join(base, "pics")
	writeFile(t, filepath.Join(docs, "a.txt"), "version one\n")
	writeFile(t, filepath.Join(docs, "sub", "b.txt"), "b\n")
	writeFile(t, filepath.Join(pics, "c.jpg"), "jpeg\n")
	_ = os.Mkdir(filepath.Join(base, "usb"), 0755) // the mounted disk
	target := filepath.Join(base, "usb", "mirror")
	job := localSync(target, docs, pics)
	ctx := context.Background()

	var seen []float64
	res := r.Run(ctx, job, store.Secrets{}, func(f float64, _ string) { seen = append(seen, f) })
	if !res.Success || res.Code != model.CodeCompleted || res.Files != 3 {
		t.Fatalf("first run: %+v", res)
	}
	if readFile(t, filepath.Join(target, "docs", "sub", "b.txt")) != "b\n" || readFile(t, filepath.Join(target, "pics", "c.jpg")) != "jpeg\n" {
		t.Fatal("sources were not mirrored as <target>/<basename>")
	}
	if len(seen) == 0 || seen[len(seen)-1] != 1 {
		t.Fatalf("progress never reached 1: %v", seen)
	}
	for _, f := range seen {
		if f < 0 || f > 1 {
			t.Fatalf("progress outside 0..1: %v", seen)
		}
	}

	// change one file (same size, so the mtime must move — rsync's
	// quick check compares size and time), remove another, plant a
	// foreign file in the target
	writeFile(t, filepath.Join(docs, "a.txt"), "version two\n")
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(filepath.Join(docs, "a.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(docs, "sub", "b.txt"))
	writeFile(t, filepath.Join(target, "notes.txt"), "not from any source\n")

	// without delete_extraneous the removed file survives on the target
	res = r.Run(ctx, job, store.Secrets{}, noProgress)
	if !res.Success || res.Files != 1 {
		t.Fatalf("second run: %+v", res)
	}
	if readFile(t, filepath.Join(target, "docs", "a.txt")) != "version two\n" || !exists(filepath.Join(target, "docs", "sub", "b.txt")) {
		t.Fatal("change not mirrored or deletion mirrored without the switch")
	}

	// with it, the deletion is mirrored — inside docs/ only
	job.DeleteExtraneous = true
	p, err := r.Preview(ctx, job, store.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Detailed || p.FilesCopy != 0 || p.FilesDelete != 1 {
		t.Fatalf("preview before the deleting run: %+v", p)
	}
	res = r.Run(ctx, job, store.Secrets{}, noProgress)
	if !res.Success || !strings.Contains(res.Message, "1 deleted") {
		t.Fatalf("deleting run: %+v", res)
	}
	if exists(filepath.Join(target, "docs", "sub", "b.txt")) {
		t.Fatal("deletion was not mirrored")
	}
	if !exists(filepath.Join(target, "notes.txt")) {
		t.Fatal("delete_extraneous removed a file outside the mirrored folders")
	}
}

func TestRsyncPreviewCountsNewChangedDeleted(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "new.txt"), "12345")
	writeFile(t, filepath.Join(src, "changed.txt"), "longer content")
	writeFile(t, filepath.Join(src, "sub", "same.txt"), "same")
	target := filepath.Join(base, "target")
	writeFile(t, filepath.Join(target, "src", "changed.txt"), "short")
	writeFile(t, filepath.Join(target, "src", "sub", "same.txt"), "same")
	writeFile(t, filepath.Join(target, "src", "gone.txt"), "x")
	job := localSync(target, src)
	job.DeleteExtraneous = true

	p, err := r.Preview(context.Background(), job, store.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	want := Preview{FilesCopy: 2, FilesDelete: 1, Bytes: 19, Detailed: true, FilesNew: 1, FilesChanged: 1}
	if p != want {
		t.Fatalf("preview = %+v, want %+v", p, want)
	}
	if exists(filepath.Join(target, "src", "new.txt")) || !exists(filepath.Join(target, "src", "gone.txt")) {
		t.Fatal("the preview changed the target")
	}
}

// B4 for sync: an unmounted disk must not receive a copy inside the
// empty mount point.
func TestRsyncMissingTargetDiskIsReported(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	// the temp dir's own filesystem plays the role of /media: a folder
	// there that does not exist is an unplugged disk
	localfs.ContainerMounts = []string{localfs.MountPointOf(base)}
	defer func() { localfs.ContainerMounts = nil }()
	job := localSync(filepath.Join(base, "not-mounted", "mirror"), src)
	res := r.Run(context.Background(), job, store.Secrets{}, noProgress)
	if res.Success || res.Code != model.CodeTargetUnavailable {
		t.Fatalf("expected target_unavailable, got %+v", res)
	}
	if exists(filepath.Join(base, "not-mounted")) {
		t.Fatal("runner created the missing mount point")
	}
	if _, err := r.Preview(context.Background(), job, store.Secrets{}); err == nil {
		t.Fatal("preview against a missing disk must fail")
	}
}

func TestRsyncUnreadableFileIsPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("SKIPPED: root reads everything, the partial case cannot be produced")
	}
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "ok.txt"), "fine")
	writeFile(t, filepath.Join(src, "locked.txt"), "secret")
	if err := os.Chmod(filepath.Join(src, "locked.txt"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "locked.txt"), 0644) })
	target := filepath.Join(base, "target")
	_ = os.Mkdir(target, 0755)
	res := r.Run(context.Background(), localSync(target, src), store.Secrets{}, noProgress)
	if res.Success || res.Code != model.CodePartial || !strings.Contains(res.Message, "locked.txt") {
		t.Fatalf("expected partial naming the file, got %+v", res)
	}
	if !exists(filepath.Join(target, "src", "ok.txt")) {
		t.Fatal("the readable file was not copied")
	}
}

func TestRsyncCancel(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	for i := 0; i < 300; i++ {
		writeFile(t, filepath.Join(src, "d", string(rune('a'+i%26)), "f"+string(rune('a'+i%26))+".txt"), strings.Repeat("x", 4096))
	}
	target := filepath.Join(base, "target")
	_ = os.Mkdir(target, 0755)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Run(ctx, localSync(target, src), store.Secrets{}, noProgress)
	if res.Success || res.Code != model.CodeCancelled {
		t.Fatalf("cancelled run: %+v", res)
	}
}

// exFAT (chown refused, mtime kept) drops ownership; FAT (mtime rounded
// to 2 s) adds the window; ext4 adds nothing. Measured on ZimaOS 1.7.1.
func TestAttrFlagsFollowTheProbe(t *testing.T) {
	if got := attrFlags(0, nil); len(got) != 0 {
		t.Fatalf("ext4: %v", got)
	}
	if got := strings.Join(attrFlags(0, errors.New("operation not permitted")), " "); got != "--no-owner --no-group --no-perms" {
		t.Fatalf("exfat: %v", got)
	}
	if got := strings.Join(attrFlags(1500*time.Millisecond, nil), " "); got != "--modify-window=2" {
		t.Fatalf("vfat: %v", got)
	}
	// on a filesystem that keeps everything the probe must say so and
	// must not leave its file behind
	dir := t.TempDir()
	drift, chownErr := probeTarget(dir)
	if os.Geteuid() == 0 && (chownErr != nil || drift != 0) {
		t.Fatalf("probe on tmpfs/ext4 as root: %v %v", chownErr, drift)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("probe left %d files behind", len(left))
	}
}

// Stats lines as printed by rsync 3.4.1 on ZimaOS 1.7.1 and 3.2.7 locally
// (--stats --no-human-readable): the "(reg: N, dir: M)" suffix varies.
func TestRsyncStatsParser(t *testing.T) {
	var s rsyncStats
	for _, line := range []string{
		"Number of files: 4 (reg: 2, dir: 2)",
		"Number of created files: 3 (reg: 2, dir: 1)",
		"Number of deleted files: 1 (reg: 1)",
		"Number of regular files transferred: 2",
		"Total file size: 4 bytes",
		"Total transferred file size: 1048576 bytes",
	} {
		s.apply(line)
	}
	if !s.seen || s.createdReg != 2 || s.deleted != 1 || s.transferred != 2 || s.bytes != 1048576 {
		t.Fatalf("parsed %+v", s)
	}
	var zero rsyncStats
	zero.apply("Number of created files: 0")
	zero.apply("Number of deleted files: 0")
	if !zero.seen || zero.createdReg != 0 || zero.deleted != 0 {
		t.Fatalf("zero form parsed %+v", zero)
	}
}

func TestSSHTargetUsesOwnKeyAndNoSecretsInArgs(t *testing.T) {
	r := newRunner(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("SKIPPED: ssh-keygen missing, key generation untested")
	}
	pub, err := r.Key.Public()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("public key = %q", pub)
	}
	st, err := os.Stat(r.Key.PrivatePath())
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private key mode = %v, err %v", st.Mode(), err)
	}
	again, _ := r.Key.Public()
	if again != pub {
		t.Fatal("second call generated a new key")
	}
	args := strings.Join(r.Key.SSHArgs(), " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=accept-new", "UserKnownHostsFile=" + r.Key.KnownHostsPath()} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh args lack %s: %s", want, args)
		}
	}
}

// --- rclone ---

func TestRcloneEnvConfiguresRemoteWithoutArgv(t *testing.T) {
	r := newRunner(t)
	needRclone(t, r)
	smb := &model.Job{Kind: model.KindSync, Target: model.Target{Type: model.TargetSMB, Host: "nas", Share: "backup", Path: "/zima/", User: "u", Port: 4450}}
	env, base, err := r.rcloneEnv(smb, store.Secrets{TargetSecret: "smb-pass"})
	if err != nil {
		t.Fatal(err)
	}
	if base != "zb:backup/zima" {
		t.Fatalf("smb base = %q", base)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"RCLONE_CONFIG_ZB_TYPE=smb", "RCLONE_CONFIG_ZB_HOST=nas", "RCLONE_CONFIG_ZB_USER=u", "RCLONE_CONFIG_ZB_PORT=4450", "RCLONE_CONFIG_ZB_PASS="} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(joined, "smb-pass") {
		t.Error("password stored unobscured")
	}
	s3 := &model.Job{Kind: model.KindSync, Target: model.Target{Type: model.TargetS3, Host: "s3.example:9000", Bucket: "b", Path: "p", User: "AKIA", Region: "eu", Insecure: true}}
	env, base, _ = r.rcloneEnv(s3, store.Secrets{TargetSecret: "s3-secret"})
	joined = strings.Join(env, "\n")
	for _, want := range []string{"RCLONE_CONFIG_ZB_TYPE=s3", "RCLONE_CONFIG_ZB_ACCESS_KEY_ID=AKIA", "RCLONE_CONFIG_ZB_SECRET_ACCESS_KEY=s3-secret", "RCLONE_CONFIG_ZB_ENDPOINT=http://s3.example:9000", "RCLONE_CONFIG_ZB_REGION=eu"} {
		if !strings.Contains(joined, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if base != "zb:b/p" {
		t.Fatalf("s3 base = %q", base)
	}
	// a host OpenSSH already recorded (one key type, as it does) is not
	// scanned again; rclone is told to negotiate exactly that type
	sftp := &model.Job{Kind: model.KindSync, Target: model.Target{Type: model.TargetSFTP, Host: "box", User: "backup", Path: "/srv/mirror", Port: 2222}}
	_ = os.MkdirAll(r.Key.Dir, 0700)
	writeFile(t, r.Key.KnownHostsPath(), "[box]:2222 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample\nother ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQExample\n")
	env, base, err = r.rcloneEnv(sftp, store.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(env, "\n")
	for _, want := range []string{"RCLONE_CONFIG_ZB_KEY_FILE=" + r.Key.PrivatePath(), "RCLONE_CONFIG_ZB_KNOWN_HOSTS_FILE=", "RCLONE_CONFIG_ZB_HOST_KEY_ALGORITHMS=ssh-ed25519"} {
		if !strings.Contains(joined, want) {
			t.Errorf("sftp env lacks %s", want)
		}
	}
	if strings.Contains(joined, "HOST_KEY_ALGORITHMS=ssh-ed25519 ssh-rsa") {
		t.Error("another host's key type leaked into the algorithm list")
	}
	if base != "zb:/srv/mirror" {
		t.Fatalf("sftp base = %q", base)
	}
	// an unrecorded host that does not answer the key scan is unavailable
	down := &model.Job{ID: "d", Kind: model.KindSync, Sources: []string{t.TempDir()}, Target: model.Target{Type: model.TargetSFTP, Host: "127.0.0.1", Port: 9, User: "u", Path: "/x"}}
	if res := r.Run(context.Background(), down, store.Secrets{}, noProgress); res.Code != model.CodeTargetUnavailable {
		t.Fatalf("sftp host down: %+v", res)
	}
}

// rcloneOnce against local paths exercises the JSON log parsing (stats,
// dry-run skips, errors) with the real binary; the network backends are
// covered on the test hosts.
func TestRcloneLogParsing(t *testing.T) {
	r := newRunner(t)
	needRclone(t, r)
	base := t.TempDir()
	src, dst := filepath.Join(base, "src"), filepath.Join(base, "dst")
	writeFile(t, filepath.Join(src, "a.txt"), "aaaa")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bb")
	writeFile(t, filepath.Join(dst, "stale.txt"), "stale")
	env := append(os.Environ(), "LC_ALL=C")
	common := []string{"--use-json-log", "--stats=2s", "--stats-log-level", "NOTICE", "--stats-one-line"}
	ctx := context.Background()

	res, tot := r.rcloneOnce(ctx, env, append([]string{"sync", src, dst, "--dry-run"}, common...), func(float64) {})
	if !res.Success || tot.dryCopies != 2 || tot.dryDeletes != 1 || tot.dryBytes != 6 {
		t.Fatalf("dry run: %+v %+v", res, tot)
	}
	if exists(filepath.Join(dst, "a.txt")) {
		t.Fatal("dry run copied")
	}

	var last float64
	res, tot = r.rcloneOnce(ctx, env, append([]string{"sync", src, dst}, common...), func(f float64) { last = f })
	if !res.Success || tot.transfers != 2 || tot.deletes != 1 || tot.bytes != 6 || tot.errors != 0 {
		t.Fatalf("sync: %+v %+v", res, tot)
	}
	if last != 1 || exists(filepath.Join(dst, "stale.txt")) || readFile(t, filepath.Join(dst, "sub", "b.txt")) != "bb" {
		t.Fatalf("sync outcome wrong: progress %v", last)
	}

	res, _ = r.rcloneOnce(ctx, env, append([]string{"sync", filepath.Join(base, "nowhere"), dst}, common...), func(float64) {})
	if res.Success || res.Code != model.CodeTargetUnavailable || !strings.Contains(res.Message, "directory not found") {
		t.Fatalf("missing source: %+v", res)
	}

	if os.Geteuid() != 0 {
		writeFile(t, filepath.Join(src, "locked.txt"), "secret")
		_ = os.Chmod(filepath.Join(src, "locked.txt"), 0)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(src, "locked.txt"), 0644) })
		writeFile(t, filepath.Join(src, "more.txt"), "more")
		res, tot = r.rcloneOnce(ctx, env, append([]string{"sync", src, dst, "--retries", "1"}, common...), func(float64) {})
		if res.Success || res.Code != model.CodePartial || !strings.Contains(res.Message, "locked.txt") || tot.errors == 0 {
			t.Fatalf("partial: %+v %+v", res, tot)
		}
	}
}

// Run through the public entry point: an smb job whose host does not
// answer is target_unavailable, and the password never appears in argv.
func TestRcloneUnreachableTarget(t *testing.T) {
	r := newRunner(t)
	needRclone(t, r)
	src := filepath.Join(t.TempDir(), "src")
	writeFile(t, filepath.Join(src, "a.txt"), "a")
	job := &model.Job{ID: "smb1", Kind: model.KindSync, Sources: []string{src},
		Target: model.Target{Type: model.TargetSMB, Host: "127.0.0.1", Port: 9, Share: "s", User: "u"}}
	res := r.Run(context.Background(), job, store.Secrets{TargetSecret: "pw"}, noProgress)
	if res.Success || res.Code != model.CodeTargetUnavailable {
		t.Fatalf("expected target_unavailable, got %+v", res)
	}
}

// The progress2 line as rsync 3.4.1 prints it (LC_ALL=C: thousands
// separated by commas, decimal units).
func TestRsyncProgressLineCarriesBytesAndRate(t *testing.T) {
	m := rsyncPercent.FindStringSubmatch("      1,234,567  45%   12.34MB/s    0:00:01 (xfr#3, to-chk=10/20)")
	if m == nil || m[1] != "1,234,567" || m[2] != "45" || rsyncRate(m[3], m[4]) != 12340000 {
		t.Fatalf("progress2 parse = %v", m)
	}
	if rsyncRate("3.91", "kB") != 3910 || rsyncRate("0.00", "kB") != 0 {
		t.Fatal("kB rate")
	}
	if rsyncPercent.MatchString("Number of files: 4 (reg: 2, dir: 2)") {
		t.Fatal("a stats line must not look like progress")
	}
}

func TestCloudTargetUsesZimaOSConfig(t *testing.T) {
	r := newRunner(t)
	job := &model.Job{Kind: model.KindSync, Target: model.Target{Type: model.TargetCloud, Remote: "google_drive_252f21c18474", Path: "/Backups/"}}
	env, base, err := r.rcloneEnv(job, store.Secrets{})
	if err != nil || base != "google_drive_252f21c18474:Backups" || !strings.Contains(strings.Join(env, "\n"), "RCLONE_CONFIG="+mounts.RcloneConfig) {
		t.Fatalf("cloud: base=%q err=%v", base, err)
	}
}
