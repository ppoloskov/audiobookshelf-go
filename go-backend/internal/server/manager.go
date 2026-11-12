package server

import (
	"context"
	"errors"
	"fmt"
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
)

// Manager owns the database, the realtime hub and the current Runtime, and
// replaces the runtime on a backup restore. While none is installed (during a
// restore, after Close, or after a restore that could not recover) requests
// answer 503. The hub outlives restores, so sockets stay connected.
type Manager struct {
	cfg     config.Config
	getenv  func(string) string
	hub     *realtime.Hub
	current atomic.Pointer[Runtime]
	mu      sync.Mutex // serialises restore and Close
}

// NewManager builds the runtime over database and starts its background work.
// The Manager owns database from now on: Close closes it.
func NewManager(database *db.DB, cfg config.Config) (*Manager, error) {
	if database == nil {
		return nil, errors.New("server: database is nil")
	}
	m := &Manager{cfg: cfg, getenv: os.Getenv}
	slog.SetDefault(slog.New(logging.Default())) // logs from log/slog reach the log stream and file
	m.hub = realtime.New(cfg, m.backend)
	if err := playback.CleanupOrphanStreams(cfg.Dirs().Streams); err != nil {
		m.hub.Close()
		return nil, err
	}
	rt, err := m.launch(database)
	if err != nil {
		m.hub.Close()
		return nil, err
	}
	m.current.Store(rt)
	return m, nil
}

// launch builds a runtime over database and starts it.
func (m *Manager) launch(database *db.DB) (*Runtime, error) {
	rt, err := newRuntime(m.cfg, database, m.hub, m.getenv, m.restore)
	if err != nil {
		return nil, err
	}
	if err := rt.start(context.Background()); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

// ServeHTTP hands the request to the current runtime.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := m.current.Load()
	if rt == nil {
		http.Error(w, "server is restarting", http.StatusServiceUnavailable)
		return
	}
	rt.ServeHTTP(w, r)
}

// Close stops the runtime, then the hub, then closes the database. Stop the
// HTTP server first.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.current.Swap(nil); rt != nil {
		m.shutdown(rt)
	}
	m.hub.Close()
}

func (m *Manager) shutdown(rt *Runtime) {
	rt.Close()
	if err := rt.data.Close(); err != nil {
		slog.Error("close database", "error", err)
	}
}

// backend is the realtime hub's view of the server: the current runtime, or
// an unavailable stand-in while there is none.
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

// restore replaces the database and the item and author metadata with the
// uploaded files. Safety sequence: validate the uploaded database; take the
// runtime out of service (503); close it and the old database; move the old
// files aside as ".restore-previous" and put the new ones in; reopen and
// rebuild the runtime over the same hub. Any failure puts the old files back
// and rebuilds the old runtime. It is the backup domain's upward call.
func (m *Manager) restore(_ context.Context, dbTemp, stagedItems, stagedAuthors string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.current.Load()
	if old == nil {
		return errors.New("server is closed")
	}
	check, err := db.Open(dbTemp)
	if err != nil {
		return fmt.Errorf("validate backup database: %w", err)
	}
	_ = check.Close() // opened read-only in effect: Open never writes
	m.current.Store(nil)
	m.shutdown(old)

	dbPath, items, authors := db.FilePath(m.cfg.ConfigPath), m.cfg.Dirs().Items, m.cfg.Dirs().Authors
	undo, err := swapFiles(dbPath, dbTemp, items, stagedItems, authors, stagedAuthors)
	var next *Runtime
	if err == nil {
		next, err = m.reopen(dbPath)
	}
	if err != nil {
		undo()
		next, resumeErr := m.reopen(dbPath)
		if resumeErr != nil {
			return fmt.Errorf("restore failed: %w; recover current state: %v", err, resumeErr)
		}
		m.current.Store(next)
		return fmt.Errorf("restore failed: %w", err)
	}
	m.current.Store(next)
	for _, target := range []string{dbPath, items, authors} {
		_ = os.RemoveAll(target + previousSuffix) // a leftover is replaced by the next restore
	}
	return nil
}

// reopen opens the database at dbPath and launches a runtime over it.
func (m *Manager) reopen(dbPath string) (*Runtime, error) {
	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	rt, err := m.launch(database)
	if err != nil {
		_ = database.Close() // the launch error is the one to report
		return nil, fmt.Errorf("rebuild runtime: %w", err)
	}
	return rt, nil
}

const previousSuffix = ".restore-previous"

// swapFiles puts the uploaded database and the staged metadata trees in place
// of the current ones. Each old target stays beside it as target+previousSuffix.
// The returned undo puts them back, last swap first; it is valid after an error.
func swapFiles(dbPath, dbTemp, items, stagedItems, authors, stagedAuthors string) (undo func(), err error) {
	var undos []func()
	undo = func() {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
		removeSQLiteSidecars(dbPath)
	}
	// put renames staged to target after moving an existing target aside.
	put := func(target, staged string) error {
		previous := target + previousSuffix
		if err := os.RemoveAll(previous); err != nil {
			return err
		}
		existed := true
		if err := os.Rename(target, previous); errors.Is(err, fs.ErrNotExist) {
			existed = false
		} else if err != nil {
			return fmt.Errorf("move %s aside: %w", filepath.Base(target), err)
		}
		undos = append(undos, func() {
			_ = os.RemoveAll(target) // best effort: the rest of the undo still runs
			if existed {
				_ = os.Rename(previous, target)
			}
		})
		return os.Rename(staged, target)
	}
	removeSQLiteSidecars(dbPath)
	if err = put(dbPath, dbTemp); err != nil {
		return undo, err
	}
	for _, tree := range [][2]string{{items, stagedItems}, {authors, stagedAuthors}} {
		stage, err := stageTree(tree[0], tree[1])
		if err != nil {
			return undo, err
		}
		if err = put(tree[0], stage); err != nil {
			_ = os.RemoveAll(stage)
			return undo, err
		}
	}
	return undo, nil
}

func removeSQLiteSidecars(path string) {
	_ = os.Remove(path + "-wal") // leftovers only: the file stays in rollback-journal mode
	_ = os.Remove(path + "-shm")
}

// stageTree copies source (nothing when empty) into a new directory next to
// target and returns it. An uploaded archive may hold only plain files and
// directories: os.CopyFS would follow a symlink out of the archive, so the
// tree is checked first.
func stageTree(target, source string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Dir(target), ".abs-restore-stage-")
	if err != nil || source == "" {
		return stage, err
	}
	files := os.DirFS(source)
	err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && !entry.Type().IsRegular() {
			err = fmt.Errorf("metadata archive contains symlink or special file %q", name)
		}
		return err
	})
	if err == nil {
		err = os.CopyFS(stage, files)
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return "", fmt.Errorf("stage %s: %w", filepath.Base(target), err)
	}
	return stage, nil
}
