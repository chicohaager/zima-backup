// Package store persists jobs, their history, their secrets and the module
// settings as JSON files with atomic writes.
//
// Layout under the base directory:
//
//	jobs.json          job definitions and last result — never contains secrets
//	logs/<id>.json     run history of one job
//	keys/<id>.json     passphrase and target secret of one job, mode 0600
//	settings.json      module settings, mode 0600
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/chicohaager/zima-backup/internal/model"
)

// Secrets is what keys/<id>.json holds.
type Secrets struct {
	Passphrase   string `json:"passphrase,omitempty"`
	TargetSecret string `json:"target_secret,omitempty"`
}

// Store is the file-backed persistence.
type Store struct {
	base string
	mu   sync.Mutex
}

// Open creates the directory tree and returns a Store.
func Open(base string) (*Store, error) {
	for _, d := range []string{base, filepath.Join(base, "logs"), filepath.Join(base, "keys")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return &Store{base: base}, nil
}

// Base returns the data directory.
func (s *Store) Base() string { return s.base }

func (s *Store) jobsPath() string     { return filepath.Join(s.base, "jobs.json") }
func (s *Store) settingsPath() string { return filepath.Join(s.base, "settings.json") }
func (s *Store) logPath(id string) string {
	return filepath.Join(s.base, "logs", filepath.Base(id)+".json")
}
func (s *Store) keyPath(id string) string {
	return filepath.Join(s.base, "keys", filepath.Base(id)+".json")
}

// LoadJobs returns every job; a missing file is an empty list.
func (s *Store) LoadJobs() ([]*model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var jobs []*model.Job
	if err := readJSON(s.jobsPath(), &jobs); err != nil {
		return nil, err
	}
	if jobs == nil {
		jobs = []*model.Job{}
	}
	return jobs, nil
}

// SaveJobs replaces the job list. Secrets on the jobs are stripped before
// writing; callers keep them through SaveSecrets.
func (s *Store) SaveJobs(jobs []*model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clean := make([]*model.Job, len(jobs))
	for i, j := range jobs {
		c := *j
		c.Passphrase, c.Target.Secret = "", ""
		clean[i] = &c
	}
	return writeJSONAtomic(s.jobsPath(), clean, 0600)
}

// LoadLogs / SaveLogs / DeleteLogs handle one job's history.
func (s *Store) LoadLogs(id string) ([]model.LogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var logs []model.LogEntry
	if err := readJSON(s.logPath(id), &logs); err != nil {
		return nil, err
	}
	if logs == nil {
		logs = []model.LogEntry{}
	}
	return logs, nil
}

func (s *Store) SaveLogs(id string, logs []model.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if logs == nil {
		logs = []model.LogEntry{}
	}
	return writeJSONAtomic(s.logPath(id), logs, 0600)
}

func (s *Store) DeleteLogs(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return removeIfExists(s.logPath(id))
}

// LoadSecrets / SaveSecrets / DeleteSecrets handle one job's credentials.
func (s *Store) LoadSecrets(id string) (Secrets, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sec Secrets
	err := readJSON(s.keyPath(id), &sec)
	return sec, err
}

func (s *Store) SaveSecrets(id string, sec Secrets) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.keyPath(id), sec, 0600)
}

func (s *Store) DeleteSecrets(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return removeIfExists(s.keyPath(id))
}

// LoadSettings / SaveSettings handle the module settings.
func (s *Store) LoadSettings() (model.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := model.Settings{TelegramOnFailure: true}
	err := readJSON(s.settingsPath(), &set)
	return set, err
}

func (s *Store) SaveSettings(set model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSONAtomic(s.settingsPath(), set, 0600)
}

// readJSON leaves v untouched when the file does not exist or is empty.
func readJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeJSONAtomic writes via temp file + rename so a crash never leaves a
// truncated file.
func writeJSONAtomic(path string, v interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(tmp), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
	}
	return nil
}
