package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/ids"
)

// Timing of the watcher (Watcher.js:20,259,233). Tests shorten the first two.
var (
	watcherPendingDelay = 10 * time.Second // quiet period after the last event before a batch is scanned
	watcherFilePoll     = 3 * time.Second  // poll interval of a file that is still being written
)

const watcherMaxPolls = 200 // 600 s with the default poll

// fileUpdate is one changed path, as the watcher queues it.
type fileUpdate struct {
	path, relPath, folderID, libraryID string
}

type watchedFolder struct{ id, path string }

type watchedLibrary struct {
	id, name string
	folders  []watchedFolder
}

// batch is the queued updates of one quiet period with their task.
type batch struct {
	updates []fileUpdate
	task    events.Task
}

// watcher debounces file events into batches and scans them one at a time.
type watcher struct {
	s   *Scanner
	ctx context.Context

	mu      sync.Mutex // guards the fields below
	libs    map[string]watchedLibrary
	pending []fileUpdate
	being   map[string]bool // files still being written
	timer   *time.Timer
	task    *events.Task

	queue   chan struct{} // wakes the worker; the batches wait in waiting
	waiting []batch
}

func (s *Scanner) newWatcher(ctx context.Context) *watcher {
	w := &watcher{s: s, ctx: ctx, libs: map[string]watchedLibrary{}, being: map[string]bool{}, queue: make(chan struct{}, 1)}
	go w.work()
	return w
}

// onFile queues a changed path of the library; kind is "added", "deleted" or
// "renamed". Paths outside the library's folders and ignored names are dropped.
func (w *watcher) onFile(libraryID, path, kind string) {
	slog.Debug("watcher: file event", "kind", kind, "path", path)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.addUpdate(libraryID, path) {
		return
	}
	if kind == "added" && !w.being[path] {
		w.being[path] = true
		go w.waitForFile(path)
	}
}

// waitForFile polls the mtime until it stops changing.
func (w *watcher) waitForFile(path string) {
	var last int64
	for loop := 0; ; loop++ {
		if loop >= watcherMaxPolls {
			w.mu.Lock()
			w.pending = slices.DeleteFunc(w.pending, func(u fileUpdate) bool { return u.path == path })
			delete(w.being, path)
			w.mu.Unlock()
			slog.Warn("watcher: waiting for file timed out", "path", path)
			return
		}
		var mtime int64
		if info, err := os.Stat(path); err == nil {
			mtime = info.ModTime().UnixMilli()
		}
		if mtime == last {
			w.mu.Lock()
			delete(w.being, path)
			w.mu.Unlock()
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(watcherFilePoll):
		}
		last = mtime
	}
}

// addUpdate queues the path; the caller holds mu.
func (w *watcher) addUpdate(libraryID, path string) bool {
	for _, u := range w.pending {
		if u.path == path {
			return false
		}
	}
	lib, ok := w.libs[libraryID]
	if !ok {
		return false
	}
	for _, f := range lib.folders {
		if path != f.path && !strings.HasPrefix(path, strings.TrimSuffix(f.path, "/")+"/") {
			continue
		}
		rel := strings.Replace(path, f.path, "", 1)
		if ignoredPath(strings.TrimPrefix(rel, "/")) {
			return false
		}
		if w.task == nil {
			task := newWatcherTask(lib.id, lib.name)
			w.task = &task
			w.s.emit.TaskStarted(task)
		}
		w.pending = append(w.pending, fileUpdate{path: path, relPath: rel, folderID: f.id, libraryID: libraryID})
		if w.timer != nil {
			w.timer.Stop()
		}
		w.timer = time.AfterFunc(watcherPendingDelay, w.fire)
		return true
	}
	return false
}

func newWatcherTask(libraryID, name string) events.Task {
	t := events.NewTask(ids.Must(), "watcher-scan", scanTaskData{LibraryID: libraryID, LibraryName: name}).
		Titled(fmt.Sprintf("Scanning file changes in %q", name), "MessageTaskScanningFileChanges", []string{name})
	t.ShowSuccess = true
	return t
}

// fire hands the pending updates to the worker once no file is in flight.
func (w *watcher) fire() {
	w.mu.Lock()
	for _, u := range w.pending {
		if w.being[u.path] {
			w.timer = time.AfterFunc(watcherPendingDelay, w.fire)
			w.mu.Unlock()
			return
		}
	}
	updates, task := w.pending, w.task
	w.pending, w.task, w.being = nil, nil, map[string]bool{}
	w.mu.Unlock()
	switch {
	case task == nil:
	case len(updates) == 0:
		w.s.emit.TaskFinished(task.Described("No files to scan", "MessageTaskNoFilesToScan").Finished())
	default:
		w.mu.Lock()
		w.waiting = append(w.waiting, batch{updates, *task})
		w.mu.Unlock()
		select {
		case w.queue <- struct{}{}:
		default:
		}
	}
}

// work scans the queued batches one at a time, in the order they were queued.
func (w *watcher) work() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.queue:
		}
		for {
			w.mu.Lock()
			if len(w.waiting) == 0 {
				w.mu.Unlock()
				break
			}
			next := w.waiting[0]
			w.waiting = w.waiting[1:]
			w.mu.Unlock()
			w.s.scanBatch(w.ctx, next)
		}
	}
}

// scanBatch rescans the folders one batch touched, one library folder at a
// time (LibraryScanner.scanFilesChanged), and finishes the batch's task.
func (s *Scanner) scanBatch(ctx context.Context, b batch) {
	var order []string
	byFolder := map[string][]fileUpdate{}
	for _, u := range b.updates {
		if _, ok := byFolder[u.folderID]; !ok {
			order = append(order, u.folderID)
		}
		byFolder[u.folderID] = append(byFolder[u.folderID], u)
	}
	var res scanResults
	var removedCount int
	for _, folderID := range order {
		updates := byFolder[folderID]
		libraryID := updates[0].libraryID
		runCtx, end, err := s.beginWait(ctx, libraryID)
		if err != nil {
			break // the watcher stopped
		}
		rels := make([]string, len(updates))
		for i, u := range updates {
			rels[i] = u.relPath
		}
		changes, err := s.scanFolderUpdates(runCtx, libraryID, folderID, rels)
		end()
		if err != nil {
			slog.Error("scanning file updates failed", "library", libraryID, "folder", folderID, "updates", len(updates), "error", err)
			continue
		}
		changed := false
		for _, c := range changes {
			switch c {
			case added:
				res.Added++
			case updated:
				res.Updated++
			case removed:
				removedCount++
			default:
				continue
			}
			changed = true
		}
		if changed {
			s.filters.ResetIssues(ctx, s.db.SQL(), libraryID)
		}
	}
	res.Removed = &removedCount
	res.Text, res.Elapsed = changesText(res.Added, res.Updated, removedCount), time.Now().UnixMilli()-b.task.StartedAt
	s.emit.TaskFinished(withResults(b.task, res).Finished())
}

// UpdateWatchedPath queues one watcher notification (kind "add", "unlink" or
// "rename", as POST /api/watcher/update validates it). A path outside the
// library's watched folders is dropped.
func (s *Scanner) UpdateWatchedPath(libraryID, watchedPath, kind string) {
	s.mu.Lock()
	w := s.watch
	s.mu.Unlock()
	if w != nil {
		w.onFile(libraryID, filepath.ToSlash(filepath.Clean(watchedPath)), map[string]string{"add": "added", "unlink": "deleted", "rename": "renamed"}[kind])
	}
}

// Start watches the library folders and runs the cron schedules until Close or
// Refresh. A folder that cannot be watched and a library cron that does not
// parse are logged and skipped.
func (s *Scanner) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return errors.New("scan: already started")
	}
	var rows []db.Library
	if err := s.db.Select(ctx, &rows, `SELECT * FROM libraries`); err != nil {
		return err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	w := s.newWatcher(runCtx)
	crons := cronspec.New()
	roots := map[string]string{}
	for _, row := range rows {
		lib := s.libraryOf(row)
		folders, err := s.folders(ctx, lib.id)
		if err != nil {
			cancel()
			_ = fsw.Close() // the start failed; the close error adds nothing
			return err
		}
		if lib.disableWatcher {
			slog.Info("watcher disabled", "library", lib.name)
		} else {
			wl := watchedLibrary{id: lib.id, name: lib.name}
			for _, f := range folders {
				root := filepath.Clean(db.Val(f.Path))
				wl.folders = append(wl.folders, watchedFolder{id: f.ID, path: filepath.ToSlash(root)})
				roots[root] = lib.id
				if err := watchDirs(fsw, root, root); err != nil {
					slog.Error("watching folder failed", "folder", root, "error", err)
				}
			}
			w.libs[lib.id] = wl
		}
		if strings.TrimSpace(lib.cron) != "" {
			id := lib.id
			if _, err := crons.AddFunc(lib.cron, func() { go s.scheduled(runCtx, id) }); err != nil {
				slog.Error("library scan schedule skipped", "library", lib.name, "cron", lib.cron, "error", err)
			}
		}
	}
	crons.Start()
	s.stop = func() {
		cancel()
		<-crons.Stop().Done()
	}
	s.watch = w
	go w.loop(fsw, roots)
	return nil
}

func (s *Scanner) scheduled(ctx context.Context, libraryID string) {
	if _, err := s.Scan(ctx, libraryID, false); err != nil {
		slog.Error("scheduled library scan failed", "library", libraryID, "error", err)
	}
}

// Refresh reloads the folders and schedules without starting a scan.
func (s *Scanner) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	s.mu.Unlock()
	return s.Start(ctx)
}

// watchDirs watches every directory under from that a scan would not ignore.
func watchDirs(fsw *fsnotify.Watcher, root, from string) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // vanished entries are not worth failing for
		}
		if rel, _ := filepath.Rel(root, p); p != root && ignoredPath(rel) {
			return filepath.SkipDir
		}
		if err := fsw.Add(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// loop feeds file system events to the watcher until its context ends.
func (w *watcher) loop(fsw *fsnotify.Watcher, roots map[string]string) {
	defer fsw.Close()
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.timer != nil {
			w.timer.Stop()
		}
	}()
	libraryOf := func(p string) (id, root string) {
		for root, id := range roots {
			if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
				return id, root
			}
		}
		return "", ""
	}
	for {
		select {
		case <-w.ctx.Done():
			return
		case ev, ok := <-fsw.Events:
			if !ok {
				return
			}
			libraryID, root := libraryOf(ev.Name)
			if libraryID == "" {
				continue
			}
			switch {
			case ev.Has(fsnotify.Create):
				// Files inside a directory that appeared before its watch was
				// registered produce no events of their own.
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = watchDirs(fsw, root, ev.Name) // best effort, like the initial watch
					_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, err error) error {
						if err == nil && !d.IsDir() {
							w.onFile(libraryID, filepath.ToSlash(p), "added")
						}
						return nil
					})
				} else {
					w.onFile(libraryID, filepath.ToSlash(ev.Name), "added")
				}
			case ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename):
				// Writes and chmods are ignored, as upstream does (Watcher.js:64-66).
				w.onFile(libraryID, filepath.ToSlash(ev.Name), "deleted")
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			slog.Error("watcher error", "error", err)
		}
	}
}
