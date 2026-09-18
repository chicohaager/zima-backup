# Sync & Backup for ZimaOS

Folder backups with versions and restore, and one-way sync to a second disk,
another ZimaOS box or a server — as a ZimaOS module in the same family as
[ZFW](https://github.com/chicohaager/zfw) and [Cron](https://github.com/chicohaager/cron).

ZimaOS' own *Backup* tile backs up phones to the Zima; this module covers
the folders on the Zima itself. Both coexist.

## Features

- **Backup** — encrypted, versioned snapshots with [restic](https://restic.net)
  (bundled): retention (last / daily / weekly / monthly), browse any point
  in time, restore single files or whole folders, check the repository.
- **Sync** — a plain one-way mirror with rsync (local disk, SSH) or rclone
  (SFTP, SMB, S3). "Mirror deletions" is a switch, off by default; a
  preview shows what a run would copy and delete before it runs.
- **Targets** — local folder or USB disk, another ZimaOS/Linux box over SSH
  with the module's own key, SFTP server, Windows/SMB share,
  S3-compatible storage.
- **Find on the network** — other ZimaOS boxes on the LAN (and on a mesh
  that carries multicast, such as ZeroTier) are discovered through their
  own `_zimaos._tcp` announcement, Tailscale peers through the local
  `tailscale` daemon; one click fills in the host.
- **Schedule** — manual, every N hours, or a cron expression with a live
  check and the next run times.
- **Notifications** — Telegram, or a webhook in generic JSON, n8n,
  Discord, Slack, Home Assistant or Uptime Kuma format.
- **Ergonomics** — three-step wizard (what · where · when), folder picker,
  job cards with live progress, history; English, German, French and
  Chinese, following the ZimaOS shell language; light and dark.
- **Safety** — an unplugged disk is refused instead of filling the system
  disk, exFAT/FAT disks work without ownership errors, wrong passphrase
  and unreachable targets are named, secrets never leave `keys/` (mode
  600) and never appear in an API response.

## Installation

Requires ZimaOS 1.7.x on amd64 or arm64.

Download `zbackup-amd64.raw` (or `zbackup-arm64.raw`) from the
[releases](https://github.com/chicohaager/zima-backup/releases), copy it to
the host **as `zbackup.raw`** and run

```sh
sudo zpkg install /tmp/zbackup.raw
```

`zpkg` accepts the image only under the name that matches
`extension-release.zbackup` inside it; any other file name is refused
with "module not pass validate" (measured on ZimaOS 1.7.1). The service
starts by itself and appears as **Sync & Backup** on the dashboard.

Upgrade: `sudo zpkg remove zbackup && sudo zpkg install /tmp/zbackup.raw`
— jobs, secrets and history under `/DATA/AppData/zbackup` are kept.

## Usage

1. **New job** → choose *Backup* (versions, encrypted) or *Sync* (plain
   copy), name it, add the folders to protect.
2. Choose the target. For SSH/SFTP the wizard shows the module's public
   key; paste it into `~/.ssh/authorized_keys` of the user on the target.
   A backup needs a passphrase — write it down, without it nothing can be
   restored.
3. Choose the schedule (and, for backups, how many snapshots to keep).

A new sync job opens its preview right away. **Restore** on a backup card
lists the snapshots; pick files or folders in the tree and restore them
to the original place or to another folder. **Check repository** verifies
the repository structure. **History** shows every run with its result.

## How sync lays out the target

Each source folder is mirrored to `<target>/<basename of source>`, so
`/DATA/Photos` and `/DATA/Documents` synced to `/media/usb1/mirror` become
`/media/usb1/mirror/Photos` and `/media/usb1/mirror/Documents`. With
"mirror deletions" on, only files inside those folders that no longer
exist in the source are removed; anything else on the target is left
alone. Two sources with the same folder name are refused
(`source_name_conflict`), and a target inside a source (or the other way
round) is refused (`target_nested`).

## Disks without ownership (exFAT, FAT, NTFS)

Before an rsync the module probes the target folder: if the filesystem
refuses `chown`, ownership and permissions are not synced (otherwise
every run would end with rsync exit 23 — measured on an exFAT USB disk);
if it rounds timestamps, a 2 s window is used. A restore onto such a disk
writes all data and reports that ownership could not be set.

## Local targets and unplugged disks

A local target must lie on a filesystem mounted below `/media` or `/mnt`
(or on `/DATA`): `/media/sdb/backups` is accepted while the disk is
mounted and refused when `/media/sdb` is just an empty folder on the
system disk — a backup must never silently land there.

## Cloud drives

Cloud drives that ZimaOS Files mounts (`/media/google_drive_…`,
`/media/onedrive_…`) are ordinary local targets for a **sync** job — but
slow: measured on 1.7.1, one small file to Google Drive took about 50 s.
A restic repository there is impractical (its 256 data folders alone took
more than ten minutes to create). A native cloud backend follows.

## Finding other machines

*Find on the network* in the target step listens for two seconds:

| Source | How | What it needs |
|---|---|---|
| ZimaOS boxes on the LAN | mDNS query for `_zimaos._tcp` — every ZimaOS box announces itself with `os=ZimaOS` | nothing |
| ZimaOS boxes over ZeroTier | the same query; ZimaOS' ZeroTier network subscribes to mDNS multicast (measured on 1.7.1) | the other box on the same ZeroTier network |
| Tailscale peers | `tailscale status --json` — online peers with a DNS name; Tailscale's own funnel nodes are skipped | Tailscale from a sysext (the App-Store container keeps its socket inside the container, so the module cannot see it) |
| ZimaNet | not measured yet: ZimaNet (`znet`) has no peer list locally; whether mDNS crosses its tunnel needs a second box | — |

The network each host is reached through (LAN, ZeroTier, ZimaNet,
Tailscale) is read from the local routes and shown next to the name.
Picking a host only fills in the address — the target still needs a user,
a folder, and for SSH the module's public key in its `authorized_keys`
(SSH is off by default on ZimaOS; enable it on the other box).

## Sessions

The module renews the ZimaOS session itself: the shell's access token
lives three hours, on 401 the UI calls the shell's own refresh endpoint
once and retries, so a page left open overnight keeps working.

## Verified

`test_deployment.sh` runs 27 end-to-end checks against a box (auth,
validation, backup → restore → check, sync with preview, wrong passphrase,
unplugged disk). It passed on ZimaOS 1.7.1 against `/DATA`, a mergerfs
pool, an ext4 USB disk and an exFAT USB disk, and again after a reboot.
Cross-host targets (rsync over ssh, restic over sftp, rclone over sftp)
were verified against a Linux box with the module's own key; SMB backup
and sync against a Samba share; restored files compared by sha256.

## systemd integration

`zbackup.raw` is a systemd-sysext image. It contributes:

| Path | Purpose |
|---|---|
| `/usr/bin/zbackupd` | the daemon |
| `/usr/libexec/zbackup/restic` | restic 0.19.1, fetched at build time and checksum-verified |
| `/usr/lib/systemd/system/zbackup.service` | ordered after `zimaos-gateway`, `zimaos-message-bus`, `zimaos-user`; `RequiresMountsFor=/DATA/AppData`; `Restart=always` |
| `/usr/share/casaos/modules/zbackup.json` | dashboard tile |
| `/usr/share/casaos/www/modules/zbackup/` | the UI, served by the ZimaOS gateway |

On ZimaOS, units inside a sysext are not scheduled at boot: `multi-user.target`
is resolved before the extension is merged into `/usr`. The daemon therefore
writes two small units to the persistent `/etc/systemd/system/` on first
start: `zbackup-watchdog.timer` (`OnBootSec=15`) starts the service if it
is not active after boot, `zbackup-refresh.path` restarts it when the
binary changes (module upgrade without reboot). Measured after a reboot
of a 1.7.1 box: the watchdog started the service 22 s after boot, the
gateway route was registered in the same second.

rsync and rclone come from the ZimaOS base image (measured on 1.7.1:
rsync 3.4.1, rclone v1.74.3-adrive.4).

## Data

```
/DATA/AppData/zbackup/
  jobs.json         job definitions and last result — never a secret
  keys/<id>.json    passphrase and target password of one job (mode 600)
  keys/             the module's ssh key pair (ed25519), its known_hosts
  logs/<id>.json    run history of one job
  settings.json     Telegram settings
  cache/            restic cache
```

## Building

```sh
./build.sh amd64      # or arm64 — produces zbackup-<arch>.raw + .sha256
go test -race ./...   # RCLONE_BIN=<path to rclone> for the rclone tests
tools/check-i18n.py   # every key in every language
ZIMA_USER=… ZIMA_PASS=… ./test_deployment.sh http://<host> [target-dir]
```

`build.sh` fetches restic for the target architecture and verifies it
against the release's `SHA256SUMS`; `tools/fetch-rclone.sh` fetches rclone
for the tests only. The Go module currently vendors
[lintux-modkit](https://github.com/chicohaager/lintux-modkit) through a
`replace` directive until it is published.

## API

All routes sit under `/v2/zbackup/api` behind the ZimaOS session
(`Authorization: Bearer <access_token>`), except `GET /api/health`.
Errors are JSON `{"code": "...", "error": "..."}` with stable codes (see
`internal/api/api.go`). Jobs: `GET/POST /api/jobs`,
`GET/PUT/DELETE /api/jobs/{id}`, `POST …/run|cancel|enable|disable`,
`GET …/logs`, `POST …/logs/clear`; backup: `GET …/snapshots`,
`GET …/snapshots/{snap}/ls?path=`, `POST …/restore`, `POST …/check`;
sync: `POST …/preview`; plus `GET /api/folders?path=`,
`GET/PUT /api/settings`, `POST /api/schedule/validate`, `GET /api/sshkey`.

## License

MIT — see `LICENSE`.
