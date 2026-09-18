# Sync & Backup for ZimaOS

Folder backups with versions and restore, and one-way sync to a second disk,
another ZimaOS box or a server — as a ZimaOS module in the same family as
[ZFW](https://github.com/chicohaager/zfw) and [Cron](https://github.com/chicohaager/cron).

**Status: step 3 of 7 — backups work, no UI yet.** Jobs, schedule, history,
session authentication, gateway route and boot watchdog are in place; backup
jobs run through restic (encrypted repository per target, retention,
snapshot browsing, restore, check) against local, sftp/ssh, S3 and SMB
targets. The sync runner (rsync/rclone) and the UI follow. See `PLAN.md`.

The backup path was verified on a ZimaOS 1.7.1 host: back up a folder,
list the snapshot, restore one file elsewhere, sha256 of the restored file
equals the original.

ZimaOS' own *Backup* tile backs up phones to the Zima; this module covers
the folders on the Zima itself. Both coexist.
