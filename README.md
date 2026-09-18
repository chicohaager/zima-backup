# Sync & Backup for ZimaOS

Folder backups with versions and restore, and one-way sync to a second disk,
another ZimaOS box or a server — as a ZimaOS module in the same family as
[ZFW](https://github.com/chicohaager/zfw) and [Cron](https://github.com/chicohaager/cron).

**Status: step 2 of 7 — daemon skeleton.** Jobs, schedule, history, session
authentication, gateway route and boot watchdog work; the backup (restic)
and sync (rsync/rclone) runners and the UI follow. See `PLAN.md`.

ZimaOS' own *Backup* tile backs up phones to the Zima; this module covers
the folders on the Zima itself. Both coexist.
