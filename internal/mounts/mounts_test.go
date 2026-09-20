package mounts

import (
	"strings"
	"testing"
)

// /proc/self/mounts of a ZimaOS 1.7.1 box (measured 2026-09-18), cut to
// the lines that matter: system partitions, two USB disks (one also bound
// into /DATA/Media), a third USB disk with two labelled partitions, a
// mergerfs pool, the encrypted folder, docker overlays, two cloud drives —
// plus (measured 2026-09-20) the three mounts Files creates for one SMB
// share connected via POST /v2_1/files/connect; only the /media one counts.
const zimaMounts = `/dev/nvme0n1p7 /mnt/overlay ext4 rw 0 0
/dev/nvme0n1p1 /mnt/boot vfat rw 0 0
/dev/nvme0n1p8 /media ext4 rw 0 0
/dev/nvme0n1p8 /DATA ext4 rw 0 0
/dev/sdb /media/sda exfat rw 0 0
/dev/sda /media/sdb ext4 rw 0 0
/dev/sdc1 /DATA/srtest/data1 ext4 rw 0 0
/dev/sda /DATA/Media ext4 rw 0 0
/dev/sdc2 /DATA/srtest/data2 ext4 rw 0 0
1:2 /DATA/StorageTest fuse.mergerfs rw 0 0
zimaos-encrypted-folder /DATA/Encrypted fuse.zimaos-encrypted-folder rw 0 0
overlay /DATA/.docker/overlay2/fa13/merged overlay rw 0 0
google_drive_252f21c18474: /media/google_drive_252f21c18474 fuse.rclone rw 0 0
onedrive_0bf38c4a183b: /media/onedrive_0bf38c4a183b fuse.rclone rw 0 0
//192.168.77.20/Test /media/192.168.77.20/Test cifs rw,nosuid,nodev,noatime,vers=3.1.1,username=tester,uid=0,gid=0 0 0
//192.168.77.20/Test /var/lib/casaos_data/.media/192.168.77.20/Test cifs rw,vers=3.1.1 0 0
//192.168.77.20/Test /DATA/.media/192.168.77.20/Test cifs rw,vers=3.1.1 0 0
`

func fakeProbe() probe {
	labels := map[string]string{"/dev/sda": "immich", "/dev/sdc1": "SR-DATA1", "/dev/sdc2": "SR-DATA2", "/dev/nvme0n1p8": "casaos-data"}
	models := map[string]string{"sda": "CT1000P3 10SSD8", "sdb": "ASMT 2115", "sdc": "ATA Hitachi HTS72323"}
	return probe{
		isUSB:  func(dev string) bool { return strings.HasPrefix(dev, "sd") },
		label:  func(dev string) string { return labels[dev] },
		model:  func(dev string) string { return models[dev] },
		statfs: func(path string) (uint64, uint64) { return 100, 40 },
	}
}

func TestListNamesTheVolumesAZimaBoxShows(t *testing.T) {
	got := list(strings.NewReader(zimaMounts), fakeProbe())
	want := []struct{ name, path, kind string }{
		{"ZimaOS-HD", "/DATA", KindSystem},
		{"StorageTest", "/DATA/StorageTest", KindPool},
		{"SR-DATA1", "/DATA/srtest/data1", KindUSB},
		{"SR-DATA2", "/DATA/srtest/data2", KindUSB},
		{"ASMT 2115", "/media/sda", KindUSB}, // exFAT without label: the model names it
		{"immich", "/media/sdb", KindUSB},    // labelled; its /DATA/Media bind is not listed twice
		{"Test", "/media/192.168.77.20/Test", KindLAN},
		{"Google Drive", "/media/google_drive_252f21c18474", KindCloud},
		{"OneDrive", "/media/onedrive_0bf38c4a183b", KindCloud},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d volumes: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Name != w.name || got[i].Path != w.path || got[i].Kind != w.kind {
			t.Errorf("volume %d = %+v, want %+v", i, got[i], w)
		}
		if got[i].Size != 100 || got[i].Free != 40 {
			t.Errorf("volume %d lacks statfs numbers", i)
		}
	}
	for _, v := range got {
		if strings.Contains(v.Path, ".media") || strings.Contains(v.Path, ".docker") || v.Path == "/media" || v.Path == "/mnt/boot" || v.Path == "/DATA/Media" || v.Path == "/DATA/Encrypted" {
			t.Errorf("%s must not be listed", v.Path)
		}
	}
	for _, v := range got {
		if v.Kind == KindLAN && v.Host != "192.168.77.20" {
			t.Errorf("lan volume %+v must carry its host", v)
		}
	}
	if got := list(strings.NewReader(""), fakeProbe()); got == nil || len(got) != 0 {
		t.Fatal("no mounts must be an empty list, not null")
	}
}

func TestHelpers(t *testing.T) {
	for dev, want := range map[string]string{"/dev/sdc1": "sdc", "/dev/sda": "sda", "/dev/nvme0n1p8": "nvme0n1", "/dev/mmcblk0p2": "mmcblk0"} {
		if got := baseDevice(dev); got != want {
			t.Errorf("baseDevice(%s) = %s", dev, got)
		}
	}
	for src, want := range map[string]string{"google_drive_252f21c18474:": "Google Drive", "onedrive_0bf38c4a183b:": "OneDrive", "my_nextcloud_ab12cd34:": "my nextcloud"} {
		if got := cloudName(src); got != want {
			t.Errorf("cloudName(%s) = %s", src, got)
		}
	}
	if got := unescape(`/media/My\040Disk`); got != "/media/My Disk" {
		t.Errorf("unescape = %q", got)
	}
}

func TestRemotesReadSectionNamesOnly(t *testing.T) {
	conf := "[onedrive_0bf38c4a183b]\ntype = onedrive\ntoken = {\"access_token\":\"SECRET\"}\n\n[google_drive_252f21c18474]\ntype = drive\nclient_id = x\n"
	got := remotes(strings.NewReader(conf))
	if len(got) != 2 || got[0].Name != "Google Drive" || got[0].Remote != "google_drive_252f21c18474" || got[1].Name != "OneDrive" {
		t.Fatalf("remotes = %+v", got)
	}
	for _, r := range got {
		if strings.Contains(r.Name+r.Remote, "SECRET") {
			t.Fatal("a value leaked into the remote list")
		}
	}
	if got := remotes(strings.NewReader("")); got == nil || len(got) != 0 {
		t.Fatal("empty config must give an empty list")
	}
}
