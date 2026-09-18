# Sync & Backup for ZimaOS

Folder backups with versions and restore, and one-way sync to a second disk,
another ZimaOS box or a server — as a ZimaOS module in the same family as
[ZFW](https://github.com/chicohaager/zfw) and [Cron](https://github.com/chicohaager/cron).

**Status: step 6 of 7 — tested on real disks and a second host.** Jobs, schedule,
history, session authentication, gateway route and boot watchdog are in
place; backup jobs run through restic (encrypted repository per target,
retention, snapshot browsing, restore, check); sync jobs mirror folders
one-way through rsync (local disk, ssh) or rclone (sftp, SMB, S3), with a
dry-run preview and an explicit "delete extraneous" switch. The UI is a
three-step wizard (what · where · when), job cards with live progress,
history, a restore browser and the sync preview — in English, German,
French and Chinese, following the ZimaOS shell language, light and dark.
Remaining: the release. See `PLAN.md`.

`test_deployment.sh` runs 27 end-to-end checks against a box (auth,
validation, backup → restore → check, sync with preview, wrong passphrase,
unplugged disk); it passed on ZimaOS 1.7.1 against /DATA, a mergerfs pool,
an ext4 USB disk and an exFAT USB disk. Cross-host targets (rsync over
ssh, restic over sftp, rclone over sftp) were verified against a Linux
box with the module's own key.

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

The module renews the ZimaOS session itself: the shell's access token
lives three hours, on 401 the UI calls the shell's own refresh endpoint
once and retries, so a page left open overnight keeps working.

Verified on a ZimaOS 1.7.1 host: backup to a local folder and to an SMB
share (snapshot listed, one file restored elsewhere, sha256 equal, check
ok); sync to a local folder, over ssh with the module's own key and to an
SMB share (per-file hashes equal, the delete switch removes a planted
stray file inside the mirrored folder only).

## How sync lays out the target

Each source folder is mirrored to `<target>/<basename of source>`, so
`/DATA/Photos` and `/DATA/Documents` synced to `/media/usb1/mirror` become
`/media/usb1/mirror/Photos` and `/media/usb1/mirror/Documents`. With
"delete extraneous" on, only files inside those folders that no longer
exist in the source are removed; anything else on the target is left
alone. Two sources with the same folder name are refused
(`source_name_conflict`), and a target inside a source (or the other way
round) is refused (`target_nested`).

## Tools

restic is bundled in the module (`tools/fetch-restic.sh`, checksum
verified). rsync and rclone come from the ZimaOS base image (measured on
1.7.1: rsync 3.4.1, rclone v1.74.3-adrive.4); `tools/fetch-rclone.sh`
only serves the tests (`RCLONE_BIN=… go test ./...`).

ZimaOS' own *Backup* tile backs up phones to the Zima; this module covers
the folders on the Zima itself. Both coexist.
