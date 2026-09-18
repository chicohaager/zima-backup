package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chicohaager/zima-backup/internal/localfs"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// resticBin is the binary build.sh places in the sysext; the tests run
// against it, not a mock, because the JSON shapes are what can break.
func resticBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("RESTIC_BIN"); p != "" {
		return p
	}
	p, _ := filepath.Abs("../../raw/usr/libexec/zbackup/restic")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("restic binary missing at %s — run tools/fetch-restic.sh amd64 raw/usr/libexec/zbackup/restic (or set RESTIC_BIN)", p)
	}
	return p
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	dir := t.TempDir()
	return &Runner{Restic: resticBin(t), CacheDir: filepath.Join(dir, "cache"), Key: sshkey.Pair{Dir: filepath.Join(dir, "keys")}}
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

func sha(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func noProgress(float64, string) {}

func localJob(src, repo string) *model.Job {
	return &model.Job{ID: "job1", Name: "photos", Kind: model.KindBackup, Sources: []string{src},
		Target: model.Target{Type: model.TargetLocal, Path: repo}}
}

// Backup twice, keep-last 1, list, restore the first version of a file
// from the older snapshot before retention — the whole B2/B3 path.
func TestBackupRetentionListRestore(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "version one\n")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "b\n")
	original := sha(t, filepath.Join(src, "a.txt"))
	_ = os.Mkdir(filepath.Join(base, "target"), 0755) // the "mounted disk"
	job := localJob(src, filepath.Join(base, "target", "repo"))
	sec := store.Secrets{Passphrase: "correct horse"}
	ctx := context.Background()

	res := r.Run(ctx, job, sec, noProgress)
	if !res.Success || res.Code != model.CodeCompleted || res.SnapshotID == "" || res.Files != 2 {
		t.Fatalf("first backup: %+v", res)
	}
	first := res.SnapshotID

	writeFile(t, filepath.Join(src, "a.txt"), "version two\n")
	res = r.Run(ctx, job, sec, noProgress)
	if !res.Success || res.SnapshotID == first {
		t.Fatalf("second backup: %+v", res)
	}
	snaps, err := r.Snapshots(ctx, job, sec)
	if err != nil || len(snaps) != 2 || snaps[0].ID == first {
		t.Fatalf("snapshots (newest first) = %+v, %v", snaps, err)
	}

	// browse the older snapshot
	nodes, err := r.List(ctx, job, sec, first, src)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.Name] = n.Type
	}
	if names["a.txt"] != "file" || names["sub"] != "dir" || len(nodes) != 2 {
		t.Fatalf("listing of %s = %+v", src, nodes)
	}

	// restore the old a.txt into a separate folder and compare hashes
	dest := filepath.Join(base, "restored")
	res = r.Restore(first, []string{filepath.Join(src, "a.txt")}, dest)(ctx, job, sec, noProgress)
	if !res.Success || res.Code != model.CodeRestored {
		t.Fatalf("restore: %+v", res)
	}
	restored := filepath.Join(dest, src, "a.txt")
	if sha(t, restored) != original {
		t.Fatalf("restored file differs from the original version")
	}

	// retention: keep the last one only
	job.Retention = model.Retention{KeepLast: 1}
	res = r.Run(ctx, job, sec, noProgress)
	if !res.Success {
		t.Fatalf("third backup: %+v", res)
	}
	snaps, _ = r.Snapshots(ctx, job, sec)
	if len(snaps) != 1 {
		t.Fatalf("retention keep-last 1 left %d snapshots", len(snaps))
	}

	res = r.Check()(ctx, job, sec, noProgress)
	if !res.Success || res.Code != model.CodeCheckOK {
		t.Fatalf("check: %+v", res)
	}
}

func TestWrongPassphraseIsNamed(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	job := localJob(src, filepath.Join(base, "repo"))
	if res := r.Run(context.Background(), job, store.Secrets{Passphrase: "one"}, noProgress); !res.Success {
		t.Fatalf("setup backup: %+v", res)
	}
	res := r.Run(context.Background(), job, store.Secrets{Passphrase: "two"}, noProgress)
	if res.Success || res.Code != model.CodePassphraseWrong {
		t.Fatalf("wrong passphrase: %+v", res)
	}
	if _, err := r.Snapshots(context.Background(), job, store.Secrets{}); err == nil {
		t.Fatal("missing passphrase must be an error")
	}
}

// B4: an unmounted disk must not become a backup into the mount point's
// parent — the run fails with target_unavailable and touches nothing.
func TestMissingTargetDiskIsReported(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	localfs.ContainerMounts = []string{localfs.MountPointOf(base)} // the temp fs plays /media
	defer func() { localfs.ContainerMounts = nil }()
	job := localJob(src, filepath.Join(base, "not-mounted", "backup"))
	res := r.Run(context.Background(), job, store.Secrets{Passphrase: "pw"}, noProgress)
	if res.Success || res.Code != model.CodeTargetUnavailable {
		t.Fatalf("expected target_unavailable, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(base, "not-mounted")); !os.IsNotExist(err) {
		t.Fatal("runner created the missing mount point")
	}
}

func TestSharedRepositoryKeepsJobsApart(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	srcA, srcB := filepath.Join(base, "a"), filepath.Join(base, "b")
	writeFile(t, filepath.Join(srcA, "a.txt"), "a")
	writeFile(t, filepath.Join(srcB, "b.txt"), "b")
	repo := filepath.Join(base, "repo")
	sec := store.Secrets{Passphrase: "pw"}
	jobA := &model.Job{ID: "A", Kind: model.KindBackup, Sources: []string{srcA}, Target: model.Target{Type: model.TargetLocal, Path: repo}}
	jobB := &model.Job{ID: "B", Kind: model.KindBackup, Sources: []string{srcB}, Target: model.Target{Type: model.TargetLocal, Path: repo}}
	for _, j := range []*model.Job{jobA, jobB, jobA} {
		if res := r.Run(context.Background(), j, sec, noProgress); !res.Success {
			t.Fatalf("backup %s: %+v", j.ID, res)
		}
	}
	a, _ := r.Snapshots(context.Background(), jobA, sec)
	b, _ := r.Snapshots(context.Background(), jobB, sec)
	if len(a) != 2 || len(b) != 1 {
		t.Fatalf("tags do not separate the jobs: A=%d B=%d", len(a), len(b))
	}
}

func TestCancelStopsRestic(t *testing.T) {
	r := newRunner(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(src, "f", string(rune('a'+i%26)), "file"+string(rune('a'+i%26))+".txt"), "payload")
	}
	job := localJob(src, filepath.Join(base, "repo"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Run(ctx, job, store.Secrets{Passphrase: "pw"}, noProgress)
	if res.Success || res.Code != model.CodeCancelled {
		t.Fatalf("cancelled run: %+v", res)
	}
}

func TestEnvNeverPutsSecretsInArgs(t *testing.T) {
	r := newRunner(t)
	job := &model.Job{ID: "x", Kind: model.KindBackup, Target: model.Target{Type: model.TargetS3, Host: "s3.example", Bucket: "b", Path: "p", User: "AKIA"}}
	env, opts, err := r.env(job, store.Secrets{Passphrase: "pass-phrase", TargetSecret: "s3-secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		if o == "pass-phrase" || o == "s3-secret" {
			t.Fatalf("secret in argv: %v", opts)
		}
	}
	joined := ""
	for _, e := range env {
		joined += e + "\n"
	}
	for _, want := range []string{"RESTIC_PASSWORD=pass-phrase", "AWS_SECRET_ACCESS_KEY=s3-secret", "RESTIC_REPOSITORY=s3:https://s3.example/b/p"} {
		if !contains(joined, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	sftp := &model.Job{ID: "y", Kind: model.KindBackup, Target: model.Target{Type: model.TargetSFTP, Host: "box", Port: 2222, User: "backup", Path: "/srv/repo"}}
	env, opts, _ = r.env(sftp, store.Secrets{Passphrase: "p"})
	if !contains(join(env), "RESTIC_REPOSITORY=sftp://backup@box:2222//srv/repo") || len(opts) != 2 || opts[0] != "-o" {
		t.Errorf("sftp env/opts (absolute path needs the double slash): %v %v", env[len(env)-3:], opts)
	}
	sftp.Target.Path = "backups/repo" // relative to the remote home
	env, _, _ = r.env(sftp, store.Secrets{Passphrase: "p"})
	if !contains(join(env), "RESTIC_REPOSITORY=sftp://backup@box:2222/backups/repo") {
		t.Errorf("sftp relative path: %v", env[len(env)-3:])
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func join(env []string) string {
	out := ""
	for _, e := range env {
		out += e + "\n"
	}
	return out
}

// stderr as restic 0.19.1 printed it on ZimaOS 1.7.1 when restoring onto an
// exFAT USB disk (measured) versus a real failure.
func TestAttributeErrorsOnlyCoverOwnership(t *testing.T) {
	exfat := []byte(`{"message_type":"error","error":{"message":"lchown /media/sda/x/DATA: operation not permitted"},"during":"restore","item":"/DATA"}
{"message_type":"exit_error","code":1,"message":"Fatal: There were 1 errors"}
`)
	if n, only := attributeErrors(exfat); n != 1 || !only {
		t.Fatalf("exfat stderr: n=%d only=%v", n, only)
	}
	real := []byte(`{"message_type":"error","error":{"message":"lchown /media/sda/x: operation not permitted"},"during":"restore","item":"/x"}
{"message_type":"error","error":{"message":"open /media/sda/x/f: no space left on device"},"during":"restore","item":"/x/f"}
`)
	if _, only := attributeErrors(real); only {
		t.Fatal("a real error hid behind an ownership error")
	}
	if n, only := attributeErrors(nil); n != 0 || only {
		t.Fatal("no errors must not count as attribute errors")
	}
}

func TestCloudTargetUsesZimaOSRcloneConfig(t *testing.T) {
	r := newRunner(t)
	r.Rclone = "/usr/bin/rclone"
	job := &model.Job{ID: "c", Kind: model.KindBackup, Target: model.Target{Type: model.TargetCloud, Remote: "google_drive_252f21c18474", Path: "/Backups/"}}
	env, opts, err := r.env(job, store.Secrets{Passphrase: "p"})
	if err != nil {
		t.Fatal(err)
	}
	joined := join(env)
	if !contains(joined, "RESTIC_REPOSITORY=rclone:google_drive_252f21c18474:Backups") || !contains(joined, "RCLONE_CONFIG=/var/lib/casaos/rclone.conf") {
		t.Fatalf("cloud env: %v", env[len(env)-3:])
	}
	if o := strings.Join(opts, " "); !contains(o, "rclone.connections=8") || !contains(o, "rclone.program=/usr/bin/rclone") {
		t.Fatalf("cloud opts: %v", opts)
	}
}

func TestRateMeterUsesRecentWindow(t *testing.T) {
	var m rateMeter
	if got := m.update(0, 0); got != 0 {
		t.Fatalf("first sample must not divide by zero: %d", got)
	}
	m.update(1000, 1)
	if got := m.update(3000, 2); got != 1500 { // (3000-0)/(2-0)
		t.Fatalf("rate over 2 s = %d", got)
	}
	for s := 3; s <= 20; s++ {
		m.update(int64(3000+(s-2)*100), float64(s)) // slows to 100 B/s
	}
	if got := m.update(4900, 21); got < 90 || got > 110 {
		t.Fatalf("recent rate = %d, want ~100 (old fast samples must drop out)", got)
	}
}
