package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/api"
)

// /DATA/AppData is 0777 without the sticky bit on ZimaOS 1.8 (KB §79.11): the jobs
// and the rclone credentials in /DATA/AppData/zbackup are used by root, so a
// swapped folder would make root copy or delete whatever it names. The folder is
// checked before the store opens it.
func TestAStoreFolderRootCannotTrustIsRefused(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(filepath.Join(base, "zbackup")); err != nil {
		t.Fatalf("a fresh folder: %v", err)
	}
	real := filepath.Join(base, "planted")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "zbackup-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked folder: err %v", err)
	}
}

// A refused folder after the wait: 0.3.5 exited and systemd restarted it over and
// over (Cron on the test NAS, 2026-10-28: /DATA of uid 999), no job ran and nothing showed
// why. Now every API route answers 503 storage_refused with the reason, the app
// files are still served, and the recheck notices a fixed folder.
func TestARefusedFolderIsShownAndRecheckedNotLooped(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "planted")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "zbackup")
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(path, 2, time.Millisecond)
	if st != nil || err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("openStore on a symlink: %v %v", st, err)
	}
	srv := httptest.NewServer(refusedHandler(err.Error()))
	defer srv.Close()
	for _, p := range []string{"/api/health", "/api/jobs", "/api/settings"} {
		resp, err := http.Get(srv.URL + api.RoutePrefix + p)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		raw, _ := json.Marshal(body)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "storage_refused") || !strings.Contains(string(raw), "symlink") {
			t.Fatalf("GET %s while refused: %d %s", p, resp.StatusCode, raw)
		}
	}

	ready := make(chan struct{})
	go recheckStore(path, 10*time.Millisecond, func() { close(ready) })
	select {
	case <-ready:
		t.Fatal("recheck passed while the folder is still a symlink")
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.Remove(path); err != nil { // the fix
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("recheck did not notice the fixed folder")
	}
}
