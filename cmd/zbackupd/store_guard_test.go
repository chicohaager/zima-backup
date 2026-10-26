package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
