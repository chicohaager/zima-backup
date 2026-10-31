package engine

import (
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/model"
)

func daily(id, name string) *model.Job {
	return &model.Job{ID: id, Name: name, Kind: model.KindBackup, Enabled: true,
		Target: model.Target{Type: model.TargetLocal}, Schedule: model.Schedule{Type: model.ScheduleCron, CronExpr: "0 3 * * *"}}
}

func entry(at time.Time, code string) model.LogEntry {
	return model.LogEntry{Time: at.UnixMilli(), Code: code, Success: code == model.CodeCompleted}
}

// The case that went unnoticed on the test NAS: three daily jobs failed every night
// for twelve days, and nothing told the user.
func TestAttentionNamesJobsThatKeepFailing(t *testing.T) {
	e, _ := newEngine(t, &fakeRunner{})
	loc := time.Local
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, loc)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, loc)
	e.clock = func() time.Time { return created }
	for _, j := range []*model.Job{daily("byd", "BYD"), daily("ok", "fine"), daily("off", "paused"), daily("man", "manual")} {
		if err := e.Put(j); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = e.SetEnabled("off", false)
	e.mu.Lock()
	e.jobs["man"].Schedule = model.Schedule{Type: model.ScheduleManual}
	lastOK := time.Date(2026, 10, 4, 3, 0, 0, 0, loc)
	var byd []model.LogEntry
	for d := 16; d >= 5; d-- {
		byd = append(byd, entry(time.Date(2026, 10, d, 3, 0, 5, 0, loc), model.CodeTargetUnavailable))
		if d == 10 { // a restore and a check in between are not runs
			byd = append(byd, entry(time.Date(2026, 10, d, 9, 0, 0, 0, loc), model.CodeRestored), entry(time.Date(2026, 10, d, 9, 5, 0, 0, loc), model.CodeCheckOK))
		}
	}
	byd = append(byd, entry(lastOK, model.CodeCompleted))
	e.logs["byd"] = byd
	e.logs["ok"] = []model.LogEntry{entry(time.Date(2026, 10, 16, 3, 0, 9, 0, loc), model.CodeCompleted)}
	failing := []model.LogEntry{entry(time.Date(2026, 10, 16, 3, 0, 0, 0, loc), model.CodeFailed)}
	e.logs["off"], e.logs["man"] = failing, failing
	e.mu.Unlock()
	e.clock = func() time.Time { return now }

	got := e.Attention()
	if len(got) != 1 {
		t.Fatalf("attention = %+v, want only BYD", got)
	}
	a := got[0]
	if a.JobID != "byd" || a.Reason != AttentionFailing || a.Code != model.CodeTargetUnavailable {
		t.Errorf("got %+v", a)
	}
	if !a.LastSuccess.Equal(lastOK) {
		t.Errorf("last success %v, want %v", a.LastSuccess, lastOK)
	}
	if want := time.Date(2026, 10, 5, 3, 0, 5, 0, loc); !a.Since.Equal(want) {
		t.Errorf("since %v, want the first failure %v", a.Since, want)
	}
}

// A run that never happened leaves no failure in the history — the box was
// off, the daemon down. That silence is what must be reported.
func TestAttentionReportsAMissedRun(t *testing.T) {
	e, _ := newEngine(t, &fakeRunner{})
	loc := time.Local
	e.clock = func() time.Time { return time.Date(2026, 10, 1, 10, 0, 0, 0, loc) }
	_ = e.Put(daily("j", "nightly"))
	lastOK := time.Date(2026, 10, 14, 3, 0, 0, 0, loc)
	e.mu.Lock()
	e.logs["j"] = []model.LogEntry{entry(lastOK, model.CodeCompleted)}
	e.mu.Unlock()

	e.clock = func() time.Time { return time.Date(2026, 10, 15, 3, 59, 0, 0, loc) } // due 03:00, grace until 04:00
	if got := e.Attention(); len(got) != 0 {
		t.Fatalf("within the grace: %+v", got)
	}
	e.clock = func() time.Time { return time.Date(2026, 10, 15, 4, 1, 0, 0, loc) }
	got := e.Attention()
	if len(got) != 1 || got[0].Reason != AttentionOverdue {
		t.Fatalf("after the grace: %+v", got)
	}
	if want := time.Date(2026, 10, 15, 3, 0, 0, 0, loc); !got[0].Since.Equal(want) {
		t.Errorf("since %v, want the missed run %v", got[0].Since, want)
	}
	// re-enabling a paused job starts the count again
	e.clock = func() time.Time { return time.Date(2026, 10, 15, 4, 2, 0, 0, loc) }
	_, _ = e.SetEnabled("j", true)
	if got := e.Attention(); len(got) != 0 {
		t.Errorf("just enabled: %+v", got)
	}
}

func TestAttentionSkipsARunningJobAndANewOne(t *testing.T) {
	e, _ := newEngine(t, &fakeRunner{})
	loc := time.Local
	e.clock = func() time.Time { return time.Date(2026, 10, 16, 10, 0, 0, 0, loc) }
	_ = e.Put(daily("new", "new"))
	_ = e.Put(daily("run", "running"))
	e.mu.Lock()
	e.jobs["run"].Running = true
	e.logs["run"] = []model.LogEntry{entry(time.Date(2026, 10, 15, 3, 0, 0, 0, loc), model.CodeFailed)}
	e.mu.Unlock()
	e.clock = func() time.Time { return time.Date(2026, 10, 17, 3, 30, 0, 0, loc) } // first run due 03:00, still in grace
	if got := e.Attention(); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
