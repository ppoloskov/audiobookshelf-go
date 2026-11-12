package server

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/realtime"
	"audiobookshelf-compat/internal/store"
)

// Manager owns the replaceable runtime graph. It releases its mutex before
// dispatching a request so a restore callback can acquire it and rebuild the
// database-bound handlers.
type Manager struct {
	mu       sync.Mutex
	cfg      config.Config
	hub      *realtime.Hub // outlives restores so sockets stay connected
	current  atomic.Pointer[Runtime]
	database *store.Store
	closed   bool
}

// backend is the realtime hub's view of the server: the current runtime, or
// an unavailable stand-in while a restore has left none.
func (m *Manager) backend() realtime.Backend {
	if rt := m.current.Load(); rt != nil {
		return rt
	}
	return unavailable{}
}

type unavailable struct{}

var errUnavailable = errors.New("server is restarting")

func (unavailable) Authenticate(*http.Request) (*auth.Principal, error) { return nil, errUnavailable }
func (unavailable) DB() *db.DB                                          { return nil }
func (unavailable) Session(string) any                                  { return nil }

func (unavailable) CancelScan(string)                                 {}
func (unavailable) SetLogListener(string, int)                        {}
func (unavailable) RemoveLogListener(string)                          {}
func (unavailable) SearchCovers(realtime.CoverSearch, realtime.Reply) {}
func (unavailable) CancelCoverSearch(string, realtime.Reply)          {}

type restoredDir struct {
	target, previous string
	existed          bool
}

func NewManager(database *store.Store, cfg config.Config) (*Manager, error) {
	if database == nil {
		return nil, errors.New("server: database is nil")
	}
	m := &Manager{cfg: cfg, database: database}
	// Logs from packages that use log/slog reach the log stream and file.
	slog.SetDefault(slog.New(logging.Default()))
	m.hub = realtime.New(cfg, m.backend)
	if err := playback.CleanupOrphanStreams(cfg.Dirs().Streams); err != nil {
		m.hub.Close()
		return nil, err
	}
	rt, err := newRuntime(database.DB(), cfg, m)
	if err != nil {
		m.hub.Close()
		return nil, err
	}
	m.current.Store(rt)
	return m, nil
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	rt := m.current.Load()
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
	if rt := m.current.Load(); rt != nil {
		rt.Close()
	}
	m.hub.Close()
	if m.database != nil {
		_ = closeStore(m.database)
	}
}

func (m *Manager) restore(ctx context.Context, dbTemp, stagedItems, stagedAuthors string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.current.Load() == nil || m.database == nil {
		return errors.New("server is closed")
	}

	check, err := store.Open(dbTemp)
	if err != nil {
		return fmt.Errorf("validate backup database: %w", err)
	}
	_ = closeStore(check)

	oldRuntime, oldStore := m.current.Load(), m.database
	dbPath := store.DatabasePath(m.cfg.ConfigPath)
	dbOld := dbPath + ".restore-previous"
	metaTargets := []struct{ target, source string }{
		{m.cfg.Dirs().Items, stagedItems},
		{m.cfg.Dirs().Authors, stagedAuthors},
	}
	moved := make([]restoredDir, 0, len(metaTargets))
	var newStore *store.Store
	var nextRuntime *Runtime

	oldRuntime.Close()
	if err = closeStore(oldStore); err != nil {
		return fmt.Errorf("close current database: %w", err)
	}
	removeSQLiteSidecars(dbPath)
	_ = os.Remove(dbOld)
	if err = os.Rename(dbPath, dbOld); err != nil {
		return m.resumeOld(fmt.Errorf("stage current database: %w", err))
	}
	if err = os.Rename(dbTemp, dbPath); err != nil {
		return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, err)
	}

	for _, pair := range metaTargets {
		if err = os.MkdirAll(filepath.Dir(pair.target), 0755); err != nil {
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, err)
		}
		stage, stageErr := copyDirectoryStage(pair.source, pair.target)
		if stageErr != nil {
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, stageErr)
		}
		previous := pair.target + ".restore-previous"
		_ = os.RemoveAll(previous)
		existed := false
		if _, statErr := os.Lstat(pair.target); statErr == nil {
			existed = true
			if err = os.Rename(pair.target, previous); err != nil {
				_ = os.RemoveAll(stage)
				return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, err)
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			_ = os.RemoveAll(stage)
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, statErr)
		}
		if err = os.Rename(stage, pair.target); err != nil {
			if existed {
				_ = os.Rename(previous, pair.target)
			}
			_ = os.RemoveAll(stage)
			return m.rollbackRestore(dbPath, dbOld, moved, nil, nil, err)
		}
		moved = append(moved, restoredDir{target: pair.target, previous: previous, existed: existed})
	}

	newStore, err = store.Open(dbPath)
	if err == nil {
		nextRuntime, err = newRuntime(newStore.DB(), m.cfg, m)
	}
	if err != nil {
		return m.rollbackRestore(dbPath, dbOld, moved, nextRuntime, newStore, err)
	}
	m.database = newStore
	m.current.Store(nextRuntime)
	// Upstream BackupManager.js sends 200 before emitting backup_applied
	// (lines 263-266); restore returns to the handler, which emits via this hub.
	_ = os.Remove(dbOld)
	for _, entry := range moved {
		_ = os.RemoveAll(entry.previous)
	}
	return nil
}

func (m *Manager) resumeOld(cause error) error {
	opened, err := store.Open(store.DatabasePath(m.cfg.ConfigPath))
	if err != nil {
		m.current.Store(nil)
		m.database = nil
		return fmt.Errorf("%v; reopen current database: %w", cause, err)
	}
	rt, err := newRuntime(opened.DB(), m.cfg, m)
	if err != nil {
		_ = closeStore(opened)
		m.current.Store(nil)
		m.database = nil
		return fmt.Errorf("%v; rebuild current runtime: %w", cause, err)
	}
	m.database = opened
	m.current.Store(rt)
	return cause
}

func (m *Manager) rollbackRestore(dbPath, dbOld string, dirs []restoredDir, rt *Runtime, db *store.Store, cause error) error {
	if rt != nil {
		rt.Close()
	}
	if db != nil {
		_ = closeStore(db)
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
	return m.resumeOld(fmt.Errorf("restore failed: %w", cause))
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

func closeStore(s *store.Store) error {
	serversettings.Forget(s.DB())
	return s.Close()
}
