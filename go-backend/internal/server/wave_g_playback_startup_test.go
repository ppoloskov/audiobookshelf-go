package server

import (
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// Playback stream cleanup belongs to process startup, matching Server.init
// (PlaybackSessionManager.js:461-483), rather than Handler construction or a
// restore/runtime rebuild. A process restart has no in-memory sessions, so a
// UUID stream directory is orphaned; unrelated names are retained.
func TestManagerStartupRemovesOrphanPlaybackStreams(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		ConfigPath:   filepath.Join(root, "config"),
		MetadataPath: filepath.Join(root, "metadata"),
		JWTSecret:    "startup-cleanup-test-secret",
	}
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	streams := cfg.Dirs().Streams
	orphan := filepath.Join(streams, "550e8400-e29b-41d4-a716-446655440000")
	unrelated := filepath.Join(streams, "not-a-session")
	for _, dir := range []string{orphan, unrelated} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(store.DatabasePath(cfg.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTestRoot(db, cfg, "stream-cleanup", "password"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	manager, err := NewManager(db, cfg)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan playback stream directory remains after startup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unrelated, "marker")); err != nil {
		t.Fatalf("non-UUID stream directory was removed: %v", err)
	}
}
