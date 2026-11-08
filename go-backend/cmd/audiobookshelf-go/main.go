package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/server"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/version"
)

// fatal logs at FATAL (console, daily log and crash log) and exits, the way
// upstream's uncaughtExceptionMonitor handler records a crash
// (server/Server.js:243).
func fatal(format string, args ...any) {
	logging.Fatalf(format, args...)
	os.Exit(1)
}

// buildCommit identifies the running build in the startup log. CI sets it
// with -ldflags "-X main.buildCommit=<git sha>" (Dockerfile.go VCS_REF);
// local builds report "dev".
var buildCommit = "dev"

func main() {
	// Anything still using the standard library logger (and http.Server's own
	// error log) goes through the one logger.
	log.SetFlags(0)
	log.SetOutput(logging.Default().Writer(logging.LevelInfo, "log"))

	cfg, err := config.LoadArgs(os.Args[1:])
	if err != nil {
		fatal("[Server] configuration error: %v", err)
	}
	logging.Info("=== Starting Server ===")
	logging.Infof("[Server] Init v%s (Go backend build %s)", version.Server, buildCommit)
	logging.Infof("[Server] Go Version: %s", runtime.Version())
	logging.Infof("[Server] Platform: %s", runtime.GOOS)
	logging.Infof("[Server] Arch: %s", runtime.GOARCH)
	if err := bootstrap.Ensure(cfg); err != nil {
		fatal("[Server] bootstrap error: %v", err)
	}
	dbPath := store.DatabasePath(cfg.ConfigPath)
	db, err := store.Open(dbPath)
	if err != nil {
		fatal("[Database] Failed to connect to db %q: %v", dbPath, err)
	}
	logging.Infof("[Database] Db connection was successful (%s)", dbPath)
	// Upstream applies logLevel when ServerSettings loads, then initializes
	// the LogManager (server/Server.js:186).
	if settings, settingsErr := serversettings.Get(context.Background(), db.DB()); settingsErr == nil {
		server.ApplyLoggingSettings(settings.Doc())
	} else {
		logging.Errorf("[Database] Failed to load server settings: %v", settingsErr)
	}
	if err := logging.Default().OpenFiles(cfg.MetadataPath); err != nil {
		logging.Errorf("[LogManager] daily log files are unavailable: %v", err)
	}
	manager, err := server.NewManager(db, cfg)
	if err != nil {
		fatal("[Server] cannot start: %v", err)
	}
	defer manager.Close()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	srv := &http.Server{Addr: addr, Handler: manager, ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: log.New(logging.Default().Writer(logging.LevelError, "http"), "", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logging.Info("SIGINT/SIGTERM received. Shutting down...")
		logging.Info("=== Stopping Server ===")
		logging.Info("[Server] Closing HTTP Server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logging.Infof("Experimental Audiobookshelf Go backend v%s (build %s); SOURCE=%s; listen=%s; config=%s; metadata=%s; routerBasePath=%q", version.Server, buildCommit, cfg.Source, addr, cfg.ConfigPath, cfg.MetadataPath, cfg.RouterBasePath)
	basePath := cfg.RouterBasePath
	if basePath == "" {
		basePath = "/"
	}
	logging.Infof("[Server] Serving from base path %q", basePath)
	// Same two wordings as server/Server.js:476.
	if cfg.Host != "" {
		host := cfg.Host
		if net.ParseIP(host) != nil && strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		logging.Infof("Listening on http://%s:%d", host, cfg.Port)
	} else {
		logging.Infof("Listening on port :%d", cfg.Port)
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("[Server] HTTP server: %v", err)
	}
	logging.Info("[Server] HTTP Server Closed")
	logging.Info("Server stopped. Exiting.")
	logging.Default().Close()
}
