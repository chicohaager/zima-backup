// Package mirror runs sync jobs: a one-way copy of folders to a target.
// Each source folder lands as <target>/<basename>, so several sources
// share one target without touching each other, and "delete extraneous"
// only ever removes files inside those folders.
//
// Local and ssh targets use rsync from the base image; sftp, smb and s3
// targets use rclone with a remote configured through the environment.
// Output shapes below were measured with rsync 3.2.7/3.4.1 and rclone
// 1.74.3 (see the tests). Secrets travel through the environment only.
package mirror

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/localfs"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/mounts"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// Runner executes sync jobs. It satisfies engine.Runner.
type Runner struct {
	Rsync  string      // path to rsync (local and ssh targets)
	Rclone string      // path to rclone (sftp, smb, s3 targets)
	Key    sshkey.Pair // the module's ssh key pair for ssh/sftp targets
}

var _ engine.Runner = (*Runner)(nil)

// Preview is the dry-run summary shown before the first real run.
// Detailed says whether the tool told new and changed files apart
// (rsync does, rclone reports copies only).
type Preview struct {
	FilesCopy    int64 `json:"files_copy"`
	FilesDelete  int64 `json:"files_delete"`
	Bytes        int64 `json:"bytes"`
	Detailed     bool  `json:"detailed"`
	FilesNew     int64 `json:"files_new"`
	FilesChanged int64 `json:"files_changed"`
}

// rsync exit codes that carry meaning for the UI (rsync(1) EXIT VALUES).
const (
	rsyncPartial     = 23  // some files/attrs were not transferred
	rsyncVanished    = 24  // source files vanished during the run: not an error
	rsyncSocketIO    = 10  // error in socket I/O
	rsyncStreamError = 12  // error in rsync protocol data stream (remote died)
	rsyncTimeout     = 30  // timeout in data send/receive
	rsyncConnTimeout = 35  // timeout waiting for daemon connection
	rsyncSSHFailure  = 255 // ssh could not connect or authenticate
)

// Run mirrors every source to the target.
func (r *Runner) Run(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
	switch job.Target.Type {
	case model.TargetLocal, model.TargetSSH:
		res, _ := r.runRsync(ctx, job, false, progress)
		return res
	case model.TargetSFTP, model.TargetSMB, model.TargetS3, model.TargetCloud:
		res, _ := r.runRclone(ctx, job, sec, false, progress)
		return res
	}
	return model.Result{Code: model.CodeFailed, Message: fmt.Sprintf("target type %q is not supported for sync", job.Target.Type)}
}

// Preview runs the job as a dry run and summarises what it would do.
func (r *Runner) Preview(ctx context.Context, job *model.Job, sec store.Secrets) (Preview, error) {
	quiet := func(float64, string) {}
	switch job.Target.Type {
	case model.TargetLocal, model.TargetSSH:
		res, st := r.runRsync(ctx, job, true, quiet)
		if !res.Success {
			return Preview{}, errors.New(res.Message)
		}
		return Preview{FilesCopy: st.transferred, FilesDelete: st.deleted, Bytes: st.bytes,
			Detailed: true, FilesNew: st.createdReg, FilesChanged: st.transferred - st.createdReg}, nil
	case model.TargetSFTP, model.TargetSMB, model.TargetS3, model.TargetCloud:
		res, tot := r.runRclone(ctx, job, sec, true, quiet)
		if !res.Success {
			return Preview{}, errors.New(res.Message)
		}
		return Preview{FilesCopy: tot.dryCopies, FilesDelete: tot.dryDeletes, Bytes: tot.dryBytes}, nil
	}
	return Preview{}, fmt.Errorf("target type %q is not supported for sync", job.Target.Type)
}

// --- rsync ---

var (
	rsyncPercent = regexp.MustCompile(`^\s*([\d,.]+)\s+(\d{1,3})%\s+([\d.]+)([kMG]?B)/s`)
	rsyncStat    = regexp.MustCompile(`^(Number of created files|Number of deleted files|Number of regular files transferred|Total transferred file size): (\d+)(?: \((?:reg: (\d+))?)?`)
)

// rsyncStats are the --stats counters a run ends with.
type rsyncStats struct {
	createdReg, deleted, transferred, bytes int64
	seen                                    bool
}

func (r *Runner) runRsync(ctx context.Context, job *model.Job, dryRun bool, progress engine.Progress) (model.Result, rsyncStats) {
	args := []string{"-a", "--no-inc-recursive", "--info=progress2", "--stats", "--no-human-readable"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if job.DeleteExtraneous {
		args = append(args, "--delete")
	}
	for _, ex := range job.Excludes {
		args = append(args, "--exclude", ex)
	}
	t := job.Target
	var dest string
	switch t.Type {
	case model.TargetLocal:
		if mounts.IsCloudMount(t.Path) {
			return model.Result{Code: model.CodeFailed, Message: "this folder is a cloud drive mounted by Files — edit the job and choose the cloud drive as target, which talks to the drive directly"}, rsyncStats{}
		}
		ensure := localfs.EnsureTarget
		if dryRun {
			ensure = localfs.Check // a preview creates nothing
		}
		if err := ensure(t.Path); err != nil {
			return model.Result{Code: model.CodeTargetUnavailable, Message: err.Error()}, rsyncStats{}
		}
		dest = filepath.Clean(t.Path) + "/"
		probeDir := filepath.Clean(t.Path)
		for {
			if st, err := os.Stat(probeDir); err == nil && st.IsDir() {
				break
			}
			probeDir = filepath.Dir(probeDir) // in a preview the target may not exist yet
		}
		args = append(args, attrFlags(probeTarget(probeDir))...)
	case model.TargetSSH:
		if err := r.Key.Ensure(); err != nil {
			return model.Result{Code: model.CodeFailed, Message: err.Error()}, rsyncStats{}
		}
		ssh := append([]string{"ssh"}, r.Key.SSHArgs()...)
		if t.Port != 0 {
			ssh = append(ssh, "-p", strconv.Itoa(t.Port))
		}
		args = append(args, "-e", strings.Join(ssh, " "))
		dest = t.User + "@" + t.Host + ":" + strings.TrimSuffix(t.Path, "/") + "/"
	}
	for _, src := range job.Sources {
		args = append(args, strings.TrimSuffix(src, "/")) // no trailing slash: copy the folder itself
	}
	args = append(args, dest)

	engine.Log(ctx, "rsync "+strings.Join(args, " "))
	progress(0, "sync")
	cmd := exec.CommandContext(ctx, r.Rsync, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}, rsyncStats{}
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}, rsyncStats{}
		}
		return model.Result{Code: model.CodeFailed, Message: "start rsync: " + err.Error()}, rsyncStats{}
	}
	var stats rsyncStats
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sc.Split(scanCRLF)
	for sc.Scan() {
		line := sc.Text()
		if m := rsyncPercent.FindStringSubmatch(line); m != nil {
			pct, _ := strconv.Atoi(m[2])
			progress(float64(pct)/100, "sync")
			done, _ := strconv.ParseInt(strings.NewReplacer(",", "", ".", "").Replace(m[1]), 10, 64)
			var total int64
			if pct > 0 {
				total = done * 100 / int64(pct)
			}
			engine.Report(ctx, engine.Transfer{Done: done, Total: total, Rate: rsyncRate(m[3], m[4])})
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			engine.Log(ctx, line)
		}
		stats.apply(line)
	}
	err = cmd.Wait()
	for _, l := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if l != "" {
			engine.Log(ctx, l)
		}
	}
	if ctx.Err() != nil {
		return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}, stats
	}
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}, stats
	}
	res := model.Result{Files: stats.transferred, Bytes: stats.bytes}
	summary := fmt.Sprintf("%d files copied (%d new, %d changed), %d deleted, %s",
		stats.transferred, stats.createdReg, stats.transferred-stats.createdReg, stats.deleted, humanBytes(stats.bytes))
	switch code {
	case 0, rsyncVanished:
		if !stats.seen {
			return model.Result{Code: model.CodeFailed, Message: "rsync finished without statistics"}, stats
		}
		res.Success, res.Code, res.Message = true, model.CodeCompleted, summary
	case rsyncPartial:
		res.Code, res.Message = model.CodePartial, summary+" · not everything could be copied: "+firstErrors(stderr.String())
	case rsyncSocketIO, rsyncStreamError, rsyncTimeout, rsyncConnTimeout, rsyncSSHFailure:
		res.Code, res.Message = model.CodeTargetUnavailable, firstErrors(stderr.String())
	default:
		res.Code, res.Message = model.CodeFailed, fmt.Sprintf("rsync exit %d: %s", code, firstErrors(stderr.String()))
	}
	return res, stats
}

// probeTarget measures what the target filesystem keeps, instead of
// guessing from its name: exFAT and FAT refuse chown, so `rsync -a` ends
// with exit 23 on every run there (measured on ZimaOS 1.7.1 with a USB
// exFAT disk, where chmod is silently ignored too); FAT rounds mtimes
// to 2 s. Only root can preserve ownership at all, so the probe runs as
// root only. A probe that cannot run reports "keeps everything" and
// leaves rsync to say what it cannot do.
func probeTarget(dir string) (chownErr error, mtimeDrift time.Duration) {
	if os.Geteuid() != 0 {
		return nil, 0
	}
	f, err := os.CreateTemp(dir, ".zbackup-probe-*")
	if err != nil {
		return nil, 0
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	chownErr = os.Lchown(name, 65534, 65534)
	want := time.Date(2020, 1, 2, 3, 4, 5, 500_000_000, time.UTC)
	if err := os.Chtimes(name, want, want); err == nil {
		if st, err := os.Stat(name); err == nil {
			mtimeDrift = st.ModTime().Sub(want)
			if mtimeDrift < 0 {
				mtimeDrift = -mtimeDrift
			}
		}
	}
	return chownErr, mtimeDrift
}

// attrFlags turns the probe into rsync options: no ownership/permission
// syncing where the filesystem has none, a 2 s window where it rounds.
func attrFlags(chownErr error, mtimeDrift time.Duration) []string {
	var flags []string
	if chownErr != nil {
		flags = append(flags, "--no-owner", "--no-group", "--no-perms")
	}
	if mtimeDrift > 100*time.Millisecond {
		flags = append(flags, "--modify-window=2")
	}
	return flags
}

// rsyncRate turns "12.34" + "MB" (rsync prints decimal units) into bytes/s.
func rsyncRate(num, unit string) int64 {
	v, _ := strconv.ParseFloat(num, 64)
	switch unit {
	case "kB":
		v *= 1e3
	case "MB":
		v *= 1e6
	case "GB":
		v *= 1e9
	}
	return int64(v)
}

func (s *rsyncStats) apply(line string) {
	m := rsyncStat.FindStringSubmatch(line)
	if m == nil {
		return
	}
	s.seen = true
	n, _ := strconv.ParseInt(m[2], 10, 64)
	switch m[1] {
	case "Number of created files":
		s.createdReg, _ = strconv.ParseInt(m[3], 10, 64) // directories are not "new files"
	case "Number of deleted files":
		s.deleted = n
	case "Number of regular files transferred":
		s.transferred = n
	case "Total transferred file size":
		s.bytes = n
	}
}

// scanCRLF splits on \n and on the \r that --info=progress2 uses to
// redraw its line.
func scanCRLF(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// firstErrors keeps the rsync/rclone diagnostics a user can act on.
func firstErrors(stderr string) string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "rsync error:") {
			continue
		}
		out = append(out, l)
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return strings.TrimSpace(stderr)
	}
	return strings.Join(out, " · ")
}

// --- rclone ---

// remote is the name of the environment-configured rclone remote.
const remote = "zb"

func (r *Runner) runRclone(ctx context.Context, job *model.Job, sec store.Secrets, dryRun bool, progress engine.Progress) (model.Result, rcloneTotals) {
	env, base, err := r.rcloneEnv(job, sec)
	if errors.Is(err, sshkey.ErrHostUnreachable) {
		return model.Result{Code: model.CodeTargetUnavailable, Message: err.Error()}, rcloneTotals{}
	}
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}, rcloneTotals{}
	}
	var total rcloneTotals
	n := float64(len(job.Sources))
	for i, src := range job.Sources {
		dest := base + "/" + filepath.Base(src)
		verb := "copy"
		if job.DeleteExtraneous {
			verb = "sync"
		}
		args := []string{verb, src, dest, "--use-json-log", "--stats=2s", "--stats-log-level", "NOTICE", "--stats-one-line"}
		if job.Target.Type == model.TargetCloud {
			// a cloud drive costs a round trip per file (measured on Google
			// Drive: 78 small files took minutes at rclone's default of 4);
			// more parallel transfers hide that latency
			args = append(args, "--transfers", "8", "--checkers", "16")
		}
		if dryRun {
			args = append(args, "--dry-run")
		}
		for _, ex := range job.Excludes {
			args = append(args, "--exclude", ex)
		}
		done := float64(i)
		engine.Log(ctx, "folder "+filepath.Base(src)+" ("+strconv.Itoa(i+1)+"/"+strconv.Itoa(len(job.Sources))+")")
		res, part := r.rcloneOnce(ctx, env, args, func(f float64) { progress((done+f)/n, "sync") })
		total.add(part)
		if !res.Success {
			if res.Code == model.CodePartial {
				res.Message = total.summary() + " · " + res.Message
			}
			return res, total
		}
	}
	res := model.Result{Success: true, Code: model.CodeCompleted, Files: total.transfers, Bytes: total.bytes, Message: total.summary()}
	if total.errors > 0 {
		res.Success, res.Code = false, model.CodePartial
	}
	return res, total
}

// rcloneTotals accumulate over the sources of one job.
type rcloneTotals struct {
	transfers, deletes, bytes, errors int64
	dryCopies, dryDeletes, dryBytes   int64
}

func (t *rcloneTotals) add(o rcloneTotals) {
	t.transfers += o.transfers
	t.deletes += o.deletes
	t.bytes += o.bytes
	t.errors += o.errors
	t.dryCopies += o.dryCopies
	t.dryDeletes += o.dryDeletes
	t.dryBytes += o.dryBytes
}

func (t rcloneTotals) summary() string {
	if t.dryCopies > 0 || t.dryDeletes > 0 {
		return fmt.Sprintf("%d files to copy, %d to delete, %s", t.dryCopies, t.dryDeletes, humanBytes(t.dryBytes))
	}
	return fmt.Sprintf("%d files copied, %d deleted, %s", t.transfers, t.deletes, humanBytes(t.bytes))
}

// rcloneOnce runs one rclone command and reads its JSON log: "stats"
// objects for progress and totals, "skipped" objects in a dry run, and
// the last notice/critical message as the error text.
func (r *Runner) rcloneOnce(ctx context.Context, env, args []string, progress func(float64)) (model.Result, rcloneTotals) {
	engine.Log(ctx, "rclone "+strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, r.Rclone, args...)
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}, rcloneTotals{}
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}, rcloneTotals{}
		}
		return model.Result{Code: model.CodeFailed, Message: "start rclone: " + err.Error()}, rcloneTotals{}
	}
	var tot rcloneTotals
	var lastMsg string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var line struct {
			Level   string `json:"level"`
			Msg     string `json:"msg"`
			Skipped string `json:"skipped"`
			Size    int64  `json:"size"`
			Stats   *struct {
				Bytes      int64   `json:"bytes"`
				TotalBytes int64   `json:"totalBytes"`
				Transfers  int64   `json:"transfers"`
				Deletes    int64   `json:"deletes"`
				Errors     int64   `json:"errors"`
				Speed      float64 `json:"speed"`
			} `json:"stats"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			lastMsg = strings.TrimSpace(sc.Text()) // rclone prints plain text before the log is set up
			engine.Log(ctx, lastMsg)
			continue
		}
		if line.Stats == nil && line.Msg != "" {
			engine.Log(ctx, strings.TrimSpace(line.Msg))
		}
		switch {
		case line.Stats != nil:
			tot.transfers, tot.deletes, tot.bytes, tot.errors = line.Stats.Transfers, line.Stats.Deletes, line.Stats.Bytes, line.Stats.Errors
			if line.Stats.TotalBytes > 0 {
				progress(float64(line.Stats.Bytes) / float64(line.Stats.TotalBytes))
			}
			engine.Report(ctx, engine.Transfer{Done: line.Stats.Bytes, Total: line.Stats.TotalBytes, Rate: int64(line.Stats.Speed)})
		case line.Skipped == "copy":
			tot.dryCopies++
			tot.dryBytes += line.Size
		case line.Skipped == "delete":
			tot.dryDeletes++
		case line.Level == "error" || line.Level == "critical" || (line.Level == "notice" && strings.HasPrefix(line.Msg, "Failed")):
			lastMsg = strings.TrimSpace(line.Msg)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}, tot
	}
	if err == nil {
		return model.Result{Success: true}, tot
	}
	msg := lastMsg
	if msg == "" {
		msg = err.Error()
	}
	switch {
	case tot.transfers > 0 || tot.errors > 0 && !strings.Contains(msg, "directory not found") && !unreachable(msg):
		return model.Result{Code: model.CodePartial, Message: msg}, tot
	case unreachable(msg):
		return model.Result{Code: model.CodeTargetUnavailable, Message: msg}, tot
	}
	return model.Result{Code: model.CodeFailed, Message: msg}, tot
}

func unreachable(msg string) bool {
	for _, s := range []string{"couldn't connect", "connection refused", "no such host", "network is unreachable", "i/o timeout", "Failed to create file system", "directory not found"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// rcloneEnv configures the remote through RCLONE_CONFIG_ZB_* variables
// (option names as in `rclone help backend <type>`) and returns the
// destination prefix "zb:<path>".
func (r *Runner) rcloneEnv(job *model.Job, sec store.Secrets) (env []string, base string, err error) {
	set := func(k, v string) { env = append(env, "RCLONE_CONFIG_"+strings.ToUpper(remote)+"_"+k+"="+v) }
	env = append(os.Environ(), "LC_ALL=C")
	t := job.Target
	switch t.Type {
	case model.TargetSFTP:
		set("TYPE", "sftp")
		set("HOST", t.Host)
		set("USER", t.User)
		if t.Port != 0 {
			set("PORT", strconv.Itoa(t.Port))
		}
		if sec.TargetSecret != "" {
			pw, err := r.obscure(sec.TargetSecret)
			if err != nil {
				return nil, "", err
			}
			set("PASS", pw)
		} else {
			if err := r.Key.Ensure(); err != nil {
				return nil, "", err
			}
			set("KEY_FILE", r.Key.PrivatePath())
		}
		types, err := r.Key.HostKeyTypes(t.Host, t.Port)
		if err != nil {
			return nil, "", err
		}
		set("KNOWN_HOSTS_FILE", r.Key.KnownHostsPath())
		set("HOST_KEY_ALGORITHMS", strings.Join(types, " "))
		base = remote + ":" + strings.TrimSuffix(t.Path, "/")
	case model.TargetSMB:
		pw, err := r.obscure(sec.TargetSecret)
		if err != nil {
			return nil, "", err
		}
		set("TYPE", "smb")
		set("HOST", t.Host)
		set("USER", t.User)
		set("PASS", pw)
		if t.Port != 0 {
			set("PORT", strconv.Itoa(t.Port))
		}
		base = remote + ":" + t.Share + "/" + strings.Trim(t.Path, "/")
	case model.TargetCloud:
		// the drive ZimaOS Files is signed in to: rclone reads ZimaOS' own
		// config (raw upload measured at 3.2 MiB/s to Google Drive, against
		// 52 s per file through the FUSE mount)
		env = append(env, "RCLONE_CONFIG="+mounts.RcloneConfig)
		base = t.Remote + ":" + strings.Trim(t.Path, "/")
	case model.TargetS3:
		scheme := "https"
		if t.Insecure {
			scheme = "http"
		}
		set("TYPE", "s3")
		set("PROVIDER", "Other")
		set("ACCESS_KEY_ID", t.User)
		set("SECRET_ACCESS_KEY", sec.TargetSecret)
		set("ENDPOINT", scheme+"://"+t.Host)
		if t.Region != "" {
			set("REGION", t.Region)
		}
		base = remote + ":" + t.Bucket + "/" + strings.Trim(t.Path, "/")
	default:
		return nil, "", fmt.Errorf("target type %q is not an rclone target", t.Type)
	}
	return env, strings.TrimSuffix(base, "/"), nil
}

// obscure runs `rclone obscure` so the password matches rclone's stored form.
func (r *Runner) obscure(pw string) (string, error) {
	cmd := exec.Command(r.Rclone, "obscure", "-")
	cmd.Stdin = strings.NewReader(pw)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rclone obscure: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// --- shared ---

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
