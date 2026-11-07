// Package scanner imports media discovered in configured library folders.
// It writes only the existing Audiobookshelf schema; it never creates or
// migrates database tables.
package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
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

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type Result struct {
	Added, Updated, Missing int
	Canceled                bool
	Nothing                 bool
}

type Scanner struct {
	db                 *sql.DB
	cfg                config.Config
	emit               events.Emitter
	mu                 sync.Mutex
	runs               map[string]context.CancelFunc
	watchCancel        context.CancelFunc
	podcastFeedFetcher func(context.Context, string) ([]map[string]any, error)
	items              items.Reader
	// filters is the shared library filter data cache the scanner patches
	// (nil when the item reader has none).
	filters      *filterdata.Cache
	filterLoader filterdata.Provider
}

func New(db *sql.DB, cfg config.Config, emit events.Emitter, itemReader items.Reader) (*Scanner, error) {
	if db == nil {
		return nil, errors.New("scanner: database is nil")
	}
	if itemReader == nil {
		return nil, errors.New("scanner: item reader is nil")
	}
	s := &Scanner{db: db, cfg: cfg, emit: emit, runs: make(map[string]context.CancelFunc), items: itemReader}
	if provider, ok := itemReader.(filterdata.Provider); ok {
		s.filterLoader, s.filters = provider, provider.FilterCache()
	}
	return s, nil
}

func (s *Scanner) Scan(ctx context.Context, libraryID string) (Result, error) {
	return s.scan(ctx, libraryID, false)
}
func (s *Scanner) ScanForce(ctx context.Context, libraryID string) (Result, error) {
	return s.scan(ctx, libraryID, true)
}

// ScanItemForce refreshes one existing library item as the item scan route
// does; it leaves the rest of the library untouched.
func (s *Scanner) ScanItemForce(ctx context.Context, itemID string) (Result, error) {
	var result Result
	var libraryID, mediaType, itemPath, folderID string
	var isFile int
	err := s.db.QueryRowContext(ctx, `SELECT libraryId,mediaType,path,libraryFolderId,isFile FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &itemPath, &folderID, &isFile)
	if err != nil {
		return result, fmt.Errorf("scanner: item %q: %w", itemID, err)
	}
	if isFile != 0 {
		return result, errors.New("scanner: cannot scan a library file")
	}
	s.mu.Lock()
	if _, ok := s.runs[libraryID]; ok {
		s.mu.Unlock()
		return result, errors.New("scanner: library scan already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.runs[libraryID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.runs, libraryID); s.mu.Unlock() }()
	var folderPath string
	if err := s.db.QueryRowContext(ctx, `SELECT path FROM libraryFolders WHERE id=?`, folderID).Scan(&folderPath); err != nil {
		return result, err
	}
	l, err := s.library(ctx, libraryID)
	if err != nil {
		return result, err
	}
	st, err := os.Stat(itemPath)
	if err != nil {
		return result, err
	}
	files := []file{}
	if st.IsDir() {
		err = filepath.WalkDir(itemPath, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if p != itemPath && (strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir") {
					return filepath.SkipDir
				}
				return nil
			}
			if !audioExt(filepath.Ext(p)) && !scannableCompanion(filepath.Ext(p)) {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			files = append(files, file{path: p, info: info, relPath: filepath.ToSlash(mustRel(itemPath, p))})
			return nil
		})
	} else if audioExt(filepath.Ext(itemPath)) {
		files = append(files, file{path: itemPath, info: st, relPath: filepath.Base(itemPath)})
	}
	if err != nil {
		return result, err
	}
	if len(files) == 0 {
		return Result{Canceled: ctx.Err() != nil, Nothing: true}, nil
	}
	g := &group{path: itemPath, folder: folder{id: folderID, path: folderPath}}
	for _, f := range files {
		if audioExt(filepath.Ext(f.path)) {
			g.files = append(g.files, f)
		} else {
			g.companions = append(g.companions, f)
		}
	}
	n, err := s.importGroup(ctx, l, g, true)
	if err != nil {
		return result, err
	}
	if n.updated {
		result.Updated = 1
		if s.emit != nil {
			if item, e := s.rescanItemJSON(ctx, n.id, n.scannedAudio); e == nil {
				s.emit("*", "item_updated", item)
			}
		}
	}
	tally := &scanTally{}
	tally.merge(n)
	if err := s.finalizeScan(ctx, libraryID, tally); err != nil {
		return result, err
	}
	result.Canceled = ctx.Err() != nil
	return result, nil
}

func (s *Scanner) Cancel(libraryID string) {
	s.mu.Lock()
	cancel := s.runs[libraryID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SetPodcastFeedFetcher wires the podcast package's secure RSS parser without
// introducing an import cycle between the scanner and podcast controllers.
func (s *Scanner) SetPodcastFeedFetcher(fetcher func(context.Context, string) ([]map[string]any, error)) {
	s.mu.Lock()
	s.podcastFeedFetcher = fetcher
	s.mu.Unlock()
}

// UpdateWatchedPath applies one source-compatible watcher notification. The
// scanner currently reconciles the containing library because its import
// pipeline commits complete book groups; this preserves additions, removals,
// and renames without constructing partial libraryFiles records.
func (s *Scanner) UpdateWatchedPath(ctx context.Context, libraryID, watchedPath, kind, oldPath string) error {
	if kind != "added" && kind != "deleted" && kind != "renamed" {
		return fmt.Errorf("scanner: unsupported watcher change %q", kind)
	}
	absPath, err := filepath.Abs(filepath.Clean(watchedPath))
	if err != nil {
		return err
	}
	folders, err := s.folders(ctx, libraryID)
	if err != nil {
		return err
	}
	inside := false
	for _, f := range folders {
		root, e := filepath.Abs(filepath.Clean(f.path))
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(root, absPath)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inside = true
			break
		}
	}
	if !inside {
		return fmt.Errorf("scanner: watcher path is outside library folders")
	}
	if kind != "deleted" {
		if _, err := os.Stat(absPath); err != nil {
			return err
		}
	}
	_, err = s.ScanForce(ctx, libraryID)
	return err
}

func (s *Scanner) scan(ctx context.Context, libraryID string, force bool) (Result, error) {
	s.mu.Lock()
	if _, ok := s.runs[libraryID]; ok {
		s.mu.Unlock()
		logging.Errorf("[LibraryScanner] Already scanning %s", libraryID) // LibraryScanner.js:53
		return Result{}, errors.New("scanner: library scan already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.runs[libraryID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.runs, libraryID); s.mu.Unlock() }()
	task := s.startScanTask(ctx, libraryID)
	forced := ""
	if force {
		forced = " (forced)"
	}
	logging.Infof("[LibraryScanner] Starting%s library scan %s for %s", forced, task.ID, task.Data.LibraryName) // LibraryScanner.js:86
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
	s.resetIssuesFilterData(ctx, libraryID) // LibraryController.js:1272
	return result, err
}

// ensureFilterData loads the library's filter data when not cached, as every
// upstream scan does before it reads authors and series.
func (s *Scanner) ensureFilterData(ctx context.Context, libraryID, mediaType string) {
	if s.filterLoader != nil {
		_ = s.filterLoader.EnsureFilterData(ctx, libraryID, mediaType)
	}
}

// resetIssuesFilterData is Database.resetLibraryIssuesFilterData.
func (s *Scanner) resetIssuesFilterData(ctx context.Context, libraryID string) {
	if s.filters == nil || !s.filters.Has(libraryID) {
		return
	}
	var n int
	if s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID).Scan(&n) == nil {
		s.filters.StoreNumIssues(libraryID, n)
	}
}

func (s *Scanner) startScanTask(ctx context.Context, libraryID string) *watcherTask {
	name := libraryID
	mediaType := ""
	var libraryName string
	if s.db.QueryRowContext(ctx, `SELECT name,mediaType FROM libraries WHERE id=?`, libraryID).Scan(&libraryName, &mediaType) == nil && libraryName != "" {
		name = libraryName
	}
	task := &watcherTask{
		ID: uuid.NewString(), Action: "library-scan", Data: watcherTaskData{LibraryID: libraryID, LibraryName: name, LibraryMediaType: mediaType},
		Title: fmt.Sprintf("Scanning %q library", name), TitleKey: "MessageTaskScanningLibrary", TitleSubs: []string{name},
		ShowSuccess: true, StartedAt: time.Now().UnixMilli(),
	}
	s.emitTask("task_started", task)
	return task
}

func (s *Scanner) finishScanTask(task *watcherTask, result Result, err error) {
	finishTask(task)
	elapsed := time.Now().UnixMilli() - task.StartedAt
	task.Data.ScanResults = &scanResults{Added: result.Added, Updated: result.Updated, Missing: &result.Missing, Elapsed: elapsed, Text: scanResultsText(result, elapsed)}
	if err != nil {
		failed, key := "Failed", "MessageTaskFailed"
		task.IsFailed, task.Error, task.ErrorKey = true, &failed, &key
	} else if result.Canceled {
		description, key := "Task canceled by user", "MessageTaskCanceledByUser"
		task.Description, task.DescriptionKey = &description, &key
	}
	s.emitTask("task_finished", task)
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

// scanResultsText is LibraryScan.scanResultsString (LibraryScan.js:61-68).
func scanResultsText(r Result, elapsedMs int64) string {
	var parts []string
	if r.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", r.Added))
	}
	if r.Updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", r.Updated))
	}
	if r.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", r.Missing))
	}
	changes := "No changes needed"
	if len(parts) > 0 {
		changes = strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%s (%s)", changes, elapsedPretty(float64(elapsedMs)/1000))
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
	mux.Handle("POST /api/items/batch/scan", adminOnly(s.batchScan))
	mux.Handle("POST /api/libraries/{id}/scan", adminOnly(s.scanLibraryRoute))
	mux.Handle("POST /api/items/{id}/scan", adminOnly(s.scanItemRoute))
}

func adminOnly(next httputil.Handler) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user, _ := store.UserFromContext(r.Context()); user.Type != "root" && user.Type != "admin" {
			return httputil.Status(http.StatusForbidden, "")
		}
		return next(w, r)
	}
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
			_, _ = s.ScanItemForce(context.Background(), item.ID)
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
		if force {
			_, _ = s.ScanForce(context.Background(), id)
		} else {
			_, _ = s.Scan(context.Background(), id)
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
	res, err := s.ScanItemForce(r.Context(), id)
	if err != nil && !res.Canceled {
		return &httputil.Error{Status: http.StatusInternalServerError, Cause: err}
	}
	status := "UPTODATE"
	if res.Added+res.Updated > 0 {
		status = "UPDATED"
	} else if res.Nothing {
		status = "NOTHING"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"result": status})
	return nil
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
	reset := func() { cancel(); s.mu.Lock(); s.watchCancel = nil; s.mu.Unlock() }
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		reset()
		return err
	}
	cronJobs := cron.New()
	libraries, err := s.watchedLibraries(watchCtx)
	if err != nil {
		_ = watcher.Close()
		reset()
		return err
	}
	fw := newFSWatcher(watchCtx, s)
	type rootRef struct{ libraryID string }
	rootToLibrary := make(map[string]rootRef)
	addDirs := func(root, from string) error {
		return filepath.WalkDir(from, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir") {
					return filepath.SkipDir
				}
				if addErr := watcher.Add(p); addErr != nil && !errors.Is(addErr, os.ErrNotExist) {
					return addErr
				}
			}
			return nil
		})
	}
	for _, lib := range libraries {
		if lib.disabled {
			logging.Infof("[Watcher] updateLibrary: Library %q watcher disabled", lib.name) // Watcher.js:136
		} else {
			logging.Infof("[Watcher] Initializing watcher for %q.", lib.name) // Watcher.js:58
			watch := &libraryWatch{id: lib.id, name: lib.name}
			for _, f := range lib.folders {
				root := filepath.Clean(f.path)
				watch.folders = append(watch.folders, watchedFolder{id: f.id, path: filepath.ToSlash(root)})
				rootToLibrary[root] = rootRef{lib.id}
				if walkErr := addDirs(root, root); walkErr != nil {
					_ = watcher.Close()
					cronJobs.Stop()
					reset()
					return fmt.Errorf("scanner: watch %q: %w", root, walkErr)
				}
			}
			fw.libraries[lib.id] = watch
			logging.Infof("[Watcher] %q Ready", lib.name) // Watcher.js:89
		}
		if strings.TrimSpace(lib.cron) != "" {
			id := lib.id
			if _, err = cronJobs.AddFunc(lib.cron, func() {
				go func() {
					if _, scanErr := s.Scan(watchCtx, id); scanErr != nil {
						logging.Errorf("[CronManager] Scheduled scan of library %s failed: %v", id, scanErr)
					}
				}()
			}); err != nil {
				_ = watcher.Close()
				cronJobs.Stop()
				reset()
				return fmt.Errorf("scanner: library %s cron: %w", id, err)
			}
		}
	}
	cronJobs.Start()
	libraryOf := func(p string) (string, string) {
		for root, ref := range rootToLibrary {
			if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
				return ref.libraryID, root
			}
		}
		return "", ""
	}
	go func() {
		defer watcher.Close()
		defer cronJobs.Stop()
		defer fw.stop()
		for {
			select {
			case <-watchCtx.Done():
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
						_ = addDirs(root, ev.Name)
						_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, e error) error {
							if e != nil {
								return nil
							}
							if d.IsDir() {
								if p != ev.Name && (strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir") {
									return filepath.SkipDir
								}
								return nil
							}
							fw.onFile(libraryID, filepath.ToSlash(p), "added")
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
	}()
	return nil
}
func (s *Scanner) Close() {
	s.mu.Lock()
	if s.watchCancel != nil {
		s.watchCancel()
		s.watchCancel = nil
	}
	for _, c := range s.runs {
		c()
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
		var settings map[string]any
		_ = json.Unmarshal(raw, &settings)
		l.cron, _ = settings["autoScanCronExpression"].(string)
		l.disabled, _ = settings["disableWatcher"].(bool)
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

func nowDB() string        { return dbtime.Now() }
func ms(t time.Time) int64 { return t.UnixMilli() }

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
	var raw []byte
	var extra []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,name,mediaType,settings,extraData FROM libraries WHERE id=?`, id).Scan(&l.id, &l.name, &l.mediaType, &raw, &extra)
	if err == nil {
		var settings map[string]any
		_ = json.Unmarshal(raw, &settings)
		l.audiobooksOnly, _ = settings["audiobooksOnly"].(bool)
		l.sortingPrefixes = []string{"the", "a"}
		l.metadataPrecedence = precedenceFromSettings(settings)
		var serverSettings []byte
		if s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&serverSettings) == nil {
			var parsed map[string]any
			_ = json.Unmarshal(serverSettings, &parsed)
			l.parseSubtitle, _ = parsed["scannerParseSubtitle"].(bool)
			if values, ok := parsed["sortingPrefixes"].([]any); ok {
				l.sortingPrefixes = l.sortingPrefixes[:0]
				for _, value := range values {
					if prefix, ok := value.(string); ok {
						l.sortingPrefixes = append(l.sortingPrefixes, prefix)
					}
				}
			}
		}
		var data map[string]any
		_ = json.Unmarshal(extra, &data)
		if values, ok := data["lastScanMetadataPrecedence"].([]any); ok {
			for _, value := range values {
				if name, ok := value.(string); ok {
					l.lastPrecedence = append(l.lastPrecedence, name)
				}
			}
		}
	}
	return l, err
}

func precedenceFromSettings(settings map[string]any) []string {
	values, ok := settings["metadataPrecedence"].([]any)
	if !ok || len(values) == 0 {
		return defaultMetadataPrecedence()
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok {
			return defaultMetadataPrecedence()
		}
		out = append(out, name)
	}
	return out
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
	s.ensureFilterData(ctx, libraryID, l.mediaType)
	groups := map[string]*group{}
	for _, f := range folders {
		root, err := filepath.Abs(filepath.Clean(f.path))
		if err != nil {
			return result, err
		}
		f.path = root
		ignoredDirs := map[string]bool{}
		var discovered []file
		err = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				if errors.Is(e, os.ErrNotExist) {
					return nil
				}
				return e
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				if p != root && (shouldIgnore(mustRel(root, p)) || ignoredDirs[p]) {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if d.Name() == ".ignore" {
				dir := filepath.Dir(p)
				if dir != root {
					ignoredDirs[dir] = true
					return filepath.SkipDir
				}
				return nil
			}
			for ignored := range ignoredDirs {
				if strings.HasPrefix(p, ignored+string(filepath.Separator)) {
					return nil
				}
			}
			ext := strings.ToLower(filepath.Ext(p))
			if filepath.Dir(rel) == "." && !(l.mediaType == "book" && (audioExt(ext) || (!l.audiobooksOnly && ebookExt(ext)))) {
				return nil
			}
			if shouldIgnore(rel) || (!audioExt(ext) && (l.mediaType != "book" || l.audiobooksOnly || !ebookExt(ext)) && !scannableCompanion(ext)) {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return nil
			}
			discovered = append(discovered, file{path: p, info: info})
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			continue
		}
		if errors.Is(err, context.Canceled) {
			break
		}
		// rra orders by depth ascending; within a depth, order is by path.
		sort.SliceStable(discovered, func(i, j int) bool {
			di, dj := strings.Count(mustRel(root, discovered[i].path), string(filepath.Separator)), strings.Count(mustRel(root, discovered[j].path), string(filepath.Separator))
			if di != dj {
				return di < dj
			}
			return discovered[i].path < discovered[j].path
		})
		var media, other []file
		for _, item := range discovered {
			ext := filepath.Ext(item.path)
			if strings.EqualFold(ext, ".epub") || ebookExt(ext) {
				if l.mediaType == "book" && !l.audiobooksOnly {
					media = append(media, item)
				} else {
					other = append(other, item)
				}
			} else if audioExt(ext) {
				media = append(media, item)
			} else {
				other = append(other, item)
			}
		}
		for _, item := range media {
			rel, _ := filepath.Rel(root, item.path)
			parts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
			if filepath.Dir(rel) == "." {
				parts = nil
			}
			keyRel := ""
			if len(parts) == 0 {
				keyRel = filepath.Base(item.path)
				g := &group{path: item.path, folder: f, isFile: true}
				item.relPath = filepath.Base(item.path)
				groups[item.path] = g
				g.files = append(g.files, item)
				continue
			}
			for i, part := range parts {
				keyRel = filepath.Join(keyRel, part)
				key := filepath.Join(root, keyRel)
				if g := groups[key]; g != nil {
					item.relPath = filepath.ToSlash(filepath.Join(filepath.Join(parts[i+1:]...), filepath.Base(item.path)))
					g.files = append(g.files, item)
					break
				}
				remaining := len(parts) - i - 1
				if remaining == 0 {
					item.relPath = filepath.Base(item.path)
					groups[key] = &group{path: key, folder: f, files: []file{item}}
					break
				}
				if remaining == 1 && cdDirectory.MatchString(parts[i+1]) {
					item.relPath = filepath.ToSlash(filepath.Join(parts[i+1], filepath.Base(item.path)))
					groups[key] = &group{path: key, folder: f, files: []file{item}}
					break
				}
			}
		}
		for _, item := range other {
			rel, _ := filepath.Rel(root, item.path)
			dirrel := filepath.ToSlash(filepath.Dir(rel))
			parts := strings.Split(dirrel, "/")
			if dirrel == "." {
				parts = nil
			}
			keyRel := ""
			for _, part := range parts {
				if part == "." {
					continue
				}
				keyRel = filepath.Join(keyRel, part)
				if g := groups[filepath.Join(root, keyRel)]; g != nil {
					item.relPath = filepath.ToSlash(mustRel(g.path, item.path))
					g.companions = append(g.companions, item)
					break
				}
			}
		}
	}
	existing, err := s.loadExistingItems(ctx, libraryID)
	if err != nil {
		return result, err
	}
	ordered := sortedGroups(groups)
	byPath := make(map[string]*group, len(ordered))
	for _, g := range ordered {
		byPath[g.path] = g
	}
	consumed := map[*group]bool{}
	tally := &scanTally{}
	var updatedItems []map[string]any
	emitUpdated := func() {
		if len(updatedItems) > 0 && s.emit != nil {
			s.emit("*", "items_updated", updatedItems)
		}
		updatedItems = nil
	}
	// Existing items first, in stored order, as LibraryScanner.scanLibrary does
	// (LibraryScanner.js:156-224).
	for _, ex := range existing {
		if ctx.Err() != nil {
			break
		}
		g := byPath[ex.path]
		if g != nil && consumed[g] {
			g = nil
		}
		if g == nil {
			g = matchGroupByInode(ex, ordered, consumed)
		}
		if g == nil {
			if l.mediaType == "podcast" {
				if _, statErr := os.Stat(ex.path); statErr == nil {
					continue
				}
			}
			result.Missing++
			scanDebug(l, "Library Item %q (inode: %s) is missing", ex.path, ex.ino) // LibraryScanner.js:187
			if !ex.missing {
				now := nowDB()
				if _, e := s.db.ExecContext(ctx, `UPDATE libraryItems SET isMissing=1,updatedAt=?,lastScan=?,lastScanVersion=? WHERE id=?`, now, now, "2.37.2", ex.id); e != nil {
					return result, e
				}
				if item, e := s.items.ItemJSON(ctx, ex.id, true); e == nil {
					updatedItems = append(updatedItems, item)
				}
			}
		} else {
			consumed[g] = true
			g.existingID = ex.id
			n, e := s.importGroup(ctx, l, g, force)
			if e != nil {
				return result, e
			}
			tally.merge(n)
			if n.updated {
				result.Updated++
				scanDebug(l, "Library item %q was updated", ex.path)
				if item, e := s.rescanItemJSON(ctx, n.id, n.scannedAudio); e == nil {
					updatedItems = append(updatedItems, item)
				}
			}
		}
		if len(updatedItems) == 10 {
			emitUpdated()
		}
	}
	emitUpdated()

	// Then new items.
	var addedItems []map[string]any
	for _, g := range ordered {
		if consumed[g] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		g.brandNew = true
		n, e := s.importGroup(ctx, l, g, force)
		if e != nil {
			return result, e
		}
		tally.merge(n)
		if n.added {
			result.Added++
			scanDebug(l, "Created new library item %q with id %q", mustRel(g.folder.path, g.path), n.id) // LibraryItemScanner.js:194
			if item, e := s.items.ItemJSON(ctx, n.id, true); e == nil {
				addedItems = append(addedItems, item)
			}
		}
		if len(addedItems) == 10 && s.emit != nil {
			s.emit("*", "items_added", addedItems)
			addedItems = nil
		}
	}
	if len(addedItems) > 0 && s.emit != nil {
		s.emit("*", "items_added", addedItems)
	}
	if ctx.Err() == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE libraries SET lastScan=?,lastScanVersion=? WHERE id=?`, nowDB(), "2.37.2", libraryID)
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
	id, path, ino string
	missing       bool
	files         []byte
}

// loadExistingItems returns the library's stored items in rowid order. The
// upstream query has no ORDER BY, so its order is whatever index SQLite picks;
// on the pinned stack that is insertion order.
func (s *Scanner) loadExistingItems(ctx context.Context, libraryID string) ([]existingItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,path,COALESCE(ino,''),COALESCE(isMissing,0),libraryFiles FROM libraryItems WHERE libraryId=? ORDER BY rowid`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []existingItem
	for rows.Next() {
		var e existingItem
		var missing int
		if err := rows.Scan(&e.id, &e.path, &e.ino, &missing, &e.files); err != nil {
			return nil, err
		}
		e.missing = missing != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func groupInodes(g *group) []string {
	var inos []string
	if info, err := os.Stat(g.path); err == nil {
		inos = append(inos, inode(info))
	}
	for _, f := range g.files {
		inos = append(inos, inode(f.info))
	}
	for _, f := range g.companions {
		inos = append(inos, inode(f.info))
	}
	return inos
}

// matchGroupByInode finds the unclaimed scanned group a stored item moved to
// (LibraryScanner.js:171-178).
func matchGroupByInode(ex existingItem, groups []*group, consumed map[*group]bool) *group {
	stored := parseStoredLibraryFiles(ex.files)
	for _, g := range groups {
		if consumed[g] {
			continue
		}
		inos := groupInodes(g)
		if ex.ino != "" && containsString(inos, ex.ino) {
			return g
		}
		for _, lf := range stored {
			if lf.Ino != "" && containsString(inos, lf.Ino) {
				return g
			}
		}
	}
	return nil
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
		if len(authors) > 0 && s.emit != nil {
			s.emit("*", "authors_num_books_updated", authorsNumBooksUpdated{LibraryID: libraryID, Authors: authors})
		}
	}
	for _, id := range uniqueStrings(t.authorsRemoved) {
		var n int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM authors WHERE id=? AND COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.authorId=authors.id)`, id).Scan(&n)
		if n == 0 {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, id); err != nil {
			return err
		}
		s.filters.RemoveAuthor(libraryID, id)
		if s.emit != nil {
			s.emit("*", "author_removed", map[string]any{"id": id, "libraryId": libraryID})
		}
	}
	for _, id := range uniqueStrings(t.seriesRemoved) {
		var n int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM series WHERE id=? AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.seriesId=series.id)`, id).Scan(&n)
		if n == 0 {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM series WHERE id=?`, id); err != nil {
			return err
		}
		s.filters.RemoveSeries(libraryID, id)
		if s.emit != nil {
			s.emit("*", "series_removed", map[string]any{"id": id, "libraryId": libraryID})
		}
	}
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
