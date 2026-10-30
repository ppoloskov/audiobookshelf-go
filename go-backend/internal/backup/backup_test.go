package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

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
	h, err := New(db, cfg, Options{BackupPath: filepath.Join(root, "backups")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	b, err := h.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.ServerVersion != serverVersion || b.Key != "sqlite" || b.FileSize == 0 {
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
	if loaded.ID != b.ID || loaded.ServerVersion != serverVersion {
		t.Fatalf("archive details failed to round trip: %#v", loaded)
	}
	var snap string
	if err = db.QueryRow(`SELECT value FROM example`).Scan(&snap); err != nil || snap != "snapshot" {
		t.Fatalf("source database changed: %q %v", snap, err)
	}
}

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
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE example (id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		h, err := New(db, cfg, Options{BackupPath: filepath.Join(root, "backups")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()

		var notified bool
		h.SetNotificationHandler(func(ctx context.Context, event string, payload map[string]any) error {
			notified = true
			if ctx == nil {
				t.Fatal("notification context is nil")
			}
			if event != "onBackupCompleted" {
				t.Fatalf("event = %q, want onBackupCompleted", event)
			}
			if got := h.list(); len(got) != 1 || got[0].FullPath != payload["backupPath"] {
				t.Fatalf("notification ran before backup was indexed: backups=%#v payload=%#v", got, payload)
			}
			b := h.list()[0]
			want := map[string]any{
				"completionTime": b.CreatedAt,
				"backupPath":     b.FullPath,
				"backupSize":     b.FileSize,
				"backupCount":    1,
				"removedOldest":  false,
			}
			if !reflect.DeepEqual(payload, want) {
				t.Fatalf("payload = %#v, want %#v", payload, want)
			}
			return errors.New("notification delivery failure must not fail backup")
		})

		b, err := h.Create(context.Background())
		if err != nil {
			t.Fatalf("Create failed because notification handler returned an error: %v", err)
		}
		if !notified || b.FullPath == "" {
			t.Fatalf("completion notification called=%t backup=%#v", notified, b)
		}
	})

	t.Run("failure notifies with source error message", func(t *testing.T) {
		root := t.TempDir()
		h, err := New(nil, config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}, Options{BackupPath: filepath.Join(root, "backups")}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()

		var notified bool
		h.SetNotificationHandler(func(_ context.Context, event string, payload map[string]any) error {
			notified = true
			if event != "onBackupFailed" {
				t.Fatalf("event = %q, want onBackupFailed", event)
			}
			want := map[string]any{"errorMsg": "database is unavailable"}
			if !reflect.DeepEqual(payload, want) {
				t.Fatalf("payload = %#v, want %#v", payload, want)
			}
			if len(h.list()) != 0 {
				t.Fatalf("failed backup was indexed before notification: %#v", h.list())
			}
			return nil
		})

		b, err := h.Create(context.Background())
		if err == nil || err.Error() != "database is unavailable" || b != (Backup{}) {
			t.Fatalf("Create = (%#v, %v), want empty backup and database-unavailable error", b, err)
		}
		if !notified {
			t.Fatal("failure notification was not called")
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

func TestMissingBackupUsesExpressSendStatus(t *testing.T) {
	root := t.TempDir()
	h, err := New(nil, config.Config{MetadataPath: root}, Options{BackupPath: filepath.Join(root, "backups")}, nil)
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
		h.Handle(w, httptest.NewRequest(method, path, nil), store.User{Type: "admin"})
		if w.Code != http.StatusNotFound || w.Body.String() != "Not Found" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("ETag") == "" {
			t.Fatalf("%s %s status=%d headers=%v body=%q", method, path, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestUploadInvalidExtensionUsesExpressSend(t *testing.T) {
	root := t.TempDir()
	h, err := New(nil, config.Config{MetadataPath: root}, Options{BackupPath: filepath.Join(root, "backups")}, nil)
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
	h.upload(res, req)
	want := "Invalid backup file"
	if res.Code != http.StatusInternalServerError || res.Body.String() != want || res.Header().Get("Content-Type") != "text/html; charset=utf-8" || res.Header().Get("ETag") != `W/"13-bxUZ24wsDGbPFzFAIqXhoyNKTHE"` {
		t.Fatalf("response = %d %q headers=%v", res.Code, res.Body.String(), res.Header())
	}
}

func TestUpdateOptionsReloadsPathAndSchedule(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	first := filepath.Join(root, "one")
	second := filepath.Join(root, "two")
	h, err := New(nil, cfg, Options{BackupPath: first}, nil)
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
	h, err := New(nil, cfg, Options{BackupPath: archiveDir, Restore: func(_ context.Context, db, items, authors string) error {
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
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if b, e := readBackup(valid); e != nil || b.ID != "fixture-id" {
		t.Fatalf("load valid fixture backup: %#v %v", b, e)
	}
	r := httptest.NewRequest("GET", "/api/backups/fixture-id/apply", nil)
	w := httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
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
		h.Handle(w, r, store.User{Type: "admin"})
		if w.Code != 400 {
			t.Fatalf("hostile archive %s returned %d: %s", id, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "panic") {
			t.Fatal(w.Body.String())
		}
	}
}

// These interoperability checks run against the actual upstream-created
// fixture when ABS_BACKUP_NODE_ARCHIVE is provided by the parity harness.
func TestUpstreamCreatedBackupImports(t *testing.T) {
	archive := os.Getenv("ABS_BACKUP_NODE_ARCHIVE")
	if archive == "" {
		t.Skip("set ABS_BACKUP_NODE_ARCHIVE to an upstream-produced backup fixture")
	}
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	if err := os.MkdirAll(cfg.ConfigPath, 0755); err != nil {
		t.Fatal(err)
	}
	var itemMeta, authorAsset string
	h, err := New(nil, cfg, Options{BackupPath: filepath.Dir(archive), Restore: func(_ context.Context, db, items, authors string) error {
		if _, e := os.Stat(db); e != nil {
			return e
		}
		found := false
		if e := filepath.WalkDir(items, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				found = true
				if strings.HasSuffix(path, "metadata.json") {
					b, e := os.ReadFile(path)
					if e == nil {
						itemMeta = string(b)
					}
				}
			}
			return nil
		}); e != nil {
			return e
		}
		if !found {
			return errors.New("upstream archive contained no item metadata")
		}
		if e := filepath.WalkDir(authors, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				b, e := os.ReadFile(path)
				if e == nil {
					authorAsset = string(b)
				}
				return e
			}
			return nil
		}); e != nil {
			return e
		}
		return nil
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var b Backup
	for _, candidate := range h.list() {
		if candidate.FullPath == archive {
			b = candidate
			break
		}
	}
	if b.ID == "" {
		t.Fatalf("upstream archive was not indexed: %s", archive)
	}
	r := httptest.NewRequest("GET", "/api/backups/"+b.ID+"/apply", nil)
	w := httptest.NewRecorder()
	h.Handle(w, r, store.User{Type: "admin"})
	if w.Code != 200 {
		t.Fatalf("import upstream backup failed (%d): %s", w.Code, w.Body.String())
	}
	if itemMeta == "" || authorAsset == "" {
		t.Fatalf("upstream archive data missing: item=%t author=%t", itemMeta != "", authorAsset != "")
	}
}

func TestExportNodeFixtureDatabaseForNodeImport(t *testing.T) {
	dbSource, metaSource, outDir := os.Getenv("ABS_BACKUP_FIXTURE_DB"), os.Getenv("ABS_BACKUP_FIXTURE_METADATA"), os.Getenv("ABS_BACKUP_EXPORT_DIR")
	if dbSource == "" || metaSource == "" || outDir == "" {
		t.Skip("set ABS_BACKUP_FIXTURE_DB, ABS_BACKUP_FIXTURE_METADATA, and ABS_BACKUP_EXPORT_DIR for Node import parity")
	}
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: metaSource}
	if err := os.MkdirAll(cfg.ConfigPath, 0755); err != nil {
		t.Fatal(err)
	}
	src, e := os.Open(dbSource)
	if e != nil {
		t.Fatal(e)
	}
	dst, e := os.Create(filepath.Join(cfg.ConfigPath, "absdatabase.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(dst, src); e != nil {
		t.Fatal(e)
	}
	_ = src.Close()
	if e = dst.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", "file:"+filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = os.MkdirAll(outDir, 0755); e != nil {
		t.Fatal(e)
	}
	h, e := New(db, cfg, Options{BackupPath: outDir, BackupsToKeep: 10}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	backup, e := h.Create(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	destPath := filepath.Join(outDir, "go-created.audiobookshelf")
	if e = os.Rename(backup.FullPath, destPath); e != nil {
		t.Fatal(e)
	}
	if _, e = readBackup(destPath); e != nil {
		t.Fatalf("Go archive is not upstream-importable: %v", e)
	}
}
