// Package backup implements the v2.37.2 .audiobookshelf archive format, the
// backup routes and the automatic backup schedule. It does not migrate or
// initialize databases.
package backup

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite" // the snapshot opens its own read-only connection

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/version"
)

// Notify delivers a backup lifecycle event (onBackupCompleted, onBackupFailed)
// to the notification system. Its error is logged, never returned to a client.
type Notify func(ctx context.Context, event string, data map[string]any) error

// Restore replaces the database and the metadata folders with the files staged
// from an archive. The server supplies it: it closes and reopens everything
// around the swap.
type Restore func(ctx context.Context, dbTemp, stagedItems, stagedAuthors string) error

// Handler owns the backup index and the schedule. Build one with New and
// release it with Close.
type Handler struct {
	settings *settings.Store
	cfg      config.Config
	emit     events.Emitter
	getenv   func(string) string
	notify   Notify
	restore  Restore

	mu      sync.Mutex // guards the fields below
	dir     string     // the folder the archives live in
	backups []Backup   // sorted by CreatedAt
	cron    *cron.Cron
}

// New loads the backups in the configured folder and starts the schedule.
// getenv supplies BACKUP_PATH (os.Getenv in production). Later changes of the
// server settings are followed through settings.Store.OnChange. A nil notify or
// emit does nothing; restore is required for the apply route.
func New(store *settings.Store, cfg config.Config, getenv func(string) string, emit events.Emitter, notify Notify, restore Restore) (*Handler, error) {
	h := &Handler{settings: store, cfg: cfg, emit: emit, getenv: getenv, notify: notify, restore: restore}
	if err := h.configure(store.Server()); err != nil {
		return nil, err
	}
	store.OnChange(func(server settings.Server) {
		if err := h.configure(server); err != nil {
			slog.Error("[BackupManager] applying settings failed", "error", err)
		}
	})
	return h, nil
}

// Close stops the schedule, waiting for a running scheduled backup.
func (h *Handler) Close() {
	h.mu.Lock()
	c := h.cron
	h.cron = nil
	h.mu.Unlock()
	if c != nil {
		<-c.Stop().Done()
	}
}

// configure follows the server settings: backup folder (re-read when it
// changes) and schedule (restarted). An invalid schedule is logged and leaves
// automatic backups off.
func (h *Handler) configure(server settings.Server) error {
	dir := cmp.Or(strings.TrimSpace(server.BackupPath), h.cfg.Dirs().Backups)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	h.mu.Lock()
	previous := h.cron
	h.cron, h.dir = nil, dir
	h.mu.Unlock()
	if previous != nil {
		<-previous.Stop().Done()
	}
	if err := h.reload(dir); err != nil {
		return err
	}
	spec := strings.TrimSpace(string(server.BackupSchedule))
	if spec == "" {
		slog.Info("[BackupManager] Auto Backups are disabled") // BackupManager.js:77
		return nil
	}
	c := cronspec.New()
	if _, err := c.AddFunc(spec, func() { _, _ = h.Create(context.Background()) }); err != nil {
		slog.Error("[BackupManager] invalid backup schedule, automatic backups stay off", "schedule", spec, "error", err)
		return nil
	}
	c.Start()
	slog.Info("[BackupManager] Starting backup schedule", "schedule", spec) // BackupManager.js:94
	h.mu.Lock()
	h.cron = c
	h.mu.Unlock()
	return nil
}

// reload indexes the archives in dir. An unreadable archive is skipped.
func (h *Handler) reload(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var loaded []Backup
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), extension) {
			continue
		}
		b, err := readBackup(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("[BackupManager] Backup could not be loaded and was ignored", "file", entry.Name(), "error", err) // upstream warns only for pre-sqlite backups (BackupManager.js:312)
		} else if slices.ContainsFunc(loaded, func(x Backup) bool { return x.ID == b.ID }) {
			slog.Warn("[BackupManager] Backup already loaded with id - ignoring", "id", b.ID) // BackupManager.js:318
		} else {
			loaded = append(loaded, b)
		}
	}
	h.mu.Lock()
	h.backups = loaded
	h.sortLocked()
	h.mu.Unlock()
	slog.Info("[BackupManager] Backups Found", "count", len(loaded)) // BackupManager.js:327
	return nil
}

func (h *Handler) sortLocked() {
	slices.SortFunc(h.backups, func(a, b Backup) int { return cmp.Compare(a.CreatedAt, b.CreatedAt) })
}

// list returns a copy of the index; never nil, because the answer is a JSON array.
func (h *Handler) list() []Backup {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Backup{}, h.backups...)
}

// keep is the number of backups to keep: backupsToKeep, 2 when it is not positive.
func (h *Handler) keep() int {
	if n := h.settings.Server().BackupsToKeep; n > 0 {
		return n
	}
	return 2
}

func (h *Handler) folder() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dir
}

// put adds or replaces b in the index, then drops the oldest backups beyond
// keep (0 keeps all) and returns them.
func (h *Handler) put(b Backup, keep int) (expired []Backup) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := slices.IndexFunc(h.backups, func(x Backup) bool { return x.ID == b.ID }); i >= 0 {
		h.backups[i] = b
	} else {
		h.backups = append(h.backups, b)
	}
	h.sortLocked()
	if excess := len(h.backups) - keep; keep > 0 && excess > 0 {
		expired = slices.Clone(h.backups[:excess])
		h.backups = h.backups[excess:]
	}
	return expired
}

// Create writes a new backup, indexes it, removes backups beyond the retention
// limit and notifies. The archive is a VACUUM INTO snapshot of the database
// plus the item and author metadata folders.
func (h *Handler) Create(ctx context.Context) (Backup, error) {
	before := len(h.list())
	slog.Info("[BackupManager] Running Backup") // BackupManager.js:335
	b, err := h.create(ctx)
	if err != nil {
		slog.Error("[BackupManager] Backup Failed", "error", err) // BackupManager.js:356
		h.notifyEvent(ctx, "onBackupFailed", map[string]any{"errorMsg": err.Error()})
		return Backup{}, err
	}
	slog.Info("[BackupManager] Backup successful", "id", b.ID) // BackupManager.js:366
	var removedOldest any = "false"                            // NotificationManager.js:62: removedOldest || 'false'
	if before >= h.keep() {
		removedOldest = true
	}
	h.notifyEvent(ctx, "onBackupCompleted", map[string]any{"completionTime": b.CreatedAt, "backupPath": b.FullPath, "backupSize": b.FileSize, "backupCount": len(h.list()), "removedOldest": removedOldest})
	return b, nil
}

func (h *Handler) notifyEvent(ctx context.Context, event string, data map[string]any) {
	if h.notify != nil {
		if err := h.notify(ctx, event, data); err != nil {
			slog.Warn("[BackupManager] notification failed", "event", event, "error", err)
		}
	}
}

func (h *Handler) create(ctx context.Context) (Backup, error) {
	server := h.settings.Server()
	now := time.Now()
	id := now.Format("2006-01-02T1504")
	dir := h.folder()
	b := Backup{ID: id, Key: ptr("sqlite"), BackupDirPath: dir, DatePretty: now.Format(prettyDate), Filename: id + extension,
		FullPath: filepath.Join(dir, id+extension), Path: filepath.Join("backups", id+extension), CreatedAt: now.UnixMilli(), ServerVersion: version.Server}
	snapshot := filepath.Join(h.cfg.ConfigPath, "absdatabase."+id+".sqlite")
	_ = os.Remove(snapshot) // a leftover of a crashed backup
	defer os.Remove(snapshot)
	if err := vacuumInto(ctx, filepath.Join(h.cfg.ConfigPath, dbEntry), snapshot); err != nil {
		return Backup{}, fmt.Errorf("snapshot database: %w", err)
	}
	dirs := h.cfg.Dirs()
	size, err := writeArchive(b, snapshot, dirs.Items, dirs.Authors)
	if err == nil && server.MaxBackupSize > 0 && float64(size) > server.MaxBackupSize*1_000_000_000 { // 0 is unlimited: BackupManager.js:46
		err = fmt.Errorf("backup exceeds configured maximum size")
	}
	if err != nil {
		_ = os.Remove(b.FullPath)
		return Backup{}, err
	}
	info, err := os.Stat(b.FullPath)
	if err != nil {
		return Backup{}, err
	}
	b.FileSize = info.Size()
	for _, old := range h.put(b, h.keep()) {
		h.removeFile(old)
	}
	return b, nil
}

// vacuumInto writes a consistent snapshot of the database file, committed WAL
// contents included, through its own connection so the server's single
// connection is not blocked meanwhile.
func vacuumInto(ctx context.Context, dbPath, dest string) error {
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	conn.SetMaxOpenConns(1)
	_, err = conn.ExecContext(ctx, "VACUUM INTO ?", dest)
	if closeErr := conn.Close(); err == nil {
		err = closeErr
	}
	return err
}

// removeFile deletes the archive of b; failure is logged and keeps nothing.
func (h *Handler) removeFile(b Backup) bool {
	if err := os.Remove(b.FullPath); err != nil && !os.IsNotExist(err) {
		slog.Error("[BackupManager] Failed to remove backup", "error", err) // BackupManager.js:400
		return false
	}
	slog.Info("[BackupManager] Backup Removed", "id", b.ID) // BackupManager.js:398
	return true
}

func ptr[T any](v T) *T { return &v }
