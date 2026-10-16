package engine

import (
	"sort"
	"time"

	"github.com/chicohaager/zima-backup/internal/model"
)

// OverdueGrace is how long after its expected run a job may be silent
// before it counts as overdue: a run that started on time and is still
// going is not late, a run that never started is.
const OverdueGrace = time.Hour

// Attention reasons.
const (
	AttentionFailing = "failing" // the last run did not complete
	AttentionOverdue = "overdue" // a run was due and none happened
)

// Attention is one scheduled job that needs the user: its last run failed,
// or the run it was due for never happened (box off, daemon down, mount
// missing at the time). LastSuccess is zero when the job never completed.
type Attention struct {
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Reason      string    `json:"reason"`
	Code        string    `json:"code,omitempty"` // the failing run's code
	LastSuccess time.Time `json:"last_success"`
	Since       time.Time `json:"since"` // first failure after LastSuccess, or the missed run
}

// operations that are not a run of the job: a restore, a check, a skipped
// tick. They neither prove nor disprove that the job does its work.
var notARun = map[string]bool{
	model.CodeRestored: true, model.CodeCheckOK: true, model.CodeCheckFailed: true,
	model.CodeSystemRestored: true, model.CodeVersionMismatch: true, model.CodeSkippedRunning: true,
}

// Attention lists the enabled, scheduled jobs that need the user, oldest
// trouble first. Manual and paused jobs are the user's own choice and are
// left out; a running job is judged when its run ends.
func (e *Engine) Attention() []Attention {
	now := e.clock()
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []Attention
	for _, j := range e.jobs {
		if !j.Enabled || j.Running || j.Schedule.Type == model.ScheduleManual {
			continue
		}
		if a, ok := attentionFor(j, e.logs[j.ID], now); ok {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].Since.Equal(out[k].Since) {
			return out[i].Since.Before(out[k].Since)
		}
		return out[i].Name < out[k].Name
	})
	return out
}

// attentionFor judges one job from its history (newest first).
func attentionFor(j *model.Job, logs []model.LogEntry, now time.Time) (Attention, bool) {
	a := Attention{JobID: j.ID, Name: j.Name}
	var lastRun *model.LogEntry
	var firstFailure time.Time
	for i := range logs {
		l := &logs[i]
		if notARun[l.Code] {
			continue
		}
		if lastRun == nil {
			lastRun = l
		}
		if l.Code == model.CodeCompleted {
			a.LastSuccess = time.UnixMilli(l.Time)
			break
		}
		firstFailure = time.UnixMilli(l.Time)
	}
	if lastRun != nil && lastRun.Code != model.CodeCompleted {
		a.Reason, a.Code, a.Since = AttentionFailing, lastRun.Code, firstFailure
		return a, true
	}
	// no failure on record: was a run due that never came? Counted from the
	// last success, or from the last edit or enabling — a job paused for a
	// month owed no runs while it was paused.
	from := a.LastSuccess
	if set := time.UnixMilli(max(j.CreatedAt, j.UpdatedAt)); set.After(from) {
		from = set
	}
	due := NextRun(j.Schedule, from)
	if due.IsZero() || !now.After(due.Add(OverdueGrace)) {
		return a, false
	}
	a.Reason, a.Since = AttentionOverdue, due
	return a, true
}
