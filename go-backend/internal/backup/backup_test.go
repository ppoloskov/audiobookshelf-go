package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/version"
	_ "modernc.org/sqlite"
)

// serve dispatches r through Routes the way the server does: the user travels
// in the request context.
func serve(h *Handler, w http.ResponseWriter, r *http.Request, u store.User) {
	mux := http.NewServeMux()
	h.Routes(mux)
	mux.ServeHTTP(w, r.WithContext(store.ContextWithUser(r.Context(), u)))
}

func TestCreateBackupUsesVacuumSnapshotAndPinnedArchiveLayout(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	if err := os.MkdirAll(cfg.ConfigPath, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	if _, err = db.Exec(`CREATE TABLE example (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO example(value) VALUES ('snapshot')`); err != nil {
		t.Fatal(err)
	}
	items := filepath.Join(cfg.MetadataPath, "items", "book1")
	authors := filepath.Join(cfg.MetadataPath, "authors", "author1")
	if err = os.MkdirAll(items, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(authors, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(items, "metadata.json"), []byte(`{"id":"book1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(authors, "metadata.json"), []byte(`{"id":"author1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := New(cfg, Options{BackupPath: filepath.Join(root, "backups")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	b, err := h.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.ServerVersion != version.Server || b.Key != "sqlite" || b.FileSize == 0 {
		t.Fatalf("bad backup DTO: %#v", b)
	}
	z, err := zip.OpenReader(b.FullPath)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	entries := map[string]bool{}
	for _, f := range z.File {
		entries[f.Name] = true
	}
	for _, name := range []string{"absdatabase.sqlite", "details", "metadata-items/book1/metadata.json", "metadata-authors/author1/metadata.json"} {
		if !entries[name] {
			t.Errorf("missing archive entry %q", name)
		}
	}
	for _, f := range z.File {
		if f.Name == "absdatabase.sqlite" {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data := make([]byte, 16)
			_, _ = r.Read(data)
			_ = r.Close()
			if string(data[:6]) != "SQLite" {
				t.Fatalf("database entry is not SQLite: %q", data)
			}
		}
	}
	loaded, err := readBackup(b.FullPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != b.ID || loaded.ServerVersion != version.Server {
		t.Fatalf("archive details failed to round trip: %#v", loaded)
	}
	var snap string
	if err = db.QueryRow(`SELECT value FROM example`).Scan(&snap); err != nil || snap != "snapshot" {
		t.Fatalf("source database changed: %q %v", snap, err)
	}
}

// Backup lifecycle events reach the notification hook given to New: completion
// only after the backup is indexed (and a delivery error does not fail the
// backup), failure with the error message and nothing indexed.
func TestCreateNotifiesBackupLifecycle(t *testing.T) {
	t.Run("success notifies after backup is indexed", func(t *testing.T) {
		root := t.TempDir()
		cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
		if err := os.MkdirAll(cfg.ConfigPath, 0755); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1) // production pool size (store.Open)
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE example (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		var h *Handler
		var events []string
		var indexedAtNotify, count any
		var path any
		h, err = New(cfg, Options{BackupPath: filepath.Join(root, "backups")}, nil, func(_ context.Context, event string, payload map[string]any) error {
			events = append(events, event)
			indexedAtNotify = len(h.list())
			path, count = payload["backupPath"], payload["backupCount"]
			return errors.New("notification delivery failure must not fail backup")
		})
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		b, err := h.Create(context.Background())
		if err != nil {
			t.Fatalf("Create failed because the notification hook returned an error: %v", err)
		}
		if !reflect.DeepEqual(events, []string{"onBackupCompleted"}) {
			t.Fatalf("events = %v", events)
		}
		if indexedAtNotify != 1 || count != 1 || path != b.FullPath {
			t.Fatalf("at notification: indexed=%v count=%v path=%v, want the one new backup %s", indexedAtNotify, count, path, b.FullPath)
		}
	})

	t.Run("failure notifies with source error message", func(t *testing.T) {
		root := t.TempDir()
		var h *Handler
		var events []string
		var sent map[string]any
		var indexed int
		h, err := New(config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}, Options{BackupPath: filepath.Join(root, "backups")}, nil, func(_ context.Context, event string, payload map[string]any) error {
			events = append(events, event)
			sent, indexed = payload, len(h.list())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		b, err := h.Create(context.Background())
		if err == nil || b != (Backup{}) {
			t.Fatalf("Create = (%#v, %v), want empty backup and the missing-database error", b, err)
		}
		if !reflect.DeepEqual(events, []string{"onBackupFailed"}) || sent["errorMsg"] != err.Error() || indexed != 0 {
			t.Fatalf("events %v payload %#v indexed %d", events, sent, indexed)
		}
	})
}

func TestReadBackupRequiresDatabaseDetailsAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.audiobookshelf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("details")
	_, _ = w.Write([]byte("id\nsqlite\n123\n2.37.2"))
	_ = z.Close()
	_ = f.Close()
	if _, err = readBackup(path); err == nil {
		t.Fatal("accepted archive without absdatabase.sqlite")
	}
}

func TestMissingBackupIs404(t *testing.T) {
	root := t.TempDir()
	h, err := New(config.Config{MetadataPath: root}, Options{BackupPath: filepath.Join(root, "backups")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, suffix := range []string{"download", "apply", ""} {
		path := "/api/backups/missing"
		if suffix != "" {
			path += "/" + suffix
		}
		method := "GET"
		if suffix == "" {
			method = "DELETE"
		}
		w := httptest.NewRecorder()
		serve(h, w, httptest.NewRequest(method, path, nil), store.User{Type: "admin"})
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%q", method, path, w.Code, w.Body.String())
		}
	}
}

func TestUploadInvalidExtensionIsRejected(t *testing.T) {
	root := t.TempDir()
	h, err := New(config.Config{MetadataPath: root}, Options{BackupPath: filepath.Join(root, "backups")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "invalid.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("not a backup"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/backups/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	res := httptest.NewRecorder()
	httputil.Handler(h.upload).ServeHTTP(res, req)
	if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "Invalid backup file") {
		t.Fatalf("response = %d %q", res.Code, res.Body.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "backups")); len(entries) != 0 {
		t.Fatalf("rejected upload left %d files in the backup directory", len(entries))
	}
}

func TestUpdateOptionsReloadsPathAndSchedule(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	first := filepath.Join(root, "one")
	second := filepath.Join(root, "two")
	h, err := New(cfg, Options{BackupPath: first}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err = h.UpdateOptions(context.Background(), Options{BackupPath: second, BackupSchedule: "*/5 * * * *", BackupsToKeep: 4, MaxBackupSizeGB: 3}); err != nil {
		t.Fatal(err)
	}
	if got := h.options(); got.BackupPath != second || got.BackupsToKeep != 4 || got.MaxBackupSizeGB != 3 || got.BackupSchedule != "*/5 * * * *" {
		t.Fatalf("options were not updated: %#v", got)
	}
	if _, err = os.Stat(second); err != nil {
		t.Fatalf("new backup directory was not created: %v", err)
	}
	if err = h.UpdateOptions(context.Background(), Options{BackupPath: second, BackupsToKeep: 2}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyStagesSafeMetadataAndRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	if err := os.MkdirAll(cfg.ConfigPath, 0755); err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(cfg.MetadataPath, "backups")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatal(err)
	}
	makeArchive := func(name string, entries []struct {
		name string
		mode os.FileMode
		data string
	}) string {
		t.Helper()
		path := filepath.Join(archiveDir, name)
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		zw := zip.NewWriter(f)
		for _, entry := range entries {
			header := &zip.FileHeader{Name: entry.name}
			header.SetMode(entry.mode)
			w, e := zw.CreateHeader(header)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = w.Write([]byte(entry.data))
		}
		if e = zw.Close(); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
		return path
	}
	makeEntries := func(metadataName string, metadataMode os.FileMode) []struct {
		name string
		mode os.FileMode
		data string
	} {
		return []struct {
			name string
			mode os.FileMode
			data string
		}{{"absdatabase.sqlite", 0600, "SQLite fixture"}, {"metadata-items/book1/item.json", 0600, "item metadata"}, {metadataName, metadataMode, "payload"}, {"metadata-authors/author1/author.json", 0600, "author metadata"}, {"details", 0600, "fixture-id\nsqlite\n1000\n2.37.2"}}
	}
	valid := makeArchive("fixture.audiobookshelf", makeEntries("metadata-items/book1/extra.json", 0600))
	var gotItem, gotAuthor string
	h, err := New(cfg, Options{BackupPath: archiveDir, Restore: func(_ context.Context, db, items, authors string) error {
		if _, e := os.Stat(db); e != nil {
			return e
		}
		a, e := os.ReadFile(filepath.Join(items, "book1", "item.json"))
		if e != nil {
			return e
		}
		gotItem = string(a)
		a, e = os.ReadFile(filepath.Join(authors, "author1", "author.json"))
		if e != nil {
			return e
		}
		gotAuthor = string(a)
		return nil
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if b, e := readBackup(valid); e != nil || b.ID != "fixture-id" {
		t.Fatalf("load valid fixture backup: %#v %v", b, e)
	}
	r := httptest.NewRequest("GET", "/api/backups/fixture-id/apply", nil)
	w := httptest.NewRecorder()
	serve(h, w, r, store.User{Type: "admin"})
	if w.Code != 200 {
		t.Fatalf("apply returned %d: %s", w.Code, w.Body.String())
	}
	if gotItem != "item metadata" || gotAuthor != "author metadata" {
		t.Fatalf("staged metadata mismatch: item=%q author=%q", gotItem, gotAuthor)
	}
	// The valid fixture is removed from the handler's list before testing each
	// hostile archive so the same request resolves to the new file.
	for i, hostile := range []struct {
		name string
		mode os.FileMode
	}{{"metadata-items/../../escape", 0600}, {"metadata-authors/author1/link", os.ModeSymlink | 0777}} {
		id := []string{"traversal", "symlink"}[i]
		entries := makeEntries(hostile.name, hostile.mode)
		entries[len(entries)-1].data = id + "\nsqlite\n2000\n2.37.2"
		p := makeArchive(id+".audiobookshelf", entries)
		b, e := readBackup(p)
		if e != nil {
			t.Fatal(e)
		}
		h.mu.Lock()
		h.backups = append(h.backups, b)
		h.mu.Unlock()
		r = httptest.NewRequest("GET", "/api/backups/"+id+"/apply", nil)
		w = httptest.NewRecorder()
		serve(h, w, r, store.User{Type: "admin"})
		if w.Code != 400 {
			t.Fatalf("hostile archive %s returned %d: %s", id, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "panic") {
			t.Fatal(w.Body.String())
		}
	}
}
