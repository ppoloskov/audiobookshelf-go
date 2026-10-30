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
	"path/filepath"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/server"
	"audiobookshelf-compat/internal/store"
)

func main() {
	cfg, err := config.LoadArgs(os.Args[1:])
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	if err := bootstrap.Ensure(cfg); err != nil {
		log.Fatalf("bootstrap error: %v", err)
	}
	dbPath := filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")
	var db *store.Store
	if cfg.ReadOnly {
		db, err = store.OpenReadOnly(dbPath)
	} else {
		db, err = store.OpenReadWrite(dbPath)
	}
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	var handler http.Handler
	if cfg.ReadOnly {
		readOnlyHandler, err := server.NewHandler(context.Background(), cfg, db)
		if err != nil {
			log.Fatalf("cannot start: %v", err)
		}
		handler = readOnlyHandler
		defer db.Close()
	} else {
		manager, err := server.NewManager(db, cfg)
		if err != nil {
			log.Fatalf("cannot start: %v", err)
		}
		handler = manager
		defer manager.Close()
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	mode := "read-write"
	if cfg.ReadOnly {
		mode = "read-only"
	}
	log.Printf("Experimental Audiobookshelf Go backend v%s; %s mode; SOURCE=%s; listen=%s; config=%s; metadata=%s; routerBasePath=%q", server.Version, mode, cfg.Source, addr, cfg.ConfigPath, cfg.MetadataPath, cfg.RouterBasePath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server: %v", err)
	}
}
