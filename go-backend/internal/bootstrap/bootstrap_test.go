package bootstrap

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/version"
)

func TestEnsureCreatesPinnedFreshSchema(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata")}
	if err := Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	defer db.Close()
	for key := range map[string]string{"version": version.Server, "maxVersion": version.Server} {
		var value string
		if err := db.QueryRow(`SELECT value FROM migrationsMeta WHERE key = ?`, key).Scan(&value); err != nil || value != version.Server {
			t.Fatalf("migration key %s value=%q err=%v", key, value, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&count); err != nil || count != 26 {
		t.Fatalf("schema table count=%d err=%v, want 26 oracle tables", count, err)
	}
	var settings []byte
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'server-settings'`).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if string(settings) == "" || strings.Contains(string(settings), "tokenSecret") {
		t.Fatalf("fresh default settings must omit the generated secret: %s", settings)
	}
	if _, err := os.Stat(cfg.MetadataPath); err != nil {
		t.Fatalf("metadata path was not created: %v", err)
	}
}

func TestEnsureExistingVersionMismatchDoesNotMutate(t *testing.T) {
	cfg := config.Config{ConfigPath: t.TempDir()}
	if err := os.MkdirAll(cfg.ConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	if _, err := db.Exec(`CREATE TABLE migrationsMeta (key TEXT, value TEXT); INSERT INTO migrationsMeta VALUES ('version','2.36.0'),('maxVersion','2.36.0')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(cfg); err == nil {
		t.Fatal("old migration version was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing database bytes changed during validation")
	}
}

func TestEnsureConcurrentFirstBoot(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata")}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = Ensure(cfg)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Ensure failed: %v", err)
		}
	}
}

// Ensure on an existing, valid database leaves it as it is.
func TestEnsureExistingDatabaseIsUnchanged(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata")}
	if err := Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(cfg); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Ensure rewrote an existing database")
	}
}
