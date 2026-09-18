package localfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A synthetic tree as measured on ZimaOS 1.7.1: / on the root device,
// /DATA and its bind /media on the data partition, /media/sdb a USB disk
// with its own device, /media/sda an unplugged disk's leftover folder.
func fakeDev(p string) (uint64, bool) {
	switch {
	case p == "/media/sdb" || strings.HasPrefix(p, "/media/sdb/"):
		return 3, true
	case p == "/DATA" || strings.HasPrefix(p, "/DATA/") || p == "/media" || strings.HasPrefix(p, "/media/"):
		return 2, true
	case p == "/mnt" || strings.HasPrefix(p, "/mnt/") || p == "/":
		return 1, true
	}
	return 0, false
}

func TestMountPointFollowsDeviceBoundaries(t *testing.T) {
	cases := map[string]string{
		"/media/sdb/backups/photos": "/media/sdb",
		"/media/sdb":                "/media/sdb",
		"/media/sda":                "/media", // unplugged: the folder sits on /media itself
		"/media":                    "/media",
		"/DATA/backup":              "/DATA",
		"/mnt/usb":                  "/",
		"/":                         "/",
	}
	for dir, want := range cases {
		if got := mountPoint(dir, fakeDev); got != want {
			t.Errorf("mountPoint(%s) = %s, want %s", dir, got, want)
		}
	}
}

// Against the real filesystem: with the temp dir's own mount declared a
// container, a missing subfolder is refused and nothing is created; with
// no containers declared, nested folders are created.
func TestEnsureTargetHonoursContainerMounts(t *testing.T) {
	base := t.TempDir()
	defer func() { ContainerMounts = nil }()

	ContainerMounts = []string{mountPoint(base, deviceOf)}
	target := filepath.Join(base, "not-mounted", "backup")
	if err := EnsureTarget(target); err == nil {
		t.Fatal("target inside a container mount was accepted")
	}
	if _, err := os.Stat(filepath.Join(base, "not-mounted")); !os.IsNotExist(err) {
		t.Fatal("refused target was created anyway")
	}

	ContainerMounts = nil
	if err := EnsureTarget(target); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		t.Fatal("target not created")
	}
	file := filepath.Join(base, "file")
	_ = os.WriteFile(file, nil, 0600)
	if err := EnsureTarget(filepath.Join(file, "x")); err == nil {
		t.Fatal("a file as ancestor was accepted")
	}
}
