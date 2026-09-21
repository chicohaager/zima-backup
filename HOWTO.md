# HowTo: Sync & Backup on ZimaOS — from install to your first restore

A step-by-step walkthrough for the **Sync & Backup** module: install it,
back up a folder to a USB disk, get a file back, mirror a folder to a
second disk, send a backup to Google Drive, and reach another Zima over
SSH. The [README](README.md) has the reference (every target type, the
API, how the module is built); this guide is the path a first-time user
walks.

> **Verified:** ZimaOS **1.7.1** on a ZimaCube Pro (amd64), module
> **v0.2.0**. Every command and every screen below was run or opened on
> that box before publishing. The arm64 image is built by CI but has not
> been run on arm64 hardware.

---

## 0. What it does — and what ZimaOS already does

ZimaOS ships a *Backup* tile. It backs up **phones** to the Zima. Nothing
in ZimaOS backs up **the Zima's own folders** — the AppData of your
apps, your documents, photos and media on `/DATA`. That is what this
module is for. Both coexist.

Two kinds of job:

| | Backup | Sync |
|---|---|---|
| What lands on the target | an encrypted [restic](https://restic.net) repository | a plain copy of your folders |
| Versions | yes — every run is a snapshot, any file from any point in time | no — the target mirrors the source |
| Readable without the module | only with restic and the passphrase | anywhere, with any file manager |
| Space on the target | deduplicated: unchanged data is stored once | the same as the source |
| Typical use | protect data you cannot re-create | a copy you can plug into any computer |

Both kinds can go to a **USB disk / local folder**, a **network share
connected in ZimaOS Files** (a NAS, another Zima, a Windows PC), a
**cloud drive signed in via ZimaOS Files**, and — for experts — **another
ZimaOS or Linux box over SSH**, an **SFTP server**, a **Windows/SMB share**
with its own credentials or **S3-compatible storage**.

---

## 1. Install

Download `zbackup-amd64.raw` (or `zbackup-arm64.raw`) from the
[release](https://github.com/chicohaager/zima-backup/releases), copy it
to the box **as `zbackup.raw`** — `zpkg` matches the file name against
the extension name, `zbackup-amd64.raw` is refused — and install it:

```sh
scp zbackup-amd64.raw <user>@<zima>:/tmp/zbackup.raw
ssh <user>@<zima>
sudo zpkg install /tmp/zbackup.raw
```

SSH is off on a fresh ZimaOS: **Settings → General → Developer mode →
View → SSH Access**; the same panel offers a *Web-based terminal* if you
prefer not to use an SSH client. Log in with the user you use for the
ZimaOS web UI. With the wrong file name `zpkg` answers
`Failed to install module:  module not pass validate` — that is the
name, nothing else.

The daemon starts, registers its route with the ZimaOS gateway, and the
**Sync & Backup** tile appears on the dashboard. The UI follows the
language of your ZimaOS shell (English, German, French, Chinese) and has
a light and a dark theme (moon/sun icon top right).

**Upgrade:** `sudo zpkg remove zbackup && sudo zpkg install /tmp/zbackup.raw`.
Jobs, history and keys live in `/DATA/AppData/zbackup/` and survive a
remove/install and a reboot.

---

## 2. Your first backup — a folder to a USB disk

Plug in the disk and let ZimaOS mount it (it appears under *Storage*).
Then open the tile and click **New backup**. One screen, two questions:

![New backup: what do you want to protect, where should it go, Start](docs/img/new-backup.png)

### 1 — What do you want to protect?

**Choose folder…** opens the folder picker. It starts with your disks —
the system disk, storage pools, USB disks, network shares and cloud
drives — so you never have to know that `/media/sda` is the USB disk.

![Folder picker](docs/img/folder-picker.png)

Walk into the disk, select the folder, **Choose this folder**. The
folder appears as a chip with its file count and size. Add more folders
the same way; one job can protect several.

### 2 — Where should it go?

The cards show everything the box can write to, in three groups: **drives
in this box** (with free space), **network shares** (the servers ZimaOS
Files has connected, see §5) and **cloud drives** (§6). Click the USB
disk. The folder on it is filled in — `Backups/<folder>` — and the line
under the cards tells you what will happen:

> Backup · daily at 03:00 · keeps 7 days, 4 weeks, 6 months · encrypted

That is the default, and for most folders it is right. **Start.**

### The passphrase

![Your passphrase: seven words, Copy, Print / save as PDF, checkbox](docs/img/passphrase.png)

A backup is encrypted, and the key is a passphrase. If you do not enter
one under *Advanced*, the module makes one for you: **seven random
words** (from the EFF short wordlist, drawn with the browser's
cryptographic random numbers — about 72 bits, far beyond what anyone
can guess). It is shown **once**:

- **Copy** puts it into the clipboard; **Print / save as PDF** prints a
  sheet with the job name, the words and the date — the sheet belongs in
  a drawer, not on the disk it protects.
- Tick **I have written the passphrase down**, then **Create backup**.

Without these words nothing can be restored — not by the module, not by
anyone. The module keeps them in `keys/<job>.json` (mode 600) so the
scheduled runs work; if that file is lost with the box, the sheet is what
brings your data back.

### What you see while it runs

The first run starts right away. The row shows the phase, a progress
bar, bytes done and the transfer rate. The first run of a backup starts
with *"Creating the repository — restic's fixed skeleton of 256 folders,
not your data yet"*: that is restic laying out its `data/00` … `data/ff`
directories, it happens once per target. Then the backup itself, then
the retention pass. When it is done the row reads
*snapshot 42e26d49: 36 files, 36 new, 0 changed, 5.6 MiB added* and
*next in 19 hours*.

**Log** in the **···** menu opens the output of the current or last run —
the exact restic commands and what they printed:

![Log](docs/img/log.png)

The first run sends everything. Every later run sends only what changed
(measured on a 9 MB folder: first run 161 s to Google Drive, second run
62 s with 0 bytes transferred).

### Advanced — when the default is not what you want

![Advanced: Backup or Sync, name, schedule in words, keep](docs/img/advanced.png)

- **Backup or Sync** (see §4).
- **Name** — filled in for you as *folder → drive*.
- **Schedule** in words: *Daily at 03:00*, *Weekly on Sunday at 03:00*,
  *Monthly on day 1*, *Every hour at minute 0*, *Every 20 minutes*,
  *Manual only* — the next three runs are shown while you choose. *Cron
  expression (experts)* takes a raw expression; one that the words cannot
  say stays visible as the expression.
- **Keep:** how many snapshots to keep — last / daily / weekly / monthly.
  Older ones are pruned after each run. All zero keeps everything.
- **Own passphrase** instead of the generated one.
- **Exclude patterns**, one per line (`*.tmp`, `node_modules`, `.cache`).
- **Timeout**, **Active**, **Notifications** (§8).
- **Direct target for experts** — SSH, SFTP, SMB with own credentials,
  S3 (§7).

An unplugged disk is refused: the folder must lie on a mounted
filesystem below `/media`, `/mnt` or `/DATA`. `/media/sdb/backups` is
accepted while the disk is there and refused when `/media/sdb` is just an
empty folder on the system disk — a backup must never silently land
there. A folder that is empty at run time ends with a yellow *nothing to
back up*, not a green success.

---

## 3. Getting a file back

Click **Restore** on a backup row.

![Restore](docs/img/restore.png)

1. **Point in time:** pick the snapshot (date, number of files, size).
2. Browse the tree — disk → folder → file — and tick what you want:
   single files, whole folders, or a mix.
3. **Restore to:** leave it empty to put the files back where they were,
   or choose another folder (e.g. `/DATA/Restored`) to look at them
   first without touching the originals.
4. **Restore selected.**

**Check repository** on the same dialog verifies the repository
structure — run it now and then, and always before you rely on an old
disk.

Restoring onto an exFAT/FAT disk writes all data; the result notes that
ownership could not be set (those filesystems have none).

---

## 4. Mirroring a folder to a second disk (Sync)

Same dialog; open **Advanced** and choose **Sync**. Differences:

- No passphrase — the target is a plain copy.
- Each source folder lands as `<target>/<folder name>`: `/DATA/Photos`
  and `/DATA/Documents` synced to `/media/usb1/mirror` become
  `/media/usb1/mirror/Photos` and `/media/usb1/mirror/Documents`.
- **Mirror deletions** (under *Advanced*) is **off** by default: the
  target only ever grows. On: files you deleted from the source are
  deleted on the target too — only inside the mirrored folders; anything
  else on the target is left alone.
- A new sync job opens its **Preview** right away, and the row has a
  *Preview* button: it shows how many files a run would copy and delete
  before anything happens. Use it after switching *Mirror deletions* on.

exFAT/FAT disks are handled: the module probes the target before each
run and skips ownership and permissions where the filesystem has none,
so a run does not end in an error.

---

## 5. A network share — a NAS, another Zima, a Windows PC

You do not enter a server address, user and password in this module.
ZimaOS Files does the connecting, and the module uses what Files has
connected:

1. In the dialog, click **Connect a network share…** (or do it in Files:
   *Files → Network*). Enter the server — the list under the field shows
   the ZimaOS boxes it finds on the network — and either *Guest* or user
   and password. **Connect.**
2. ZimaOS mounts **every share of that server**; a few seconds later
   they are cards under *Network shares* (*Test @ nas*, *Photos @ nas*, …)
   and folders in Files.
3. Click the card. Done — the folder on it is filled in like on a disk.

The connection belongs to ZimaOS, so it survives a reboot (measured:
Files reconnects about 20 s after boot) and shows in Files too. If the
share is disconnected while a job points at it, the run fails with
*the network share \\server\\share is not connected — connect it in
Files or with 'Connect a network share…'* and runs again once it is
back; nothing is written anywhere else.

Restic on such a share works like on a disk (measured: a small backup
in 6 s, `check` passed, second run 0 B added).

---

## 6. Backup to Google Drive (or OneDrive)

1. Sign in to the drive in **ZimaOS Files** (its cloud-drive feature).
   The drive then appears under *Cloud drives* in the dialog.
2. Click the card. The folder inside the drive is filled in
   (`Backups/<folder>`); change it if you like. **Start.**

Do **not** pick the mounted folder (`/media/google_drive_…`) as a local
target — the module refuses it (`cloud_mount`) and points you to the
cloud card. The reason is speed: through the mounted folder restic
needed more than ten minutes just to create its repository skeleton and
52 s per small file (measured on 1.7.1). As a cloud target the module
talks to the drive directly through ZimaOS' own rclone configuration:
the repository is created in 14–46 s, a large file uploads at about
3 MiB/s, a backup runs at about 0.35 MB/s.

What to expect: Google Drive charges **one round trip per file**, about
a second, whatever the file's size (measured with 16 parallel
transfers). A backup packs many small files into few large ones, so it
copes; a **sync** of many small files is slow, and the row shows the
rate as *files/s* so you can see that it is the count, not the bytes.
The dialog says so before you start a sync to a cloud drive. The first
backup of a large folder is an overnight job; later runs only send
changes and finish in a minute.

Each job gets its own folder in the drive. Two backup jobs pointed at
the same folder would have to share one passphrase; the module refuses
the second one instead of letting it fail later with *wrong passphrase*.

---

## 7. Another ZimaOS box (or any Linux box) over SSH — experts

On the **other** box:

1. Enable SSH there: **Settings → General → Developer mode → View →
   SSH Access** (off by default).
2. You need a user there and its `~/.ssh/authorized_keys`.

On **this** box, in the dialog under **Advanced → Direct target for
experts**:

1. Target: **Another ZimaOS / Linux box (SSH)**.
2. **Find on the network** listens for two seconds and lists other ZimaOS
   boxes on the LAN (every ZimaOS box announces itself via mDNS) and your
   Tailscale peers, with the network each one is reached through (LAN,
   ZeroTier, Tailscale). Click one and the host is filled in — or type it.
3. User, port (empty = 22), **Folder on the target**.
4. The dialog shows the module's **public key** with a *Copy* button.
   Paste that line into `~/.ssh/authorized_keys` of that user on the
   other box:

   ```sh
   mkdir -p ~/.ssh && chmod 700 ~/.ssh
   echo 'ssh-ed25519 AAAA… zbackup' >> ~/.ssh/authorized_keys
   chmod 600 ~/.ssh/authorized_keys
   ```

   The module logs in with this key only — no password is stored for SSH
   targets. The first connection records the host key; a changed host key
   later is an error, not silently accepted.

Backup over SSH uses restic's sftp backend, sync uses rsync over ssh —
both need only a standard sshd on the other side; rsync must be
installed there for sync (ZimaOS has it).

If *Find on the network* shows nothing: the other box is off, on another
network, or its Tailscale runs in the App-Store container (the module can
only see a Tailscale that runs on the host as a sysext). Type the host.

**SFTP server**, **Windows/SMB share** (with its own user and password,
without going through Files) and **S3** work the same way with their own
fields (share and password; bucket, endpoint and keys). For an ordinary
share on the LAN, §5 is the easier road.

---

## 8. Notifications

- **Telegram:** gear icon top right → *Settings* → bot token and chat
  ID. Then in each job under *Advanced → Notifications* tick *Telegram*,
  on success and/or on failure.
- **Webhook:** per job, a URL and a format — generic JSON, n8n, Discord,
  Slack, Home Assistant, Uptime Kuma.

---

## 9. When something goes wrong

The result on the card names the cause; the codes:

| Card says | Meaning | What to do |
|---|---|---|
| **target unavailable** | disk not mounted, host unreachable, share gone — the sentence names the disk or share | plug the disk in / reconnect the share in Files; the run is not retried by itself |
| **user or password refused** | the SMB server rejected the credentials of an expert target | fix user/password in the job — or connect the share through Files (§5) |
| **share not found on the server** | the server answered, the share name does not exist there | check the name on the server |
| **repository at the target unreadable** | the server serves the folder but not restic's files — one Samba answered HTTP 500 for a `config` directory | choose another folder or fix the share; the run stops after at most 90 s instead of retrying for a quarter of an hour |
| **nothing to back up** (yellow) | the source folders were empty at run time | is the source disk mounted? the job stays, the next run may find data |
| **wrong passphrase** | the repository at that folder was created with a different passphrase | edit the job and enter the passphrase that created it — or choose an empty folder for a new repository |
| **repository locked** | another job is working on the same repository right now | wait for it; locks a cancelled run left behind are cleared automatically before each run (since 0.1.1) |
| **partly copied** | sync finished but some files could not be read or written | *Log* lists the first errors |
| **timed out** | the run exceeded the job's timeout (default 6 h) | raise the timeout under *Advanced*, or split the job |
| **cancelled** | you pressed *Cancel* | — |
| **failed** | anything else | *Log* has the tool's own message |

Beyond the card:

- **Log** on the card: the last 400 lines of the run.
- **History** on the card: every run with duration and result.
- On the box: `journalctl -u zbackup -e` for the daemon,
  `sudo systemctl status zbackup` for the service.
- Health without login: `curl http://<zima>/v2/zbackup/api/health`.

One limit worth knowing: a job whose target hangs inside the kernel — a
mounted cloud folder that stops answering — can stay *running* until the
mount answers. The module's own probes and tools are bounded (the
repository probe stops after 90 s, a timed-out tool is killed together
with its children), but *Cancel* cannot interrupt a process stuck in a
kernel mount.

---

## 10. Where things live, and uninstalling

```
/DATA/AppData/zbackup/
  jobs.json         job definitions and last result — never a secret
  keys/<id>.json    passphrase and target password of one job (mode 600)
  keys/             the module's ssh key pair (ed25519), its known_hosts
  logs/<id>.json    run history of one job
  settings.json     Telegram settings
  cache/            restic cache
```

`sudo zpkg remove zbackup` removes the module; the folder above stays,
so a later install finds the jobs again. Delete the folder yourself if
you want a clean slate. The backups on your targets are ordinary restic
repositories: with restic and the passphrase you can read them on any
computer, module or not — that is the point.
