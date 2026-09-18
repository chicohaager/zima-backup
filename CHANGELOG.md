# Changelog

## 0.1.0 — 2026-09-18

First release. A ZimaOS module (systemd-sysext) that backs up and mirrors
folders on the Zima itself — the built-in *Backup* tile covers phones.

### Backup (restic 0.19.1, bundled)
- Encrypted, versioned snapshots per job; several jobs may share one
  repository (snapshots are tagged with the job id).
- Retention: keep last / daily / weekly / monthly, pruned after each run.
- Browse any snapshot, restore single files or folders to the original
  place or elsewhere, check the repository.
- Targets: local folder or USB disk, the cloud drives ZimaOS Files is
  signed in to (restic's rclone backend against ZimaOS' own remote
  configuration), another ZimaOS/Linux box over SSH, SFTP server,
  Windows/SMB share, S3-compatible storage.
- Wrong passphrase, locked repository and an unreachable target are
  reported by name, never as a stack trace.

### Sync (rsync / rclone from the ZimaOS base image)
- One-way mirror of folders to a local disk, over SSH (rsync), or to
  SFTP/SMB/S3 (rclone). Each source lands as `<target>/<folder name>`.
- "Mirror deletions" is off by default; the preview (dry run) shows what
  a run would copy and delete before anything happens.
- exFAT/FAT disks: ownership and permissions are not synced there (the
  filesystem cannot keep them), so every run succeeds instead of ending
  with rsync's exit 23.

### Module
- *Drives*: mounted volumes (system disk, pools, USB disks by label or
  model, cloud drives by provider) are listed with free space in the
  target step and as the folder picker's top level.
- *Find on the network*: ZimaOS boxes on the LAN and ZeroTier (mDNS
  `_zimaos._tcp`) and online Tailscale peers can be picked as the target
  host instead of typed.
- Three-step wizard (what · where · when), job cards with live progress
  and the current phase (creating the repository — counted folder by
  folder on slow drives —, backing up, pruning, restoring, checking,
  copying) with transfer rate, bytes moved and time left, a *Log* window with the tool's output of the current or last
  run, history, restore browser, sync preview, Telegram and webhook
  notifications (generic, n8n, Discord, Slack, Home Assistant, Uptime Kuma).
- English, German, French, Chinese — follows the ZimaOS shell language.
- ZFW design tokens, light and dark, no webfont.
- Session authentication against the ZimaOS gateway keys (ES256 JWT);
  the UI renews an expired session itself.
- Boot watchdog and refresh path unit so the module survives the
  systemd-sysext race on ZimaOS 1.7.
- Secrets (passphrases, target passwords) live in `keys/<job>.json`
  (mode 600) and never appear in `jobs.json` or an API response.

### Known limits
- ZimaNet peers are not listed (ZimaNet has no local peer API); Tailscale
  peers appear only when Tailscale runs as a sysext, not from the
  App-Store container.
- Cloud drives are reached directly, never through the folder ZimaOS
  mounts under `/media` (refused as a target: `cloud_mount`). Google
  Drive charges a round trip per file: the first backup of a large
  folder runs at about 0.35 MB/s, a sync of many small files at roughly
  one file per six seconds; later runs only send changes.
- A job whose target hangs (a FUSE mount that stops answering) stays
  "running" until the mount answers; cancel cannot interrupt a process
  stuck in the kernel, and a module upgrade started meanwhile waits for
  it too (measured: `zpkg remove` hung until the stuck restic returned).
- vfat and NTFS disks were not measured; the exFAT handling covers the
  chown/permission case they share.
