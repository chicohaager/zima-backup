// Package localfs holds the one rule both runners share for local targets:
// a target must sit on a mounted disk, never inside an empty mount point.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/netpath"
)

// ContainerMounts are the filesystems a target must not end up on: the
// root filesystem and the directories ZimaOS mounts disks *into*. When a
// USB disk is unplugged, /media/sdb is just an empty folder on /media —
// writing there would fill the system disk instead of the backup disk
// (measured on ZimaOS 1.7.1: /media is a bind of the /DATA partition, a
// USB disk is its own mount below it). main sets this; tests leave it
// empty or set their own.
var ContainerMounts []string

// EnsureTarget creates path (and missing parents) when the deepest
// existing ancestor lies on a filesystem mounted below the container
// mounts, e.g. /media/sdb/backups/photos on a mounted USB disk. It refuses
// /media/sdb/... while that disk is not mounted, because the mount point
// then belongs to /media itself.
func EnsureTarget(path string) error {
	if err := Check(path); err != nil {
		return err
	}
	return os.MkdirAll(path, 0700)
}

// Check is EnsureTarget without the mkdir, for dry runs.
func Check(path string) error {
	existing := filepath.Clean(path)
	for {
		if st, err := os.Stat(existing); err == nil {
			if !st.IsDir() {
				return fmt.Errorf("%s is not a folder", existing)
			}
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("no existing folder above %s", path)
		}
		existing = parent
	}
	mp := mountPoint(existing, deviceOf)
	for _, c := range ContainerMounts {
		if mp == c {
			if host, share, ok := shareOf(path); ok {
				return &MissingShare{Host: host, Share: share, Existing: existing, Mount: mp}
			}
			return fmt.Errorf("%s (nearest existing folder %s sits on %s)", describeMissing(path), existing, mp)
		}
	}
	return nil
}

// MissingShare is a target on a network share Files has not mounted.
type MissingShare struct {
	Host, Share     string
	Existing, Mount string // nearest existing folder and the filesystem it sits on
}

func (m *MissingShare) Error() string {
	return fmt.Sprintf("the network share \\\\%s\\%s is not connected — connect it in Files or with \"Connect a network share…\" (nearest existing folder %s sits on %s)", m.Host, m.Share, m.Existing, m.Mount)
}

// shareOf reads /media/<host>/<share>/…: Files mounts network shares
// there, and the host carries a dot (a name or an address).
func shareOf(path string) (host, share string, ok bool) {
	rel := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/media/"), "/")
	if strings.HasPrefix(path, "/media/") && len(rel) >= 2 && strings.Contains(rel[0], ".") {
		return rel[0], rel[1], true
	}
	return "", "", false
}

// Explain says why the host of a missing share is not there; main sets it
// (netpath against the real network), tests leave it nil or set their own.
var Explain func(ctx context.Context, host string) netpath.Reason

// Unavailable is the result of a run whose target is not there. For a
// network share it carries the reason (Tailscale down, machine offline,
// machine answers but the share is not mounted) as a detail the UI
// translates.
func Unavailable(ctx context.Context, err error) model.Result {
	res := model.Result{Code: model.CodeTargetUnavailable, Message: err.Error()}
	var ms *MissingShare
	if !errors.As(err, &ms) || Explain == nil {
		return res
	}
	r := Explain(ctx, ms.Host)
	if r.Kind == "" {
		return res
	}
	res.Message += " — " + r.Sentence()
	d := &model.ResultDetail{Kind: model.DetailShareMissing, Target: `\\` + ms.Host + `\` + ms.Share, NoteKind: r.Kind, Net: r.Net, State: r.State}
	if !r.LastSeen.IsZero() {
		d.Seen = r.LastSeen.UnixMilli()
	}
	res.Detail = d
	return res
}

// describeMissing says in words why a target under /media is not there:
// Files mounts network shares at /media/<host>/<share> (the host carries
// a dot: a name or an address), disks at /media/<device or label>.
func describeMissing(path string) string {
	rel := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/media/"), "/")
	if strings.HasPrefix(path, "/media/") && len(rel) >= 2 && strings.Contains(rel[0], ".") {
		return fmt.Sprintf("the network share \\\\%s\\%s is not connected — connect it in Files or with \"Connect a network share…\"", rel[0], rel[1])
	}
	if strings.HasPrefix(path, "/media/") && len(rel) >= 1 && rel[0] != "" {
		return fmt.Sprintf("the drive %s is not mounted — is it plugged in?", rel[0])
	}
	return fmt.Sprintf("target folder %s is not on a mounted disk", path)
}

// mountPoint walks up from dir until the device number changes; dev
// reports (device, ok) for a path. Bind mounts of the same filesystem
// (ZimaOS binds /DATA to /media) share the device, so /media/x on an
// unplugged disk resolves to /media's own mount, which is the point.
func mountPoint(dir string, dev func(string) (uint64, bool)) string {
	dir = filepath.Clean(dir)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		d, ok := dev(dir)
		p, pok := dev(parent)
		if !ok || !pok || d != p {
			return dir
		}
		dir = parent
	}
}

// MountPointOf is the mount point of an existing directory.
func MountPointOf(dir string) string { return mountPoint(dir, deviceOf) }

func deviceOf(p string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, false
	}
	return st.Dev, true
}
