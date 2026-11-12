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

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/bootstrap"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/store"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/websocket"
)

func TestManagerRestoreSwapsRuntimeDatabaseAndMetadata(t *testing.T) {
	currentCfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), RouterBasePath: "/audiobookshelf", JWTSecret: "test-secret"}
	manager, _ := newTestManager(t, currentCfg, "before-restore", "shelf-before")
	defer manager.Close()
	oldRuntime := manager.current.Load()
	oldHub := manager.hub
	srv := httptest.NewServer(manager)
	defer srv.Close()
	socket := openSocket(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/audiobookshelf/socket.io/?EIO=4&transport=websocket", makeToken(t, currentCfg.JWTSecret, `{"userId":"root-test","username":"before-restore","type":"access","exp":4102444800}`))
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
	if manager.current.Load() == oldRuntime {
		t.Fatal("restore reused the old runtime graph")
	}
	if manager.current.Load().realtime != oldHub {
		t.Fatal("restore replaced the realtime hub")
	}
	// SocketAuthority lives in the same process through backup apply; upstream
	// BackupManager.js:263-266 emits backup_applied after the response, without
	// disconnecting authenticated clients. The connected socket must survive and
	// receive the event.
	if got := oldHub.UsersOnline(); len(got) != 1 || got[0].ID != "root-test" {
		t.Fatalf("online users after restore=%v", got)
	}
	oldHub.Emit("*", "backup_applied", nil)
	waitFrame(t, socket, `42["backup_applied"`)
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
	if err := manager.current.Load().db.QueryRow(`SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
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
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), RouterBasePath: "/audiobookshelf", JWTSecret: "test-secret"}
	manager, _ := newTestManager(t, cfg, "stable-user", "shelf-stable")
	defer manager.Close()
	srv := httptest.NewServer(manager)
	defer srv.Close()
	socket := openSocket(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/audiobookshelf/socket.io/?EIO=4&transport=websocket", makeToken(t, cfg.JWTSecret, `{"userId":"root-test","username":"stable-user","type":"access","exp":4102444800}`))
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
	manager.hub.Emit("*", "after_rollback", nil)
	waitFrame(t, socket, `42["after_rollback"`)
	if body := readFile(t, filepath.Join(cfg.MetadataPath, "items", "state.txt")); body != "stable item" {
		t.Fatalf("item metadata after rollback=%q", body)
	}
	if body := readFile(t, filepath.Join(cfg.MetadataPath, "authors", "state.txt")); body != "stable author" {
		t.Fatalf("author metadata after rollback=%q", body)
	}
}

// openSocket connects and authenticates a Socket.IO client and returns it once
// the server has answered with init.
func openSocket(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	send := func(m string) {
		if err := websocket.Message.Send(conn, m); err != nil {
			t.Fatal(err)
		}
	}
	waitFrame(t, conn, "0{")
	send("40")
	waitFrame(t, conn, "40{")
	send(`42["auth","` + token + `"]`)
	waitFrame(t, conn, `42["init"`)
	return conn
}

// waitFrame reads frames until one starts with prefix; it fails if the
// connection closes or nothing matching arrives within the deadline.
func waitFrame(t *testing.T, conn *websocket.Conn, prefix string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg string
		if err := websocket.Message.Receive(conn, &msg); err != nil {
			t.Fatalf("waiting for %q: %v", prefix, err)
		}
		if strings.HasPrefix(msg, prefix) {
			return
		}
	}
}

// snapshotWithVersion is createSnapshot for a database whose migration state
// claims another server version.
func snapshotWithVersion(t *testing.T, cfg config.Config, username, password, ver string) string {
	t.Helper()
	path := createSnapshot(t, cfg, username, password)
	database, err := store.OpenUnchecked(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE migrationsMeta SET value=? WHERE key IN ('version','maxVersion')`, ver); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A backup whose database is of another server version is refused before
// anything is touched: the running database, runtime, metadata and the
// connected socket are all unchanged.
func TestManagerRestoreRefusesWrongVersionBackup(t *testing.T) {
	cfg := config.Config{ConfigPath: filepath.Join(t.TempDir(), "config"), MetadataPath: filepath.Join(t.TempDir(), "metadata"), JWTSecret: "test-secret"}
	manager, _ := newTestManager(t, cfg, "stable-user", "shelf-stable")
	defer manager.Close()
	srv := httptest.NewServer(manager)
	defer srv.Close()
	socket := openSocket(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/socket.io/?EIO=4&transport=websocket", makeToken(t, cfg.JWTSecret, `{"userId":"root-test","username":"stable-user","type":"access","exp":4102444800}`))
	if err := os.MkdirAll(filepath.Join(cfg.MetadataPath, "items"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MetadataPath, "items", "state.txt"), []byte("stable item"), 0644); err != nil {
		t.Fatal(err)
	}
	oldRuntime := manager.current.Load()
	wrong := snapshotWithVersion(t, config.Config{ConfigPath: filepath.Join(t.TempDir(), "c"), MetadataPath: filepath.Join(t.TempDir(), "m")}, "other-user", "shelf-other", "2.0.0")
	stagedItems := t.TempDir()
	if err := os.WriteFile(filepath.Join(stagedItems, "state.txt"), []byte("replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	err := manager.restore(context.Background(), wrong, stagedItems, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "validate backup database") {
		t.Fatalf("restore error = %v, want the validation failure", err)
	}
	if manager.current.Load() != oldRuntime {
		t.Fatal("a refused backup replaced the runtime")
	}
	if got := loginStatus(manager, "stable-user", "shelf-stable"); got != http.StatusOK {
		t.Fatalf("login after refused restore = %d", got)
	}
	if got := loginStatus(manager, "other-user", "shelf-other"); got != http.StatusUnauthorized {
		t.Fatalf("refused backup's user can log in: %d", got)
	}
	if body := readFile(t, filepath.Join(cfg.MetadataPath, "items", "state.txt")); body != "stable item" {
		t.Fatalf("metadata after refused restore = %q", body)
	}
	manager.hub.Emit("*", "ping_after_refusal", nil)
	waitFrame(t, socket, `42["ping_after_refusal"`)
}

func newTestManager(t *testing.T, cfg config.Config, username, password string) (*Manager, *store.Store) {
	t.Helper()
	if err := bootstrap.Ensure(cfg); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(store.DatabasePath(cfg.ConfigPath))
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
	database, err := store.Open(store.DatabasePath(cfg.ConfigPath))
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
	data := db.New(database.DB())
	st, err := settings.NewStore(context.Background(), data, os.Getenv, "")
	if err != nil {
		return err
	}
	if _, err := auth.New(data, st, nil, cfg); err != nil {
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
