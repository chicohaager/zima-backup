package api

import (
	"bytes"
	"encoding/json"
	"github.com/chicohaager/zima-backup/internal/backup"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/mirror"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
	"github.com/chicohaager/zima-backup/internal/system"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(st, map[string]engine.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	bk := &backup.Runner{Restic: "/nonexistent/restic", CacheDir: t.TempDir()}
	s := &Server{Engine: eng, Store: st, Backup: bk, Sync: &mirror.Runner{Rsync: "rsync"}, System: &system.Runner{Backup: bk, Cmd: system.Exec{}, ModuleDir: t.TempDir()},
		Key: sshkey.Pair{Dir: filepath.Join(t.TempDir(), "keys")}, Version: "test", Started: time.Now()}
	srv := httptest.NewServer(s.Routes(auth.Disabled().Middleware))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path string, body interface{}) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, srv.URL+RoutePrefix+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func validJob() model.Job {
	return model.Job{Name: "photos", Kind: model.KindBackup, Sources: []string{"/DATA/Photos"},
		Target:   model.Target{Type: model.TargetLocal, Path: "/media/usb1/backup"},
		Schedule: model.Schedule{Type: model.ScheduleCron, CronExpr: "0 3 * * *"}, Passphrase: "pw", Enabled: true}
}

func TestCreateJobHidesSecretsAndSchedules(t *testing.T) {
	srv := newServer(t)
	status, raw := call(t, srv, http.MethodPost, "/api/jobs", validJob())
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var j model.Job
	_ = json.Unmarshal(raw, &j)
	if j.Passphrase != "" || j.Target.Secret != "" {
		t.Error("secret returned by the API")
	}
	if j.NextRunAt == 0 || time.UnixMilli(j.NextRunAt).Hour() != 3 {
		t.Errorf("next_run_at = %d", j.NextRunAt)
	}
	status, _ = call(t, srv, http.MethodPost, "/api/jobs/"+j.ID+"/run", nil)
	if status != http.StatusAccepted {
		t.Fatalf("run: %d", status)
	}
	time.Sleep(100 * time.Millisecond)
	_, raw = call(t, srv, http.MethodGet, "/api/jobs/"+j.ID+"/logs", nil)
	var logs []model.LogEntry
	_ = json.Unmarshal(raw, &logs)
	if len(logs) != 1 || logs[0].Code != model.CodeRunnerMissing {
		t.Fatalf("without a runner the run must be recorded as runner_missing: %+v", logs)
	}
}

func TestValidationCodes(t *testing.T) {
	srv := newServer(t)
	cases := []struct {
		mutate func(*model.Job)
		code   string
	}{
		{func(j *model.Job) { j.Name = " " }, "name_required"},
		{func(j *model.Job) { j.Kind = "mirror" }, "kind_invalid"},
		{func(j *model.Job) { j.Sources = nil }, "sources_required"},
		{func(j *model.Job) { j.Sources = []string{"/etc"} }, "source_outside_roots"},
		{func(j *model.Job) { j.Sources = []string{"/DATA/../etc"} }, "source_outside_roots"},
		{func(j *model.Job) { j.Target.Path = "/tmp/x" }, "target_path_invalid"},
		{func(j *model.Job) { j.Target = model.Target{Type: model.TargetS3, Host: "s3.example"} }, "target_incomplete"},
		{func(j *model.Job) { j.Target.Type = "ftp" }, "target_type_invalid"},
		{func(j *model.Job) {
			j.Target = model.Target{Type: model.TargetCloud, Remote: "nowhere_0000", Path: "Backups"}
		}, "cloud_remote_unknown"},
		{func(j *model.Job) { j.Schedule = model.Schedule{Type: model.ScheduleInterval, IntervalMin: 1} }, "interval_invalid"},
		{func(j *model.Job) { j.Schedule.CronExpr = "99 * * * *" }, "cron_invalid"},
		{func(j *model.Job) { j.Passphrase = "" }, "passphrase_required"},
		{func(j *model.Job) { j.Target.Path = "/DATA/Photos/backup" }, "target_nested"},
		{func(j *model.Job) { j.Sources = []string{"/DATA"}; j.Target.Path = "/DATA/backup" }, "target_nested"},
		{func(j *model.Job) { j.Kind = model.KindSync; j.Sources = []string{"/DATA/a/docs", "/DATA/b/docs"} }, "source_name_conflict"},
	}
	for _, c := range cases {
		j := validJob()
		c.mutate(&j)
		status, raw := call(t, srv, http.MethodPost, "/api/jobs", j)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if status != 400 || body["code"] != c.code {
			t.Errorf("want 400 %s, got %d %s", c.code, status, raw)
		}
	}
}

func TestEditKeepsPassphraseWhenMasked(t *testing.T) {
	srv := newServer(t)
	_, raw := call(t, srv, http.MethodPost, "/api/jobs", validJob())
	var j model.Job
	_ = json.Unmarshal(raw, &j)
	edit := validJob()
	edit.Name = "photos v2"
	edit.Passphrase = maskedSecret
	status, _ := call(t, srv, http.MethodPut, "/api/jobs/"+j.ID, edit)
	if status != 200 {
		t.Fatalf("edit: %d", status)
	}
	status, _ = call(t, srv, http.MethodPost, "/api/jobs/"+j.ID+"/disable", nil)
	if status != 200 {
		t.Fatalf("disable: %d", status)
	}
	_, raw = call(t, srv, http.MethodGet, "/api/jobs/"+j.ID, nil)
	_ = json.Unmarshal(raw, &j)
	if j.Name != "photos v2" || j.Enabled || j.NextRunAt != 0 {
		t.Fatalf("after edit+disable: %+v", j)
	}
	status, _ = call(t, srv, http.MethodDelete, "/api/jobs/"+j.ID, nil)
	if status != 204 {
		t.Fatalf("delete: %d", status)
	}
	status, _ = call(t, srv, http.MethodGet, "/api/jobs/"+j.ID, nil)
	if status != 404 {
		t.Fatalf("after delete: %d", status)
	}
}

func TestFoldersStayInsideRoots(t *testing.T) {
	srv := newServer(t)
	status, raw := call(t, srv, http.MethodGet, "/api/folders?path=/etc", nil)
	if status != 400 {
		t.Fatalf("/etc listed: %d %s", status, raw)
	}
	status, raw = call(t, srv, http.MethodGet, "/api/folders?path=/DATA/../etc", nil)
	if status != 400 {
		t.Fatalf("traversal listed: %d %s", status, raw)
	}
	status, _ = call(t, srv, http.MethodGet, "/api/folders", nil)
	if status != 200 {
		t.Fatalf("roots: %d", status)
	}
}

func TestHealthOpenAndCSRF(t *testing.T) {
	srv := newServer(t)
	status, raw := call(t, srv, http.MethodGet, "/api/health", nil)
	if status != 200 || !bytes.Contains(raw, []byte(`"jobs_total":0`)) {
		t.Fatalf("health: %d %s", status, raw)
	}
	body, _ := json.Marshal(validJob())
	req, _ := http.NewRequest(http.MethodPost, srv.URL+RoutePrefix+"/api/jobs", bytes.NewReader(body))
	req.Header.Set("Origin", "http://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", resp.StatusCode)
	}
}

// A backup job's snapshot/restore/check routes exist and reject a sync job.
func TestBackupRoutesRejectSyncJobs(t *testing.T) {
	srv := newServer(t)
	j := validJob()
	j.Kind = model.KindSync
	j.Passphrase = ""
	_, raw := call(t, srv, http.MethodPost, "/api/jobs", j)
	var created model.Job
	_ = json.Unmarshal(raw, &created)
	status, body := call(t, srv, http.MethodGet, "/api/jobs/"+created.ID+"/snapshots", nil)
	if status != 400 || !bytes.Contains(body, []byte("not_a_backup")) {
		t.Fatalf("snapshots on sync job: %d %s", status, body)
	}
}

// A sync job needs no passphrase and keeps no retention; preview is its
// own, and refused on a backup job.
func TestSyncJobsPreviewAndValidation(t *testing.T) {
	srv := newServer(t)
	j := validJob()
	j.Kind = model.KindSync
	j.Passphrase = ""
	j.Retention = model.Retention{KeepLast: 3}
	status, raw := call(t, srv, http.MethodPost, "/api/jobs", j)
	if status != 201 {
		t.Fatalf("create sync: %d %s", status, raw)
	}
	var created model.Job
	_ = json.Unmarshal(raw, &created)
	if created.Retention.KeepLast != 0 {
		t.Fatal("retention kept on a sync job")
	}

	b := validJob()
	_, raw = call(t, srv, http.MethodPost, "/api/jobs", b)
	var backup model.Job
	_ = json.Unmarshal(raw, &backup)
	status, body := call(t, srv, http.MethodPost, "/api/jobs/"+backup.ID+"/preview", nil)
	if status != 400 || !bytes.Contains(body, []byte("not_a_sync")) {
		t.Fatalf("preview on backup job: %d %s", status, body)
	}

	// paths that do not exist on the test machine make the dry run fail
	// loudly with the tool's message, never with a silent empty preview
	if _, err := exec.LookPath("rsync"); err == nil {
		status, body = call(t, srv, http.MethodPost, "/api/jobs/"+created.ID+"/preview", nil)
		if status != 502 || !bytes.Contains(body, []byte("preview_failed")) {
			t.Fatalf("preview with missing paths: %d %s", status, body)
		}
	}
}

func TestSSHKeyEndpoint(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("SKIPPED: ssh-keygen missing")
	}
	srv := newServer(t)
	status, raw := call(t, srv, http.MethodGet, "/api/sshkey", nil)
	var body map[string]string
	_ = json.Unmarshal(raw, &body)
	if status != 200 || !strings.HasPrefix(body["public_key"], "ssh-ed25519 ") {
		t.Fatalf("sshkey: %d %s", status, raw)
	}
}

// The schedule endpoint accepts a form and answers with the expression it
// became plus the form read back — and a typed expression comes back as
// words when it has a list shape, as cron when it has not.
func TestScheduleValidateSpeaksForms(t *testing.T) {
	srv := newServer(t)
	var resp struct {
		Valid    bool
		Errors   []struct{ Field, Message string }
		NextRuns []int64 `json:"next_runs"`
		CronExpr string  `json:"cron_expr"`
		Form     struct {
			Kind    string
			Hour    int
			Minute  int
			Weekday int
			Every   int
			Expr    string
		}
	}
	zero := resp
	_, raw := call(t, srv, http.MethodPost, "/api/schedule/validate", json.RawMessage(`{"form":{"kind":"weekly","weekday":0,"hour":3}}`))
	resp = zero
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !resp.Valid || resp.CronExpr != "0 3 * * 0" || resp.Form.Kind != "weekly" || len(resp.NextRuns) != 5 {
		t.Fatalf("weekly form: %+v", resp)
	}
	_, raw = call(t, srv, http.MethodPost, "/api/schedule/validate", json.RawMessage(`{"type":"cron","cron_expr":"*/15 * * * *"}`))
	resp = zero
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !resp.Valid || resp.Form.Kind != "minutes" || resp.Form.Every != 15 {
		t.Fatalf("typed */15: %+v", resp)
	}
	_, raw = call(t, srv, http.MethodPost, "/api/schedule/validate", json.RawMessage(`{"type":"cron","cron_expr":"0 3 * * 1,5"}`))
	resp = zero
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !resp.Valid || resp.Form.Kind != "cron" || resp.Form.Expr != "0 3 * * 1,5" {
		t.Fatalf("foreign expression must stay cron: %+v", resp)
	}
	_, raw = call(t, srv, http.MethodPost, "/api/schedule/validate", json.RawMessage(`{"form":{"kind":"minutes","every":7}}`))
	resp = zero
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if resp.Valid || len(resp.Errors) != 1 || resp.Errors[0].Field != "form" {
		t.Fatalf("every 7 minutes must be refused with a form error: %+v", resp)
	}
}

// The dialog shows what a source holds; an empty folder must come back as
// files 0 (not an error), a filled one with its count and size.
func TestFolderStatCountsFiles(t *testing.T) {
	srv := newServer(t)
	base := t.TempDir()
	browseRoots = append(browseRoots, base)
	defer func() { browseRoots = browseRoots[:len(browseRoots)-1] }()
	if err := os.MkdirAll(filepath.Join(base, "full", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"a.txt", "sub/b.txt", "sub/c.bin"} {
		if err := os.WriteFile(filepath.Join(base, "full", name), make([]byte, 100*(i+1)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var st struct {
		Files, Dirs int
		Bytes       int64
		Truncated   bool
	}
	code, raw := call(t, srv, http.MethodGet, "/api/folders/stat?path="+filepath.Join(base, "full"), nil)
	if err := json.Unmarshal(raw, &st); err != nil || code != 200 {
		t.Fatalf("stat full: %d %s", code, raw)
	}
	if st.Files != 3 || st.Dirs != 1 || st.Bytes != 600 || st.Truncated {
		t.Fatalf("full = %+v", st)
	}
	code, raw = call(t, srv, http.MethodGet, "/api/folders/stat?path="+filepath.Join(base, "empty"), nil)
	if err := json.Unmarshal(raw, &st); err != nil || code != 200 || st.Files != 0 || st.Bytes != 0 {
		t.Fatalf("empty = %d %+v", code, st)
	}
	if code, _ := call(t, srv, http.MethodGet, "/api/folders/stat?path=/etc", nil); code != 400 {
		t.Fatalf("outside the roots must be refused, got %d", code)
	}
}

func TestSnapshotIDAcceptsResticIDsOnly(t *testing.T) {
	for _, ok := range []string{"latest", "35c1392a", "35c1392a6919484ff5d82ba4fff5e73c46d1d4845bf7544d3aa77c84c518c1b7"} {
		if !snapshotID(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "--target=/etc", "35C1392A", "abc", "latest;rm", "35c1392a6919484ff5d82ba4fff5e73c46d1d4845bf7544d3aa77c84c518c1b7ff"} {
		if snapshotID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestRestoreRefusesFlagsAndRelativePaths: the restore body reaches restic's
// argv, so a snapshot that looks like a flag or a path that is not what the
// snapshot lists is rejected before any process starts.
func TestRestoreRefusesFlagsAndRelativePaths(t *testing.T) {
	srv := newServer(t)
	status, raw := call(t, srv, http.MethodPost, "/api/jobs", validJob())
	if status != 201 {
		t.Fatalf("create: %d %s", status, raw)
	}
	var created model.Job
	_ = json.Unmarshal(raw, &created)
	cases := []map[string]interface{}{
		{"snapshot": "--target=/etc", "paths": []string{"/DATA/Photos"}},
		{"snapshot": "latest", "paths": []string{"DATA/Photos"}},
		{"snapshot": "latest", "paths": []string{"/DATA/Photos"}, "target": "/etc"},
	}
	for _, body := range cases {
		status, raw := call(t, srv, http.MethodPost, "/api/jobs/"+created.ID+"/restore", body)
		if status != 400 {
			t.Errorf("%v: %d %s", body, status, raw)
		}
	}
	status, raw = call(t, srv, http.MethodGet, "/api/jobs/"+created.ID+"/snapshots/--help/ls", nil)
	if status != 400 {
		t.Errorf("ls with a flag as snapshot: %d %s", status, raw)
	}
	// positive control: a well-formed request passes the guard (202; the
	// run itself fails later on the missing restic binary, off this path)
	status, raw = call(t, srv, http.MethodPost, "/api/jobs/"+created.ID+"/restore", map[string]interface{}{"snapshot": "latest", "paths": []string{"/DATA/Photos"}, "target": "/DATA/Restored"})
	if status != 202 {
		t.Errorf("well-formed restore: %d %s", status, raw)
	}
}

// A system job carries no sources of its own (the runner decides them),
// needs a passphrase like a backup, answers the snapshot routes, and its
// restore is guarded by the typed word and the snapshot id.
func TestSystemJobsValidationAndRoutes(t *testing.T) {
	srv := newServer(t)
	j := validJob()
	j.Kind = model.KindSystem
	j.Sources = nil
	j.Passphrase = ""
	status, body := call(t, srv, http.MethodPost, "/api/jobs", j)
	if status != 400 || !bytes.Contains(body, []byte("passphrase_required")) {
		t.Fatalf("system job without passphrase: %d %s", status, body)
	}
	j.Passphrase = "pw"
	j.Sources = []string{"/DATA/whatever-the-client-sent"}
	j.IncludeData = true
	status, raw := call(t, srv, http.MethodPost, "/api/jobs", j)
	if status != 201 {
		t.Fatalf("create system job: %d %s", status, raw)
	}
	var created model.Job
	_ = json.Unmarshal(raw, &created)
	if len(created.Sources) != 0 || !created.IncludeData || created.Kind != model.KindSystem {
		t.Fatalf("stored job: sources %v include_data %v kind %s", created.Sources, created.IncludeData, created.Kind)
	}
	// snapshot routes are open to system jobs (they are restic repositories) — here the binary is missing, so 502 not 400
	if status, body := call(t, srv, http.MethodGet, "/api/jobs/"+created.ID+"/snapshots", nil); status == 400 {
		t.Fatalf("snapshots must not be refused for a system job: %d %s", status, body)
	}
	status, body = call(t, srv, http.MethodPost, "/api/jobs/"+created.ID+"/system-restore", map[string]any{"snapshot": "latest"})
	if status != 400 || !bytes.Contains(body, []byte("confirm_required")) {
		t.Fatalf("restore without the typed word: %d %s", status, body)
	}
	status, body = call(t, srv, http.MethodPost, "/api/jobs/"+created.ID+"/system-restore", map[string]any{"snapshot": "../x", "confirm": "RESTORE"})
	if status != 400 || !bytes.Contains(body, []byte("snapshot_invalid")) {
		t.Fatalf("restore with a bad snapshot id: %d %s", status, body)
	}
	// a backup job is not a system job
	b := validJob()
	_, raw = call(t, srv, http.MethodPost, "/api/jobs", b)
	var plain model.Job
	_ = json.Unmarshal(raw, &plain)
	status, body = call(t, srv, http.MethodPost, "/api/jobs/"+plain.ID+"/system-restore", map[string]any{"snapshot": "latest", "confirm": "RESTORE"})
	if status != 400 || !bytes.Contains(body, []byte("not_a_system_job")) {
		t.Fatalf("system-restore on a backup job: %d %s", status, body)
	}
}
