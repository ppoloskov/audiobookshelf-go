package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// A metadata staging failure after the items directory has already been
// swapped must restore the old database and both metadata trees, then rebuild
// a usable runtime. This drives the real Manager rollback path without a
// filesystem injection seam.
func TestWaveGRestoreFailureRestoresStateAndLeavesNoSwapArtifacts(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	manager, _ := newTestManager(t, cfg, "stable-user", "shelf-stable")
	defer manager.Close()

	for _, name := range []string{"items", "authors"} {
		dir := filepath.Join(cfg.MetadataPath, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.txt"), []byte("stable "+name), 0644); err != nil {
			t.Fatal(err)
		}
	}

	backupDB := createSnapshot(t, config.Config{ConfigPath: filepath.Join(t.TempDir(), "backup-config"), MetadataPath: filepath.Join(t.TempDir(), "backup-metadata")}, "rejected-user", "shelf-rejected")
	stagedItems, stagedAuthors := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(stagedItems, "state.txt"), []byte("replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(stagedAuthors, "unsafe")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	err := manager.restore(context.Background(), backupDB, stagedItems, stagedAuthors)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("restore error = %v, want metadata staging failure", err)
	}
	if got := loginStatus(manager, "stable-user", "shelf-stable"); got != http.StatusOK {
		t.Fatalf("original account after rollback = %d", got)
	}
	if got := loginStatus(manager, "rejected-user", "shelf-rejected"); got != http.StatusUnauthorized {
		t.Fatalf("rejected account after rollback = %d", got)
	}
	for _, name := range []string{"items", "authors"} {
		if got := readFile(t, filepath.Join(cfg.MetadataPath, name, "state.txt")); got != "stable "+name {
			t.Fatalf("%s metadata after rollback = %q", name, got)
		}
		if _, err := os.Lstat(filepath.Join(cfg.MetadataPath, name+".restore-previous")); !os.IsNotExist(err) {
			t.Fatalf("stale previous %s tree after rollback: %v", name, err)
		}
	}
	dbPath := store.DatabasePath(cfg.ConfigPath)
	if _, err := os.Stat(dbPath + ".restore-previous"); !os.IsNotExist(err) {
		t.Fatalf("stale previous database after rollback: %v", err)
	}
	check, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen restored database: %v", err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
}
