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
- Targets: local folder or USB disk, another ZimaOS/Linux box over SSH,
  SFTP server, Windows/SMB share, S3-compatible storage.
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
- *Find on the network*: ZimaOS boxes on the LAN and ZeroTier (mDNS
  `_zimaos._tcp`) and online Tailscale peers can be picked as the target
  host instead of typed.
- Three-step wizard (what · where · when), job cards with live progress,
  history, restore browser, sync preview, Telegram and webhook
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
- Cloud drives mounted by ZimaOS Files (`/media/google_drive_…`) work as
  a *sync* target but are slow (measured: ~50 s per file to Google Drive)
  and are impractical for a restic repository (creating its 256 data
  folders took more than ten minutes). A native cloud backend follows.
- A job whose target hangs (a FUSE mount that stops answering) stays
  "running" until the mount answers; cancel cannot interrupt a process
  stuck in the kernel.
- vfat and NTFS disks were not measured; the exFAT handling covers the
  chown/permission case they share.
