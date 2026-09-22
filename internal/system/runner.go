package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chicohaager/zima-backup/internal/backup"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/store"
)

// Runner executes system jobs: it decides the sources, writes manifest
// and database copies into the module's staging dir, then hands the job
// to the backup runner with --one-file-system (the data partition has
// other disks mounted below it — measured: /DATA/.media/sda is a 3.6 T
// exFAT disk, /DATA/srtest/* are other partitions).
type Runner struct {
	Backup    *backup.Runner
	Layout    Layout
	Cmd       Commander
	ModuleDir string // /DATA/AppData/zbackup
}

var _ engine.Runner = (*Runner)(nil)

// Staging is the directory manifest and database copies are written to.
func (r *Runner) Staging() string { return filepath.Join(r.ModuleDir, StagingName) }

// Run prepares the staging dir and runs the backup.
func (r *Runner) Run(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
	progress(0, "init")
	srcs, err := Sources(r.Layout, job.IncludeData)
	if err != nil {
		return model.Result{Code: model.CodeFailed, Message: err.Error()}
	}
	staging := r.Staging()
	if _, warnings, err := Prepare(ctx, r.Layout, r.Cmd, staging, srcs, job.IncludeData); err != nil {
		return model.Result{Code: model.CodeFailed, Message: "manifest: " + err.Error()}
	} else {
		for _, w := range warnings {
			engine.Log(ctx, "manifest: "+w)
		}
	}
	j := prepareJob(job, srcs, staging)
	return r.Backup.Run(ctx, &j, sec, progress)
}

// prepareJob is the job the backup runner sees: the decided sources (plus
// the staging dir when it is not inside one of them), one file system,
// and the restore scratch excluded.
func prepareJob(job *model.Job, srcs []string, staging string) model.Job {
	j := *job
	j.Sources = append([]string{}, srcs...)
	if !underAny(staging, srcs) {
		j.Sources = append(j.Sources, staging)
	}
	j.OneFileSystem = true
	// the restore scratch and the lower mount point live in staging too; they never belong in a snapshot
	j.Excludes = append(append([]string{}, job.Excludes...), filepath.Join(staging, "restore-*"), filepath.Join(staging, "lower"))
	return j
}

// Restore returns the operation that restores snapshot into a scratch
// directory, applies it to the running system and cleans up. It is an
// engine.Operation so it shows in the history and cannot overlap a run.
func (r *Runner) Restore(snapshot string, opts Options) engine.Operation {
	return func(ctx context.Context, job *model.Job, sec store.Secrets, progress engine.Progress) model.Result {
		if opts.ModuleDir == "" {
			opts.ModuleDir = r.ModuleDir
		}
		root := filepath.Join(r.Staging(), "restore-"+shortID(snapshot))
		_ = os.RemoveAll(root)
		defer os.RemoveAll(root)
		res := r.Backup.Restore(snapshot, nil, root)(ctx, job, sec, func(f float64, _ string) { progress(f*0.8, "restore") })
		if !res.Success {
			return res
		}
		progress(0.8, "apply")
		rep, err := Apply(ctx, r.Layout, r.Cmd, func(s string) { engine.Log(ctx, s) }, root, opts)
		if err != nil {
			code := model.CodeFailed
			if errors.Is(err, ErrVersionMismatch) {
				code = model.CodeVersionMismatch
			}
			return model.Result{Code: code, Message: err.Error(), SnapshotID: snapshot}
		}
		msg := fmt.Sprintf("system restored from %s: %d files in /etc, %d databases, %d apps started", shortID(snapshot), rep.FilesWritten, len(rep.Databases), len(rep.AppsStarted))
		if len(rep.AppsFailed) > 0 {
			msg += fmt.Sprintf(", %d apps failed to start (%s)", len(rep.AppsFailed), strings.Join(rep.AppsFailed, ", "))
		}
		msg += " — reboot to finish"
		return model.Result{Success: true, Code: model.CodeSystemRestored, SnapshotID: snapshot, Files: int64(rep.FilesWritten), Message: msg}
	}
}

// PlanFor restores only the staging directory of a snapshot (manifest and
// database copies, a few MB) and reports what a full restore would do.
func (r *Runner) PlanFor(ctx context.Context, job *model.Job, sec store.Secrets, snapshot string) (Plan, error) {
	root := filepath.Join(r.Staging(), "restore-plan")
	_ = os.RemoveAll(root)
	defer os.RemoveAll(root)
	res := r.Backup.Restore(snapshot, []string{filepath.Join(r.ModuleDir, StagingName)}, root)(ctx, job, sec, func(float64, string) {})
	if !res.Success {
		return Plan{}, errors.New(res.Message)
	}
	m, err := ReadManifest(manifestPath(root, r.ModuleDir))
	if err != nil {
		return Plan{}, fmt.Errorf("this snapshot is not a system backup: %w", err)
	}
	p := Plan{Manifest: m, Apps: m.Apps, Databases: m.Databases, HasAppData: true, HasData: m.IncludeData}
	p.RunningVersion = readOSRelease(r.Layout.Path("/etc/os-release"))["VERSION"]
	p.VersionMatch = m.OSVersion() != "" && m.OSVersion() == p.RunningVersion
	if !p.VersionMatch {
		p.Warnings = append(p.Warnings, fmt.Sprintf("snapshot from ZimaOS %q, running %q", m.OSVersion(), p.RunningVersion))
	}
	return p, nil
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
