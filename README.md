# Sync & Backup for ZimaOS

Folder backups with versions and restore, and one-way sync to a second disk,
another ZimaOS box or a server — as a ZimaOS module in the same family as
[ZFW](https://github.com/chicohaager/zfw) and [Cron](https://github.com/chicohaager/cron).

**Status: step 4 of 7 — backups and sync work, no UI yet.** Jobs, schedule,
history, session authentication, gateway route and boot watchdog are in
place; backup jobs run through restic (encrypted repository per target,
retention, snapshot browsing, restore, check); sync jobs mirror folders
one-way through rsync (local disk, ssh) or rclone (sftp, SMB, S3), with a
dry-run preview and an explicit "delete extraneous" switch. The UI follows.
See `PLAN.md`.

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
