package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/identity"
	"audiobookshelf-compat/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestManagerRestoreSwapsRuntimeDatabaseAndMetadata(t *testing.T) {
	currentCfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), RouterBasePath: "/audiobookshelf"}
	manager, _ := newTestManager(t, currentCfg, "before-restore", "shelf-before")
	defer manager.Close()
	oldRuntime := manager.current
	oldHub := oldRuntime.realtime
	oldHub.AddTestClient("restore-socket", store.User{ID: "root-test", Username: "before-restore", Type: "root"})
	var events []string
	oldHub.SetTestEmitter("restore-socket", func(event string) { events = append(events, event) })
	if err := os.MkdirAll(filepath.Join(currentCfg.MetadataPath, "items"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentCfg.MetadataPath, "items", "state.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := loginStatus(manager, "before-restore", "shelf-before"); got != http.StatusOK {
		t.Fatalf("pre-restore login status=%d", got)
	}

	backupCfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "backup-config"), MetadataPath: filepath.Join(t.TempDir(), "backup-metadata"), RouterBasePath: "/audiobookshelf"}
	backupDB := createSnapshot(t, backupCfg, "after-restore", "shelf-after")
	stagedItems := filepath.Join(t.TempDir(), "metadata-items")
	stagedAuthors := filepath.Join(t.TempDir(), "metadata-authors")
	if err := os.MkdirAll(stagedItems, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagedAuthors, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedItems, "state.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedAuthors, "state.txt"), []byte("new author"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := manager.restore(context.Background(), backupDB, stagedItems, stagedAuthors); err != nil {
		t.Fatal(err)
	}
	if manager.current == oldRuntime {
		t.Fatal("restore reused the old runtime graph")
	}
	if manager.current.realtime != oldHub {
		t.Fatal("restore replaced the realtime hub")
	}
	// SocketAuthority lives in the same process through backup apply; upstream
	// BackupManager.js:263-266 emits backup_applied after the response, without
	// disconnecting authenticated clients. Preserve that connected identity.
	if got := manager.current.realtime.UsersOnline(); len(got) != 1 || got[0]["id"] != "root-test" {
		t.Fatalf("online users after restore=%v", got)
	}
	oldHub.Emit("*", "backup_applied", nil)
	if len(events) != 1 || events[0] != "backup_applied" {
		t.Fatalf("events after restore=%v", events)
	}
	if got := loginStatus(manager, "after-restore", "shelf-after"); got != http.StatusOK {
		t.Fatalf("restored login status=%d", got)
	}
	if got := loginStatus(manager, "before-restore", "shelf-before"); got != http.StatusUnauthorized {
		t.Fatalf("old credentials after restore status=%d", got)
	}
	if body := readFile(t, filepath.Join(currentCfg.MetadataPath, "items", "state.txt")); body != "new" {
		t.Fatalf("restored item metadata=%q", body)
	}
	if body := readFile(t, filepath.Join(currentCfg.MetadataPath, "authors", "state.txt")); body != "new author" {
		t.Fatalf("restored author metadata=%q", body)
	}
	var raw []byte
	if err := manager.current.db.QueryRow(`SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["tokenSecret"] == nil {
		t.Fatal("restored runtime did not load a token secret")
	}
}

func TestManagerRestoreFailureRollsBackDatabaseAndMetadata(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), RouterBasePath: "/audiobookshelf"}
	manager, _ := newTestManager(t, cfg, "stable-user", "shelf-stable")
	defer manager.Close()
	if err := os.MkdirAll(filepath.Join(cfg.MetadataPath, "items"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.MetadataPath, "authors"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MetadataPath, "items", "state.txt"), []byte("stable item"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MetadataPath, "authors", "state.txt"), []byte("stable author"), 0644); err != nil {
		t.Fatal(err)
	}
	backupCfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "backup-config"), MetadataPath: filepath.Join(t.TempDir(), "backup-metadata")}
	backupDB := createSnapshot(t, backupCfg, "rejected-user", "shelf-rejected")
	stagedItems, stagedAuthors := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(stagedItems, "state.txt"), []byte("replace item"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(stagedAuthors, "unsafe")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		for i := 0; i < 20; i++ {
			w := httptest.NewRecorder()
			manager.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
			if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
				return
			}
		}
	}()
	err := manager.restore(ctx, backupDB, stagedItems, stagedAuthors)
	<-requestDone
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("restore error=%v, want staged metadata symlink failure", err)
	}
	if got := loginStatus(manager, "stable-user", "shelf-stable"); got != http.StatusOK {
		t.Fatalf("original credentials after rollback status=%d", got)
	}
	if got := loginStatus(manager, "rejected-user", "shelf-rejected"); got != http.StatusUnauthorized {
		t.Fatalf("rejected credentials after rollback status=%d", got)
	}
	if body := readFile(t, filepath.Join(cfg.MetadataPath, "items", "state.txt")); body != "stable item" {
		t.Fatalf("item metadata after rollback=%q", body)
	}
	if body := readFile(t, filepath.Join(cfg.MetadataPath, "authors", "state.txt")); body != "stable author" {
		t.Fatalf("author metadata after rollback=%q", body)
	}
}

func newTestManager(t *testing.T, cfg config.Config, username, password string) (*Manager, *store.Store) {
	t.Helper()
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTestRoot(database, cfg, username, password); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	manager, err := NewManager(database, cfg)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	return manager, database
}

func createSnapshot(t *testing.T, cfg config.Config, username, password string) string {
	t.Helper()
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenReadWrite(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := insertTestRoot(database, cfg, username, password); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	var busy, logFrames, checkpointed int
	if err := database.DB().QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := copyFile(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"), path); err != nil {
		t.Fatal(err)
	}
	return path
}

func insertTestRoot(database *store.Store, cfg config.Config, username, password string) error {
	if _, err := identity.New(database.DB(), cfg); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	permissions := `{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"download":true,"update":true,"delete":true,"upload":true,"createEreader":true,"librariesAccessible":[],"itemTagsSelected":[]}`
	_, err = database.DB().Exec(`INSERT INTO users(id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES('root-test',?,NULL,?,'root','legacy-token',1,0,NULL,?,'[]','{}',?,?)`, username, string(hash), permissions, now, now)
	return err
}

func loginStatus(handler http.Handler, username, password string) int {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0600)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
