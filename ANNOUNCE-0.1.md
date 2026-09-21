# Forum post (EN) — community.zimaspace.com

**Title:** Sync & Backup — a native ZimaOS module: encrypted backups with restore, one-way sync to USB, cloud drives, another Zima or a server

---

A while ago someone here wrote: *"If a developer could create a sync and backup app as ergonomic and easy to use as [Lintux Firewall, Cron], that would be great."* — so here it is. Third module in the same family as **ZFW** and **Cron**: a native ZimaOS module (systemd-sysext, no Docker), a tile on the dashboard, ZimaOS session login, four languages, light and dark.

ZimaOS' own *Backup* tile backs up **phones** to the Zima. This module covers **the folders on the Zima itself** — the AppData of your apps, your documents, photos, media. Both coexist.

![Overview](https://raw.githubusercontent.com/chicohaager/zima-backup/main/docs/img/overview.png)

**Two kinds of job**

- **Backup** — encrypted, versioned snapshots with [restic](https://restic.net) (bundled). Keep last / daily / weekly / monthly, browse any point in time, restore single files or whole folders, check the repository. The first run sends everything, every later run only what changed.
- **Sync** — a plain one-way mirror with rsync or rclone. Readable anywhere without any tool. "Mirror deletions" is a switch, **off** by default, and a **preview** shows what a run would copy and delete before it runs.

**Where it can go**

- a **USB disk** or any local folder — the target step lists what is mounted (system disk, pools, USB disks by label, free space); one click sets the folder. An unplugged disk is refused instead of silently filling the system disk.
- the **cloud drives ZimaOS Files is signed in to** (Google Drive, OneDrive, …) — the module talks to the drive directly through ZimaOS' own rclone configuration, not through the mounted folder. Measured against Google Drive: the mounted folder needed >10 minutes just to create restic's repository skeleton; the direct path does it in 14–46 s.
- **another ZimaOS or Linux box over SSH** — the module has its own key; paste the public key into `authorized_keys` on the other box. *Find on the network* lists other ZimaOS boxes on the LAN (they announce themselves via mDNS) and your Tailscale peers, one click fills in the host.
- **SFTP**, **Windows/SMB share**, **S3-compatible storage**.

**The rest**

- three-step wizard: *what · where · when*
- schedule: manual, every N hours, or a cron expression with a live check that shows the next runs
- job cards with live progress, phase, transfer rate; a *Log* window with the tool's own output; run history
- notifications: Telegram, or a webhook in generic JSON / n8n / Discord / Slack / Home Assistant / Uptime Kuma format
- exFAT/FAT USB disks work without the usual ownership errors
- secrets stay in `/DATA/AppData/zbackup/keys/` (mode 600) and never appear in an API response

<table><tr>
<td><img src="https://raw.githubusercontent.com/chicohaager/zima-backup/main/docs/img/wizard-where.png" width="380"></td>
<td><img src="https://raw.githubusercontent.com/chicohaager/zima-backup/main/docs/img/restore.png" width="380"></td>
</tr></table>

**Install** (ZimaOS 1.7.x; verified on amd64 — the arm64 image is built by CI but I have no arm64 box, reports welcome)

Two ways, both need SSH (Settings → General → Developer mode → View → SSH Access):

*1. One installer for the Lintux modules* — you pick whether the firewall comes along. Each script installs what is missing and updates what is old, nothing else; run it again later to update.

Cron + Sync & Backup, **without** the firewall:

```
curl -fsSL https://raw.githubusercontent.com/chicohaager/lintux-modkit/main/install-without-firewall.sh -o /tmp/lintux-install.sh
sudo bash /tmp/lintux-install.sh
```

ZFW Firewall + Cron + Sync & Backup:

```
curl -fsSL https://raw.githubusercontent.com/chicohaager/lintux-modkit/main/install-with-firewall.sh -o /tmp/lintux-install.sh
sudo bash /tmp/lintux-install.sh
```

*2. Just this module, by hand:* download `zbackup-amd64.raw` (or `-arm64`) from the release, copy it to the box **as `zbackup.raw`** — zpkg matches the file name against the extension name — and run:

```
sudo zpkg install /tmp/zbackup.raw
```

Either way the *Sync & Backup* tile appears on the dashboard; the UI follows your ZimaOS language (EN/DE/FR/ZH). Step-by-step with the expected output: see the installation thread (link below).

**Repo:** https://github.com/chicohaager/zima-backup
**Release v0.1.1:** https://github.com/chicohaager/zima-backup/releases/tag/v0.1.1
**Step-by-step HowTo (first backup, restore, sync, cloud, SSH):** https://github.com/chicohaager/zima-backup/blob/main/HOWTO.md
**Installation thread:** *link to the installation post*

Verified on ZimaOS 1.7.1 (ZimaCube Pro): 27 end-to-end checks against `/DATA`, a mergerfs pool, ext4 and exFAT USB disks, across a reboot; SSH/SFTP/SMB targets against a Linux box; Google Drive as a direct target. Known limits are in the changelog — the honest one: Google Drive charges one round trip per file, so the first backup of a big folder is an overnight job (later runs are seconds).

Not affiliated with IceWhale. Feedback and issues welcome — and please write down your passphrase, without it nobody can restore, not even me. 🙂
