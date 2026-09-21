# Changelog

## 0.2.0 — unreleased

Built around one sentence from a tester: *the goal of ZimaOS is a complex
application that beginners can use.* Everything a first backup needs is
now a click; everything else moved under *Advanced*.

### Changed
- **One screen instead of a three-step wizard**: *What do you want to
  protect?* (folder chips with file count and size) → *Where should it
  go?* (drive cards) → *Start*. Name, target folder (`Backups/<folder>`),
  schedule (daily at 03:00), retention (7/7/4/6) and passphrase are filled
  in; the first backup starts right away. Measured: nine clicks, zero
  characters typed.
- **Drive cards in three groups** — drives in this box, network shares,
  cloud drives — with free space; picking a card sets the target.
- **Network shares come from ZimaOS Files**: every share Files has
  connected (`/media/<host>/<share>`) is a card, and *Connect a network
  share…* hands host and credentials to Files' own endpoint
  (`POST /v2_1/files/connect`), so the share is there after a reboot and
  visible in Files too. No SMB form for the everyday case; SMB with own
  credentials stays as an expert target.
- **Schedules in words** — daily at / weekly on / monthly on day / every
  hour at minute / every N minutes / every N hours / manual / cron for
  experts — with the next three runs while choosing, and *next in 19
  hours* in the overview. Cron expressions from 0.1 are shown in words
  where the words fit and as the expression where they do not.
- **A generated passphrase**: seven words from the EFF short wordlist
  (1296 words, `crypto.getRandomValues` with rejection sampling, ≈72
  bits), shown once with *Copy* and *Print / save as PDF*, and a checkbox
  before the backup is created. An own passphrase goes under *Advanced*.
- **Overview as rows**: *from → to · kind · schedule · next · last
  result*; multi-folder jobs are named after their folders
  (*Photos, Documents → USB disk*), not "2 folders".
- **One repository per job**: the default folder is `Backups/<folder>`
  and a second backup job on the same folder is refused (two jobs would
  need one passphrase; a generated one never matches — seen on a tester's
  box as "wrong password").
- The folder picker shows the server next to a share and shortens long
  paths from the left.
- Cloud sync runs with 16 parallel transfers and 32 checkers; the
  transfer line shows **files per second** first, because Google Drive
  charges about one second per file whatever its size (measured, four
  variants), and the dialog says so before a sync to a cloud drive.
- A run whose sources are empty ends as *nothing to back up* (yellow),
  not as a green success.

### Fixed
- rclone's real cause is no longer swallowed: a wrong SMB password reads
  *user or password refused*, a missing share *share not found on the
  server*, a repository the server cannot serve (one Samba answered
  HTTP 500 for a `config` directory — the tester's "unable to open config
  file: unexpected HTTP response (500)") *repository at the target
  unreadable*, each with the line rclone printed. The repository probe
  is bounded to 90 s instead of restic's quarter-hour retry, and a
  timed-out tool is killed together with its children (a child holding
  stderr kept a 30 s wait alive; now 1 s).
- A missing mount is named in words: *the network share \\server\\share
  is not connected — connect it in Files or with 'Connect a network
  share…'* / *the drive sdb is not mounted — is it plugged in?*
- *Copy* buttons (passphrase, SSH key) did nothing on `http://<lan-ip>`,
  where the browser has no clipboard API; they now fall back to
  `execCommand` and say *Not copied — select the text and press Ctrl+C*
  when even that fails, instead of failing silently.
- Printing the passphrase uses the print dialog instead of a download
  (Chrome flags every download from an http page as insecure).

### Added
- `GET /api/folders/stat` — files, folders and bytes of a source
  (bounded: 20 000 entries or 3 s, then marked truncated).
- `POST /api/schedule/validate` accepts a `form` and answers with the
  expression and the form; the `lan` volume kind in `GET /api/mounts`
  with its `host`.
- Screenshots, README and HowTo for the new flow.

## 0.1.1 — 2026-09-19

### Fixed
- A cancelled backup left restic's lock file in the repository, and restic
  never removes it by itself: every later run reported *completed* but
  its retention pass failed with "repository is already locked", so
  snapshots were never pruned, and *Check repository* failed. The module
  now runs `restic unlock` before backup, restore and check; it only
  removes locks whose process is gone or that are older than 30 minutes,
  so a job running on the same repository is left alone.
- The release asset `zbackup.raw.sha256` named `zbackup-amd64.raw`, so
  `sha256sum -c` could not verify the download.

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
