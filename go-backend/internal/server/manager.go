package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/realtime"
	"audiobookshelf-compat/internal/store"
)

// Manager owns the replaceable runtime graph. It releases its mutex before
// dispatching a request so a restore callback can acquire it and rebuild the
// database-bound handlers.
type Manager struct {
	mu       sync.Mutex
	cfg      config.Config
	current  *Runtime
	database *store.Store
	closed   bool
}

type restoredDir struct {
	target, previous string
	existed          bool
}

func NewManager(database *store.Store, cfg config.Config) (*Manager, error) {
	if database == nil {
		return nil, errors.New("server: database is nil")
	}
	m := &Manager{cfg: cfg, database: database}
	rt, err := newRuntime(database.DB(), cfg, database, m)
	if err != nil {
		return nil, err
	}
	m.current = rt
	return m, nil
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	rt, closed := m.current, m.closed
	m.mu.Unlock()
	if closed || rt == nil {
		http.Error(w, "server is restarting", http.StatusServiceUnavailable)
		return
	}
	rt.ServeHTTP(w, r)
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	if m.current != nil {
		m.current.Close()
	}
	if m.database != nil {
		_ = m.database.Close()
	}
}

func (m *Manager) updateBackupPath(ctx context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.database == nil {
		return errors.New("server is closed")
	}
	var raw []byte
	if err := m.database.DB().QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	settings["backupPath"] = path
	updated, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = m.database.DB().ExecContext(ctx, `UPDATE settings SET value=? WHERE key='server-settings'`, updated)
	return err
}

func (m *Manager) restore(ctx context.Context, dbTemp, stagedItems, stagedAuthors string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.current == nil || m.database == nil {
		return errors.New("server is closed")
	}

	// Validate the extracted database before taking the live service offline.
	check, err := store.OpenReadWrite(dbTemp)
	if err != nil {
		return fmt.Errorf("validate backup database: %w", err)
	}
	_ = check.Close()

	oldRuntime, oldStore := m.current, m.database
	dbPath := filepath.Join(m.cfg.ConfigPath, "absdatabase.sqlite")
	dbOld := dbPath + ".restore-previous"
	metaTargets := []struct{ target, source string }{
		{filepath.Join(m.cfg.MetadataPath, "items"), stagedItems},
		{filepath.Join(m.cfg.MetadataPath, "authors"), stagedAuthors},
	}
	moved := make([]restoredDir, 0, len(metaTargets))
	var newStore *store.Store
	var nextRuntime *Runtime

	sharedHub := oldRuntime.realtime
	oldRuntime.close(false)
	if err = oldStore.Close(); err != nil {
		return fmt.Errorf("close current database: %w", err)
	}
	removeSQLiteSidecars(dbPath)
	_ = os.Remove(dbOld)
	if err = os.Rename(dbPath, dbOld); err != nil {
		return m.resumeOld(oldStore, oldRuntime, fmt.Errorf("stage current database: %w", err))
	}
	if err = os.Rename(dbTemp, dbPath); err != nil {
		return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, err)
	}

	for _, pair := range metaTargets {
		if err = os.MkdirAll(filepath.Dir(pair.target), 0755); err != nil {
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, err)
		}
		stage, stageErr := copyDirectoryStage(pair.source, pair.target)
		if stageErr != nil {
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, stageErr)
		}
		previous := pair.target + ".restore-previous"
		_ = os.RemoveAll(previous)
		existed := false
		if _, statErr := os.Lstat(pair.target); statErr == nil {
			existed = true
			if err = os.Rename(pair.target, previous); err != nil {
				_ = os.RemoveAll(stage)
				return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, err)
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			_ = os.RemoveAll(stage)
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, statErr)
		}
		if err = os.Rename(stage, pair.target); err != nil {
			if existed {
				_ = os.Rename(previous, pair.target)
			}
			_ = os.RemoveAll(stage)
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, oldStore, oldRuntime, err)
		}
		moved = append(moved, restoredDir{target: pair.target, previous: previous, existed: existed})
	}

	newStore, err = store.OpenReadWrite(dbPath)
	if err == nil {
		nextRuntime, err = newRuntime(dbOrNil(newStore), m.cfg, newStore, m, sharedHub)
	}
	if err != nil {
		return m.rollbackRestore(dbPath, dbOld, moved, nextRuntime, newStore, oldStore, oldRuntime, err)
	}
	m.database, m.current = newStore, nextRuntime
	// Upstream BackupManager.js sends 200 before emitting backup_applied
	// (lines 263-266); restore returns to the handler, which emits via this hub.
	_ = os.Remove(dbOld)
	for _, entry := range moved {
		_ = os.RemoveAll(entry.previous)
	}
	return nil
}

func (m *Manager) resumeOld(oldStore *store.Store, oldRuntime *Runtime, cause error) error {
	opened, err := store.OpenReadWrite(filepath.Join(m.cfg.ConfigPath, "absdatabase.sqlite"))
	if err != nil {
		m.current, m.database = nil, nil
		return fmt.Errorf("%v; reopen current database: %w", cause, err)
	}
	var hub []*realtime.Hub
	if oldRuntime != nil && oldRuntime.realtime != nil {
		hub = append(hub, oldRuntime.realtime)
	}
	rt, err := newRuntime(opened.DB(), m.cfg, opened, m, hub...)
	if err != nil {
		_ = opened.Close()
		m.current, m.database = nil, nil
		return fmt.Errorf("%v; rebuild current runtime: %w", cause, err)
	}
	m.database, m.current = opened, rt
	_ = oldStore
	_ = oldRuntime
	return cause
}

func (m *Manager) rollbackRestore(dbPath, dbOld string, dirs []restoredDir, rt *Runtime, db *store.Store, oldStore *store.Store, oldRuntime *Runtime, cause error) error {
	if rt != nil {
		rt.Close()
	}
	if db != nil {
		_ = db.Close()
	}
	_ = os.Remove(dbPath)
	_ = os.Rename(dbOld, dbPath)
	removeSQLiteSidecars(dbPath)
	for i := len(dirs) - 1; i >= 0; i-- {
		entry := dirs[i]
		_ = os.RemoveAll(entry.target)
		if entry.existed {
			_ = os.Rename(entry.previous, entry.target)
		}
	}
	return m.resumeOld(oldStore, oldRuntime, fmt.Errorf("restore failed: %w", cause))
}

func removeSQLiteSidecars(path string) {
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}

func copyDirectoryStage(source, target string) (string, error) {
	parent := filepath.Dir(target)
	stage, err := os.MkdirTemp(parent, ".abs-restore-stage-")
	if err != nil {
		return "", err
	}
	if source == "" {
		return stage, nil
	}
	err = filepath.WalkDir(source, func(src string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("metadata archive contains symlink %q", rel)
		}
		dst := filepath.Join(stage, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("metadata archive contains non-file %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		inErr, outErr := in.Close(), out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inErr != nil {
			return inErr
		}
		return outErr
	})
	if err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

func dbOrNil(s *store.Store) *sql.DB {
	if s == nil {
		return nil
	}
	return s.DB()
}
