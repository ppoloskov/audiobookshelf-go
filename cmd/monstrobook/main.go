package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"monstrobook/internal/config"
	data "monstrobook/internal/db"
	"monstrobook/internal/logging"
	"monstrobook/internal/server"
	"monstrobook/internal/settings"
	"monstrobook/internal/version"
)

// fatal logs at FATAL (console, daily log and crash log) and exits, the way
// upstream's uncaughtExceptionMonitor handler records a crash
// (server/Server.js:243).
func fatal(format string, args ...any) {
	slog.Log(context.Background(), slog.Level((logging.LevelFatal-logging.LevelInfo)*4), fmt.Sprintf(format, args...))
	os.Exit(1)
}

// buildCommit identifies the running build in the startup log. CI sets it
// with -ldflags "-X main.buildCommit=<git sha>" (Dockerfile VCS_REF);
// local builds report "dev".
var buildCommit = "dev"

func main() {
	// The stdlib log package (INFO) and new code log through the handler.
	slog.SetDefault(slog.New(logging.Default()))

	cfg, err := config.LoadArgs(os.Args[1:])
	if err != nil {
		fatal("[Server] configuration error: %v", err)
	}
	slog.Info("=== Starting Server ===")
	slog.Info("          ⢀⣀⣤⠤⠤⠤⠤⣤⣀⡀")
	slog.Info("       ⢀⡴⡞⣍⡧⠿⠓⠒⠒⠚⠿⢼⣩⢳⢦⡀")
	slog.Info("     ⢀⡴⢋⡼⠋⠁        ⠈⠙⢧⡙⢦⡀")
	slog.Info("    ⢀⣾⣤⢋⣤⣶⣶⣶⣶⣤⡀⢀⣤⣶⣶⣶⣶⣤⡙⣤⣷⡀")
	slog.Info("  ⢠⣾⠿⠿⡇⣾⣿⣿⣿⣿⣿⣿⣷⣾⣿⣿⣿⣿⣿⣿⣷⢸⠿⠿⣷⡄")
	slog.Info("  ⣿⡏⢠⣿⡥⢹⣿⡿⠿⢿⣿⣿⣿⣿⣿⣿⡿⠿⢿⣿⡏⢬⣿⡄⢹⣿")
	slog.Info(" ⠸⣿⡇⢸⠏⣴⣿⡏⠾⠆ ⢹⣿⣿⣿⣿⡏⠾⠆ ⢹⣿⣦⠹⡇⢸⣿⠇")
	slog.Info("  ⢿⣧⠈⢸⣿⣿⣧⡀ ⢂⣼⠿⠿⠿⠿⣧⡀ ⢂⣼⣿⣿⡇⠁⣼⡿")
	slog.Info("  ⠈⠻⠟⣈⣿⣕⣽⣿⣿⣿⣿⣄⢔⢔⣤⣿⣿⣿⣿⣕⣕⣽⣁⠻⠟⠁")
	slog.Info("   ⠰⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⠆    MONSTROBOOK")
	slog.Info(fmt.Sprintf("⢠⡖⠒⠒⠂⢸⣾⣾⠺⢾⣾⣽⠽⢽⣽⣽⡽⠽⣽⣾⡾⠞⣾⣾⡎⠐⠒⠒⢲⡄ Init v%s (Go backend build %s)", version.Monstrobook, buildCommit))
	slog.Info(fmt.Sprintf("⢸⡇⢠⠤⠤⠸⣿⡿ ⢸⣿⣿ ⢸⣿⣿⡇ ⣿⣿⡇ ⢿⣿⠇⠤⠤⡄⢸⡇ Upstream compatibility: Audiobookshelf v%s", version.Server))
	slog.Info(fmt.Sprintf("⢸⡇⢸⠐⢀⠐⢈⠐⢀⠈⠛⠃⢀⠸⣿⣿⠇⠐⠘⠛⢁⠐⢀⠁⢀⠐ ⡇⢸⡇ Go Version: %s", runtime.Version()))
	slog.Info(fmt.Sprintf("⢸⡇⢸⠐⢀⠐⢀⠐⢀⠐⢀⠐⢀ ⢙⠋⢀⠐⢀⠐⢀⠐⢀⠐⢀⠐ ⡇⢸⡇ Platform: %s | Arch: %s", runtime.GOOS, runtime.GOARCH))

	dbPath := data.FilePath(cfg.ConfigPath)
	db, err := server.OpenDatabase(cfg)
	if err != nil {
		fatal("[Database] Failed to connect to db %q: %v", dbPath, err)
	}
	slog.Info(fmt.Sprintf("[Database] Db connection was successful (%s)", dbPath))
	// Upstream applies logLevel when ServerSettings loads, then initializes
	// the LogManager (server/Server.js:186).
	dailyLogsToKeep := 0
	if stored, settingsErr := settings.NewStore(context.Background(), db, os.Getenv, cfg.Dirs().Backups); settingsErr == nil {
		dailyLogsToKeep = server.ApplyLoggingSettings(stored.Server())
	} else {
		slog.Error(fmt.Sprintf("[Database] Failed to load server settings: %v", settingsErr))
	}
	if err := logging.Default().OpenFiles(cfg.Dirs().Logs, dailyLogsToKeep); err != nil {
		slog.Error(fmt.Sprintf("[LogManager] daily log files are unavailable: %v", err))
	}
	manager, err := server.NewManager(db, cfg)
	if err != nil {
		fatal("[Server] cannot start: %v", err)
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           manager,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(logging.Default(), slog.LevelError),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("SIGINT/SIGTERM received. Shutting down...")
		slog.Info("=== Stopping Server ===")
		slog.Info("[Server] Closing HTTP Server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	slog.Info(fmt.Sprintf("Monstrobook v%s (build %s; Audiobookshelf v%s compat); SOURCE=%s; listen=%s; config=%s; metadata=%s; routerBasePath=%q", version.Monstrobook, buildCommit, version.Server, cfg.Source, addr, cfg.ConfigPath, cfg.MetadataPath, cfg.RouterBasePath))
	basePath := cfg.RouterBasePath
	if basePath == "" {
		basePath = "/"
	}
	slog.Info(fmt.Sprintf("[Server] Serving from base path %q", basePath))
	// Same two wordings as server/Server.js:476.
	if cfg.Host != "" {
		host := cfg.Host
		if net.ParseIP(host) != nil && strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		slog.Info(fmt.Sprintf("Listening on http://%s:%d", host, cfg.Port))
	} else {
		slog.Info(fmt.Sprintf("Listening on port :%d", cfg.Port))
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("[Server] HTTP server: %v", err)
	}
	<-shutdownDone // ListenAndServe returns as soon as Shutdown starts
	slog.Info("[Server] HTTP Server Closed")
	manager.Close() // runtime domains, then the realtime hub, then the database
	slog.Info("Server stopped. Exiting.")
	logging.Default().Close()
}
