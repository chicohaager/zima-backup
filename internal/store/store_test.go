package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chicohaager/zima-backup/internal/model"
)

func TestJobsNeverCarrySecretsToDisk(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := &model.Job{ID: "a", Name: "a", Kind: model.KindBackup, Passphrase: "hunter2",
		Target: model.Target{Type: model.TargetS3, Secret: "s3cret"}}
	if err := s.SaveJobs([]*model.Job{job}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.jobsPath())
	for _, secret := range []string{"hunter2", "s3cret"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("jobs.json contains %q", secret)
		}
	}
	// the in-memory job is untouched
	if job.Passphrase != "hunter2" || job.Target.Secret != "s3cret" {
		t.Error("SaveJobs modified the caller's job")
	}
	if err := s.SaveSecrets("a", Secrets{Passphrase: "hunter2", TargetSecret: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(s.keyPath("a"))
	if info.Mode().Perm() != 0600 {
		t.Errorf("keys file mode %o, want 0600", info.Mode().Perm())
	}
	sec, err := s.LoadSecrets("a")
	if err != nil || sec.Passphrase != "hunter2" || sec.TargetSecret != "s3cret" {
		t.Fatalf("LoadSecrets = %+v, %v", sec, err)
	}
	if err := s.DeleteSecrets("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.keyPath("a")); !os.IsNotExist(err) {
		t.Error("secrets file survived delete")
	}
}

func TestLogsAndSettingsRoundTrip(t *testing.T) {
	s, _ := Open(t.TempDir())
	if logs, err := s.LoadLogs("none"); err != nil || len(logs) != 0 {
		t.Fatalf("missing history should be empty: %v %v", logs, err)
	}
	if err := s.SaveLogs("j", []model.LogEntry{{Time: 1, Code: model.CodeCompleted, Success: true}}); err != nil {
		t.Fatal(err)
	}
	logs, _ := s.LoadLogs("j")
	if len(logs) != 1 || logs[0].Code != model.CodeCompleted {
		t.Fatalf("logs = %+v", logs)
	}
	set, _ := s.LoadSettings()
	if !set.TelegramOnFailure {
		t.Error("default settings must notify on failure")
	}
	set.TelegramChatID = "42"
	if err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	again, _ := s.LoadSettings()
	if again.TelegramChatID != "42" {
		t.Error("settings not persisted")
	}
}

func TestPathsStayInsideBase(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, p := range []string{s.logPath("../../etc/x"), s.keyPath("../x")} {
		if !strings.HasPrefix(p, s.base) || strings.Contains(filepath.Base(p), "..") {
			t.Errorf("path escaped base: %s", p)
		}
	}
}
