package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/store"
)

// fakeRunner records calls and can block until released.
type fakeRunner struct {
	calls   int32
	block   chan struct{}
	secrets store.Secrets
	result  model.Result
}

func (f *fakeRunner) Run(ctx context.Context, j *model.Job, sec store.Secrets, p Progress) model.Result {
	atomic.AddInt32(&f.calls, 1)
	f.secrets = sec
	p(0.5, "half")
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return model.Result{Code: model.CodeCancelled, Message: ctx.Err().Error()}
		}
	}
	return f.result
}

func newEngine(t *testing.T, r Runner) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(st, map[string]Runner{model.KindBackup: r})
	if err != nil {
		t.Fatal(err)
	}
	return e, st
}

func TestPutKeepsSecretsOutOfJobsAndInKeys(t *testing.T) {
	e, st := newEngine(t, &fakeRunner{result: model.Result{Success: true, Code: model.CodeCompleted}})
	job := &model.Job{ID: "j1", Name: "one", Kind: model.KindBackup, Enabled: true, Passphrase: "pw",
		Sources: []string{"/DATA/x"}, Target: model.Target{Type: model.TargetS3, Secret: "s3"},
		Schedule: model.Schedule{Type: model.ScheduleInterval, IntervalMin: 60}}
	if err := e.Put(job); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Job("j1")
	if got.Passphrase != "" || got.Target.Secret != "" {
		t.Error("secrets left on the job")
	}
	if got.NextRunAt == 0 {
		t.Error("interval job not scheduled")
	}
	sec, _ := st.LoadSecrets("j1")
	if sec.Passphrase != "pw" || sec.TargetSecret != "s3" {
		t.Fatalf("secrets not stored: %+v", sec)
	}
	// update without secrets keeps them
	job2 := *got
	job2.Name = "renamed"
	if err := e.Put(&job2); err != nil {
		t.Fatal(err)
	}
	sec, _ = st.LoadSecrets("j1")
	if sec.Passphrase != "pw" {
		t.Error("update without passphrase wiped the stored one")
	}
}

func TestRunRecordsHistoryAndPassesSecrets(t *testing.T) {
	r := &fakeRunner{result: model.Result{Success: true, Code: model.CodeCompleted, Files: 3, SnapshotID: "abc"}}
	e, _ := newEngine(t, r)
	_ = e.Put(&model.Job{ID: "j", Name: "j", Kind: model.KindBackup, Enabled: true, Passphrase: "pw",
		Target: model.Target{Type: model.TargetLocal, Path: "/media/x"}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	e.Run("j")
	if r.secrets.Passphrase != "pw" {
		t.Error("runner did not receive the passphrase")
	}
	logs := e.Logs("j")
	if len(logs) != 1 || logs[0].Code != model.CodeCompleted || logs[0].Files != 3 || logs[0].SnapshotID != "abc" {
		t.Fatalf("history = %+v", logs)
	}
	j, _ := e.Job("j")
	if j.Running || j.LastResult == nil || !j.LastResult.Success || j.LastRunAt == 0 {
		t.Fatalf("job state after run: %+v", j)
	}
}

func TestOverlapIsSkipped(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{}), result: model.Result{Success: true, Code: model.CodeCompleted}}
	e, _ := newEngine(t, r)
	_ = e.Put(&model.Job{ID: "j", Name: "j", Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	done := make(chan struct{})
	go func() { e.Run("j"); close(done) }()
	waitFor(t, func() bool { j, _ := e.Job("j"); return j.Running && j.Progress == 0.5 })
	e.Run("j") // second call while blocked
	if logs := e.Logs("j"); len(logs) != 1 || logs[0].Code != model.CodeSkippedRunning {
		t.Fatalf("overlap not recorded as skipped: %+v", logs)
	}
	close(r.block)
	<-done
	if atomic.LoadInt32(&r.calls) != 1 {
		t.Fatalf("runner ran %d times, want 1", r.calls)
	}
}

func TestDeleteDuringRunCancelsAndWritesNothingBack(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	e, st := newEngine(t, r)
	_ = e.Put(&model.Job{ID: "j", Name: "j", Kind: model.KindBackup, Enabled: true, Passphrase: "pw",
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	done := make(chan struct{})
	go func() { e.Run("j"); close(done) }()
	waitFor(t, func() bool { j, _ := e.Job("j"); return j.Running })
	if err := e.Delete("j"); err != nil {
		t.Fatal(err)
	}
	<-done // the cancel must have released the runner
	if _, ok := e.Job("j"); ok {
		t.Fatal("job came back after delete")
	}
	jobs, _ := st.LoadJobs()
	if len(jobs) != 0 {
		t.Fatalf("jobs.json still has %d jobs", len(jobs))
	}
	if sec, _ := st.LoadSecrets("j"); sec.Passphrase != "" {
		t.Error("secrets survived delete")
	}
}

func TestFireDueRunsOnlyDueEnabledJobs(t *testing.T) {
	r := &fakeRunner{result: model.Result{Success: true, Code: model.CodeCompleted}}
	e, _ := newEngine(t, r)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	e.clock = func() time.Time { return now }
	_ = e.Put(&model.Job{ID: "due", Name: "due", Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleInterval, IntervalMin: 10}})
	_ = e.Put(&model.Job{ID: "paused", Name: "p", Kind: model.KindBackup, Enabled: false,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleInterval, IntervalMin: 10}})
	_ = e.Put(&model.Job{ID: "manual", Name: "m", Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	e.fireDue()
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&r.calls) != 0 {
		t.Fatal("nothing is due yet")
	}
	now = now.Add(11 * time.Minute)
	e.fireDue()
	waitFor(t, func() bool { return atomic.LoadInt32(&r.calls) == 1 })
	j, _ := e.Job("due")
	if j.NextRunAt != now.Add(10*time.Minute).UnixMilli() {
		t.Errorf("next run not re-armed from the clock: %d", j.NextRunAt)
	}
}

func TestNextRunCronAndManual(t *testing.T) {
	from := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC) // Wednesday
	if got := NextRun(model.Schedule{Type: model.ScheduleCron, CronExpr: "0 3 * * 0"}, from); got.Weekday() != time.Sunday || got.Hour() != 3 {
		t.Errorf("cron next = %s", got)
	}
	if !NextRun(model.Schedule{Type: model.ScheduleManual}, from).IsZero() {
		t.Error("manual must not schedule")
	}
	if !NextRun(model.Schedule{Type: model.ScheduleInterval}, from).IsZero() {
		t.Error("interval 0 must not schedule")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
