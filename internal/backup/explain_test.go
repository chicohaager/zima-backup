package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// stderr/stdout pairs recorded on 2026-09-20 with restic 0.19.1 and rclone
// 1.74.3 against a Samba container; the addresses are the test ones.
// restic's own verdict arrives as the exit_error JSON line on stdout, the
// cause sits one line earlier on stderr with an "rclone:" prefix — which
// the old lastFatal() threw away.
const (
	rcloneNotice   = `rclone: 2026/09/20 14:31:41 NOTICE: Config file "/root/.config/rclone/rclone.conf" not found - using defaults` + "\n"
	stderrBadLogin = rcloneNotice + `rclone: 2026/09/20 14:31:46 CRITICAL: Failed to create file system for "smb:Test/repo": couldn't connect SMB: response error: The attempted logon is invalid. This is either due to a bad username or authentication information.` + "\n"
	stderrNoShare  = rcloneNotice + `rclone: 2026/09/20 14:31:50 CRITICAL: Failed to create file system for "smb:Nope/repo": couldn't initialize SMB: response error: {Network Name Not Found} The specified share name cannot be found on the remote server.` + "\n"
	stderrRefused  = rcloneNotice + `rclone: 2026/09/20 14:30:51 CRITICAL: Failed to create file system for "smb:Test/repo": couldn't connect SMB: dial tcp 127.0.0.1:1: connect: connection refused` + "\n"
	msgOpenFailed  = `Fatal: unable to open repository at rclone:smb:Test/repo: error talking HTTP to rclone: exit status 1`
	// a "config" *directory* in the repository: rclone answers 500 to Stat,
	// restic retries for minutes and, once cancelled, blames the context
	stderrConfigDir = `Stat(<config/>) returned error, retrying after 695.487922ms: unexpected HTTP response (500): 500 Internal Server Error
Stat(<config/>) returned error, retrying after 1.186560501s: unexpected HTTP response (500): 500 Internal Server Error
signal terminated received, cleaning up 
`
	msgConfigDir = "Fatal: unable to open config file: context canceled\nrclone:smb:Test/"
	// the tester's line (0.1.1, restic gave up by itself)
	msgConfigDirTester = "Fatal: unable to open config file: unexpected HTTP response (500): 500 Internal Server Error"
)

func exit1() error {
	cmd := exec.Command("sh", "-c", "exit 1")
	return cmd.Run()
}

func TestExplainNamesRcloneCause(t *testing.T) {
	cases := []struct {
		name, stderr, message, wantCode, wantIn, wantNotIn string
	}{
		{"bad login", stderrBadLogin, msgOpenFailed, model.CodeTargetAuth, "logon is invalid", "NOTICE"},
		{"no share", stderrNoShare, msgOpenFailed, model.CodeTargetShareMissing, "share name cannot be found", "error talking HTTP"},
		{"refused", stderrRefused, msgOpenFailed, model.CodeTargetUnavailable, "connection refused", "NOTICE"},
		{"config dir, cancelled", stderrConfigDir, msgConfigDir, model.CodeRepoUnreadable, "config", "context canceled"},
		{"config dir, tester", "", msgConfigDirTester, model.CodeRepoUnreadable, "config", ""},
		// no rclone involved: restic's own line stays the message
		{"plain fatal", "ssh: chatter\nFatal: wrong password or no key found\n", "", model.CodeFailed, "wrong password", "ssh: chatter"},
	}
	for _, c := range cases {
		res := explain(exit1(), 0, c.message, c.stderr)
		if res.Success {
			t.Errorf("%s: must not be a success", c.name)
		}
		if res.Code != c.wantCode {
			t.Errorf("%s: code %q, want %q (message %q)", c.name, res.Code, c.wantCode, res.Message)
		}
		if !strings.Contains(res.Message, c.wantIn) {
			t.Errorf("%s: message %q lacks %q", c.name, res.Message, c.wantIn)
		}
		if c.wantNotIn != "" && strings.Contains(res.Message, c.wantNotIn) {
			t.Errorf("%s: message %q still carries %q", c.name, res.Message, c.wantNotIn)
		}
		if strings.Contains(res.Message, "2026/09/20") || strings.HasPrefix(res.Message, "rclone:") {
			t.Errorf("%s: message %q keeps rclone's log prefix", c.name, res.Message)
		}
	}
}

func TestExplainKeepsResticExitCodes(t *testing.T) {
	var exitErr *exec.ExitError
	if !errors.As(exit1(), &exitErr) {
		t.Fatal("exit1 must be an ExitError")
	}
	if res := explain(exit1(), exitWrongPassword, "Fatal: wrong password", ""); res.Code != model.CodePassphraseWrong {
		t.Errorf("exit 12 = %q", res.Code)
	}
	if res := explain(exit1(), exitRepoLocked, "Fatal: locked", ""); res.Code != model.CodeRepoLocked {
		t.Errorf("exit 11 = %q", res.Code)
	}
	if res := explain(exit1(), exitRepoMissing, "Fatal: repository does not exist", ""); res.Code != codeRepoMissing {
		t.Errorf("exit 10 = %q", res.Code)
	}
}

// A repository probe that hangs (restic retrying a backend that answers
// 500) must end after repoProbeTimeout with repo_unreadable — not run into
// restic's own fifteen minutes, and not look like a user cancel.
func TestRepositoryProbeIsBounded(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "restic")
	// stands in for restic: `cat config` hangs, everything else fails loudly
	writeFile(t, fake, "#!/bin/sh\ncase \"$*\" in *'cat config'*) sleep 30;; esac\necho '{\"message_type\":\"exit_error\",\"code\":1,\"message\":\"fake\"}'\nexit 1\n")
	if err := os.Chmod(fake, 0755); err != nil {
		t.Fatal(err)
	}
	old := repoProbeTimeout
	repoProbeTimeout = time.Second
	defer func() { repoProbeTimeout = old }()
	r := &Runner{Restic: fake, CacheDir: filepath.Join(dir, "cache"), Key: sshkey.Pair{Dir: filepath.Join(dir, "keys")}}
	src := filepath.Join(dir, "src")
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	start := time.Now()
	res := r.Run(context.Background(), localJob(src, filepath.Join(dir, "repo")), store.Secrets{Passphrase: "pw"}, noProgress)
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("probe took %s, the deadline did not bite", d)
	}
	if res.Success || res.Code != model.CodeRepoUnreadable {
		t.Fatalf("got %+v, want repo_unreadable", res)
	}
	if !strings.Contains(res.Message, "config") {
		t.Errorf("message %q does not tell the user what to look for", res.Message)
	}
}
