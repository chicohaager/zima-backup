// Package engine keeps the job registry in memory, decides when jobs run
// and executes them through a Runner. Scheduling is a single ticker that
// compares NextRunAt with the clock — no timer per job, so pause, edit and
// delete cannot race a firing callback.
package engine

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/schedule"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/store"
)

const (
	tick           = 30 * time.Second
	defaultTimeout = 6 * time.Hour
	maxLogEntries  = 200
	maxMessageLen  = 8000
)

// Progress is called by runners with a 0..1 fraction and a short phase
// name (init, backup, retention, restore, check, sync …) that the UI
// translates.
type Progress func(fraction float64, phase string)

// logKey carries the output sink of the current run in the context.
type logKey struct{}

// Log appends one line to the output of the run ctx belongs to. Runners
// call it for every line of their tool worth reading later; outside a
// run it does nothing.
func Log(ctx context.Context, line string) {
	if sink, ok := ctx.Value(logKey{}).(func(string)); ok {
		sink(line)
	}
}

// Transfer is what a runner knows about the bytes moving right now —
// and the files: on a cloud drive every file costs a round trip, so
// "12 of 62 files, 1.2 files/s" tells the truth where "60 KiB/s" looks
// like a broken link (measured on Google Drive: 1 KiB or 20 MiB, each
// file ~1 s; a 20 MiB file alone runs at 2 MiB/s).
type Transfer struct {
	Done, Total           int64 // bytes
	Rate                  int64 // bytes per second
	FilesDone, FilesTotal int64 // 0 when the tool does not count
}

type transferKey struct{}

// Report updates the transfer figures of the run ctx belongs to.
func Report(ctx context.Context, t Transfer) {
	if sink, ok := ctx.Value(transferKey{}).(func(Transfer)); ok {
		sink(t)
	}
}

// maxOutputLines is how much of a run's output is kept per job.
const maxOutputLines = 400

// OutputLine is one kept line of a run's output.
type OutputLine struct {
	Time int64  `json:"time"`
	Text string `json:"text"`
}

// Runner executes one kind of job. Implementations live in the backup and
// sync packages; the engine only knows this contract.
type Runner interface {
	// Run performs the job and returns its result. secrets carries the
	// passphrase and target secret the store keeps out of the job itself.
	Run(ctx context.Context, job *model.Job, secrets store.Secrets, progress Progress) model.Result
}

// Engine is the in-memory registry plus scheduler.
type Engine struct {
	store   *store.Store
	runners map[string]Runner
	clock   func() time.Time
	output  map[string][]OutputLine // per job: the current or last run's output

	mu   sync.RWMutex
	jobs map[string]*model.Job
	logs map[string][]model.LogEntry
	// cancel holds the cancel func of a running job so a stop request or a
	// delete can end it.
	cancel map[string]context.CancelFunc

	stop chan struct{}
	wg   sync.WaitGroup
}

// New loads jobs and history from the store. Jobs that were running when
// the daemon died are marked as cancelled.
func New(st *store.Store, runners map[string]Runner) (*Engine, error) {
	e := &Engine{store: st, runners: runners, clock: time.Now,
		jobs: map[string]*model.Job{}, logs: map[string][]model.LogEntry{},
		output: map[string][]OutputLine{},
		cancel: map[string]context.CancelFunc{}, stop: make(chan struct{})}
	jobs, err := st.LoadJobs()
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Running {
			j.Running = false
			j.LastResult = &model.Result{Code: model.CodeCancelled, Message: "daemon restarted during the run"}
		}
		j.Progress = 0
		e.jobs[j.ID] = j
		logs, err := st.LoadLogs(j.ID)
		if err != nil {
			log.Printf("[zbackup] history of %s: %v", j.ID, err)
		}
		e.logs[j.ID] = logs
		e.rescheduleLocked(j)
	}
	log.Printf("[zbackup] loaded %d jobs", len(jobs))
	return e, nil
}

// Start runs the scheduler loop until Stop is called.
func (e *Engine) Start() {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-t.C:
				e.fireDue()
			}
		}
	}()
}

// Stop ends the loop and cancels running jobs.
func (e *Engine) Stop() {
	close(e.stop)
	e.mu.Lock()
	for _, c := range e.cancel {
		c()
	}
	e.mu.Unlock()
	e.wg.Wait()
}

// fireDue starts every enabled job whose next run is due.
func (e *Engine) fireDue() {
	now := e.clock()
	e.mu.RLock()
	var due []*model.Job
	for _, j := range e.jobs {
		if j.Enabled && j.Schedule.Type != model.ScheduleManual && j.NextRunAt != 0 && j.NextRunAt <= now.UnixMilli() {
			due = append(due, j)
		}
	}
	e.mu.RUnlock()
	for _, j := range due {
		go e.Run(j.ID)
	}
}

// Jobs returns the registry ordered by name.
func (e *Engine) Jobs() []*model.Job {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*model.Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		c := *j
		out = append(out, &c)
	}
	sort.Slice(out, func(i, k int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[k].Name) })
	return out
}

// Job returns a copy of one job.
func (e *Engine) Job(id string) (*model.Job, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	j, ok := e.jobs[id]
	if !ok {
		return nil, false
	}
	c := *j
	return &c, true
}

// Put creates or replaces a job definition, keeps runtime state of an
// existing one, stores secrets separately and recomputes the schedule.
// Empty secrets on an update keep the stored ones.
func (e *Engine) Put(j *model.Job) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock().UnixMilli()
	sec, _ := e.store.LoadSecrets(j.ID)
	if j.Passphrase != "" {
		sec.Passphrase = j.Passphrase
	}
	if j.Target.Secret != "" {
		sec.TargetSecret = j.Target.Secret
	}
	if old, ok := e.jobs[j.ID]; ok {
		j.CreatedAt, j.LastRunAt, j.LastResult, j.Running, j.Progress = old.CreatedAt, old.LastRunAt, old.LastResult, old.Running, old.Progress
	} else {
		j.CreatedAt = now
		if _, ok := e.logs[j.ID]; !ok {
			e.logs[j.ID] = []model.LogEntry{}
		}
	}
	j.UpdatedAt = now
	j.Passphrase, j.Target.Secret = "", ""
	e.jobs[j.ID] = j
	e.rescheduleLocked(j)
	if err := e.store.SaveSecrets(j.ID, sec); err != nil {
		return err
	}
	return e.persistJobsLocked()
}

// Delete removes a job, its history and its secrets; a running instance is cancelled.
func (e *Engine) Delete(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.cancel[id]; ok {
		c()
	}
	delete(e.jobs, id)
	delete(e.output, id)
	delete(e.logs, id)
	if err := e.persistJobsLocked(); err != nil {
		return err
	}
	if err := e.store.DeleteLogs(id); err != nil {
		return err
	}
	return e.store.DeleteSecrets(id)
}

// SetEnabled pauses or resumes a job.
func (e *Engine) SetEnabled(id string, enabled bool) (*model.Job, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job %s not found", id)
	}
	j.Enabled = enabled
	j.UpdatedAt = e.clock().UnixMilli()
	e.rescheduleLocked(j)
	c := *j
	return &c, e.persistJobsLocked()
}

// Logs returns a copy of a job's history, newest first.
func (e *Engine) Logs(id string) []model.LogEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]model.LogEntry(nil), e.logs[id]...)
}

// ClearLogs empties a job's history.
func (e *Engine) ClearLogs(id string) error {
	e.mu.Lock()
	e.logs[id] = []model.LogEntry{}
	e.mu.Unlock()
	return e.store.SaveLogs(id, nil)
}

// Cancel stops a running job.
func (e *Engine) Cancel(id string) bool {
	e.mu.RLock()
	c, ok := e.cancel[id]
	e.mu.RUnlock()
	if ok {
		c()
	}
	return ok
}

// NextRun computes the next firing time after from for a schedule; zero
// for manual or invalid schedules.
func NextRun(s model.Schedule, from time.Time) time.Time {
	switch s.Type {
	case model.ScheduleInterval:
		if s.IntervalMin < 1 {
			return time.Time{}
		}
		return from.Add(time.Duration(s.IntervalMin) * time.Minute)
	case model.ScheduleCron:
		return schedule.Next(s.CronExpr, from)
	default:
		return time.Time{}
	}
}

// rescheduleLocked sets NextRunAt from the clock. Caller holds mu.
func (e *Engine) rescheduleLocked(j *model.Job) {
	if !j.Enabled {
		j.NextRunAt = 0
		return
	}
	next := NextRun(j.Schedule, e.clock())
	if next.IsZero() {
		j.NextRunAt = 0
		return
	}
	j.NextRunAt = next.UnixMilli()
}

func (e *Engine) persistJobsLocked() error {
	all := make([]*model.Job, 0, len(e.jobs))
	for _, j := range e.jobs {
		all = append(all, j)
	}
	return e.store.SaveJobs(all)
}

// Operation is one unit of work on a job: the scheduled run, a restore, a
// repository check. It receives the job snapshot and the stored secrets.
type Operation func(ctx context.Context, job *model.Job, secrets store.Secrets, progress Progress) model.Result

// Run executes the job's runner now. A second Run while one is going is
// recorded as skipped: backups and mirrors must never overlap themselves.
func (e *Engine) Run(id string) {
	e.mu.RLock()
	j, ok := e.jobs[id]
	var runner Runner
	if ok {
		runner = e.runners[j.Kind]
	}
	e.mu.RUnlock()
	if !ok {
		return
	}
	if runner == nil {
		e.Execute(id, func(_ context.Context, job *model.Job, _ store.Secrets, _ Progress) model.Result {
			return model.Result{Code: model.CodeRunnerMissing, Message: "no runner for kind " + job.Kind}
		})
		return
	}
	e.Execute(id, runner.Run)
}

// Execute performs op on the job with the engine's bookkeeping: the job is
// marked running, can be cancelled, gets a history entry and is rescheduled
// afterwards. Restore and check use it as well, so they show up in the
// history and cannot overlap a backup of the same job.
func (e *Engine) Execute(id string, op Operation) {
	e.mu.Lock()
	j, ok := e.jobs[id]
	if !ok {
		e.mu.Unlock()
		return
	}
	if j.Running {
		e.mu.Unlock()
		e.record(id, model.Result{Code: model.CodeSkippedRunning, Message: "previous run still in progress"}, 0)
		return
	}
	timeout := time.Duration(j.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	j.Running, j.Progress, j.Phase = true, 0, "start"
	e.cancel[id] = cancel
	e.output[id] = nil
	snapshot := *j
	e.mu.Unlock()
	defer cancel()
	ctx = context.WithValue(ctx, logKey{}, func(line string) { e.appendOutput(id, line) })
	var fileMeter rateMeter
	ctx = context.WithValue(ctx, transferKey{}, func(t Transfer) {
		e.mu.Lock()
		if cur, ok := e.jobs[id]; ok {
			cur.BytesDone, cur.BytesTotal, cur.Rate = t.Done, t.Total, t.Rate
			cur.FilesDone, cur.FilesTotal = t.FilesDone, t.FilesTotal
			if t.FilesDone > 0 {
				cur.FileRate = fileMeter.update(t.FilesDone, e.clock())
			}
		}
		e.mu.Unlock()
	})

	secrets, err := e.store.LoadSecrets(id)
	if err != nil {
		log.Printf("[zbackup] secrets of %s: %v", id, err)
	}
	start := e.clock()
	result := op(ctx, &snapshot, secrets, func(f float64, phase string) {
		e.mu.Lock()
		if cur, ok := e.jobs[id]; ok {
			cur.Progress = f
			if phase != "" && phase != cur.Phase {
				cur.Phase = phase
			}
		}
		e.mu.Unlock()
	})
	e.appendOutput(id, fmt.Sprintf("result: %s — %s", result.Code, result.Message))
	if ctx.Err() == context.DeadlineExceeded && !result.Success {
		result.Code = model.CodeTimeout
	}
	duration := e.clock().Sub(start)

	e.mu.Lock()
	delete(e.cancel, id)
	cur, still := e.jobs[id]
	if still {
		cur.Running, cur.Progress, cur.Phase = false, 0, ""
		cur.BytesDone, cur.BytesTotal, cur.Rate = 0, 0, 0
		cur.FilesDone, cur.FilesTotal, cur.FileRate = 0, 0, 0
		cur.LastRunAt = e.clock().UnixMilli()
		cur.LastResult = &result
		e.rescheduleLocked(cur)
	}
	e.mu.Unlock()
	if !still {
		return // deleted while running: nothing to write back
	}
	e.record(id, result, duration)
	e.notify(&snapshot, result, duration)
}

// appendOutput keeps the last maxOutputLines lines of a job's run.
func (e *Engine) appendOutput(id, line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	lines := append(e.output[id], OutputLine{Time: e.clock().UnixMilli(), Text: line})
	if len(lines) > maxOutputLines {
		lines = lines[len(lines)-maxOutputLines:]
	}
	e.output[id] = lines
}

// Output returns the kept output of a job's current or last run.
func (e *Engine) Output(id string) []OutputLine {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]OutputLine(nil), e.output[id]...)
	if out == nil {
		out = []OutputLine{}
	}
	return out
}

// record appends a history entry and persists job state and history.
func (e *Engine) record(id string, r model.Result, d time.Duration) {
	if len(r.Message) > maxMessageLen {
		r.Message = r.Message[:maxMessageLen] + "…"
	}
	entry := model.LogEntry{Time: e.clock().UnixMilli(), DurationMs: d.Milliseconds(), Success: r.Success,
		Code: r.Code, Message: r.Message, Files: r.Files, Bytes: r.Bytes, SnapshotID: r.SnapshotID}
	e.mu.Lock()
	if _, ok := e.jobs[id]; !ok {
		e.mu.Unlock()
		return
	}
	e.logs[id] = append([]model.LogEntry{entry}, e.logs[id]...)
	if len(e.logs[id]) > maxLogEntries {
		e.logs[id] = e.logs[id][:maxLogEntries]
	}
	logs := append([]model.LogEntry(nil), e.logs[id]...)
	if err := e.persistJobsLocked(); err != nil {
		log.Printf("[zbackup] persist jobs: %v", err)
	}
	e.mu.Unlock()
	if err := e.store.SaveLogs(id, logs); err != nil {
		log.Printf("[zbackup] persist history of %s: %v", id, err)
	}
}

func (e *Engine) notify(j *model.Job, r model.Result, d time.Duration) {
	info := notify.TaskInfo{ID: j.ID, Name: j.Name, Command: j.Kind + " → " + j.Target.Type}
	res := notify.ResultInfo{Success: r.Success, Message: r.Code + ": " + r.Message, DurationMs: d.Milliseconds()}
	if len(j.Notifications) > 0 {
		notify.Send(j.Notifications, info, res)
	}
	set, err := e.store.LoadSettings()
	if err == nil && set.TelegramBotToken != "" && set.TelegramChatID != "" {
		notify.Send([]notify.Config{{Enabled: true, Type: "telegram", Target: set.TelegramChatID,
			OnSuccess: set.TelegramOnSuccess, OnFailure: set.TelegramOnFailure, TelegramBotToken: set.TelegramBotToken}}, info, res)
	}
}

// rateMeter turns a cumulative count into a recent rate (the last ten
// seconds, not the average since the start), in units per second ×100 so
// the API can carry 1.25 files/s as 125.
type rateMeter struct {
	samples []struct {
		n  int64
		at time.Time
	}
}

func (m *rateMeter) update(n int64, at time.Time) int64 {
	m.samples = append(m.samples, struct {
		n  int64
		at time.Time
	}{n, at})
	for len(m.samples) > 1 && at.Sub(m.samples[0].at) > 10*time.Second {
		m.samples = m.samples[1:]
	}
	first := m.samples[0]
	if at.Sub(first.at) < time.Second {
		return 0
	}
	return int64(float64(n-first.n) / at.Sub(first.at).Seconds() * 100)
}
