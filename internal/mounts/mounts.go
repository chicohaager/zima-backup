// Package mounts lists the volumes a local target can live on: the system
// disk, USB and other disks under /media, storage pools, and the cloud
// drives ZimaOS Files mounts. It reads /proc/self/mounts and sysfs; the
// shapes were measured on ZimaOS 1.7.1 (see the tests).
package mounts

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Volume is one mounted filesystem shown to the user.
type Volume struct {
	Name   string `json:"name"`   // label, model, cloud provider or pool name
	Path   string `json:"path"`   // mount point
	Kind   string `json:"kind"`   // system, usb, disk, pool, cloud
	FSType string `json:"fstype"` // ext4, exfat, fuse.mergerfs, fuse.rclone, …
	Size   uint64 `json:"size"`   // bytes, 0 when unknown
	Free   uint64 `json:"free"`
}

// Volume kinds.
const (
	KindSystem = "system"
	KindUSB    = "usb"
	KindDisk   = "disk"
	KindPool   = "pool"
	KindCloud  = "cloud"
)

// probe answers the sysfs/udev questions List needs; tests inject one.
type probe struct {
	isUSB  func(dev string) bool   // device sits on a USB bus
	label  func(dev string) string // filesystem label
	model  func(dev string) string // vendor + model
	statfs func(path string) (uint64, uint64)
}

// List returns the volumes, system disk first, then pools, disks, cloud.
func List() []Volume {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return []Volume{}
	}
	defer f.Close()
	return list(f, sysProbe())
}

type mountLine struct {
	dev, path, fstype string
}

func list(r io.Reader, p probe) []Volume {
	var lines []mountLine
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		lines = append(lines, mountLine{dev: unescape(fields[0]), path: unescape(fields[1]), fstype: fields[2]})
	}
	seenDev := map[string]bool{}
	systemDisk := ""
	for _, m := range lines {
		if m.path == "/DATA" {
			systemDisk = baseDevice(m.dev)
		}
	}
	var out []Volume
	// /media paths first so a disk bound into /DATA as well is named by its
	// /media mount (ZimaOS binds e.g. /media/sdb to /DATA/Media)
	sort.SliceStable(lines, func(i, j int) bool {
		return strings.HasPrefix(lines[i].path, "/media/") && !strings.HasPrefix(lines[j].path, "/media/")
	})
	for _, m := range lines {
		v, ok := classify(m, p)
		if !ok {
			continue
		}
		if m.path != "/DATA" && strings.HasPrefix(m.dev, "/dev/") && baseDevice(m.dev) == systemDisk {
			continue // other partitions of the system disk (boot, overlay, the /media bind)
		}
		if strings.HasPrefix(m.dev, "/dev/") {
			if seenDev[m.dev] {
				continue
			}
			seenDev[m.dev] = true
		}
		v.Size, v.Free = p.statfs(v.Path)
		out = append(out, v)
	}
	order := map[string]int{KindSystem: 0, KindPool: 1, KindUSB: 2, KindDisk: 3, KindCloud: 4}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Kind] != order[out[j].Kind] {
			return order[out[i].Kind] < order[out[j].Kind]
		}
		return out[i].Path < out[j].Path
	})
	if out == nil {
		out = []Volume{}
	}
	return out
}

// classify decides whether a mount is a volume worth showing and names it.
func classify(m mountLine, p probe) (Volume, bool) {
	switch {
	case m.path == "/DATA":
		return Volume{Name: "ZimaOS-HD", Path: m.path, Kind: KindSystem, FSType: m.fstype}, true
	case m.fstype == "fuse.rclone" && strings.HasPrefix(m.path, "/media/"):
		return Volume{Name: cloudName(m.dev), Path: m.path, Kind: KindCloud, FSType: m.fstype}, true
	case m.fstype == "fuse.mergerfs" && strings.HasPrefix(m.path, "/DATA/"):
		return Volume{Name: filepath.Base(m.path), Path: m.path, Kind: KindPool, FSType: m.fstype}, true
	case strings.HasPrefix(m.dev, "/dev/") && (underOnce(m.path, "/media") || underOnce(m.path, "/mnt") || (strings.HasPrefix(m.path, "/DATA/") && !strings.HasPrefix(m.path, "/DATA/."))):
		base := baseDevice(m.dev)
		v := Volume{Path: m.path, Kind: KindDisk, FSType: m.fstype}
		if p.isUSB(base) {
			v.Kind = KindUSB
		}
		v.Name = p.label(m.dev)
		if v.Name == "" {
			v.Name = p.model(base)
		}
		if v.Name == "" {
			v.Name = filepath.Base(m.path)
		}
		return v, true
	}
	return Volume{}, false
}

// underOnce says whether path is a direct child of dir.
func underOnce(path, dir string) bool {
	return strings.HasPrefix(path, dir+"/") && !strings.Contains(strings.TrimPrefix(path, dir+"/"), "/")
}

// baseDevice strips the partition: /dev/sdc1 → sdc, /dev/nvme0n1p8 → nvme0n1.
func baseDevice(dev string) string {
	name := strings.TrimPrefix(dev, "/dev/")
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		if i := strings.LastIndex(name, "p"); i > 0 {
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				return name[:i]
			}
		}
		return name
	}
	return strings.TrimRight(name, "0123456789")
}

var cloudProviders = map[string]string{
	"google_drive": "Google Drive", "onedrive": "OneDrive", "dropbox": "Dropbox", "box": "Box",
	"adrive": "Aliyun Drive", "s3": "S3", "webdav": "WebDAV",
}

var cloudSuffix = regexp.MustCompile(`_[0-9a-f]{6,}$`)

// cloudName turns rclone's mount source "google_drive_252f21c18474:" into
// "Google Drive" (ZimaOS names its remotes <provider>_<id>).
func cloudName(src string) string {
	name := cloudSuffix.ReplaceAllString(strings.TrimSuffix(src, ":"), "")
	if pretty, ok := cloudProviders[name]; ok {
		return pretty
	}
	return strings.ReplaceAll(name, "_", " ")
}

// unescape decodes the octal escapes /proc/mounts uses for spaces.
func unescape(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

func sysProbe() probe {
	labels := map[string]string{}
	if entries, err := os.ReadDir("/dev/disk/by-label"); err == nil {
		for _, e := range entries {
			if target, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-label", e.Name())); err == nil {
				labels[target] = unescape(e.Name())
			}
		}
	}
	return probe{
		isUSB: func(dev string) bool {
			// the sysfs path of a USB disk runs through the usb bus (measured: /usb4/)
			real, err := filepath.EvalSymlinks("/sys/class/block/" + dev)
			return err == nil && strings.Contains(real, "/usb")
		},
		label: func(dev string) string { return labels[dev] },
		model: func(dev string) string {
			vendor, _ := os.ReadFile("/sys/class/block/" + dev + "/device/vendor")
			model, _ := os.ReadFile("/sys/class/block/" + dev + "/device/model")
			return strings.TrimSpace(strings.TrimSpace(string(vendor)) + " " + strings.TrimSpace(string(model)))
		},
		statfs: func(path string) (uint64, uint64) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(path, &st); err != nil {
				return 0, 0
			}
			return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
		},
	}
}
