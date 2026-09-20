// Package model holds the job definition shared by the API, the store and
// the engine. It is deliberately free of behaviour: validation lives in
// the API package, execution in the engine.
package model

import "github.com/chicohaager/lintux-modkit/notify"

// Job kinds.
const (
	KindBackup = "backup" // versioned snapshots with retention and restore (restic)
	KindSync   = "sync"   // one-way mirror of folders to a target (rsync / rclone)
)

// Target types. Local covers a second disk or USB drive mounted under /media.
const (
	TargetLocal = "local"
	TargetSSH   = "ssh"   // another ZimaOS box or any Linux host, rsync over ssh
	TargetSFTP  = "sftp"  // sftp server (restic sftp: / rclone sftp:)
	TargetSMB   = "smb"   // Windows/Samba share
	TargetS3    = "s3"    // S3-compatible bucket
	TargetCloud = "cloud" // a cloud drive ZimaOS Files is signed in to (rclone remote from ZimaOS' own config)
)

// Schedule types.
const (
	ScheduleManual   = "manual"
	ScheduleInterval = "interval" // every N minutes
	ScheduleCron     = "cron"     // 5-field expression
)

// Result codes, stable for the UI.
const (
	CodeCompleted          = "completed"
	CodeEmpty              = "empty" // ran through, but the source held no files — success, shown in yellow
	CodeFailed             = "failed"
	CodePartial            = "partial" // sync: ran through, but some files could not be copied
	CodeTimeout            = "timeout"
	CodeSkippedRunning     = "skipped_running"
	CodeTargetUnavailable  = "target_unavailable"
	CodeTargetAuth         = "target_auth"          // the server refused user or password
	CodeTargetShareMissing = "target_share_missing" // SMB: no share of that name on the server
	CodeRepoUnreadable     = "repo_unreadable"      // the target answers, the repository there cannot be read
	CodeRunnerMissing      = "runner_missing"
	CodeCancelled          = "cancelled"
	CodePassphraseWrong    = "passphrase_wrong"
	CodeRepoLocked         = "repo_locked"
	CodeRestored           = "restored"
	CodeCheckOK            = "check_ok"
	CodeCheckFailed        = "check_failed"
)

// Target describes where a job writes. Secret is the password (SMB, SFTP)
// or the S3 secret key; it is stored separately by the store and never
// returned by the API.
type Target struct {
	Type     string `json:"type"`
	Path     string `json:"path"`               // local dir, remote dir, or bucket prefix
	Host     string `json:"host,omitempty"`     // ssh/sftp/smb/s3 endpoint
	Port     int    `json:"port,omitempty"`     // 0 = protocol default
	User     string `json:"user,omitempty"`     // ssh/sftp/smb user or S3 access key id
	Share    string `json:"share,omitempty"`    // smb share name
	Remote   string `json:"remote,omitempty"`   // cloud: rclone remote name as ZimaOS keeps it (google_drive_<id>)
	Bucket   string `json:"bucket,omitempty"`   // s3
	Region   string `json:"region,omitempty"`   // s3
	Secret   string `json:"secret,omitempty"`   // write-only; masked on read
	Insecure bool   `json:"insecure,omitempty"` // s3 over plain http
}

// Schedule says when a job runs.
type Schedule struct {
	Type        string `json:"type"`
	IntervalMin int    `json:"interval_min,omitempty"`
	CronExpr    string `json:"cron_expr,omitempty"`
}

// Retention is the restic forget policy of a backup job. Zero means "not
// used"; an all-zero policy keeps every snapshot.
type Retention struct {
	KeepLast    int `json:"keep_last,omitempty"`
	KeepDaily   int `json:"keep_daily,omitempty"`
	KeepWeekly  int `json:"keep_weekly,omitempty"`
	KeepMonthly int `json:"keep_monthly,omitempty"`
}

// Job is one backup or sync definition plus its runtime state.
type Job struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Sources  []string `json:"sources"`
	Excludes []string `json:"excludes,omitempty"`
	Target   Target   `json:"target"`
	Schedule Schedule `json:"schedule"`

	Retention        Retention `json:"retention,omitempty"`         // backup
	Passphrase       string    `json:"passphrase,omitempty"`        // backup, write-only
	DeleteExtraneous bool      `json:"delete_extraneous,omitempty"` // sync: mirror deletions

	Enabled       bool            `json:"enabled"`
	TimeoutMin    int             `json:"timeout_min,omitempty"`
	Notifications []notify.Config `json:"notifications,omitempty"`

	// Runtime state, maintained by the engine.
	NextRunAt  int64   `json:"next_run_at"`
	LastRunAt  int64   `json:"last_run_at"`
	LastResult *Result `json:"last_result"`
	Running    bool    `json:"running"`
	Progress   float64 `json:"progress"`    // 0..1 while running
	Phase      string  `json:"phase"`       // what the run is doing right now (init, backup, retention, restore, check, sync …)
	BytesDone  int64   `json:"bytes_done"`  // transfer so far, while running
	BytesTotal int64   `json:"bytes_total"` // 0 when the tool does not know
	Rate       int64   `json:"rate"`        // bytes per second, while running
	FilesDone  int64   `json:"files_done"`  // files moved so far, while running (rclone/rsync)
	FilesTotal int64   `json:"files_total"` // 0 when the tool does not know
	FileRate   int64   `json:"file_rate"`   // files per second ×100, while running

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// Result is the outcome of one run.
type Result struct {
	Success    bool   `json:"success"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Files      int64  `json:"files,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// LogEntry is one line of a job's history.
type LogEntry struct {
	Time       int64  `json:"time"`
	DurationMs int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Files      int64  `json:"files,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// Settings are the module-wide options.
type Settings struct {
	TelegramBotToken  string `json:"telegram_bot_token,omitempty"`
	TelegramChatID    string `json:"telegram_chat_id,omitempty"`
	TelegramOnSuccess bool   `json:"telegram_on_success"`
	TelegramOnFailure bool   `json:"telegram_on_failure"`
}
