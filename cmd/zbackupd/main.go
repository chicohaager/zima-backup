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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/IceWhaleTech/CasaOS-Common/external"
	casamodel "github.com/IceWhaleTech/CasaOS-Common/model"
	"github.com/IceWhaleTech/CasaOS-Common/utils/constants"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/notify"
	"github.com/chicohaager/lintux-modkit/watchdog"
	"github.com/chicohaager/zima-backup/internal/api"
	"github.com/chicohaager/zima-backup/internal/backup"
	"github.com/chicohaager/zima-backup/internal/engine"
	"github.com/chicohaager/zima-backup/internal/model"
	"github.com/chicohaager/zima-backup/internal/store"
)

const (
	version     = "0.1.0"
	serviceName = "zbackup"
	binaryPath  = "/usr/bin/zbackupd"
	dataPath    = "/DATA/AppData/zbackup"
	staticDir   = "/usr/share/casaos/www/modules/zbackup"
)

func main() {
	log.Printf("[zbackup] starting v%s", version)
	notify.AppName = serviceName
	if err := watchdog.Install(watchdog.Options{Service: serviceName, Binary: binaryPath}); err != nil {
		log.Printf("[zbackup] %v", err)
	}

	st := openStore(envOr("ZBACKUP_DATA_PATH", dataPath))
	bk := newBackupRunner(st)
	// sync (rsync/rclone) follows in the next step; a sync run is recorded as runner_missing until then
	eng, err := engine.New(st, map[string]engine.Runner{model.KindBackup: bk})
	if err != nil {
		log.Fatalf("[zbackup] load jobs: %v", err)
	}
	eng.Start()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[zbackup] listening on http://%s", listener.Addr())

	runtimePath := envOr("CASAOS_RUNTIME_PATH", constants.DefaultRuntimePath)
	go registerRoute(runtimePath, "http://"+listener.Addr().String())

	srv := &api.Server{Engine: eng, Store: st, Backup: bk, Version: version, Started: time.Now()}
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

// newBackupRunner wires restic from the sysext and rclone from the base image.
func newBackupRunner(st *store.Store) *backup.Runner {
	return &backup.Runner{
		Restic:   envOr("ZBACKUP_RESTIC", "/usr/libexec/zbackup/restic"),
		Rclone:   envOr("ZBACKUP_RCLONE", "/usr/bin/rclone"),
		CacheDir: filepath.Join(st.Base(), "cache"),
		KeyDir:   filepath.Join(st.Base(), "keys"),
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
		ms, err := external.NewManagementService(runtimePath)
		if err == nil {
			err = ms.CreateRoute(&casamodel.Route{Path: api.RoutePrefix, Target: target})
		}
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
