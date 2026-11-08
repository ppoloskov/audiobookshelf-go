// Package backup implements the v2.37.2 .audiobookshelf archive format and
// administrative backup routes. It does not migrate or initialize databases.
package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/version"
	"github.com/robfig/cron/v3"
)

const maxRestoreUncompressedBytes = 20 << 30

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
	db      *sql.DB
	cfg     config.Config
	opt     Options
	emit    events.Emitter
	mu      sync.Mutex
	backups []Backup
	cron    *cron.Cron
	notify  NotificationFunc
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

// withDefaults fills the backup directory and retention count a settings
// document leaves out.
func (h *Handler) withDefaults(opt Options) Options {
	if strings.TrimSpace(opt.BackupPath) == "" {
		opt.BackupPath = filepath.Join(h.cfg.MetadataPath, "backups")
	}
	if opt.BackupsToKeep <= 0 {
		opt.BackupsToKeep = 2
	}
	return opt
}

// schedule starts the automatic backup cron for spec; a blank spec means
// automatic backups are off and returns no cron.
func (h *Handler) schedule(spec string) (*cron.Cron, error) {
	if strings.TrimSpace(spec) == "" {
		logging.Info("[BackupManager] Auto Backups are disabled") // BackupManager.js:77
		return nil, nil
	}
	c := cronspec.New()
	if _, err := c.AddFunc(spec, func() { _, _ = h.Create(context.Background()) }); err != nil {
		return nil, fmt.Errorf("invalid backup schedule %q: %w", spec, err)
	}
	c.Start()
	logging.Infof("[BackupManager] Starting backup schedule %s", spec) // BackupManager.js:94
	return c, nil
}

func New(db *sql.DB, cfg config.Config, opt Options, emit events.Emitter) (*Handler, error) {
	if emit == nil {
		emit = func(string, string, any) {}
	}
	h := &Handler{db: db, cfg: cfg, emit: emit}
	h.opt = h.withDefaults(opt)
	if err := os.MkdirAll(h.opt.BackupPath, 0755); err != nil {
		return nil, fmt.Errorf("create backup directory: %w", err)
	}
	if err := h.Reload(); err != nil {
		return nil, err
	}
	var err error
	if h.cron, err = h.schedule(h.opt.BackupSchedule); err != nil {
		return nil, err
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
	opt = h.withDefaults(opt)
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
	c, err := h.schedule(opt.BackupSchedule)
	if err != nil {
		return err
	}
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
			logging.Warnf("[BackupManager] Backup %q could not be loaded and was ignored: %v", e.Name(), err) // upstream warns only for pre-sqlite backups (BackupManager.js:312)
			continue
		}
		if seen[b.ID] {
			logging.Warnf("[BackupManager] Backup already loaded with id %s - ignoring", b.ID) // BackupManager.js:318
			continue
		}
		seen[b.ID] = true
		loaded = append(loaded, b)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].CreatedAt < loaded[j].CreatedAt })
	h.mu.Lock()
	h.backups = loaded
	h.mu.Unlock()
	logging.Infof("[BackupManager] %d Backups Found", len(loaded)) // BackupManager.js:327
	return nil
}

// Routes registers the backup routes on mux. The server attaches the user to
// the request context before dispatch (store.ContextWithUser). Every route
// answers 403 to a user who is not an admin, before it looks at the backup.
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(fn httputil.Handler) httputil.Handler { return access.RequireAdmin(http.StatusForbidden, fn) }
	// onBackup resolves {id} first; an unknown backup answers 404.
	onBackup := func(fn func(http.ResponseWriter, *http.Request, Backup) error) httputil.Handler {
		return admin(func(w http.ResponseWriter, r *http.Request) error {
			b, ok := h.find(r.PathValue("id"))
			if !ok {
				return httputil.Status(http.StatusNotFound, "")
			}
			return fn(w, r, b)
		})
	}
	mux.Handle("GET /api/backups", admin(h.listRoute))
	mux.Handle("POST /api/backups", admin(h.createRoute))
	mux.Handle("POST /api/backups/upload", admin(h.upload))
	mux.Handle("PATCH /api/backups/path", admin(h.updatePath))
	mux.Handle("GET /api/backups/{id}/download", onBackup(func(w http.ResponseWriter, r *http.Request, b Backup) error {
		w.Header().Set("Content-Disposition", "attachment; filename="+b.Filename)
		http.ServeFile(w, r, b.FullPath)
		return nil
	}))
	mux.Handle("GET /api/backups/{id}/apply", onBackup(h.apply))
	mux.Handle("DELETE /api/backups/{id}", onBackup(func(w http.ResponseWriter, r *http.Request, b Backup) error {
		h.remove(b)
		return h.writeList(w, r)
	}))
}

func (h *Handler) writeList(w http.ResponseWriter, r *http.Request) error {
	return httputil.JSON(w, r, map[string]any{"backups": h.list()})
}

func (h *Handler) listRoute(w http.ResponseWriter, r *http.Request) error {
	opt := h.options()
	return httputil.JSON(w, r, map[string]any{"backups": h.list(), "backupLocation": opt.BackupPath, "backupPathEnvSet": opt.BackupPathEnvSet})
}

func (h *Handler) createRoute(w http.ResponseWriter, r *http.Request) error {
	if _, err := h.Create(r.Context()); err != nil {
		return httputil.Internal("backup failed", err)
	}
	return h.writeList(w, r)
}

func (h *Handler) Create(ctx context.Context) (Backup, error) {
	before := h.list()
	logging.Info("[BackupManager] Running Backup") // BackupManager.js:335
	backup, err := h.create(ctx)
	if err != nil {
		logging.Errorf("[BackupManager] Backup Failed %v", err) // BackupManager.js:356
		h.notifyEvent(ctx, "onBackupFailed", map[string]any{"errorMsg": err.Error()})
		return Backup{}, err
	}
	logging.Infof("[BackupManager] Backup successful %s", backup.ID) // BackupManager.js:366
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
	backup := Backup{ID: id, Key: "sqlite", BackupDirPath: dir, DatePretty: now.Format("Mon, Jan 2 2006 15:04"), Filename: id + ".audiobookshelf", FullPath: filepath.Join(dir, id+".audiobookshelf"), Path: filepath.Join("backups", id+".audiobookshelf"), CreatedAt: now.UnixMilli(), ServerVersion: version.Server}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Backup{}, err
	}
	tmpDB := filepath.Join(h.cfg.ConfigPath, "absdatabase."+id+".sqlite")
	_ = os.Remove(tmpDB)
	// VACUUM INTO produces a consistent SQLite snapshot, including committed WAL
	// contents, without copying a potentially changing main database file.
	copyDB, err := store.OpenUnchecked(store.DatabasePath(h.cfg.ConfigPath))
	if err != nil {
		return Backup{}, err
	}
	_, err = copyDB.DB().ExecContext(ctx, "VACUUM INTO ?", tmpDB)
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
		if sizeErr == nil && processed > limit {
			sizeErr = fmt.Errorf("backup exceeds configured maximum size")
		}
		if sizeErr != nil {
			_ = os.Remove(backup.FullPath)
			return Backup{}, sizeErr
		}
	}
	for _, old := range h.put(backup, opt.BackupsToKeep) {
		if err := os.Remove(old.FullPath); err != nil {
			logging.Errorf("[BackupManager] Failed to remove backup: %v", err) // BackupManager.js:400
		} else {
			logging.Infof("[BackupManager] Backup %q Removed", old.ID) // BackupManager.js:398
		}
	}
	return backup, nil
}

// put adds b to the list (replacing a backup with the same id), keeps the list
// ordered by creation time and returns the oldest backups beyond keep (all
// when keep is not positive means none are dropped).
func (h *Handler) put(b Backup, keep int) (expired []Backup) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := slices.IndexFunc(h.backups, func(x Backup) bool { return x.ID == b.ID }); i >= 0 {
		h.backups[i] = b
	} else {
		h.backups = append(h.backups, b)
	}
	sort.Slice(h.backups, func(i, j int) bool { return h.backups[i].CreatedAt < h.backups[j].CreatedAt })
	if excess := len(h.backups) - keep; keep > 0 && excess > 0 {
		expired = append(expired, h.backups[:excess]...)
		h.backups = h.backups[excess:]
	}
	return expired
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) error {
	opt := h.options()
	invalid := func(err error) error { return httputil.Internal("Invalid backup file", err) }
	moveFailed := func(err error) error {
		return httputil.Internal("Failed to move backup file into backups directory", err)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return invalid(err)
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		return invalid(err)
	}
	defer f.Close()
	if filepath.Ext(header.Filename) != ".audiobookshelf" {
		return invalid(errors.New("uploaded file " + header.Filename + " does not end in .audiobookshelf"))
	}
	name := filepath.Base(header.Filename)
	dest := filepath.Join(opt.BackupPath, name)
	tmp, err := os.CreateTemp(opt.BackupPath, "upload-*.tmp")
	if err != nil {
		return moveFailed(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, err = io.Copy(tmp, f)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return moveFailed(err)
	}
	b, err := readBackup(tmpName)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, err.Error())
	}
	b.Filename = name
	b.FullPath = dest
	b.BackupDirPath = opt.BackupPath
	b.Path = filepath.Join("backups", name)
	if err = os.Rename(tmpName, dest); err != nil {
		return moveFailed(err)
	}
	if st, e := os.Stat(dest); e == nil {
		b.FileSize = st.Size()
	}
	h.put(b, 0)
	return h.writeList(w, r)
}

func (h *Handler) updatePath(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || strings.TrimSpace(body.Path) == "" {
		return httputil.Status(http.StatusBadRequest, "Invalid request body. Must include path.")
	}
	path, err := filepath.Abs(filepath.Clean(body.Path))
	if err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid path")
	}
	opt := h.options()
	if path == opt.BackupPath {
		httputil.SendText(w, http.StatusOK, "Backup path unchanged", httputil.ForRequest(r))
		return nil
	}
	if err = os.MkdirAll(path, 0755); err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid backup path \""+body.Path+"\"")
	}
	logging.Infof("[BackupController] Updating backup path to %q from %q", path, opt.BackupPath) // BackupController.js:101
	if opt.BackupPathEnvSet {
		logging.Warn("[BackupController] Backup path is set in environment variable BACKUP_PATH. Backup path will be reverted on server restart.") // BackupController.js:105
	}
	if err = opt.UpdatePath(r.Context(), path); err != nil {
		return httputil.Internal("failed to save backup path", err)
	}
	opt.BackupPath = path
	if err = h.UpdateOptions(r.Context(), opt); err != nil {
		return httputil.Internal("failed to reload backup path", err)
	}
	return httputil.JSON(w, r, "OK")
}

// remove deletes the archive and then forgets the backup; a backup whose file
// cannot be removed stays listed (BackupManager.removeBackup, js:393-401).
func (h *Handler) remove(b Backup) {
	if err := os.Remove(b.FullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.Errorf("[BackupManager] Failed to remove backup: %v", err) // BackupManager.js:400
		return
	}
	h.mu.Lock()
	h.backups = slices.DeleteFunc(h.backups, func(x Backup) bool { return x.ID == b.ID })
	h.mu.Unlock()
	logging.Infof("[BackupManager] Backup %q Removed", b.ID) // BackupManager.js:398
}

// apply extracts the archive to a staging directory and hands it to
// opt.Restore, which replaces the database and metadata (BackupManager.js:201).
func (h *Handler) apply(w http.ResponseWriter, r *http.Request, b Backup) error {
	opt := h.options()
	logging.Infof("[BackupManager] Applying backup at %q", b.FullPath) // BackupManager.js:201
	zipr, err := zip.OpenReader(b.FullPath)
	if err != nil {
		return httputil.Internal("Invalid backup file", err)
	}
	defer zipr.Close()
	stage, err := os.MkdirTemp(h.cfg.ConfigPath, "backup-restore-")
	if err != nil {
		return httputil.Internal("Failed to prepare restore", err)
	}
	defer os.RemoveAll(stage)
	items := filepath.Join(stage, "metadata-items")
	authors := filepath.Join(stage, "metadata-authors")
	for _, dir := range []string{items, authors} {
		if err = os.MkdirAll(dir, 0755); err != nil {
			return httputil.Internal("Failed to prepare restore", err)
		}
	}
	var dbTemp string
	var total uint64
	for _, f := range zipr.File {
		if f.Name == "absdatabase.sqlite" {
			dbTemp = filepath.Join(h.cfg.ConfigPath, "absdatabase-temp.sqlite")
			if total+f.UncompressedSize64 > maxRestoreUncompressedBytes {
				return httputil.Status(http.StatusBadRequest, "Backup contents exceed extraction limit")
			}
			total += f.UncompressedSize64
			if err = extractEntry(f, dbTemp); err != nil {
				return httputil.Internal("Failed to extract sqlite db from backup", err)
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
			return httputil.Status(http.StatusBadRequest, "Backup contains a symbolic link")
		}
		if total+f.UncompressedSize64 > maxRestoreUncompressedBytes {
			return httputil.Status(http.StatusBadRequest, "Backup contents exceed extraction limit")
		}
		total += f.UncompressedSize64
		rel := strings.TrimPrefix(strings.TrimSuffix(f.Name, "/"), strings.TrimSuffix(prefix, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return httputil.Status(http.StatusBadRequest, "Backup contains an invalid path")
		}
		out := filepath.Join(targetRoot, clean)
		if f.FileInfo().IsDir() {
			err = os.MkdirAll(out, 0755)
		} else {
			err = extractEntry(f, out)
		}
		if err != nil {
			return httputil.Internal("Failed to extract backup metadata", err)
		}
	}
	if dbTemp == "" {
		return &httputil.Error{Status: http.StatusInternalServerError, Message: "Invalid backup file. Does not include absdatabase.sqlite. This might be from an older Audiobookshelf server.", Cause: errors.New("the archive does not include absdatabase.sqlite")}
	}
	defer os.Remove(dbTemp)
	if err = opt.Restore(r.Context(), dbTemp, items, authors); err != nil {
		return httputil.Internal("Failed to apply backup", err)
	}
	if err = httputil.OK(w, r); err != nil {
		return err
	}
	h.emit("*", "backup_applied", nil) // BackupManager.js:263-266: sendStatus(200), then the event
	return nil
}

func (h *Handler) list() []Backup {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Non-nil even when empty: the response is a JSON array, never null.
	out := make([]Backup, len(h.backups))
	copy(out, h.backups)
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
