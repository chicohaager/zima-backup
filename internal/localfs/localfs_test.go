package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/netpath"
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

// The words for a missing mount name the thing to plug in or connect.
func TestDescribeMissing(t *testing.T) {
	cases := map[string]string{
		"/media/192.0.2.20/Test/zb": `the network share \\192.0.2.20\Test is not connected`,
		"/media/nas.local/Backups":  `the network share \\nas.local\Backups is not connected`,
		"/media/sdb/Backups":        "the drive sdb is not mounted",
		"/media/SR-DATA1":           "the drive SR-DATA1 is not mounted",
		"/mnt/x/y":                  "target folder /mnt/x/y is not on a mounted disk",
	}
	for p, want := range cases {
		if got := describeMissing(p); !strings.HasPrefix(got, want) {
			t.Errorf("describeMissing(%q) = %q, want prefix %q", p, got, want)
		}
	}
}

func TestUnavailableNamesWhyAShareIsMissing(t *testing.T) {
	old := Explain
	t.Cleanup(func() { Explain = old })
	var asked string
	Explain = func(_ context.Context, host string) netpath.Reason {
		asked = host
		return netpath.Reason{Kind: netpath.TailscaleOffline, Net: netpath.NetTailscale, Host: host, LastSeen: time.UnixMilli(1791000000000)}
	}
	err := &MissingShare{Host: "cube.tail0000.ts.net", Share: "Backup", Existing: "/media", Mount: "/media"}
	r := Unavailable(context.Background(), err)
	if asked != "cube.tail0000.ts.net" || r.Code != model.CodeTargetUnavailable {
		t.Fatalf("asked %q, result %+v", asked, r)
	}
	d := r.Detail
	if d == nil || d.Kind != model.DetailShareMissing || d.NoteKind != netpath.TailscaleOffline || d.Net != netpath.NetTailscale || d.Seen != 1791000000000 || d.Target != `\\cube.tail0000.ts.net\Backup` {
		t.Fatalf("detail %+v", d)
	}
	if !strings.Contains(r.Message, `\\cube.tail0000.ts.net\Backup is not connected`) || !strings.Contains(r.Message, "offline in Tailscale") {
		t.Errorf("message %q", r.Message)
	}
	// any other error stays as it was, and so does a share without an explainer
	if r := Unavailable(context.Background(), errors.New("target folder /x is not on a mounted disk")); r.Detail != nil || r.Message != "target folder /x is not on a mounted disk" {
		t.Errorf("plain error: %+v", r)
	}
	Explain = nil
	if r := Unavailable(context.Background(), err); r.Detail != nil {
		t.Errorf("no explainer: %+v", r)
	}
}

func TestCheckReturnsMissingShareForAnUnmountedShare(t *testing.T) {
	old := ContainerMounts
	t.Cleanup(func() { ContainerMounts = old })
	ContainerMounts = []string{"/"}
	err := Check("/media/zbackup-no-such.host/Share/x")
	var ms *MissingShare
	if !errors.As(err, &ms) || ms.Host != "zbackup-no-such.host" || ms.Share != "Share" {
		t.Fatalf("got %v", err)
	}
	if err := Check("/media/zbackup-no-such-disk/x"); errors.As(err, &ms) {
		t.Errorf("a disk is not a share: %v", err)
	}
}
