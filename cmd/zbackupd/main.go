// Command zbackupd is the Sync & Backup daemon for ZimaOS: it keeps backup
// and sync jobs, runs them on schedule and serves the JSON API behind the
// ZimaOS gateway.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/gateway"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/watchdog"
	"github.com/chicohaager/zima-backup/internal/api"
	"github.com/chicohaager/zima-backup/internal/backup"
	"github.com/chicohaager/zima-backup/internal/discover"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/localfs"
	"github.com/chicohaager/zima-backup/internal/mirror"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/mounts"
	"github.com/chicohaager/zima-backup/internal/sshkey"
	"github.com/chicohaager/zima-backup/internal/store"
	"github.com/chicohaager/zima-backup/internal/system"
)

const (
	version     = "0.2.1"
	serviceName = "zbackup"
	binaryPath  = "/usr/bin/zbackupd"
	dataPath    = "/DATA/AppData/zbackup"
	staticDir   = "/usr/share/casaos/www/modules/zbackup"
	rclonePath  = "/usr/bin/rclone" // base image (measured on 1.7.1: v1.74.3-adrive.4)
)

func main() {
	log.Printf("[zbackup] starting v%s", version)
	notify.AppName = serviceName
	if err := watchdog.Install(watchdog.Options{Service: serviceName, Binary: binaryPath}); err != nil {
		log.Printf("[zbackup] %v", err)
	}

	// a local target must sit on a disk mounted below these, never on them
	localfs.ContainerMounts = []string{"/", "/media", "/mnt"}
	mounts.RcloneConfig = envOr("ZBACKUP_RCLONE_CONF", mounts.RcloneConfig)
	mounts.ProcMounts = envOr("ZBACKUP_PROC_MOUNTS", mounts.ProcMounts)
	st := openStore(envOr("ZBACKUP_DATA_PATH", dataPath))
	key := sshkey.Pair{Dir: filepath.Join(st.Base(), "keys")}
	bk := newBackupRunner(st, key)
	sy := newSyncRunner(key)
	sysr := &system.Runner{Backup: bk, Cmd: system.Exec{}, ModuleDir: st.Base()}
	eng, err := engine.New(st, map[string]engine.Runner{model.KindBackup: bk, model.KindSync: sy, model.KindSystem: sysr})
	if err != nil {
		log.Fatalf("[zbackup] load jobs: %v", err)
	}
	eng.Start()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[zbackup] listening on http://%s", listener.Addr())

	runtimePath := envOr("CASAOS_RUNTIME_PATH", "/var/run/casaos")
	go registerRoute(runtimePath, "http://"+listener.Addr().String())

	srv := &api.Server{Engine: eng, Store: st, Backup: bk, Sync: sy, System: sysr, Key: key, Version: version, Started: time.Now(),
		Finder: discover.Finder{Tailscale: optionalBinary("tailscale")}}
	handler := httpx.Static("/modules/zbackup/", envOr("ZBACKUP_STATIC_DIR", staticDir), srv.Routes(newVerifier(runtimePath).Middleware))
	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		s := <-sig
		log.Printf("[zbackup] received %v, shutting down", s)
		eng.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()
	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// optionalBinary returns the path of a tool when the host has it, else "".
func optionalBinary(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// newBackupRunner wires restic from the sysext and rclone from the base image.
func newBackupRunner(st *store.Store, key sshkey.Pair) *backup.Runner {
	return &backup.Runner{
		Restic:   envOr("ZBACKUP_RESTIC", "/usr/libexec/zbackup/restic"),
		Rclone:   envOr("ZBACKUP_RCLONE", rclonePath),
		CacheDir: filepath.Join(st.Base(), "cache"),
		Key:      key,
	}
}

// newSyncRunner wires rsync and rclone from the base image.
func newSyncRunner(key sshkey.Pair) *mirror.Runner {
	return &mirror.Runner{
		Rsync:  envOr("ZBACKUP_RSYNC", "/usr/bin/rsync"),
		Rclone: envOr("ZBACKUP_RCLONE", rclonePath),
		Key:    key,
	}
}

func openStore(path string) *store.Store {
	for i := 1; ; i++ {
		st, err := store.Open(path)
		if err == nil {
			return st
		}
		if i >= 30 {
			log.Fatalf("[zbackup] storage at %s not available after %d attempts: %v", path, i, err)
		}
		log.Printf("[zbackup] storage not ready (attempt %d/30): %v", i, err)
		time.Sleep(2 * time.Second)
	}
}

func newVerifier(runtimePath string) *auth.Verifier {
	if os.Getenv("ZBACKUP_DISABLE_AUTH") == "1" {
		log.Printf("[zbackup] WARNING: authentication disabled by ZBACKUP_DISABLE_AUTH")
		return auth.Disabled()
	}
	return auth.NewVerifier(auth.JWKSResolver(runtimePath))
}

// registerRoute tells the gateway where we listen; it retries because the
// gateway may still be starting after boot.
func registerRoute(runtimePath, target string) {
	for i := 1; i <= 60; i++ {
		err := gateway.Register(context.Background(), runtimePath, api.RoutePrefix, target, 10*time.Second)
		if err == nil {
			log.Printf("[zbackup] gateway route registered: %s -> %s", api.RoutePrefix, target)
			return
		}
		log.Printf("[zbackup] gateway not ready (attempt %d/60): %v", i, err)
		time.Sleep(2 * time.Second)
	}
	log.Printf("[zbackup] WARNING: could not register gateway route")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
