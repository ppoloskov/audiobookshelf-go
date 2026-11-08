// Package scanner imports media discovered in configured library folders.
// It writes only the existing Audiobookshelf schema; it never creates or
// migrates database tables.
package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/version"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"github.com/fsnotify/fsnotify"
	"github.com/robfig/cron/v3"
)

type Result struct {
	Added, Updated, Missing int
	Canceled                bool
	Nothing                 bool
}

type Scanner struct {
	db          *sql.DB
	cfg         config.Config
	emit        events.Emitter
	mu          sync.Mutex
	runs        map[string]*run
	watchCancel context.CancelFunc
	fw          *fsWatcher
	filters     *filterdata.Cache
}

func New(db *sql.DB, cfg config.Config, emit events.Emitter, filters *filterdata.Cache) (*Scanner, error) {
	if db == nil {
		return nil, errors.New("scanner: database is nil")
	}
	if filters == nil {
		return nil, errors.New("scanner: filter data provider is nil")
	}
	return &Scanner{db: db, cfg: cfg, emit: emit, runs: make(map[string]*run), filters: filters}, nil
}

// run is one registered scan of a library; done closes when it ends.
type run struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// begin registers a running scan of the library, which at most one may be at a
// time (library scans, item scans and watcher batches alike); the returned func
// ends it. The context ends when Cancel is called.
func (s *Scanner) begin(ctx context.Context, libraryID string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[libraryID]; ok {
		logging.Errorf("[LibraryScanner] Already scanning %s", libraryID) // LibraryScanner.js:53
		return ctx, nil, errors.New("scanner: library scan already running")
	}
	return s.register(ctx, libraryID)
}

// beginWait is begin for a watcher batch, which must not be dropped: it waits
// for the running scan of the library to end.
func (s *Scanner) beginWait(ctx context.Context, libraryID string) (context.Context, func(), error) {
	for {
		s.mu.Lock()
		r, busy := s.runs[libraryID]
		if !busy {
			defer s.mu.Unlock()
			return s.register(ctx, libraryID)
		}
		s.mu.Unlock()
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx, nil, ctx.Err()
		}
	}
}

// register adds the run; the caller holds mu and the library has none.
func (s *Scanner) register(ctx context.Context, libraryID string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	r := &run{cancel: cancel, done: make(chan struct{})}
	s.runs[libraryID] = r
	return ctx, func() { cancel(); s.mu.Lock(); delete(s.runs, libraryID); s.mu.Unlock(); close(r.done) }, nil
}

// scanItemForce refreshes one existing library item as the item scan route
// does; it leaves the rest of the library untouched.
func (s *Scanner) scanItemForce(ctx context.Context, itemID string) (Result, error) {
	var libraryID string
	if err := s.db.QueryRowContext(ctx, `SELECT libraryId FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID); err != nil {
		return Result{}, fmt.Errorf("scanner: item %q: %w", itemID, err)
	}
	ctx, end, err := s.begin(ctx, libraryID)
	if err != nil {
		return Result{}, err
	}
	defer end()
	res, err := s.rescanStoredItem(ctx, itemID, nil)
	return Result{Updated: boolInt(res == scanUpdated), Nothing: res == scanNothing, Canceled: ctx.Err() != nil}, err
}

func (s *Scanner) Cancel(libraryID string) {
	s.mu.Lock()
	r := s.runs[libraryID]
	s.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// UpdateWatchedPath queues one watcher notification (kind add, unlink or
// rename, as POST /api/watcher/update validates it) on the internal watcher, as
// Watcher.onFileAdded / onFileRemoved / onFileRename do (MiscController.js:
// 593-612): the batch is rescanned after the pending delay. A path outside the
// library's watched folders is dropped, as Watcher.addFileUpdate does.
func (s *Scanner) UpdateWatchedPath(libraryID, watchedPath, kind string) {
	s.mu.Lock()
	fw := s.fw
	s.mu.Unlock()
	if fw == nil {
		return
	}
	fw.onFile(libraryID, filepath.ToSlash(filepath.Clean(watchedPath)), map[string]string{"add": "added", "unlink": "deleted", "rename": "renamed"}[kind])
}

// Scan's force rescans items even when no file changed.
func (s *Scanner) Scan(ctx context.Context, libraryID string, force bool) (Result, error) {
	ctx, end, err := s.begin(ctx, libraryID)
	if err != nil {
		return Result{}, err
	}
	defer end()
	task := s.startScanTask(ctx, libraryID)
	libraryName := task.Data.(scanTaskData).LibraryName
	forced := ""
	if force {
		forced = " (forced)"
	}
	logging.Infof("[LibraryScanner] Starting%s library scan %s for %s", forced, task.ID, libraryName) // LibraryScanner.js:86
	result, err := s.scanLibrary(ctx, libraryID, force)
	if errors.Is(ctx.Err(), context.Canceled) {
		result.Canceled = true
	}
	// LibraryScanner.js:92,119: elapsed is secondsToTimestamp(elapsed / 1000).
	elapsed := secondsToTimestamp(float64(time.Now().UnixMilli()-task.StartedAt) / 1000)
	stats := fmt.Sprintf("%d Added | %d Updated | %d Missing", result.Added, result.Updated, result.Missing)
	switch {
	case err != nil:
		logging.Errorf("[LibraryScanner] Library scan %s failed after %s | %s. %v", task.ID, elapsed, stats, err)
	case result.Canceled:
		logging.Infof("[LibraryScanner] Library scan %q canceled after %s | %s", task.ID, elapsed, stats)
	default:
		logging.Infof("[LibraryScanner] Library scan %q completed in %s | %s", task.ID, elapsed, stats)
	}
	s.finishScanTask(task, result, err)
	s.filters.ResetIssues(ctx, s.db, libraryID) // LibraryController.js:1272
	return result, err
}

func (s *Scanner) startScanTask(ctx context.Context, libraryID string) events.Task {
	name := libraryID
	mediaType := ""
	var libraryName string
	if s.db.QueryRowContext(ctx, `SELECT name,mediaType FROM libraries WHERE id=?`, libraryID).Scan(&libraryName, &mediaType) == nil && libraryName != "" {
		name = libraryName
	}
	task := events.NewTask(ids.Must(), "library-scan", scanTaskData{LibraryID: libraryID, LibraryName: name, LibraryMediaType: mediaType}).
		Titled(fmt.Sprintf("Scanning %q library", name), "MessageTaskScanningLibrary", []string{name})
	task.ShowSuccess = true
	s.emit.TaskStarted(task)
	return task
}

func (s *Scanner) finishScanTask(task events.Task, result Result, err error) {
	task = task.Finished()
	elapsed := *task.FinishedAt - task.StartedAt
	task = withResults(task, &scanResults{Added: result.Added, Updated: result.Updated, Missing: &result.Missing, Elapsed: elapsed, Text: fmt.Sprintf("%s (%s)", changesText(result.Added, result.Updated, result.Missing), elapsedPretty(float64(elapsed)/1000))})
	if err != nil {
		task = task.Failed("Failed", "MessageTaskFailed")
	} else if result.Canceled {
		task = task.Described("Task canceled by user", "MessageTaskCanceledByUser")
	}
	s.emit.TaskFinished(task)
}

// markMissing flags a stored item whose files are gone and returns it as the
// clients receive it (LibraryScanner.js:195, 590: isMissing, then save).
func (s *Scanner) markMissing(ctx context.Context, itemID string) (libitem.Expanded, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE libraryItems SET isMissing=1,updatedAt=? WHERE id=?`, dbtime.Now(), itemID); err != nil {
		return libitem.Expanded{}, err
	}
	return s.itemEvent(ctx, itemID)
}

// secondsToTimestamp is server/utils/index.js:123 without milliseconds.
func secondsToTimestamp(seconds float64) string {
	minutes := int(seconds / 60)
	secs := int(seconds - float64(minutes)*60)
	hours := minutes / 60
	minutes -= hours * 60
	if hours == 0 {
		return fmt.Sprintf("%d:%02d", minutes, secs)
	}
	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
}

// scanDebug is LibraryScan.addLog with verbose set: a DEBUG line tagged with
// the library name (LibraryScan.js:119).
func scanDebug(l library, format string, args ...any) {
	logging.Debugf("[LibraryScan] %q: %s", l.name, fmt.Sprintf(format, args...))
}

// changesText lists what a scan changed (LibraryScan.js:61-68).
func changesText(added, updated, missing int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", updated))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", missing))
	}
	if len(parts) == 0 {
		return "No changes needed"
	}
	return strings.Join(parts, ", ")
}

// elapsedPretty is server/utils/index.js:92-120 for the ranges a scan reaches.
func elapsedPretty(seconds float64) string {
	if seconds > 0 && seconds < 1 {
		return fmt.Sprintf("%d ms", int(seconds*1000))
	}
	if seconds < 60 {
		return fmt.Sprintf("%d sec", int(seconds))
	}
	minutes := int(seconds / 60)
	if minutes < 70 {
		return fmt.Sprintf("%d min", minutes)
	}
	hours := minutes / 60
	minutes -= hours * 60
	days := hours / 24
	hours -= days * 24
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d d", days))
	}
	if hours > 0 || (days > 0 && minutes > 0) {
		parts = append(parts, fmt.Sprintf("%d hr", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d min", minutes))
	}
	return strings.Join(parts, " ")
}

// Routes registers the admin scan endpoints (LibraryController.scan,
// LibraryItemController.scan, batchScan).
func (s *Scanner) Routes(mux *http.ServeMux) {
	mux.Handle("POST /api/items/batch/scan", access.RequireAdmin(http.StatusForbidden, s.batchScan))
	mux.Handle("POST /api/libraries/{id}/scan", access.RequireAdmin(http.StatusForbidden, s.scanLibraryRoute))
	mux.Handle("POST /api/items/{id}/scan", access.RequireAdmin(http.StatusForbidden, s.scanItemRoute))
}

func (s *Scanner) batchScan(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.LibraryItemIDs) == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	type itemRef struct {
		ID, LibraryID string
		IsFile        int
	}
	items := make([]itemRef, 0, len(body.LibraryItemIDs))
	for _, id := range body.LibraryItemIDs {
		var item itemRef
		item.ID = id
		if err := s.db.QueryRowContext(r.Context(), `SELECT id,libraryId,isFile FROM libraryItems WHERE id=?`, id).Scan(&item.ID, &item.LibraryID, &item.IsFile); err == nil {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return httputil.Status(http.StatusBadRequest, "")
	}
	w.WriteHeader(http.StatusOK)
	go func(items []itemRef) {
		for _, item := range items {
			if item.IsFile != 0 {
				continue
			}
			if _, err := s.scanItemForce(context.Background(), item.ID); err != nil {
				logging.Errorf("[LibraryScanner] Scan of item %s failed: %v", item.ID, err)
			}
		}
	}(items)
	return nil
}

func (s *Scanner) scanLibraryRoute(w http.ResponseWriter, r *http.Request) error {
	id, err := url.PathUnescape(r.PathValue("id"))
	if err != nil {
		return httputil.Status(http.StatusBadRequest, "")
	}
	if _, err := s.library(r.Context(), id); err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	force := r.URL.Query().Get("force") == "1" || strings.EqualFold(r.URL.Query().Get("force"), "true")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
	go func() {
		if _, err := s.Scan(context.Background(), id, force); err != nil {
			logging.Errorf("[LibraryScanner] Scan of library %s failed: %v", id, err)
		}
	}()
	return nil
}

func (s *Scanner) scanItemRoute(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var isFile int
	if err := s.db.QueryRowContext(r.Context(), `SELECT isFile FROM libraryItems WHERE id=?`, id).Scan(&isFile); err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if isFile != 0 {
		logging.RequestDebug(r, "responding 500: cannot scan a library file")
		return httputil.Status(http.StatusInternalServerError, "")
	}
	res, err := s.scanItemForce(r.Context(), id)
	if err != nil && !res.Canceled {
		return &httputil.Error{Status: http.StatusInternalServerError, Cause: err}
	}
	status := "UPTODATE"
	if res.Added+res.Updated > 0 {
		status = "UPDATED"
	} else if res.Nothing {
		status = "NOTHING"
	}
	return httputil.JSON(w, r, map[string]string{"result": status})
}

// Start initializes recursive filesystem watchers and per-library schedules.
// Canceling its context stops those resources and any scans they started.
func (s *Scanner) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.watchCancel != nil {
		s.mu.Unlock()
		return errors.New("scanner: already started")
	}
	watchCtx, cancel := context.WithCancel(ctx)
	s.watchCancel = cancel
	s.mu.Unlock()
	watcher, cronJobs, fw, roots, err := s.arm(watchCtx)
	s.mu.Lock()
	if err != nil {
		cancel()
		s.watchCancel = nil
	} else {
		s.fw = fw
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	cronJobs.Start()
	go s.eventLoop(watchCtx, watcher, cronJobs, fw, roots)
	return nil
}

func (s *Scanner) arm(ctx context.Context) (_ *fsnotify.Watcher, _ *cron.Cron, _ *fsWatcher, roots map[string]string, err error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	cronJobs := cron.New()
	defer func() {
		if err != nil {
			_ = watcher.Close()
			cronJobs.Stop()
		}
	}()
	libraries, err := s.watchedLibraries(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	fw := newFSWatcher(ctx, s)
	roots = map[string]string{}
	for _, lib := range libraries {
		if lib.disabled {
			logging.Infof("[Watcher] updateLibrary: Library %q watcher disabled", lib.name) // Watcher.js:136
		} else {
			logging.Infof("[Watcher] Initializing watcher for %q.", lib.name) // Watcher.js:58
			watch := &libraryWatch{id: lib.id, name: lib.name}
			for _, f := range lib.folders {
				root := filepath.Clean(f.path)
				watch.folders = append(watch.folders, watchedFolder{id: f.id, path: filepath.ToSlash(root)})
				roots[root] = lib.id
				// Watching is best effort (inotify limits, permissions): a folder
				// that cannot be watched must not stop the other libraries or
				// the cron schedules.
				if err := watchDirs(watcher, root, root); err != nil {
					logging.Errorf("[Watcher] Watching %q failed: %v", root, err)
				}
			}
			fw.libraries[lib.id] = watch
			logging.Infof("[Watcher] %q Ready", lib.name) // Watcher.js:89
		}
		if strings.TrimSpace(lib.cron) != "" {
			id := lib.id
			if _, err := cronJobs.AddFunc(lib.cron, func() {
				go func() {
					if _, scanErr := s.Scan(ctx, id, false); scanErr != nil {
						logging.Errorf("[CronManager] Scheduled scan of library %s failed: %v", id, scanErr)
					}
				}()
			}); err != nil {
				return nil, nil, nil, nil, fmt.Errorf("scanner: library %s cron: %w", id, err)
			}
		}
	}
	return watcher, cronJobs, fw, roots, nil
}

func watchDirs(watcher *fsnotify.Watcher, root, from string) error {
	return walkVisible(root, from, func(p string, d os.DirEntry) error {
		if d.IsDir() {
			if err := watcher.Add(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func (s *Scanner) eventLoop(ctx context.Context, watcher *fsnotify.Watcher, cronJobs *cron.Cron, fw *fsWatcher, roots map[string]string) {
	defer watcher.Close()
	defer cronJobs.Stop()
	defer fw.stop()
	libraryOf := func(p string) (string, string) {
		for root, id := range roots {
			if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
				return id, root
			}
		}
		return "", ""
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			libraryID, root := libraryOf(ev.Name)
			if libraryID == "" {
				continue
			}
			posix := filepath.ToSlash(ev.Name)
			switch {
			case ev.Op&fsnotify.Create != 0:
				// Files already inside a directory that appeared before its
				// watch was registered produce no events of their own.
				if st, statErr := os.Stat(ev.Name); statErr == nil && st.IsDir() {
					_ = watchDirs(watcher, root, ev.Name)
					_ = walkVisible(root, ev.Name, func(p string, d os.DirEntry) error {
						if !d.IsDir() {
							fw.onFile(libraryID, filepath.ToSlash(p), "added")
						}
						return nil
					})
				} else {
					fw.onFile(libraryID, posix, "added")
				}
			case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
				// "change" (Write/Chmod) events are ignored, as upstream does
				// (Watcher.js:64-66).
				fw.onFile(libraryID, posix, "deleted")
			}
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logging.Errorf("[Watcher] %v", watchErr) // Watcher.js:86
		}
	}
}

func (s *Scanner) Close() {
	s.mu.Lock()
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
	for _, r := range s.runs {
		r.cancel()
	}
	s.mu.Unlock()
}

// Refresh reloads the persisted folder and schedule settings without starting
// a scan. Catalog integrations call it only after library/folder writes commit.
func (s *Scanner) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
	s.mu.Unlock()
	return s.Start(ctx)
}

type watchedLibrary struct {
	id, name, cron string
	disabled       bool
	folders        []folder
}

func (s *Scanner) watchedLibraries(ctx context.Context) ([]watchedLibrary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,settings FROM libraries`)
	if err != nil {
		return nil, err
	}
	var out []watchedLibrary
	for rows.Next() {
		var l watchedLibrary
		var raw []byte
		if err := rows.Scan(&l.id, &l.name, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		settings := decodeLibrarySettings(raw)
		l.cron, l.disabled = settings.AutoScanCronExpression, settings.DisableWatcher
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Close the libraries cursor before querying folders: some supported SQLite
	// connections use a single open connection.
	for i := range out {
		fs, err := s.folders(ctx, out[i].id)
		if err != nil {
			return nil, err
		}
		out[i].folders = fs
	}
	return out, nil
}

// librarySettings is the part of libraries.settings the scanner reads.
type librarySettings struct {
	AutoScanCronExpression string   `json:"autoScanCronExpression"`
	DisableWatcher         bool     `json:"disableWatcher"`
	AudiobooksOnly         bool     `json:"audiobooksOnly"`
	MetadataPrecedence     []string `json:"metadataPrecedence"`
}

// decodeLibrarySettings reads the stored settings once; a missing or unreadable
// field has its zero value and an empty precedence is the default order.
func decodeLibrarySettings(raw []byte) librarySettings {
	var settings librarySettings
	_ = json.Unmarshal(raw, &settings)
	if len(settings.MetadataPrecedence) == 0 {
		settings.MetadataPrecedence = serversettings.DefaultMetadataPrecedence()
	}
	return settings
}

type library struct {
	id, name, mediaType string
	audiobooksOnly      bool
	metadataPrecedence  []string
	lastPrecedence      []string
	parseSubtitle       bool
	sortingPrefixes     []string
}
type folder struct{ id, path string }

func (s *Scanner) library(ctx context.Context, id string) (library, error) {
	var l library
	var raw, extra []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,name,mediaType,settings,extraData FROM libraries WHERE id=?`, id).Scan(&l.id, &l.name, &l.mediaType, &raw, &extra)
	if err == nil {
		settings := decodeLibrarySettings(raw)
		l.audiobooksOnly, l.metadataPrecedence = settings.AudiobooksOnly, settings.MetadataPrecedence
		server := serversettings.Load(ctx, s.db)
		l.parseSubtitle = server.Bool("scannerParseSubtitle")
		l.sortingPrefixes = server.SortingPrefixes()
		var data struct {
			LastPrecedence []string `json:"lastScanMetadataPrecedence"`
		}
		if json.Unmarshal(extra, &data) == nil {
			l.lastPrecedence = data.LastPrecedence
		}
	}
	return l, err
}

func (s *Scanner) folders(ctx context.Context, id string) ([]folder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path FROM libraryFolders WHERE libraryId=? ORDER BY createdAt`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []folder{}
	for rows.Next() {
		var f folder
		if err := rows.Scan(&f.id, &f.path); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Scanner) scanLibrary(ctx context.Context, libraryID string, force bool) (Result, error) {
	var result Result
	l, err := s.library(ctx, libraryID)
	if err != nil {
		return result, fmt.Errorf("scanner: library %q: %w", libraryID, err)
	}
	if l.mediaType == "book" && strings.Join(l.metadataPrecedence, ",") != strings.Join(l.lastPrecedence, ",") {
		force = true
	}
	folders, err := s.folders(ctx, libraryID)
	if err != nil {
		return result, err
	}
	if len(folders) == 0 {
		logging.Warnf("[LibraryScanner] Library has no folders to scan %q", l.name) // LibraryScanner.js:58
		return result, errors.New("scanner: library has no folders")
	}
	_, _ = s.filters.Get(ctx, s.db, libraryID, l.mediaType)
	var ordered []*group
	for _, f := range folders {
		root, err := filepath.Abs(filepath.Clean(f.path))
		if err != nil {
			return result, err
		}
		f.path = root
		discovered, err := walkFiles(ctx, root, l.wantFile)
		if errors.Is(err, context.Canceled) {
			break
		}
		if err != nil {
			continue
		}
		ordered = append(ordered, groupFiles(f, discovered, l.mediaExt)...)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })
	existing, err := s.loadExistingItems(ctx, libraryID)
	if err != nil {
		return result, err
	}
	matcher := newGroupMatcher(ordered)
	tally := &scanTally{}
	var updatedItems []libitem.Expanded
	emitUpdated := func() {
		if len(updatedItems) > 0 {
			s.emit.ToAll("items_updated", updatedItems)
		}
		updatedItems = nil
	}
	// Existing items first, in stored order, as LibraryScanner.scanLibrary does
	// (LibraryScanner.js:156-224).
	for _, ex := range existing {
		if ctx.Err() != nil {
			break
		}
		g := matcher.match(ex)
		if g == nil {
			if l.mediaType == "podcast" {
				if _, statErr := os.Stat(ex.path); statErr == nil {
					continue
				}
			}
			result.Missing++
			scanDebug(l, "Library Item %q (inode: %s) is missing", ex.path, ex.ino) // LibraryScanner.js:187
			if !ex.missing {
				item, e := s.markMissing(ctx, ex.id)
				if e != nil {
					return result, e
				}
				updatedItems = append(updatedItems, item)
			}
		} else {
			matcher.claim(g)
			g.old = &ex
			n, e := s.importGroup(ctx, l, g, force)
			if e != nil {
				return result, e
			}
			tally.merge(n)
			if n.updated {
				result.Updated++
				scanDebug(l, "Library item %q was updated", ex.path)
				if item, e := s.itemEvent(ctx, n.id, n.scannedAudio...); e == nil {
					updatedItems = append(updatedItems, item)
				}
			}
		}
		if len(updatedItems) == 10 {
			emitUpdated()
		}
	}
	emitUpdated()

	var addedItems []libitem.Expanded
	for _, g := range matcher.unclaimed() {
		if ctx.Err() != nil {
			break
		}
		n, e := s.importGroup(ctx, l, g, force)
		if e != nil {
			return result, e
		}
		tally.merge(n)
		if n.added {
			result.Added++
			scanDebug(l, "Created new library item %q with id %q", mustRel(g.folder.path, g.path), n.id) // LibraryItemScanner.js:194
			if item, e := s.itemEvent(ctx, n.id); e == nil {
				addedItems = append(addedItems, item)
			}
		}
		if len(addedItems) == 10 {
			s.emit.ToAll("items_added", addedItems)
			addedItems = nil
		}
	}
	if len(addedItems) > 0 {
		s.emit.ToAll("items_added", addedItems)
	}
	if ctx.Err() == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE libraries SET lastScan=?,lastScanVersion=? WHERE id=?`, dbtime.Now(), version.Server, libraryID)
		if err != nil {
			return result, err
		}
		if l.mediaType == "book" {
			var extra map[string]any
			var raw []byte
			if err := s.db.QueryRowContext(ctx, `SELECT extraData FROM libraries WHERE id=?`, libraryID).Scan(&raw); err != nil {
				return result, err
			}
			_ = json.Unmarshal(raw, &extra)
			if extra == nil {
				extra = map[string]any{}
			}
			extra["lastScanMetadataPrecedence"] = l.metadataPrecedence
			encoded, err := json.Marshal(extra)
			if err != nil {
				return result, err
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE libraries SET extraData=? WHERE id=?`, string(encoded), libraryID); err != nil {
				return result, err
			}
		}
		if err := s.finalizeScan(ctx, libraryID, tally); err != nil {
			return result, err
		}
	}
	return result, nil
}

// authorBookCount and authorsNumBooksUpdated are the authors_num_books_updated
// payload (BookScanner.js:926-930).
type authorBookCount struct {
	ID       string `json:"id"`
	NumBooks int    `json:"numBooks"`
}

type authorsNumBooksUpdated struct {
	LibraryID string            `json:"libraryId"`
	Authors   []authorBookCount `json:"authors"`
}

// scanTally collects what the per-item imports changed so the scan can emit
// one authors_num_books_updated and remove emptied authors/series at the end.
type scanTally struct {
	authorsChanged, authorsRemoved, seriesRemoved []string
}

func (t *scanTally) merge(n imported) {
	t.authorsChanged = append(t.authorsChanged, n.authorsChanged...)
	t.authorsRemoved = append(t.authorsRemoved, n.authorsRemoved...)
	t.seriesRemoved = append(t.seriesRemoved, n.seriesRemoved...)
}

type existingItem struct {
	id, mediaID, path, relPath, ino, mediaType string
	missing                                    bool
	files                                      []byte
	mtime, ctime, birthtime                    string
}

// queryItems reads stored items matching a WHERE/ORDER clause. The upstream
// library query has no ORDER BY, so its order is whatever index SQLite picks;
// on the pinned stack that is insertion order.
func (s *Scanner) queryItems(ctx context.Context, clause string, args ...any) ([]existingItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(mediaId,''),path,COALESCE(relPath,''),COALESCE(ino,''),COALESCE(mediaType,''),COALESCE(isMissing,0),libraryFiles,COALESCE(mtime,''),COALESCE(ctime,''),COALESCE(birthtime,'') FROM libraryItems WHERE `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []existingItem
	for rows.Next() {
		var e existingItem
		var missing int
		if err := rows.Scan(&e.id, &e.mediaID, &e.path, &e.relPath, &e.ino, &e.mediaType, &missing, &e.files, &e.mtime, &e.ctime, &e.birthtime); err != nil {
			return nil, err
		}
		e.missing = missing != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Scanner) loadExistingItems(ctx context.Context, libraryID string) ([]existingItem, error) {
	return s.queryItems(ctx, `libraryId=? ORDER BY rowid`, libraryID)
}

func (s *Scanner) storedItemByID(ctx context.Context, id string) (*existingItem, error) {
	items, err := s.queryItems(ctx, `id=?`, id)
	if err != nil || len(items) == 0 {
		return nil, fmt.Errorf("scanner: item %q: %w", id, cmp.Or(err, sql.ErrNoRows))
	}
	return &items[0], nil
}

// finalizeScan emits author book-count changes, then removes authors and
// series that lost their last book during this scan
// (LibraryItemScanner.js:84-93, BookScanner.js:908-1010).
func (s *Scanner) finalizeScan(ctx context.Context, libraryID string, t *scanTally) error {
	if ids := uniqueStrings(t.authorsChanged); len(ids) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT authorId,COUNT(id) FROM bookAuthors WHERE authorId IN (`+marks+`) GROUP BY authorId ORDER BY authorId`, args...)
		if err != nil {
			return err
		}
		var authors []authorBookCount
		for rows.Next() {
			var count authorBookCount
			if err := rows.Scan(&count.ID, &count.NumBooks); err != nil {
				rows.Close()
				return err
			}
			authors = append(authors, count)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(authors) > 0 {
			s.emit.ToAll("authors_num_books_updated", authorsNumBooksUpdated{LibraryID: libraryID, Authors: authors})
		}
	}
	itemwrite.Env{DB: s.db, Filters: s.filters, Emit: s.emit}.Sweep(ctx, t.authorsRemoved, t.seriesRemoved)
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func mustRel(root, p string) string { r, _ := filepath.Rel(root, p); return r }
