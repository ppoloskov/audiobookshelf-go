package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/version"
)

const rootID = "c2eedd44-2b7f-44cf-a18f-2687b798c386"

type env struct {
	t        *testing.T
	h        *Handler
	store    *settings.Store
	mux      *http.ServeMux
	cfg      config.Config
	dir      string
	notified []string
	payloads []map[string]any
	emitted  []string
	staged   []string
}

// newEnv builds a handler over a copy of the fixture database with seeded
// item and author metadata folders.
func newEnv(t *testing.T, notifyErr error) *env {
	t.Helper()
	d := dbtest.Fixture(t)
	var dbFile string
	if err := d.Get(t.Context(), &dbFile, "SELECT file FROM pragma_database_list WHERE name = 'main'"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	e := &env{t: t, mux: http.NewServeMux(), dir: filepath.Join(root, "backups")}
	e.cfg = config.Config{ConfigPath: filepath.Dir(dbFile), MetadataPath: filepath.Join(root, "metadata")}
	for path, data := range map[string]string{
		filepath.Join(e.cfg.Dirs().Items, "book1", "metadata.json"):     `{"id":"book1"}`,
		filepath.Join(e.cfg.Dirs().Authors, "author1", "metadata.json"): `{"id":"author1"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if e.store, err = settings.NewStore(t.Context(), d, func(string) string { return "" }, e.cfg.Dirs().Backups); err != nil {
		t.Fatal(err)
	}
	e.setServer(func(s *settings.Server) { s.BackupPath = e.dir })
	notify := func(_ context.Context, event string, data map[string]any) error {
		e.notified, e.payloads = append(e.notified, event), append(e.payloads, data)
		return notifyErr
	}
	restore := func(_ context.Context, dbTemp, items, authors string) error {
		item, _ := os.ReadFile(filepath.Join(items, "book1", "metadata.json"))
		author, _ := os.ReadFile(filepath.Join(authors, "author1", "metadata.json"))
		_, statErr := os.Stat(dbTemp)
		e.staged = append(e.staged, fmt.Sprintf("%v %s %s", statErr == nil, item, author))
		return nil
	}
	e.h, err = New(e.store, e.cfg, func(string) string { return "" }, func(audience, event string, _ any) { e.emitted = append(e.emitted, audience+" "+event) }, notify, restore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.h.Close)
	e.h.Routes(e.mux)
	return e
}

func (e *env) setServer(fn func(*settings.Server)) {
	e.t.Helper()
	if _, err := e.store.UpdateServer(e.t.Context(), func(s *settings.Server) error { fn(s); return nil }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) do(admin bool, r *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()
	userType := "user"
	if admin {
		userType = "admin"
	}
	r = r.WithContext(auth.WithUser(r.Context(), auth.NewPrincipal(db.User{ID: rootID, Type: &userType, IsActive: true})))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

// zipOf builds an archive from entries in order; a "->" data prefix makes a symlink.
func zipOf(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry[0]}
		header.SetMode(0o600)
		data := entry[1]
		if strings.HasPrefix(data, "->") {
			header.SetMode(os.ModeSymlink | 0o777)
			data = data[2:]
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func upload(t *testing.T, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/api/backups/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestCreateWritesPinnedArchiveLayout(t *testing.T) {
	e := newEnv(t, errors.New("delivery failure must not fail the backup"))
	b, err := e.h.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b.ServerVersion != version.Server || b.Key == nil || *b.Key != "sqlite" || b.FileSize == 0 || b.FullPath != filepath.Join(e.dir, b.ID+".audiobookshelf") {
		t.Fatalf("backup %+v", b)
	}
	zr, err := zip.OpenReader(b.FullPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entries := map[string]bool{}
	for _, f := range zr.File {
		entries[f.Name] = true
	}
	for _, name := range []string{"absdatabase.sqlite", "details", "metadata-items/book1/", "metadata-items/book1/metadata.json", "metadata-authors/author1/metadata.json"} {
		if !entries[name] {
			t.Errorf("missing archive entry %q", name)
		}
	}
	if head := make([]byte, 6); func() bool {
		f, _ := zr.Open("absdatabase.sqlite")
		defer f.Close()
		_, _ = f.Read(head)
		return string(head) != "SQLite"
	}() {
		t.Errorf("database entry is not SQLite: %q", head)
	}
	if loaded, err := readBackup(b.FullPath); err != nil || loaded.ID != b.ID || loaded.CreatedAt != b.CreatedAt {
		t.Fatalf("details did not round trip: %+v %v", loaded, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(e.cfg.ConfigPath, "absdatabase.2*.sqlite"))
	if len(leftovers) != 0 {
		t.Errorf("snapshot left behind: %v", leftovers)
	}
	// Completion is notified after the backup is indexed, with the Apprise template variables.
	sent := e.payloads[0]
	if !reflect.DeepEqual(e.notified, []string{"onBackupCompleted"}) || sent["backupCount"] != 1 || sent["backupPath"] != b.FullPath ||
		sent["completionTime"] != b.CreatedAt || sent["backupSize"] != b.FileSize || sent["removedOldest"] != "false" {
		t.Fatalf("notification %v %v", e.notified, sent)
	}
}

func TestRetentionMaxSizeAndFailure(t *testing.T) {
	e := newEnv(t, nil)
	e.setServer(func(s *settings.Server) { s.BackupsToKeep = 1 })
	for _, id := range []string{"old1", "old2"} {
		if w := e.do(true, upload(t, id+".audiobookshelf", zipOf(t, [2]string{"absdatabase.sqlite", "x"}, [2]string{"details", id + "\nsqlite\n1000\n2.37.2"}))); w.Code != 200 {
			t.Fatalf("upload %s: %d %s", id, w.Code, w.Body)
		}
	}
	if _, err := e.h.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 1 || len(e.h.list()) != 1 {
		t.Fatalf("%d files, %d indexed; want the retention limit of 1", len(entries), len(e.h.list()))
	}
	if last := e.payloads[len(e.payloads)-1]; last["removedOldest"] != true || last["backupCount"] != 1 {
		t.Fatalf("payload %v", last)
	}
	e.setServer(func(s *settings.Server) { s.MaxBackupSize = 1e-9 }) // one byte
	b, err := e.h.Create(t.Context())
	if err == nil || b.ID != "" || e.notified[len(e.notified)-1] != "onBackupFailed" || e.payloads[len(e.payloads)-1]["errorMsg"] != err.Error() {
		t.Fatalf("Create = %+v, %v; notified %v", b, err, e.notified)
	}
}

func TestReadBackupRequiresDatabaseAndDetails(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"nodb":      zipOf(t, [2]string{"details", "id\nsqlite\n123\n2.37.2"}),
		"nodetails": zipOf(t, [2]string{"absdatabase.sqlite", "x"}),
		"old":       zipOf(t, [2]string{"absdatabase.sqlite", "x"}, [2]string{"details", "id\nsqlite\n123"}),
		"notzip":    []byte("nope"),
	} {
		path := filepath.Join(dir, name+".audiobookshelf")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBackup(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRoutesAdminOnlyAndMissingBackup(t *testing.T) {
	e := newEnv(t, nil)
	for _, route := range []string{"GET /api/backups", "POST /api/backups", "POST /api/backups/upload", "PATCH /api/backups/path", "GET /api/backups/x/download", "GET /api/backups/x/apply", "DELETE /api/backups/x"} {
		method, path, _ := strings.Cut(route, " ")
		if w := e.do(false, httptest.NewRequest(method, path, strings.NewReader("{}"))); w.Code != 403 {
			t.Errorf("%s as user: %d, want 403", route, w.Code)
		}
	}
	for _, route := range []string{"GET /api/backups/x/download", "GET /api/backups/x/apply", "DELETE /api/backups/x"} {
		method, path, _ := strings.Cut(route, " ")
		if w := e.do(true, httptest.NewRequest(method, path, nil)); w.Code != 404 {
			t.Errorf("%s: %d, want 404", route, w.Code)
		}
	}
}

func TestUploadRejections(t *testing.T) {
	e := newEnv(t, nil)
	good := zipOf(t, [2]string{"absdatabase.sqlite", "x"}, [2]string{"details", "up\nsqlite\n1000\n2.37.2"})
	for name, c := range map[string]struct {
		file   string
		data   []byte
		status int
	}{
		"wrong extension": {"x.zip", good, 500},
		"not a zip":       {"x.audiobookshelf", []byte("nope"), 400},
		"no database":     {"x.audiobookshelf", zipOf(t, [2]string{"details", "a\nb\nc\nd"}), 400},
		"good":            {"../../up.audiobookshelf", good, 200},
	} {
		if w := e.do(true, upload(t, c.file, c.data)); w.Code != c.status {
			t.Errorf("%s: %d %s, want %d", name, w.Code, strings.TrimSpace(w.Body.String()), c.status)
		}
	}
	entries, _ := os.ReadDir(e.dir)
	if len(entries) != 1 || entries[0].Name() != "up.audiobookshelf" {
		t.Fatalf("backup folder holds %v, want only up.audiobookshelf", entries)
	}
	if w := e.do(true, httptest.NewRequest("GET", "/api/backups/up/download", nil)); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), good) {
		t.Fatalf("download: %d", w.Code)
	}
}

func TestUpdatePathAndSettingsFollowing(t *testing.T) {
	e := newEnv(t, nil)
	moved := filepath.Join(t.TempDir(), "moved")
	if w := e.do(true, httptest.NewRequest("PATCH", "/api/backups/path", strings.NewReader(`{"path":"`+moved+`"}`))); w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if got := e.store.Server().BackupPath; got != moved || e.h.folder() != moved {
		t.Fatalf("stored %q, in use %q", got, e.h.folder())
	}
	if w := e.do(true, httptest.NewRequest("PATCH", "/api/backups/path", strings.NewReader(`{"path":"`+moved+`"}`))); w.Body.String() != "Backup path unchanged" {
		t.Fatalf("same path answered %q", w.Body)
	}
	for _, body := range []string{`{"path":" "}`, `x`, `{"path":"/dev/null/x"}`} {
		if w := e.do(true, httptest.NewRequest("PATCH", "/api/backups/path", strings.NewReader(body))); w.Code != 400 {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	// A schedule change restarts the cron; an invalid one leaves automatic backups off.
	e.setServer(func(s *settings.Server) { s.BackupSchedule = "*/5 * * * *" })
	if e.h.cron == nil {
		t.Fatal("schedule not started")
	}
	e.setServer(func(s *settings.Server) { s.BackupSchedule = "not a cron" })
	if e.h.cron != nil {
		t.Fatal("an invalid schedule started a cron")
	}
}

func TestApplyStagesAndRejectsHostileArchives(t *testing.T) {
	e := newEnv(t, nil)
	valid := zipOf(t, [2]string{"absdatabase.sqlite", "SQLite fixture"}, [2]string{"metadata-items/book1/metadata.json", "item metadata"},
		[2]string{"metadata-authors/author1/metadata.json", "author metadata"}, [2]string{"details", "fixture-id\nsqlite\n1000\n2.37.2"})
	hostile := func(id, name, data string) []byte {
		return zipOf(t, [2]string{"absdatabase.sqlite", "x"}, [2]string{name, data}, [2]string{"details", id + "\nsqlite\n2000\n2.37.2"})
	}
	for id, data := range map[string][]byte{
		"fixture-id": valid,
		"traversal":  hostile("traversal", "metadata-items/../../escape", "x"),
		"symlink":    hostile("symlink", "metadata-authors/a/link", "->/etc/passwd"),
	} {
		if w := e.do(true, upload(t, id+".audiobookshelf", data)); w.Code != 200 {
			t.Fatalf("upload %s: %d", id, w.Code)
		}
	}
	if w := e.do(true, httptest.NewRequest("GET", "/api/backups/fixture-id/apply", nil)); w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(e.staged, []string{"true item metadata author metadata"}) || !reflect.DeepEqual(e.emitted, []string{"* backup_applied"}) {
		t.Fatalf("staged %v, events %v", e.staged, e.emitted)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.ConfigPath, "absdatabase-temp.sqlite")); err == nil {
		t.Error("the extracted database was not removed")
	}
	for _, id := range []string{"traversal", "symlink"} {
		if w := e.do(true, httptest.NewRequest("GET", "/api/backups/"+id+"/apply", nil)); w.Code != 400 {
			t.Errorf("%s: %d %s, want 400", id, w.Code, w.Body)
		}
	}
	if len(e.staged) != 1 {
		t.Errorf("a hostile archive reached the restore hook: %v", e.staged)
	}
}
