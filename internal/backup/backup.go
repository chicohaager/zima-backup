// Package backup runs backup jobs with restic: one repository per target,
// encrypted with the job's passphrase, snapshots tagged with the job id so
// several jobs may share a repository, retention through forget --prune.
//
// Every restic call uses --json; the message shapes below were measured
// with restic 0.19.1 (see the tests). Secrets travel through the process
// environment, never through argv.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/localfs"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// restic exit codes that carry meaning for the UI (restic ≥ 0.17).
const (
	exitRepoMissing   = 10
	exitRepoLocked    = 11
	exitWrongPassword = 12
)

// Runner executes backup jobs. It satisfies engine.Runner.
type Runner struct {
	Restic   string      // path to the restic binary
	Rclone   string      // path to rclone, used for smb targets
	CacheDir string      // restic cache; persisted so re-runs are fast
	Key      sshkey.Pair // the module's ssh key pair for sftp targets
}

var _ engine.Runner = (*Runner)(nil)

// Snapshot is one restic snapshot as shown to the UI.
type Snapshot struct {
	ID    string   `json:"id"`
	Short string   `json:"short_id"`
	Time  string   `json:"time"`
	Paths []string `json:"paths"`
	Files int64    `json:"files"`
	Bytes int64    `json:"bytes"`
}

// Node is one entry of a snapshot listing.
type Node struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size,omitempty"`
	Mtime string `json:"mtime"`
}

// Run performs the backup: initialise the repository on first use, back up
// the sources, then apply retention.
func (r *Runner) Run(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
	if job.Target.Type == model.TargetLocal {
		if err := localfs.EnsureTarget(job.Target.Path); err != nil {
			return model.Result{Code: model.CodeTargetUnavailable, Message: err.Error()}
		}
	}
	env, opts, err := r.env(job, sec)
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}
	}
	rc := &client{r: r, env: env, opts: opts}
	if res, ok := rc.ensureRepo(ctx); !ok {
		return res
	}

	args := []string{"backup", "--json", "--tag", tagOf(job)}
	for _, ex := range job.Excludes {
		args = append(args, "--exclude", ex)
	}
	args = append(args, job.Sources...)
	var summary struct {
		FilesNew       int64  `json:"files_new"`
		FilesChanged   int64  `json:"files_changed"`
		TotalFiles     int64  `json:"total_files_processed"`
		TotalBytes     int64  `json:"total_bytes_processed"`
		DataAdded      int64  `json:"data_added"`
		SnapshotID     string `json:"snapshot_id"`
		DryRun         bool   `json:"dry_run"`
		MessageTypeSet bool   `json:"-"`
	}
	res := rc.exec(ctx, args, func(msg map[string]json.RawMessage, raw []byte) {
		switch typeOf(msg) {
		case "status":
			var st struct {
				Percent float64 `json:"percent_done"`
			}
			_ = json.Unmarshal(raw, &st)
			progress(st.Percent, "backup")
		case "summary":
			_ = json.Unmarshal(raw, &summary)
			summary.MessageTypeSet = true
		}
	})
	if !res.Success {
		return res
	}
	if !summary.MessageTypeSet {
		return model.Result{Code: model.CodeFailed, Message: "restic finished without a summary"}
	}
	result := model.Result{Success: true, Code: model.CodeCompleted, SnapshotID: summary.SnapshotID,
		Files: summary.TotalFiles, Bytes: summary.TotalBytes,
		Message: fmt.Sprintf("snapshot %s: %d files, %d new, %d changed, %s added",
			short(summary.SnapshotID), summary.TotalFiles, summary.FilesNew, summary.FilesChanged, humanBytes(summary.DataAdded))}

	if keep := retentionArgs(job.Retention); len(keep) > 0 {
		progress(1, "retention")
		forget := append([]string{"forget", "--json", "--prune", "--tag", tagOf(job)}, keep...)
		if fr := rc.exec(ctx, forget, nil); !fr.Success {
			result.Message += " · retention failed: " + fr.Message
		}
	}
	return result
}

// Snapshots lists the job's snapshots, newest first.
func (r *Runner) Snapshots(ctx context.Context, job *model.Job, sec store.Secrets) ([]Snapshot, error) {
	env, opts, err := r.env(job, sec)
	if err != nil {
		return nil, err
	}
	rc := &client{r: r, env: env, opts: opts}
	out, res := rc.capture(ctx, []string{"snapshots", "--json", "--tag", tagOf(job)})
	if !res.Success {
		return nil, errors.New(res.Message)
	}
	var raw []struct {
		ID      string   `json:"id"`
		Short   string   `json:"short_id"`
		Time    string   `json:"time"`
		Paths   []string `json:"paths"`
		Summary struct {
			Files int64 `json:"total_files_processed"`
			Bytes int64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse snapshots: %w", err)
	}
	snaps := make([]Snapshot, 0, len(raw))
	for i := len(raw) - 1; i >= 0; i-- { // restic lists oldest first
		s := raw[i]
		snaps = append(snaps, Snapshot{ID: s.ID, Short: s.Short, Time: s.Time, Paths: s.Paths, Files: s.Summary.Files, Bytes: s.Summary.Bytes})
	}
	return snaps, nil
}

// List returns the direct children of path inside a snapshot (the roots
// when path is empty).
func (r *Runner) List(ctx context.Context, job *model.Job, sec store.Secrets, snapshot, path string) ([]Node, error) {
	env, opts, err := r.env(job, sec)
	if err != nil {
		return nil, err
	}
	rc := &client{r: r, env: env, opts: opts}
	args := []string{"ls", "--json", snapshot}
	if path != "" {
		args = append(args, path)
	}
	out, res := rc.capture(ctx, args)
	if !res.Success {
		return nil, errors.New(res.Message)
	}
	nodes := []Node{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var n struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			Type  string `json:"type"`
			Size  int64  `json:"size"`
			Mtime string `json:"mtime"`
			Kind  string `json:"message_type"`
		}
		if err := json.Unmarshal(sc.Bytes(), &n); err != nil || n.Kind != "node" {
			continue
		}
		if path != "" && (n.Path == path || filepath.Dir(n.Path) != path) {
			continue // restic echoes the directory itself and, without a path, every ancestor
		}
		if path == "" && strings.Count(strings.Trim(n.Path, "/"), "/") != 0 {
			continue
		}
		nodes = append(nodes, Node{Name: n.Name, Path: n.Path, Type: n.Type, Size: n.Size, Mtime: n.Mtime})
	}
	return nodes, nil
}

// Restore writes the given paths of a snapshot below target (empty target
// = original location). It is an engine.Operation so it shows in the
// history and cannot overlap a backup.
func (r *Runner) Restore(snapshot string, paths []string, target string) engine.Operation {
	return func(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
		env, opts, err := r.env(job, sec)
		if err != nil {
			return model.Result{Code: model.CodeFailed, Message: err.Error()}
		}
		rc := &client{r: r, env: env, opts: opts}
		if target == "" {
			target = "/"
		}
		args := []string{"restore", "--json", snapshot, "--target", target}
		for _, p := range paths {
			args = append(args, "--include", p)
		}
		var summary struct {
			Files int64 `json:"files_restored"`
			Bytes int64 `json:"bytes_restored"`
		}
		res := rc.exec(ctx, args, func(msg map[string]json.RawMessage, raw []byte) {
			switch typeOf(msg) {
			case "status":
				var st struct {
					Percent float64 `json:"percent_done"`
				}
				_ = json.Unmarshal(raw, &st)
				progress(st.Percent, "restore")
			case "summary":
				_ = json.Unmarshal(raw, &summary)
			}
		})
		if !res.Success {
			return res
		}
		msg := fmt.Sprintf("restored %d files (%s) from %s to %s", summary.Files, humanBytes(summary.Bytes), short(snapshot), target)
		if res.Message != "" {
			msg += " · " + res.Message
		}
		return model.Result{Success: true, Code: model.CodeRestored, SnapshotID: snapshot, Files: summary.Files, Bytes: summary.Bytes, Message: msg}
	}
}

// Check verifies the repository structure.
func (r *Runner) Check() engine.Operation {
	return func(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
		env, opts, err := r.env(job, sec)
		if err != nil {
			return model.Result{Code: model.CodeFailed, Message: err.Error()}
		}
		rc := &client{r: r, env: env, opts: opts}
		var summary struct {
			Errors int `json:"num_errors"`
		}
		res := rc.exec(ctx, []string{"check", "--json"}, func(msg map[string]json.RawMessage, raw []byte) {
			if typeOf(msg) == "summary" {
				_ = json.Unmarshal(raw, &summary)
			}
		})
		if !res.Success {
			return res
		}
		if summary.Errors > 0 {
			return model.Result{Code: model.CodeCheckFailed, Message: fmt.Sprintf("%d errors found", summary.Errors)}
		}
		return model.Result{Success: true, Code: model.CodeCheckOK, Message: "repository is consistent"}
	}
}

// --- helpers ---

// env builds the restic environment (repository, passphrase, backend
// credentials, cache) and the extended options restic only accepts as
// "-o key=value". Nothing secret goes into argv.
func (r *Runner) env(job *model.Job, sec store.Secrets) (env, opts []string, err error) {
	if sec.Passphrase == "" {
		return nil, nil, errors.New("no passphrase stored for this job")
	}
	env = append(os.Environ(),
		"RESTIC_PASSWORD="+sec.Passphrase,
		"RESTIC_CACHE_DIR="+r.CacheDir,
		"RESTIC_PROGRESS_FPS=2",
	)
	t := job.Target
	switch t.Type {
	case model.TargetLocal:
		env = append(env, "RESTIC_REPOSITORY="+t.Path)
	case model.TargetSSH, model.TargetSFTP:
		// In restic's URL form a single slash after the host means "relative
		// to the user's home"; an absolute path needs a double slash
		// (measured: sftp://user@host/srv/repo landed in ~/srv/repo on the target).
		hostPort := t.Host
		if t.Port != 0 {
			hostPort = fmt.Sprintf("%s:%d", t.Host, t.Port)
		}
		env = append(env, "RESTIC_REPOSITORY=sftp://"+url.PathEscape(t.User)+"@"+hostPort+"/"+t.Path)
		// restic runs "ssh" from the base image; our own key, no password prompt.
		if err := r.Key.Ensure(); err != nil {
			return nil, nil, err
		}
		opts = append(opts, "-o", "sftp.args="+strings.Join(r.Key.SSHArgs(), " "))
	case model.TargetS3:
		scheme := "https"
		if t.Insecure {
			scheme = "http"
		}
		env = append(env, "RESTIC_REPOSITORY="+fmt.Sprintf("s3:%s://%s/%s/%s", scheme, t.Host, t.Bucket, strings.Trim(t.Path, "/")),
			"AWS_ACCESS_KEY_ID="+t.User, "AWS_SECRET_ACCESS_KEY="+sec.TargetSecret)
		if t.Region != "" {
			env = append(env, "AWS_DEFAULT_REGION="+t.Region)
		}
	case model.TargetSMB:
		// restic's rclone backend with a remote configured through the
		// environment: no rclone.conf on disk, the password stays in memory.
		obscured, err := r.obscure(sec.TargetSecret)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, "RESTIC_REPOSITORY=rclone:smb:"+t.Share+"/"+strings.Trim(t.Path, "/"),
			"RCLONE_CONFIG_SMB_TYPE=smb", "RCLONE_CONFIG_SMB_HOST="+t.Host,
			"RCLONE_CONFIG_SMB_USER="+t.User, "RCLONE_CONFIG_SMB_PASS="+obscured)
		if t.Port != 0 {
			env = append(env, "RCLONE_CONFIG_SMB_PORT="+strconv.Itoa(t.Port))
		}
		if r.Rclone != "" {
			opts = append(opts, "-o", "rclone.program="+r.Rclone)
		}
	default:
		return nil, nil, fmt.Errorf("target type %q is not supported for backups", t.Type)
	}
	return env, opts, nil
}

// obscure runs `rclone obscure` so the password matches rclone's stored form.
func (r *Runner) obscure(pw string) (string, error) {
	bin := r.Rclone
	if bin == "" {
		bin = "rclone"
	}
	cmd := exec.Command(bin, "obscure", "-")
	cmd.Stdin = strings.NewReader(pw)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rclone obscure: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// client binds one job's environment and options to the restic calls.
type client struct {
	r    *Runner
	env  []string
	opts []string
}

// ensureRepo initialises the repository when restic reports it missing.
func (c *client) ensureRepo(ctx context.Context) (model.Result, bool) {
	_, res := c.capture(ctx, []string{"cat", "config"})
	if res.Success {
		return res, true
	}
	if res.Code != codeRepoMissing {
		return res, false
	}
	_, res = c.capture(ctx, []string{"init", "--json"})
	if !res.Success {
		return res, false
	}
	return model.Result{Success: true}, true
}

const codeRepoMissing = "repo_missing" // internal only; never reaches the UI

type lineHandler func(msg map[string]json.RawMessage, raw []byte)

// exec runs restic, feeds JSON lines to onLine and classifies the outcome.
func (c *client) exec(ctx context.Context, args []string, onLine lineHandler) model.Result {
	cmd := exec.CommandContext(ctx, c.r.Restic, append(append([]string{}, c.opts...), args...)...)
	cmd.Env = c.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return model.Result{Code: model.CodeFailed, Message: "start restic: " + err.Error()}
	}
	var lastErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		raw := sc.Bytes()
		var msg map[string]json.RawMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if typeOf(msg) == "exit_error" {
			_ = json.Unmarshal(raw, &lastErr)
			continue
		}
		if onLine != nil {
			onLine(msg, append([]byte(nil), raw...))
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	err = cmd.Wait()
	if err == nil {
		return model.Result{Success: true}
	}
	if ctx.Err() != nil {
		return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}
	}
	if n, only := attributeErrors(stderr.Bytes()); only {
		// restic 0.19.1 exits 1 after restoring everything when the target
		// filesystem refuses lchown (measured on ZimaOS 1.7.1 with a USB
		// exFAT disk: summary complete, one "lchown …: operation not
		// permitted" error). The data is there; say what is missing.
		return model.Result{Success: true, Message: fmt.Sprintf("ownership of %d items could not be set on this filesystem", n)}
	}
	return classify(err, lastErr.Code, lastErr.Message, stderr.String())
}

// attributeErrors counts restic's JSON error lines on stderr and says
// whether every one of them is an ownership/permission error.
func attributeErrors(stderr []byte) (n int, only bool) {
	for _, line := range bytes.Split(stderr, []byte("\n")) {
		var m struct {
			Type string `json:"message_type"`
			Err  struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &m) != nil || m.Type != "error" {
			continue
		}
		msg := m.Err.Message
		if (strings.HasPrefix(msg, "lchown ") || strings.HasPrefix(msg, "chmod ") || strings.HasPrefix(msg, "chown ")) && strings.HasSuffix(msg, "operation not permitted") {
			n++
			continue
		}
		return n, false
	}
	return n, n > 0
}

// capture is exec with stdout collected, for commands whose whole output
// is one JSON document.
func (c *client) capture(ctx context.Context, args []string) ([]byte, model.Result) {
	cmd := exec.CommandContext(ctx, c.r.Restic, append(append([]string{}, c.opts...), args...)...)
	cmd.Env = c.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), model.Result{Success: true}
	}
	if ctx.Err() != nil {
		return nil, model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}
	}
	var lastErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		var m map[string]json.RawMessage
		if json.Unmarshal(line, &m) == nil && typeOf(m) == "exit_error" {
			_ = json.Unmarshal(line, &lastErr)
		}
	}
	return nil, classify(err, lastErr.Code, lastErr.Message, stderr.String())
}

// classify maps a restic failure to a result code.
func classify(err error, code int, message, stderr string) model.Result {
	var exit *exec.ExitError
	if errors.As(err, &exit) && code == 0 {
		code = exit.ExitCode()
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = strings.TrimSpace(stderr)
	}
	if msg == "" {
		msg = err.Error()
	}
	switch code {
	case exitRepoMissing:
		return model.Result{Code: codeRepoMissing, Message: msg}
	case exitRepoLocked:
		return model.Result{Code: model.CodeRepoLocked, Message: msg}
	case exitWrongPassword:
		return model.Result{Code: model.CodePassphraseWrong, Message: msg}
	}
	if strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "Could not resolve") {
		return model.Result{Code: model.CodeTargetUnavailable, Message: msg}
	}
	return model.Result{Code: model.CodeFailed, Message: msg}
}

func typeOf(msg map[string]json.RawMessage) string {
	var t string
	_ = json.Unmarshal(msg["message_type"], &t)
	return t
}

func tagOf(job *model.Job) string { return "zbackup:" + job.ID }

func retentionArgs(r model.Retention) []string {
	var a []string
	if r.KeepLast > 0 {
		a = append(a, "--keep-last", strconv.Itoa(r.KeepLast))
	}
	if r.KeepDaily > 0 {
		a = append(a, "--keep-daily", strconv.Itoa(r.KeepDaily))
	}
	if r.KeepWeekly > 0 {
		a = append(a, "--keep-weekly", strconv.Itoa(r.KeepWeekly))
	}
	if r.KeepMonthly > 0 {
		a = append(a, "--keep-monthly", strconv.Itoa(r.KeepMonthly))
	}
	return a
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
