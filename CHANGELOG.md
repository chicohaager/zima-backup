# Changelog

## 0.3.0 — unreleased

### Added
- **System backup**: a third kind next to Backup and Sync. One click backs
  up what makes the box *this* box — the `/etc` overlay (network, users,
  hostname, SSH keys), the ZimaOS state (apps, users, app store, file
  service), the installed modules and `/DATA/AppData`; optionally all of
  `/DATA`. restic with `--one-file-system`, a manifest (ZimaOS version,
  RAUC slots, partition table, image digests, apps, modules — no secrets)
  and consistent `sqlite3 .backup` copies of the six state databases.
  Measured on a well-used box: 85.8 GiB in 315 s, the next run 8 MiB in
  10 s.
- **Restore the system**: a guided dialog reads the snapshot's manifest
  (version, hostname, apps, databases), warns when the snapshot comes from
  another ZimaOS version, and wants the word RESTORE. The restore stops the
  ZimaOS services and every container, writes `/etc` back through the
  overlay, replaces the state directories, checks every database, brings
  every compose project up from the directory it ran in, starts the rest
  again and asks for a reboot. On a fresh install of the same ZimaOS
  version this is the bare-metal path: install, add the module, restore.

### Fixed
- After the ZimaOS user service restarts it signs with a new key; the
  module refreshes its key set once when a token matches none, instead of
  rejecting every login until its cache expires (lintux-modkit).

## 0.2.1 — unreleased

### Fixed
- The dashboard tile and the page header showed Cron's clock — the icon
  file was a copy of Cron's (reported by a tester on 0.2.0). Sync & Backup
  now has its own: two arrows around a small disk, same tile and palette
  as the rest of the family.

## 0.2.0 — 2026-09-21

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
- **Restore → Browse…** opened the folder picker *behind* the restore
  dialog (same stacking level, earlier in the DOM), so the button looked
  dead. The picker and the confirmation dialog now sit above every other
  dialog (found by the tester on 0.2.0-dev8; present since 0.1.0).
- **Restoring a file whose name contains `[`, `]`, `*` or `?`** restored
  nothing: restic reads `--include` as a glob pattern. Measured with
  0.19.1: `--include "/x/a[1].txt"` → 0 files, escaped → 1 file. The
  ticked paths are escaped now; a test backs up `a[1].txt` and restores
  it by its literal path (present since 0.1.0).
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
- Spanish as the fifth UI language (all 290 strings, drafted by a
  non-native speaker — corrections welcome; the module follows a Spanish
  ZimaOS shell and the language can be picked in the header).
- Built with Go 1.26.8 (govulncheck: no known issues; 0.1.1, built with
  Go 1.22.2, carried 43 known standard-library issues). CasaOS-Common
  and its dependency tree are gone — the gateway route is registered
  through lintux-modkit's `gateway` package.

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
