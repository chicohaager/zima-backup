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
	Log(ctx, "tool line")
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

// The structured detail travels from the runner's result into the job's
// last result and the persisted history, so the UI can translate it.
func TestRunCarriesDetailIntoHistory(t *testing.T) {
	d := &model.ResultDetail{Kind: model.DetailSnapshot, Snapshot: "921bd3e2", Files: 18342, New: 23, Bytes: 101082726}
	r := &fakeRunner{result: model.Result{Success: true, Code: model.CodeCompleted, Message: "snapshot 921bd3e2: …", Detail: d}}
	e, st := newEngine(t, r)
	_ = e.Put(&model.Job{ID: "j", Name: "j", Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal, Path: "/media/x"}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	e.Run("j")
	logs := e.Logs("j")
	if len(logs) != 1 || logs[0].Detail == nil || *logs[0].Detail != *d || logs[0].Message != "snapshot 921bd3e2: …" {
		t.Fatalf("history = %+v", logs)
	}
	saved, err := st.LoadLogs("j")
	if err != nil || len(saved) != 1 || saved[0].Detail == nil || *saved[0].Detail != *d {
		t.Fatalf("persisted history = %+v, %v", saved, err)
	}
	j, _ := e.Job("j")
	if j.LastResult == nil || j.LastResult.Detail == nil || j.LastResult.Detail.Kind != model.DetailSnapshot {
		t.Fatalf("last result = %+v", j.LastResult)
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

// A run longer than the tick must not be fired again on every tick: each
// firing wrote a skipped_running entry (measured before the fix: 20 in
// ten minutes), and a long run pushed the real history out.
func TestFireDueLeavesARunningJobAlone(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{}), result: model.Result{Success: true, Code: model.CodeCompleted}}
	e, _ := newEngine(t, r)
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	e.clock = func() time.Time { return now }
	_ = e.Put(&model.Job{ID: "long", Name: "long", Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleInterval, IntervalMin: 60}})
	now = now.Add(61 * time.Minute)
	e.fireDue()
	waitFor(t, func() bool { j, _ := e.Job("long"); return j.Running && j.Progress == 0.5 })
	for i := 0; i < 20; i++ { // ten minutes of ticks while the run goes on
		now = now.Add(tick)
		e.fireDue()
	}
	time.Sleep(100 * time.Millisecond) // a wrongly fired Run would have recorded by now
	close(r.block)
	waitFor(t, func() bool { j, _ := e.Job("long"); return !j.Running })
	logs := e.Logs("long")
	if len(logs) != 1 || logs[0].Code != model.CodeCompleted {
		codes := []string{}
		for _, l := range logs {
			codes = append(codes, l.Code)
		}
		t.Fatalf("history after one long run: %v, want [completed]", codes)
	}
	if atomic.LoadInt32(&r.calls) != 1 {
		t.Fatalf("runner ran %d times, want 1", r.calls)
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

// A runner's lines and its phase are visible while it runs and the lines
// stay readable after the run; a job that never ran has an empty list.
func TestOutputAndPhaseFollowTheRun(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{}), result: model.Result{Success: true, Code: model.CodeCompleted, Message: "done"}}
	e, _ := newEngine(t, r)
	_ = e.Put(&model.Job{ID: "j", Name: "j", Kind: model.KindBackup, Enabled: true, Passphrase: "pw",
		Target: model.Target{Type: model.TargetLocal, Path: "/media/x"}, Schedule: model.Schedule{Type: model.ScheduleManual}})
	if got := e.Output("j"); got == nil || len(got) != 0 {
		t.Fatalf("output before any run = %v", got)
	}
	go e.Run("j")
	deadline := time.Now().Add(2 * time.Second)
	for {
		j, _ := e.Job("j")
		if j.Running && j.Phase == "half" && len(e.Output("j")) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("phase/output during run: %+v %v", j, e.Output("j"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(r.block)
	for {
		j, _ := e.Job("j")
		if !j.Running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	j, _ := e.Job("j")
	out := e.Output("j")
	if j.Phase != "" || len(out) != 2 || out[0].Text != "tool line" || out[1].Text != "result: completed — done" {
		t.Fatalf("after run: phase=%q output=%+v", j.Phase, out)
	}
	if Log(context.Background(), "outside a run"); len(e.Output("j")) != 2 {
		t.Fatal("a log call outside a run must not reach a job")
	}
}
