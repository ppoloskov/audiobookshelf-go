// Package backup implements the v2.37.2 .audiobookshelf archive format and
// administrative backup routes. It does not migrate or initialize databases.
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	"github.com/robfig/cron/v3"
)

const serverVersion = "2.37.2"
const maxRestoreUncompressedBytes = 20 << 30

type EmitFunc func(userID, event string, payload any)
type RestoreFunc func(context.Context, string, string, string) error
type PathUpdateFunc func(context.Context, string) error
type NotificationFunc func(context.Context, string, map[string]any) error

// Options contains the server-side hooks unavailable to an isolated handler.
// Restore must close/reopen the database around replacement and reset caches.
type Options struct {
	BackupPath       string
	BackupPathEnvSet bool
	BackupSchedule   string
	BackupsToKeep    int
	MaxBackupSizeGB  float64
	Restore          RestoreFunc
	UpdatePath       PathUpdateFunc
}

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	opt      Options
	emitFunc EmitFunc
	mu       sync.Mutex
	backups  []Backup
	cron     *cron.Cron
	notify   NotificationFunc
}

// Backup uses the JSON field names emitted by the upstream Backup.toJSON().
type Backup struct {
	ID            string `json:"id"`
	Key           any    `json:"key"`
	BackupDirPath string `json:"backupDirPath"`
	DatePretty    string `json:"datePretty"`
	FullPath      string `json:"fullPath"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	FileSize      int64  `json:"fileSize"`
	CreatedAt     int64  `json:"createdAt"`
	ServerVersion string `json:"serverVersion"`
}

func New(db *sql.DB, cfg config.Config, opt Options, emit EmitFunc) (*Handler, error) {
	if opt.BackupPath == "" {
		opt.BackupPath = filepath.Join(cfg.MetadataPath, "backups")
	}
	if opt.BackupsToKeep <= 0 {
		opt.BackupsToKeep = 2
	}
	if emit == nil {
		emit = func(string, string, any) {}
	}
	h := &Handler{db: db, cfg: cfg, opt: opt, emitFunc: emit}
	if err := os.MkdirAll(opt.BackupPath, 0755); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	if err := h.Reload(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.BackupSchedule) != "" {
		h.cron = cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)))
		if _, err := h.cron.AddFunc(opt.BackupSchedule, func() { _, _ = h.Create(context.Background()) }); err != nil {
			return nil, fmt.Errorf("invalid backup schedule %q: %w", opt.BackupSchedule, err)
		}
		h.cron.Start()
	}
	return h, nil
}

func (h *Handler) Close() {
	h.mu.Lock()
	c := h.cron
	h.cron = nil
	h.mu.Unlock()
	if c != nil {
		ctx := c.Stop()
		<-ctx.Done()
	}
}

// SetNotificationHandler wires runtime notification delivery for backup
// lifecycle events without coupling this package to notification storage.
func (h *Handler) SetNotificationHandler(fn NotificationFunc) {
	h.mu.Lock()
	h.notify = fn
	h.mu.Unlock()
}

func (h *Handler) notifyEvent(ctx context.Context, name string, data map[string]any) {
	h.mu.Lock()
	fn := h.notify
	h.mu.Unlock()
	if fn != nil {
		_ = fn(ctx, name, data)
	}
}

// UpdateOptions applies already-persisted server backup settings. It refreshes
// backup discovery and reschedules cron work without replacing runtime sockets.
func (h *Handler) UpdateOptions(ctx context.Context, opt Options) error {
	if strings.TrimSpace(opt.BackupPath) == "" {
		opt.BackupPath = filepath.Join(h.cfg.MetadataPath, "backups")
	}
	if opt.BackupsToKeep <= 0 {
		opt.BackupsToKeep = 2
	}
	if err := os.MkdirAll(opt.BackupPath, 0755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	h.mu.Lock()
	oldCron := h.cron
	h.cron = nil
	h.opt = opt
	h.mu.Unlock()
	if oldCron != nil {
		stopped := oldCron.Stop()
		<-stopped.Done()
	}
	if err := h.Reload(); err != nil {
		return err
	}
	if strings.TrimSpace(opt.BackupSchedule) == "" {
		return nil
	}
	c := cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)))
	if _, err := c.AddFunc(opt.BackupSchedule, func() { _, _ = h.Create(context.Background()) }); err != nil {
		return fmt.Errorf("invalid backup schedule %q: %w", opt.BackupSchedule, err)
	}
	c.Start()
	h.mu.Lock()
	h.cron = c
	h.mu.Unlock()
	return nil
}

func (h *Handler) Reload() error {
	opt := h.options()
	entries, err := os.ReadDir(opt.BackupPath)
	if err != nil {
		return err
	}
	var loaded []Backup
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".audiobookshelf") {
			continue
		}
		b, err := readBackup(filepath.Join(opt.BackupPath, e.Name()))
		if err != nil {
			continue
		}
		if seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		loaded = append(loaded, b)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].CreatedAt < loaded[j].CreatedAt })
	h.mu.Lock()
	h.backups = loaded
	h.mu.Unlock()
	return nil
}

// Handle handles /api/backups routes and returns whether it owned the route.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, u store.User) bool {
	p := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api"), "/")
	if p == "" {
		p = "/"
	}
	if p != "/backups" && !strings.HasPrefix(p, "/backups/") {
		return false
	}
	if u.Type != "admin" && u.Type != "root" {
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	if p == "/backups" && r.Method == http.MethodGet {
		opt := h.options()
		writeJSON(w, 200, map[string]any{"backups": h.list(), "backupLocation": opt.BackupPath, "backupPathEnvSet": opt.BackupPathEnvSet})
		return true
	}
	if p == "/backups" && r.Method == http.MethodPost {
		if _, err := h.Create(r.Context()); err != nil {
			http.Error(w, "backup failed", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"backups": h.list()})
		return true
	}
	if p == "/backups/upload" && r.Method == http.MethodPost {
		h.upload(w, r)
		return true
	}
	if p == "/backups/path" && r.Method == http.MethodPatch {
		h.updatePath(w, r)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/backups/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		return false
	}
	b, ok := h.find(parts[0])
	if !ok {
		sendStatus(w, http.StatusNotFound)
		return true
	}
	if len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet {
		w.Header().Set("Content-Disposition", "attachment; filename="+b.Filename)
		http.ServeFile(w, r, b.FullPath)
		return true
	}
	if len(parts) == 2 && parts[1] == "apply" && r.Method == http.MethodGet {
		h.apply(w, r, b)
		return true
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		h.delete(w, b)
		return true
	}
	return false
}

func (h *Handler) Create(ctx context.Context) (Backup, error) {
	before := h.list()
	backup, err := h.create(ctx)
	if err != nil {
		h.notifyEvent(ctx, "onBackupFailed", map[string]any{"errorMsg": err.Error()})
		return Backup{}, err
	}
	backups := h.list()
	h.notifyEvent(ctx, "onBackupCompleted", map[string]any{"completionTime": backup.CreatedAt, "backupPath": backup.FullPath, "backupSize": backup.FileSize, "backupCount": len(backups), "removedOldest": len(before) >= h.options().BackupsToKeep})
	return backup, nil
}

func (h *Handler) create(ctx context.Context) (Backup, error) {
	opt := h.options()
	if h.db == nil {
		return Backup{}, errors.New("database is unavailable")
	}
	now := time.Now()
	id := now.Format("2006-01-02T1504")
	dir := opt.BackupPath
	backup := Backup{ID: id, Key: "sqlite", BackupDirPath: dir, DatePretty: now.Format("Mon, Jan 2 2006 15:04"), Filename: id + ".audiobookshelf", FullPath: filepath.Join(dir, id+".audiobookshelf"), Path: filepath.Join("backups", id+".audiobookshelf"), CreatedAt: now.UnixMilli(), ServerVersion: serverVersion}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Backup{}, err
	}
	tmpDB := filepath.Join(h.cfg.ConfigPath, "absdatabase."+id+".sqlite")
	_ = os.Remove(tmpDB)
	// VACUUM INTO produces a consistent SQLite snapshot, including committed WAL
	// contents, without copying a potentially changing main database file.
	copyDB, err := sql.Open("sqlite", "file:"+filepath.Join(h.cfg.ConfigPath, "absdatabase.sqlite")+"?mode=ro")
	if err != nil {
		return Backup{}, err
	}
	copyDB.SetMaxOpenConns(1)
	_, err = copyDB.ExecContext(ctx, "VACUUM INTO ?", tmpDB)
	closeErr := copyDB.Close()
	if err != nil {
		return Backup{}, fmt.Errorf("snapshot database: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpDB)
		return Backup{}, closeErr
	}
	defer os.Remove(tmpDB)
	if err = writeArchive(backup.FullPath, tmpDB, filepath.Join(h.cfg.MetadataPath, "items"), filepath.Join(h.cfg.MetadataPath, "authors"), details(backup)); err != nil {
		return Backup{}, err
	}
	st, err := os.Stat(backup.FullPath)
	if err != nil {
		return Backup{}, err
	}
	backup.FileSize = st.Size()
	if opt.MaxBackupSizeGB > 0 {
		limit := int64(opt.MaxBackupSizeGB * 1_000_000_000)
		processed, sizeErr := treeBytes(tmpDB, filepath.Join(h.cfg.MetadataPath, "items"), filepath.Join(h.cfg.MetadataPath, "authors"))
		if sizeErr != nil {
			_ = os.Remove(backup.FullPath)
			return Backup{}, sizeErr
		}
		if processed > limit {
			_ = os.Remove(backup.FullPath)
			return Backup{}, fmt.Errorf("backup exceeds configured maximum size")
		}
	}
	h.mu.Lock()
	replaced := false
	for i, prior := range h.backups {
		if prior.ID == backup.ID {
			h.backups[i] = backup
			replaced = true
			break
		}
	}
	if !replaced {
		h.backups = append(h.backups, backup)
	}
	sort.Slice(h.backups, func(i, j int) bool { return h.backups[i].CreatedAt < h.backups[j].CreatedAt })
	excess := len(h.backups) - opt.BackupsToKeep
	expired := append([]Backup(nil), h.backups[:max(0, excess)]...)
	h.backups = h.backups[max(0, excess):]
	h.mu.Unlock()
	for _, old := range expired {
		_ = os.Remove(old.FullPath)
	}
	return backup, nil
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	opt := h.options()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Invalid backup file", 500)
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Invalid backup file", 500)
		return
	}
	defer f.Close()
	if filepath.Ext(header.Filename) != ".audiobookshelf" {
		sendBody(w, 500, "Invalid backup file")
		return
	}
	name := filepath.Base(header.Filename)
	dest := filepath.Join(opt.BackupPath, name)
	tmp, err := os.CreateTemp(opt.BackupPath, "upload-*.tmp")
	if err != nil {
		http.Error(w, "Failed to move backup file into backups directory", 500)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, f); err != nil {
		tmp.Close()
		http.Error(w, "Failed to move backup file into backups directory", 500)
		return
	}
	if err = tmp.Close(); err != nil {
		http.Error(w, "Failed to move backup file into backups directory", 500)
		return
	}
	b, err := readBackup(tmpName)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	b.Filename = name
	b.FullPath = dest
	b.BackupDirPath = opt.BackupPath
	b.Path = filepath.Join("backups", name)
	if err = os.Rename(tmpName, dest); err != nil {
		http.Error(w, "Failed to move backup file into backups directory", 500)
		return
	}
	if st, e := os.Stat(dest); e == nil {
		b.FileSize = st.Size()
	}
	h.mu.Lock()
	replaced := false
	for i, x := range h.backups {
		if x.ID == b.ID {
			h.backups[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		h.backups = append(h.backups, b)
	}
	h.mu.Unlock()
	writeJSON(w, 200, map[string]any{"backups": h.list()})
}

func (h *Handler) updatePath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || strings.TrimSpace(body.Path) == "" {
		http.Error(w, "Invalid request body. Must include path.", 400)
		return
	}
	path, err := filepath.Abs(filepath.Clean(body.Path))
	if err != nil {
		http.Error(w, "Invalid path", 400)
		return
	}
	opt := h.options()
	if path == opt.BackupPath {
		_, _ = io.WriteString(w, "Backup path unchanged")
		return
	}
	if err = os.MkdirAll(path, 0755); err != nil {
		http.Error(w, "Invalid backup path \""+body.Path+"\"", 400)
		return
	}
	if opt.UpdatePath == nil {
		http.Error(w, "backup path updates are unavailable", 501)
		return
	}
	if err = opt.UpdatePath(r.Context(), path); err != nil {
		http.Error(w, "failed to save backup path", 500)
		return
	}
	opt.BackupPath = path
	if err = h.UpdateOptions(r.Context(), opt); err != nil {
		http.Error(w, "failed to reload backup path", 500)
		return
	}
	writeJSON(w, 200, "OK")
}

func (h *Handler) delete(w http.ResponseWriter, b Backup) {
	_ = os.Remove(b.FullPath)
	h.mu.Lock()
	next := h.backups[:0]
	for _, x := range h.backups {
		if x.ID != b.ID {
			next = append(next, x)
		}
	}
	h.backups = next
	h.mu.Unlock()
	writeJSON(w, 200, map[string]any{"backups": h.list()})
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request, b Backup) {
	opt := h.options()
	if opt.Restore == nil {
		http.Error(w, "backup restore is unavailable", 501)
		return
	}
	zipr, err := zip.OpenReader(b.FullPath)
	if err != nil {
		http.Error(w, "Invalid backup file", 500)
		return
	}
	defer zipr.Close()
	stage, err := os.MkdirTemp(h.cfg.ConfigPath, "backup-restore-")
	if err != nil {
		http.Error(w, "Failed to prepare restore", 500)
		return
	}
	defer os.RemoveAll(stage)
	var dbTemp string
	items := filepath.Join(stage, "metadata-items")
	authors := filepath.Join(stage, "metadata-authors")
	if err = os.MkdirAll(items, 0755); err != nil {
		http.Error(w, "Failed to prepare restore", 500)
		return
	}
	if err = os.MkdirAll(authors, 0755); err != nil {
		http.Error(w, "Failed to prepare restore", 500)
		return
	}
	var total uint64
	for _, f := range zipr.File {
		if f.Name == "absdatabase.sqlite" {
			dbTemp = filepath.Join(h.cfg.ConfigPath, "absdatabase-temp.sqlite")
			if total+f.UncompressedSize64 > maxRestoreUncompressedBytes {
				http.Error(w, "Backup contents exceed extraction limit", 400)
				return
			}
			total += f.UncompressedSize64
			if err = extractEntry(f, dbTemp); err != nil {
				http.Error(w, "Failed to extract sqlite db from backup", 500)
				return
			}
			continue
		}
		var targetRoot, prefix string
		switch {
		case f.Name == "metadata-items" || f.Name == "metadata-items/":
			targetRoot, prefix = items, "metadata-items"
		case strings.HasPrefix(f.Name, "metadata-items/"):
			targetRoot, prefix = items, "metadata-items/"
		case f.Name == "metadata-authors" || f.Name == "metadata-authors/":
			targetRoot, prefix = authors, "metadata-authors"
		case strings.HasPrefix(f.Name, "metadata-authors/"):
			targetRoot, prefix = authors, "metadata-authors/"
		default:
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			http.Error(w, "Backup contains a symbolic link", 400)
			return
		}
		if total+f.UncompressedSize64 > maxRestoreUncompressedBytes {
			http.Error(w, "Backup contents exceed extraction limit", 400)
			return
		}
		total += f.UncompressedSize64
		rel := strings.TrimPrefix(strings.TrimSuffix(f.Name, "/"), strings.TrimSuffix(prefix, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			http.Error(w, "Backup contains an invalid path", 400)
			return
		}
		out := filepath.Join(targetRoot, clean)
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(out, 0755); err != nil {
				http.Error(w, "Failed to extract backup metadata", 500)
				return
			}
		} else if err = extractEntry(f, out); err != nil {
			http.Error(w, "Failed to extract backup metadata", 500)
			return
		}
	}
	if dbTemp == "" {
		http.Error(w, "Invalid backup file. Does not include absdatabase.sqlite. This might be from an older Audiobookshelf server.", 500)
		return
	}
	defer os.Remove(dbTemp)
	if err = opt.Restore(r.Context(), dbTemp, items, authors); err != nil {
		http.Error(w, "Failed to apply backup", 500)
		return
	}
	w.WriteHeader(200)
	h.emit("*", "backup_applied", nil)
}

func (h *Handler) list() []Backup {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]Backup(nil), h.backups...)
	return out
}
func (h *Handler) options() Options { h.mu.Lock(); defer h.mu.Unlock(); return h.opt }
func (h *Handler) find(id string) (Backup, bool) {
	for _, b := range h.list() {
		if b.ID == id {
			return b, true
		}
	}
	return Backup{}, false
}

func readBackup(path string) (Backup, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return Backup{}, fmt.Errorf("Failed to read backup file - backup might not be a valid .zip file")
	}
	defer z.Close()
	var raw []byte
	hasDB := false
	for _, f := range z.File {
		if f.Name == "absdatabase.sqlite" {
			hasDB = true
		}
		if f.Name == "details" {
			if f.UncompressedSize64 > 1<<20 {
				return Backup{}, errors.New("Invalid backup file - details entry too large")
			}
			r, e := f.Open()
			if e != nil {
				return Backup{}, e
			}
			raw, e = io.ReadAll(io.LimitReader(r, (1<<20)+1))
			_ = r.Close()
			if e != nil {
				return Backup{}, e
			}
		}
	}
	if !hasDB {
		return Backup{}, errors.New("Invalid backup with no absdatabase.sqlite file - might be a backup created on an old Audiobookshelf server.")
	}
	if raw == nil {
		return Backup{}, errors.New("Invalid backup file - missing details entry")
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[3]) == "" {
		return Backup{}, errors.New("Invalid backup. Might be from before version 2.0.0.")
	}
	created, _ := strconv.ParseInt(strings.TrimSpace(lines[2]), 10, 64)
	key := strings.TrimSpace(lines[1])
	var keyValue any = key
	if key == "1" {
		keyValue = nil
	}
	fi, _ := os.Stat(path)
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	name := filepath.Base(path)
	dir := filepath.Dir(path)
	return Backup{ID: strings.TrimSpace(lines[0]), Key: keyValue, BackupDirPath: dir, DatePretty: time.UnixMilli(created).Format("Mon, Jan 2 2006 15:04"), FullPath: path, Path: filepath.Join("backups", name), Filename: name, FileSize: size, CreatedAt: created, ServerVersion: strings.TrimSpace(lines[3])}, nil
}

func details(b Backup) []byte {
	key := ""
	if value, ok := b.Key.(string); ok {
		key = value
	}
	return []byte(fmt.Sprintf("%s\n%s\n%d\n%s", b.ID, key, b.CreatedAt, b.ServerVersion))
}

func writeArchive(path, dbPath, itemsDir, authorsDir string, details []byte) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	failed := func(e error) error { _ = zw.Close(); _ = out.Close(); _ = os.Remove(path); return e }
	if err = addFile(zw, dbPath, "absdatabase.sqlite"); err != nil {
		return failed(err)
	}
	for _, pair := range [][2]string{{itemsDir, "metadata-items"}, {authorsDir, "metadata-authors"}} {
		if err = addDir(zw, pair[0], pair[1]); err != nil {
			return failed(err)
		}
	}
	w, err := zw.Create("details")
	if err != nil {
		return failed(err)
	}
	if _, err = w.Write(details); err != nil {
		return failed(err)
	}
	if err = zw.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
func addFile(zw *zip.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}
func addDir(zw *zip.Writer, root, name string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		entry := name + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			entry += "/"
			_, err = zw.Create(entry)
			return err
		}
		return addFile(zw, p, entry)
	})
}

func treeBytes(paths ...string) (int64, error) {
	var total int64
	for _, root := range paths {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return 0, err
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			return nil
		}); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func extractEntry(f *zip.File, dest string) error {
	if f.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic link entries are not accepted")
	}
	if f.UncompressedSize64 > maxRestoreUncompressedBytes {
		return errors.New("archive entry exceeds extraction limit")
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, e1 := io.CopyN(out, r, int64(f.UncompressedSize64))
	e2 := out.Sync()
	e3 := out.Close()
	if e1 != nil {
		return e1
	}
	if uint64(n) != f.UncompressedSize64 {
		return errors.New("archive entry size mismatch")
	}
	if e2 != nil {
		return e2
	}
	return e3
}

func (h *Handler) emit(user, event string, payload any) { h.emitFunc(user, event, payload) }
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// sendStatus mirrors Express res.sendStatus: status text, text/plain, no newline.
func sendStatus(w http.ResponseWriter, status int) {
	body := http.StatusText(status)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	sum := sha1.Sum([]byte(body))
	w.Header().Set("ETag", fmt.Sprintf("W/\"%x-%s\"", len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// sendBody mirrors Express res.send for a text response, including its ETag.
func sendBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	sum := sha1.Sum([]byte(body))
	w.Header().Set("ETag", fmt.Sprintf("W/\"%x-%s\"", len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
