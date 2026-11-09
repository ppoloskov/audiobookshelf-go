package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// archiveBytes builds a backup archive: a database entry (unless noDB), a
// metadata item and the details entry.
func archiveBytes(t *testing.T, id string, created int64, serverVersion string, noDB bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, data string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(data))
	}
	if !noDB {
		add("absdatabase.sqlite", "SQLite format 3\x00")
	}
	add("metadata-items/book1/metadata.json", `{"id":"book1"}`)
	add("details", fmt.Sprintf("%s\nsqlite\n%d\n%s", id, created, serverVersion))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type env struct {
	h      *Handler
	dir    string
	cfg    config.Config
	mu     sync.Mutex
	events []string
}

func newEnv(t *testing.T, opt Options) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{dir: filepath.Join(root, "backups"), cfg: config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}}
	if err := os.MkdirAll(e.cfg.ConfigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if opt.BackupPath == "" {
		opt.BackupPath = e.dir
	}
	h, err := New(e.cfg, opt, func(_ string, event string, _ any) {
		e.mu.Lock()
		e.events = append(e.events, event)
		e.mu.Unlock()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	e.h = h
	return e
}

func (e *env) call(method, path string, body []byte, contentType string, u store.User) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	serve(e.h, w, req, u)
	return w
}

var admin = store.User{Type: "admin"}

func (e *env) listIDs(t *testing.T) []string {
	t.Helper()
	w := e.call("GET", "/api/backups", nil, "", admin)
	var body struct {
		Backups []Backup `json:"backups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("list body %q: %v", w.Body.String(), err)
	}
	ids := []string{}
	for _, b := range body.Backups {
		ids = append(ids, b.ID)
	}
	return ids
}

func upload(t *testing.T, e *env, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	return e.call("POST", "/api/backups/upload", body.Bytes(), mw.FormDataContentType(), admin)
}

// An uploaded archive is validated, stored under its own name, listed, and can
// be deleted again, which removes the file; non-admins are refused throughout.
func TestUploadListAndDeleteBackup(t *testing.T) {
	e := newEnv(t, Options{})
	if w := upload(t, e, "good.audiobookshelf", archiveBytes(t, "up-1", 1000, "2.37.2", false)); w.Code != 200 {
		t.Fatalf("upload = %d %s", w.Code, w.Body)
	}
	if ids := e.listIDs(t); len(ids) != 1 || ids[0] != "up-1" {
		t.Fatalf("list after upload = %v", ids)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "good.audiobookshelf")); err != nil {
		t.Fatalf("uploaded archive not stored: %v", err)
	}
	for _, c := range [][2]string{{"GET", "/api/backups"}, {"POST", "/api/backups"}, {"DELETE", "/api/backups/up-1"}, {"GET", "/api/backups/up-1/apply"}, {"GET", "/api/backups/up-1/download"}} {
		if w := e.call(c[0], c[1], nil, "", store.User{Type: "user"}); w.Code != http.StatusForbidden {
			t.Fatalf("%s %s as user = %d, want 403", c[0], c[1], w.Code)
		}
	}
	if w := e.call("DELETE", "/api/backups/up-1", nil, "", admin); w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	if ids := e.listIDs(t); len(ids) != 0 {
		t.Fatalf("list after delete = %v", ids)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "good.audiobookshelf")); !os.IsNotExist(err) {
		t.Fatalf("deleted backup's file remains: %v", err)
	}
}

// Uploads that are not valid backups are refused and leave nothing behind:
// not a zip, no database, no version in the details.
func TestUploadRejectsInvalidArchives(t *testing.T) {
	e := newEnv(t, Options{})
	cases := map[string][]byte{
		"notzip.audiobookshelf":    []byte("plain text"),
		"nodb.audiobookshelf":      archiveBytes(t, "x", 1, "2.37.2", true),
		"noversion.audiobookshelf": archiveBytes(t, "x", 1, "", false),
	}
	for name, data := range cases {
		if w := upload(t, e, name, data); w.Code < 400 {
			t.Errorf("%s accepted with %d", name, w.Code)
		}
	}
	if ids := e.listIDs(t); len(ids) != 0 {
		t.Fatalf("invalid uploads were listed: %v", ids)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 0 {
		t.Fatalf("invalid uploads left files: %d", len(entries))
	}
}

// Reload (also run at startup) lists each backup id once, oldest first, and
// ignores files that are not valid backups.
func TestReloadIgnoresDuplicateIDsAndInvalidFiles(t *testing.T) {
	e := newEnv(t, Options{})
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(e.dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.audiobookshelf", archiveBytes(t, "later", 2000, "2.37.2", false))
	write("a.audiobookshelf", archiveBytes(t, "earlier", 1000, "2.37.2", false))
	write("a-copy.audiobookshelf", archiveBytes(t, "earlier", 1000, "2.37.2", false))
	write("junk.audiobookshelf", []byte("junk"))
	write("readme.txt", []byte("not a backup"))
	if err := e.h.Reload(); err != nil {
		t.Fatal(err)
	}
	ids := e.listIDs(t)
	if len(ids) != 2 || ids[0] != "earlier" || ids[1] != "later" {
		t.Fatalf("list = %v, want [earlier later]", ids)
	}
}

// PATCH /api/backups/path saves the new path through the hook, moves the
// handler to it (listing that directory's backups) and creates the directory;
// an empty body is refused, an unchanged path does nothing, and a failing
// hook leaves the old path in force.
func TestUpdatePathRoute(t *testing.T) {
	var saved []string
	var failSave bool
	e := newEnv(t, Options{UpdatePath: func(_ context.Context, p string) error {
		if failSave {
			return errors.New("settings write failed")
		}
		saved = append(saved, p)
		return nil
	}})
	newDir := filepath.Join(filepath.Dir(e.dir), "elsewhere")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "x.audiobookshelf"), archiveBytes(t, "there", 1, "2.37.2", false), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := func(path string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"path": path})
		return e.call("PATCH", "/api/backups/path", body, "application/json", admin)
	}
	if w := patch(""); w.Code != 400 {
		t.Fatalf("empty path = %d", w.Code)
	}
	failSave = true
	if w := patch(newDir); w.Code != 500 {
		t.Fatalf("failing hook = %d", w.Code)
	}
	if got := e.h.options().BackupPath; got != e.dir {
		t.Fatalf("failed update changed the path to %s", got)
	}
	failSave = false
	if w := patch(newDir); w.Code != 200 {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	if len(saved) != 1 || saved[0] != newDir || e.h.options().BackupPath != newDir {
		t.Fatalf("saved %v, path %s", saved, e.h.options().BackupPath)
	}
	if ids := e.listIDs(t); len(ids) != 1 || ids[0] != "there" {
		t.Fatalf("list after path change = %v", ids)
	}
	if w := patch(newDir); w.Code != 200 || len(saved) != 1 {
		t.Fatalf("unchanged path: %d, saved %v", w.Code, saved)
	}
}

// The schedule is validated when the service starts or its options change: a
// bad expression is refused and leaves the previous options; a good one is
// accepted, and Close (twice) stops it. Whether the five-field cron fires is
// the cron library's job; the schedule has minute resolution, so a firing
// cannot be observed without waiting a minute.
func TestScheduleIsValidatedAndStoppedByClose(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ConfigPath: filepath.Join(root, "config"), MetadataPath: filepath.Join(root, "metadata")}
	if _, err := New(cfg, Options{BackupPath: filepath.Join(root, "bad"), BackupSchedule: "not a cron"}, nil, nil); err == nil {
		t.Fatal("invalid schedule accepted")
	}
	h, err := New(cfg, Options{BackupPath: filepath.Join(root, "backups"), BackupSchedule: "0 3 * * *"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.UpdateOptions(context.Background(), Options{BackupPath: filepath.Join(root, "backups"), BackupSchedule: "@daily"}); err == nil {
		t.Fatal("descriptor schedule accepted on update")
	}
	if err := h.UpdateOptions(context.Background(), Options{BackupPath: filepath.Join(root, "backups")}); err != nil {
		t.Fatalf("disabling the schedule: %v", err)
	}
	done := make(chan struct{})
	go func() { h.Close(); h.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
}

// Apply hands the extracted database and metadata to the restore hook, then
// announces backup_applied. When the hook fails the answer is an error, no
// backup_applied is announced and the staging area is gone.
func TestApplyRestoreFailureAnnouncesNothing(t *testing.T) {
	var restoreErr error
	var staged string
	e := newEnv(t, Options{Restore: func(_ context.Context, db, items, authors string) error {
		staged = db
		return restoreErr
	}})
	if w := upload(t, e, "r.audiobookshelf", archiveBytes(t, "r-1", 1, "2.37.2", false)); w.Code != 200 {
		t.Fatalf("upload = %d", w.Code)
	}
	restoreErr = errors.New("restore failed")
	if w := e.call("GET", "/api/backups/r-1/apply", nil, "", admin); w.Code != 500 {
		t.Fatalf("failed restore = %d", w.Code)
	}
	e.mu.Lock()
	n := len(e.events)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("events after failed restore: %v", e.events)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged database left behind: %v", err)
	}
	restoreErr = nil
	if w := e.call("GET", "/api/backups/r-1/apply", nil, "", admin); w.Code != 200 {
		t.Fatalf("restore = %d %s", w.Code, w.Body)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.events) != 1 || e.events[0] != "backup_applied" {
		t.Fatalf("events = %v", e.events)
	}
}
