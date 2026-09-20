// Package api is the HTTP surface of zbackupd. Every route except /health
// sits behind the ZimaOS session check; errors are JSON with stable codes.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/schedule"
	"github.com/chicohaager/zima-backup/internal/backup"
	"github.com/chicohaager/zima-backup/internal/discover"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/mirror"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/mounts"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
)

// RoutePrefix is the gateway route; the gateway forwards it verbatim.
const RoutePrefix = "/v2/zbackup"

const maskedSecret = "********"

// browseRoots are the only directory trees the folder picker may list.
var browseRoots = []string{"/DATA", "/media", "/mnt"}

// Server holds the dependencies of the handlers.
type Server struct {
	Engine  *engine.Engine
	Store   *store.Store
	Backup  *backup.Runner
	Sync    *mirror.Runner
	Key     sshkey.Pair
	Finder  discover.Finder
	Version string
	Started time.Time
}

// Routes builds the mux. verify wraps guarded handlers with the session check.
func (s *Server) Routes(verify func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	logged := httpx.Logging("zbackup", httpx.DefaultMaxBody)
	open := func(p string, h http.HandlerFunc) { mux.Handle(RoutePrefix+p, logged(h)) }
	guarded := func(p string, h http.HandlerFunc) { mux.Handle(RoutePrefix+p, logged(verify(h))) }

	open("/api/health", s.health)
	guarded("/api/jobs", s.jobs)
	guarded("/api/jobs/", s.job)
	guarded("/api/folders", s.folders)
	guarded("/api/folders/stat", s.folderStat)
	guarded("/api/settings", s.settings)
	guarded("/api/schedule/validate", s.validateSchedule)
	guarded("/api/sshkey", s.sshKey)
	guarded("/api/discover", s.discoverHosts)
	guarded("/api/mounts", s.mountsList)
	return httpx.CSRF(mux)
}

// --- jobs ---

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httpx.WriteJSON(w, http.StatusOK, s.Engine.Jobs())
	case http.MethodPost:
		var j model.Job
		if !httpx.Decode(w, r, &j) {
			return
		}
		j.ID = newID()
		if err := validate(&j, true); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if err := s.Engine.Put(&j); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		got, _ := s.Engine.Job(j.ID)
		httpx.WriteJSON(w, http.StatusCreated, got)
	default:
		httpx.MethodNotAllowed(w)
	}
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, RoutePrefix+"/api/jobs/"), "/")
	id := parts[0]
	current, ok := s.Engine.Job(id)
	if !ok {
		httpx.NotFound(w)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			httpx.WriteJSON(w, http.StatusOK, current)
		case http.MethodPut:
			var j model.Job
			if !httpx.Decode(w, r, &j) {
				return
			}
			j.ID = id
			if j.Passphrase == maskedSecret {
				j.Passphrase = ""
			}
			if j.Target.Secret == maskedSecret {
				j.Target.Secret = ""
			}
			if err := validate(&j, false); err != nil {
				httpx.WriteErr(w, err)
				return
			}
			if err := s.Engine.Put(&j); err != nil {
				httpx.WriteErr(w, err)
				return
			}
			got, _ := s.Engine.Job(id)
			httpx.WriteJSON(w, http.StatusOK, got)
		case http.MethodDelete:
			if err := s.Engine.Delete(id); err != nil {
				httpx.WriteErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			httpx.MethodNotAllowed(w)
		}
		return
	}
	if r.Method != http.MethodPost && parts[1] != "logs" && parts[1] != "snapshots" && parts[1] != "output" {
		httpx.MethodNotAllowed(w)
		return
	}
	switch parts[1] {
	case "run":
		go s.Engine.Run(id)
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
	case "cancel":
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"cancelled": s.Engine.Cancel(id)})
	case "enable", "disable":
		j, err := s.Engine.SetEnabled(id, parts[1] == "enable")
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, j)
	case "snapshots", "restore", "check":
		s.backupAction(w, r, current, parts[1:])
	case "preview":
		s.preview(w, r, current)
	case "output":
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{"running": current.Running, "phase": current.Phase, "lines": s.Engine.Output(id)})
	case "logs":
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "clear" {
			if err := s.Engine.ClearLogs(id); err != nil {
				httpx.WriteErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, s.Engine.Logs(id))
	default:
		httpx.NotFound(w)
	}
}

// validate normalises and checks a job; create says whether a passphrase
// must be present (on edit an empty one keeps the stored value).
func validate(j *model.Job, create bool) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" {
		return httpx.BadRequest("name_required", "name is required")
	}
	if j.Kind != model.KindBackup && j.Kind != model.KindSync {
		return httpx.BadRequest("kind_invalid", "kind must be backup or sync")
	}
	if len(j.Sources) == 0 {
		return httpx.BadRequest("sources_required", "at least one source folder is required")
	}
	for i, src := range j.Sources {
		src = filepath.Clean(strings.TrimSpace(src))
		if !underRoot(src) {
			return httpx.BadRequest("source_outside_roots", "source %q must be under %s", src, strings.Join(browseRoots, ", "))
		}
		j.Sources[i] = src
	}
	if err := validateTarget(&j.Target); err != nil {
		return err
	}
	if j.Target.Type == model.TargetLocal {
		target := filepath.Clean(j.Target.Path)
		for _, src := range j.Sources {
			if nested(src, target) {
				return httpx.BadRequest("target_nested", "target %s and source %s must not contain each other", target, src)
			}
		}
	}
	if j.Kind == model.KindSync {
		seen := map[string]string{}
		for _, src := range j.Sources {
			name := filepath.Base(src)
			if other, dup := seen[name]; dup {
				return httpx.BadRequest("source_name_conflict", "%s and %s would both land in the same folder %q on the target", other, src, name)
			}
			seen[name] = src
		}
		j.Passphrase, j.Retention = "", model.Retention{}
	}
	switch j.Schedule.Type {
	case model.ScheduleManual:
	case model.ScheduleInterval:
		if j.Schedule.IntervalMin < 5 {
			return httpx.BadRequest("interval_invalid", "interval must be at least 5 minutes")
		}
	case model.ScheduleCron:
		if errs, ok := schedule.Validate(j.Schedule.CronExpr); !ok {
			return httpx.BadRequest("cron_invalid", "%s", errs[0].Message)
		}
		if schedule.Next(j.Schedule.CronExpr, time.Now()).IsZero() {
			return httpx.BadRequest("cron_never_fires", "expression never fires within a year")
		}
	default:
		return httpx.BadRequest("schedule_invalid", "schedule type must be manual, interval or cron")
	}
	if j.Kind == model.KindBackup && create && j.Passphrase == "" {
		return httpx.BadRequest("passphrase_required", "a backup needs a passphrase — without it nothing can be restored")
	}
	if j.TimeoutMin < 0 {
		return httpx.BadRequest("value_negative", "timeout must not be negative")
	}
	for _, n := range j.Notifications {
		switch n.Type {
		case "webhook":
			if err := notify.ValidateWebhookFormat(n.WebhookFormat); err != nil {
				return httpx.BadRequest("notification_invalid", "%s", err.Error())
			}
			if err := notify.ValidateWebhookURL(n.Target); err != nil {
				return httpx.BadRequest("notification_invalid", "%s", err.Error())
			}
		case "email", "telegram":
		default:
			return httpx.BadRequest("notification_invalid", "invalid notification type %q", n.Type)
		}
	}
	return nil
}

func validateTarget(t *model.Target) error {
	t.Path = strings.TrimSpace(t.Path)
	t.Host = strings.TrimSpace(t.Host)
	switch t.Type {
	case model.TargetLocal:
		if !strings.HasPrefix(filepath.Clean(t.Path)+"/", "/media/") && !strings.HasPrefix(filepath.Clean(t.Path)+"/", "/mnt/") && !strings.HasPrefix(filepath.Clean(t.Path)+"/", "/DATA/") {
			return httpx.BadRequest("target_path_invalid", "a local target must be a folder under /media, /mnt or /DATA")
		}
		if mounts.IsCloudMount(filepath.Clean(t.Path)) {
			return httpx.BadRequest("cloud_mount", "this folder is a cloud drive mounted by Files — choose the cloud drive as target instead")
		}
	case model.TargetCloud:
		t.Remote = strings.TrimSpace(t.Remote)
		if t.Remote == "" || !mounts.HasRemote(t.Remote) {
			return httpx.BadRequest("cloud_remote_unknown", "choose one of the cloud drives ZimaOS Files is signed in to")
		}
		t.Path = strings.Trim(t.Path, "/")
		if t.Path == "" {
			return httpx.BadRequest("target_incomplete", "a folder inside the cloud drive is required")
		}
	case model.TargetSSH, model.TargetSFTP:
		if t.Host == "" || t.User == "" || t.Path == "" {
			return httpx.BadRequest("target_incomplete", "host, user and path are required")
		}
	case model.TargetSMB:
		if t.Host == "" || t.Share == "" {
			return httpx.BadRequest("target_incomplete", "host and share are required")
		}
	case model.TargetS3:
		if t.Host == "" || t.Bucket == "" || t.User == "" {
			return httpx.BadRequest("target_incomplete", "endpoint, bucket and access key are required")
		}
	default:
		return httpx.BadRequest("target_type_invalid", "target type must be local, cloud, ssh, sftp, smb or s3")
	}
	return nil
}

// nested says whether one path lies inside the other (or both are equal).
func nested(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func underRoot(p string) bool {
	for _, root := range browseRoots {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

// --- backup: snapshots, browse, restore, check ---

// backupAction handles /api/jobs/{id}/snapshots[/{snap}/ls?path=],
// /restore and /check for backup jobs.
func (s *Server) backupAction(w http.ResponseWriter, r *http.Request, job *model.Job, rest []string) {
	if job.Kind != model.KindBackup || s.Backup == nil {
		httpx.WriteError(w, http.StatusBadRequest, "not_a_backup", "this job is not a backup")
		return
	}
	secrets, err := s.Store.LoadSecrets(job.ID)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	switch {
	case rest[0] == "snapshots" && len(rest) == 1 && r.Method == http.MethodGet:
		snaps, err := s.Backup.Snapshots(ctx, job, secrets)
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "repository_error", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, snaps)
	case rest[0] == "snapshots" && len(rest) == 3 && rest[2] == "ls" && r.Method == http.MethodGet:
		nodes, err := s.Backup.List(ctx, job, secrets, rest[1], r.URL.Query().Get("path"))
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "repository_error", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, nodes)
	case rest[0] == "restore" && r.Method == http.MethodPost:
		var req struct {
			Snapshot string   `json:"snapshot"`
			Paths    []string `json:"paths"`
			Target   string   `json:"target"`
		}
		if !httpx.Decode(w, r, &req) {
			return
		}
		if req.Snapshot == "" || len(req.Paths) == 0 {
			httpx.WriteError(w, http.StatusBadRequest, "restore_incomplete", "snapshot and paths are required")
			return
		}
		if req.Target != "" && !underRoot(filepath.Clean(req.Target)) {
			httpx.WriteError(w, http.StatusBadRequest, "target_path_invalid", "restore target must be under "+strings.Join(browseRoots, ", "))
			return
		}
		go s.Engine.Execute(job.ID, s.Backup.Restore(req.Snapshot, req.Paths, req.Target))
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
	case rest[0] == "check" && r.Method == http.MethodPost:
		go s.Engine.Execute(job.ID, s.Backup.Check())
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
	default:
		httpx.NotFound(w)
	}
}

// --- sync: preview ---

// preview handles POST /api/jobs/{id}/preview for sync jobs: a dry run
// that reports what a real run would copy and delete.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, job *model.Job) {
	if job.Kind != model.KindSync || s.Sync == nil {
		httpx.WriteError(w, http.StatusBadRequest, "not_a_sync", "this job is not a sync")
		return
	}
	secrets, err := s.Store.LoadSecrets(job.ID)
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	p, err := s.Sync.Preview(ctx, job, secrets)
	if err != nil {
		if ctx.Err() != nil {
			httpx.WriteError(w, http.StatusGatewayTimeout, "preview_timeout", "the dry run took longer than "+previewTimeout.String())
			return
		}
		httpx.WriteError(w, http.StatusBadGateway, "preview_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

// previewTimeout bounds the synchronous dry run; large trees over ssh
// can take a while, the UI shows a spinner meanwhile.
const previewTimeout = 3 * time.Minute

// sshKey returns the module's public key for ssh/sftp targets.
func (s *Server) sshKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	pub, err := s.Key.Public()
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"public_key": pub})
}

// discoverHosts answers GET /api/discover with the machines found on the
// LAN, on meshes that carry multicast, and in the tailnet — a few seconds
// of listening, so the UI shows a spinner.
func (s *Server) discoverHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{"hosts": s.Finder.Hosts(r.Context())})
}

// mountsList answers GET /api/mounts with the volumes a local target can
// live on: system disk, pools, USB and other disks, cloud drives.
func (s *Server) mountsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{"volumes": mounts.List(), "remotes": mounts.Remotes()})
}

// --- folders (the picker) ---

type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"` // only at the root: the volume kind
}

// folders lists the sub-directories of ?path= within the browse roots. The
// daemon runs as root, so it lists directly instead of proxying the Files API.
// folderStat counts what a source folder holds, so the dialog can show
// "6 files · 346 kB" next to it — and "empty" before anyone backs up an
// empty folder (the tester did: HDD-Storage/…/incoming instead of the
// SSD-Storage twin, a green "0 files" run). The walk stops after
// statBudget files or statTime, whichever comes first, and says so.
func (s *Server) folderStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	p := filepath.Clean(r.URL.Query().Get("path"))
	if !underRoot(p) {
		httpx.WriteError(w, http.StatusBadRequest, "path_outside_roots", "path must be under "+strings.Join(browseRoots, ", "))
		return
	}
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() {
		httpx.WriteError(w, http.StatusNotFound, "path_unreadable", "not a readable folder: "+p)
		return
	}
	out := folderStats{}
	deadline := time.Now().Add(statTime)
	_ = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			out.Unreadable++
			return nil
		}
		if out.Files >= statBudget || time.Now().After(deadline) {
			out.Truncated = true
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			out.Files++
			if info, err := d.Info(); err == nil {
				out.Bytes += info.Size()
			}
		} else if d.IsDir() && path != p {
			out.Dirs++
		}
		return nil
	})
	httpx.WriteJSON(w, http.StatusOK, out)
}

type folderStats struct {
	Files      int   `json:"files"`
	Dirs       int   `json:"dirs"`
	Bytes      int64 `json:"bytes"`
	Truncated  bool  `json:"truncated"`  // budget hit: files/bytes are a lower bound
	Unreadable int   `json:"unreadable"` // entries the walk could not open
}

const (
	statBudget = 20000
	statTime   = 3 * time.Second
)

func (s *Server) folders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	p := filepath.Clean(r.URL.Query().Get("path"))
	if p == "." || p == "/" {
		// the top level is the list of volumes, named as the user knows them
		out := []folderEntry{}
		for _, v := range mounts.List() {
			if underRoot(v.Path) {
				out = append(out, folderEntry{Name: v.Name, Path: v.Path, Kind: v.Kind})
			}
		}
		if len(out) == 0 { // no volume recognised: fall back to the bare roots
			for _, root := range browseRoots {
				if st, err := os.Stat(root); err == nil && st.IsDir() {
					out = append(out, folderEntry{Name: root, Path: root})
				}
			}
		}
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	if !underRoot(p) {
		httpx.WriteError(w, http.StatusBadRequest, "path_outside_roots", "path must be under "+strings.Join(browseRoots, ", "))
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "path_unreadable", err.Error())
		return
	}
	out := []folderEntry{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, folderEntry{Name: e.Name(), Path: filepath.Join(p, e.Name())})
		}
	}
	sort.Slice(out, func(i, k int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[k].Name) })
	httpx.WriteJSON(w, http.StatusOK, out)
}

// --- misc ---

func (s *Server) validateSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.MethodNotAllowed(w)
		return
	}
	// the UI may send a form ("daily at 03:00") instead of an expression;
	// the answer always carries both, so a saved expression can be shown as
	// words and a picked form as the expression it became
	var req struct {
		model.Schedule
		Form *schedule.Form `json:"form,omitempty"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	resp := struct {
		Valid    bool                       `json:"valid"`
		Errors   []schedule.ValidationError `json:"errors"`
		NextRuns []int64                    `json:"next_runs"`
		CronExpr string                     `json:"cron_expr,omitempty"`
		Form     *schedule.Form             `json:"form,omitempty"`
	}{Errors: []schedule.ValidationError{}, NextRuns: []int64{}}
	if req.Form != nil && req.Form.Kind != schedule.FormCron {
		expr, err := req.Form.Expression()
		if err != nil {
			resp.Errors = append(resp.Errors, schedule.ValidationError{Field: "form", Value: req.Form.Kind, Message: err.Error()})
			httpx.WriteJSON(w, http.StatusOK, resp)
			return
		}
		req.Type, req.CronExpr = model.ScheduleCron, expr
	}
	if req.Type == model.ScheduleCron {
		resp.Errors, resp.Valid = schedule.Validate(req.CronExpr)
		if resp.Errors == nil {
			resp.Errors = []schedule.ValidationError{}
		}
		if resp.Valid {
			f := schedule.Describe(req.CronExpr)
			resp.CronExpr, resp.Form = req.CronExpr, &f
		}
	} else {
		resp.Valid = req.Type == model.ScheduleManual || (req.Type == model.ScheduleInterval && req.IntervalMin >= 5)
	}
	now := time.Now()
	for i := 0; resp.Valid && i < 5; i++ {
		next := engine.NextRun(req.Schedule, now)
		if next.IsZero() {
			break
		}
		resp.NextRuns = append(resp.NextRuns, next.UnixMilli())
		now = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

type settingsView struct {
	TelegramBotToken  string `json:"telegram_bot_token"`
	TelegramChatID    string `json:"telegram_chat_id"`
	TelegramOnSuccess bool   `json:"telegram_on_success"`
	TelegramOnFailure bool   `json:"telegram_on_failure"`
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		set, err := s.Store.LoadSettings()
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, settingsView{
			TelegramBotToken: maskToken(set.TelegramBotToken), TelegramChatID: set.TelegramChatID,
			TelegramOnSuccess: set.TelegramOnSuccess, TelegramOnFailure: set.TelegramOnFailure})
	case http.MethodPut:
		var req settingsView
		if !httpx.Decode(w, r, &req) {
			return
		}
		set, err := s.Store.LoadSettings()
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		if req.TelegramBotToken != "" && req.TelegramBotToken != maskToken(set.TelegramBotToken) {
			set.TelegramBotToken = req.TelegramBotToken
		}
		set.TelegramChatID = strings.TrimSpace(req.TelegramChatID)
		set.TelegramOnSuccess, set.TelegramOnFailure = req.TelegramOnSuccess, req.TelegramOnFailure
		if err := s.Store.SaveSettings(set); err != nil {
			httpx.WriteErr(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
	default:
		httpx.MethodNotAllowed(w)
	}
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return strings.Repeat("*", len(t))
	}
	return t[:4] + strings.Repeat("*", len(t)-8) + t[len(t)-4:]
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	jobs := s.Engine.Jobs()
	running := 0
	for _, j := range jobs {
		if j.Running {
			running++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"status": "healthy", "version": s.Version,
		"uptime_seconds": int64(time.Since(s.Started).Seconds()),
		"jobs_total":     len(jobs), "jobs_running": running,
	})
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format("20060102150405.000")
	}
	return hex.EncodeToString(b)
}
