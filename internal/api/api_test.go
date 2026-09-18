package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/store"
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
	s := &Server{Engine: eng, Store: st, Version: "test", Started: time.Now()}
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
		{func(j *model.Job) { j.Schedule = model.Schedule{Type: model.ScheduleInterval, IntervalMin: 1} }, "interval_invalid"},
		{func(j *model.Job) { j.Schedule.CronExpr = "99 * * * *" }, "cron_invalid"},
		{func(j *model.Job) { j.Passphrase = "" }, "passphrase_required"},
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
