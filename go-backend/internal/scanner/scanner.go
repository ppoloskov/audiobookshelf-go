// Package scanner imports media discovered in configured library folders.
// It writes only the existing Audiobookshelf schema; it never creates or
// migrates database tables.
package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type EmitFunc func(userID, event string, payload any)

type Result struct {
	Added, Updated, Missing int
	Canceled                bool
	Nothing                 bool
}

type Scanner struct {
	db                 *sql.DB
	cfg                config.Config
	emit               EmitFunc
	mu                 sync.Mutex
	runs               map[string]context.CancelFunc
	watchCancel        context.CancelFunc
	podcastFeedFetcher func(context.Context, string) ([]map[string]any, error)
	items              items.Reader
}

func New(db *sql.DB, cfg config.Config, emit EmitFunc, itemReader items.Reader) (*Scanner, error) {
	if db == nil {
		return nil, errors.New("scanner: database is nil")
	}
	if itemReader == nil {
		return nil, errors.New("scanner: item reader is nil")
	}
	return &Scanner{db: db, cfg: cfg, emit: emit, runs: make(map[string]context.CancelFunc), items: itemReader}, nil
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
			if item, e := s.items.ItemJSON(ctx, n.id, true); e == nil {
				s.emit("*", "item_updated", item)
			}
		}
	}
	if err := s.cleanupAssociations(ctx, libraryID); err != nil {
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
		return Result{}, errors.New("scanner: library scan already running")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.runs[libraryID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.runs, libraryID); s.mu.Unlock() }()
	task := s.startScanTask(ctx, libraryID)
	result, err := s.scanLibrary(ctx, libraryID, force)
	if errors.Is(ctx.Err(), context.Canceled) {
		result.Canceled = true
	}
	s.finishScanTask(task, result, err)
	return result, err
}

func (s *Scanner) startScanTask(ctx context.Context, libraryID string) map[string]any {
	name := libraryID
	mediaType := ""
	var libraryName string
	if s.db.QueryRowContext(ctx, `SELECT name,mediaType FROM libraries WHERE id=?`, libraryID).Scan(&libraryName, &mediaType) == nil && libraryName != "" {
		name = libraryName
	}
	now := time.Now().UnixMilli()
	task := map[string]any{"id": uuid.NewString(), "action": "library-scan", "data": map[string]any{"libraryId": libraryID, "libraryName": name, "libraryMediaType": mediaType}, "title": fmt.Sprintf("Scanning %q library", name), "titleKey": "MessageTaskScanningLibrary", "titleSubs": []string{name}, "description": nil, "descriptionKey": nil, "descriptionSubs": nil, "error": nil, "errorKey": nil, "errorSubs": nil, "showSuccess": true, "isFailed": false, "isFinished": false, "startedAt": now, "finishedAt": nil}
	if s.emit != nil {
		startSnapshot := make(map[string]any, len(task))
		for key, value := range task {
			startSnapshot[key] = value
		}
		s.emit("*", "task_started", startSnapshot)
	}
	return task
}

func (s *Scanner) finishScanTask(task map[string]any, result Result, err error) {
	task["isFinished"] = true
	task["finishedAt"] = time.Now().UnixMilli()
	task["data"].(map[string]any)["scanResults"] = map[string]any{"added": result.Added, "updated": result.Updated, "missing": result.Missing}
	if err != nil {
		task["isFailed"] = true
		task["error"] = "Failed"
		task["errorKey"] = "MessageTaskFailed"
	} else if result.Canceled {
		task["description"] = "Task canceled by user"
		task["descriptionKey"] = "MessageTaskCanceledByUser"
	} else {
		task["description"] = nil
		task["descriptionKey"] = nil
		task["descriptionSubs"] = nil
	}
	if s.emit != nil {
		s.emit("*", "task_finished", task)
	}
}

func (s *Scanner) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	if r.Method != http.MethodPost {
		return false
	}
	p := path.Clean(r.URL.Path)
	if p == "/api/items/batch/scan" {
		if user.Type != "root" && user.Type != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return true
		}
		var body struct {
			LibraryItemIDs []string `json:"libraryItemIds"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.LibraryItemIDs) == 0 {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return true
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
			http.Error(w, "Items not found", http.StatusBadRequest)
			return true
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
		return true
	}
	if !strings.HasPrefix(p, "/api/libraries/") && !strings.HasPrefix(p, "/api/items/") {
		return false
	}
	if strings.HasPrefix(p, "/api/libraries/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/libraries/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "scan" {
			return false
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(p, "/api/items/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] != "scan" {
			return false
		}
	}
	if user.Type != "root" && user.Type != "admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return true
	}
	if strings.HasPrefix(p, "/api/libraries/") {
		parts := strings.Split(strings.TrimPrefix(p, "/api/libraries/"), "/")
		id, err := url.PathUnescape(parts[0])
		if err != nil {
			http.Error(w, "Bad library id", 400)
			return true
		}
		if _, err := s.library(r.Context(), id); err != nil {
			http.Error(w, "Library not found", 404)
			return true
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
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/api/items/"), "/")
	var isFile int
	err := s.db.QueryRowContext(r.Context(), `SELECT isFile FROM libraryItems WHERE id=?`, parts[0]).Scan(&isFile)
	if err != nil {
		http.Error(w, "Item not found", 404)
		return true
	}
	if isFile != 0 {
		http.Error(w, "Cannot scan a library file", http.StatusInternalServerError)
		return true
	}
	res, err := s.ScanItemForce(r.Context(), parts[0])
	if err != nil && !res.Canceled {
		http.Error(w, err.Error(), 500)
		return true
	}
	status := "UPTODATE"
	if res.Added+res.Updated > 0 {
		status = "UPDATED"
	} else if res.Nothing {
		status = "NOTHING"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"result": status})
	return true
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
	rootToID := make(map[string]string)
	for _, lib := range libraries {
		for _, root := range lib.roots {
			root = filepath.Clean(root)
			rootToID[root] = lib.id
			if !lib.disabled {
				walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
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
				if walkErr != nil {
					_ = watcher.Close()
					cronJobs.Stop()
					reset()
					return fmt.Errorf("scanner: watch %q: %w", root, walkErr)
				}
			}
		}
		if strings.TrimSpace(lib.cron) != "" {
			id := lib.id
			if _, err = cronJobs.AddFunc(lib.cron, func() { go func() { _, _ = s.Scan(watchCtx, id) }() }); err != nil {
				_ = watcher.Close()
				cronJobs.Stop()
				reset()
				return fmt.Errorf("scanner: library %s cron: %w", id, err)
			}
		}
	}
	cronJobs.Start()
	go func() {
		defer watcher.Close()
		defer cronJobs.Stop()
		timers := map[string]*time.Timer{}
		for {
			select {
			case <-watchCtx.Done():
				for _, timer := range timers {
					timer.Stop()
				}
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
					continue
				}
				if ev.Op&fsnotify.Create != 0 {
					if st, statErr := os.Stat(ev.Name); statErr == nil && st.IsDir() {
						_ = filepath.WalkDir(ev.Name, func(p string, d os.DirEntry, e error) error {
							if e != nil {
								return nil
							}
							if d.IsDir() {
								if strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir" {
									return filepath.SkipDir
								}
								_ = watcher.Add(p)
							}
							return nil
						})
					}
				}
				dir := filepath.Dir(ev.Name)
				for root, id := range rootToID {
					if dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator)) || ev.Name == root {
						if timer := timers[id]; timer != nil {
							timer.Stop()
						}
						timers[id] = time.AfterFunc(750*time.Millisecond, func() { _, _ = s.Scan(watchCtx, id) })
						break
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
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
	id, cron string
	disabled bool
	roots    []string
}

func (s *Scanner) watchedLibraries(ctx context.Context) ([]watchedLibrary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,settings FROM libraries`)
	if err != nil {
		return nil, err
	}
	var out []watchedLibrary
	for rows.Next() {
		var l watchedLibrary
		var raw []byte
		if err := rows.Scan(&l.id, &raw); err != nil {
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
		for _, f := range fs {
			out[i].roots = append(out[i].roots, f.path)
		}
	}
	return out, nil
}

func nowDB() string        { return dbtime.Now() }
func ms(t time.Time) int64 { return t.UnixMilli() }

type library struct {
	id, mediaType      string
	audiobooksOnly     bool
	metadataPrecedence []string
	lastPrecedence     []string
	parseSubtitle      bool
	sortingPrefixes    []string
}
type folder struct{ id, path string }

func (s *Scanner) library(ctx context.Context, id string) (library, error) {
	var l library
	var raw []byte
	var extra []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,mediaType,settings,extraData FROM libraries WHERE id=?`, id).Scan(&l.id, &l.mediaType, &raw, &extra)
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
		return result, errors.New("scanner: library has no folders")
	}
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
	seen := map[string]bool{}
	var addedItems, updatedItems []map[string]any
	for _, g := range sortedGroups(groups) {
		if err := ctx.Err(); err != nil {
			break
		}
		n, e := s.importGroup(ctx, l, g, force)
		if e != nil {
			return result, e
		}
		seen[n.id] = true
		if n.added {
			result.Added++
			if item, e := s.items.ItemJSON(ctx, n.id, true); e == nil {
				addedItems = append(addedItems, item)
			}
		} else if n.updated {
			result.Updated++
			if item, e := s.items.ItemJSON(ctx, n.id, true); e == nil {
				updatedItems = append(updatedItems, item)
			}
		}
	}
	if s.emit != nil {
		emitChunks := func(event string, items []map[string]any) {
			for len(items) > 0 {
				n := 10
				if len(items) < n {
					n = len(items)
				}
				s.emit("*", event, items[:n])
				items = items[n:]
			}
		}
		emitChunks("items_added", addedItems)
		emitChunks("items_updated", updatedItems)
	}
	if ctx.Err() == nil {
		rows, err := s.db.QueryContext(ctx, `SELECT id,path FROM libraryItems WHERE libraryId=? AND isFile=0`, libraryID)
		if err != nil {
			return result, err
		}
		type old struct{ id, path string }
		var olds []old
		for rows.Next() {
			var o old
			if e := rows.Scan(&o.id, &o.path); e != nil {
				rows.Close()
				return result, e
			}
			olds = append(olds, o)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		rows.Close()
		for _, o := range olds {
			if seen[o.id] {
				continue
			}
			if l.mediaType == "podcast" {
				var exists bool
				if _, statErr := os.Stat(o.path); statErr == nil {
					exists = true
				}
				if exists {
					continue
				}
			}
			result.Missing++
			var missing int
			if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(isMissing,0) FROM libraryItems WHERE id=?`, o.id).Scan(&missing); err != nil {
				return result, err
			}
			if missing == 0 {
				now := nowDB()
				if _, e := s.db.ExecContext(ctx, `UPDATE libraryItems SET isMissing=1,updatedAt=?,lastScan=?,lastScanVersion=? WHERE id=?`, now, now, "2.37.2", o.id); e != nil {
					return result, e
				}
			}
		}
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
		if err := s.cleanupAssociations(ctx, libraryID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Scanner) cleanupAssociations(ctx context.Context, libraryID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM authors WHERE libraryId=? AND COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.authorId=authors.id)`, libraryID)
	if err != nil {
		return err
	}
	var authors []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		authors = append(authors, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range authors {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM authors WHERE id=?`, id); err != nil {
			return err
		}
		if s.emit != nil {
			s.emit("*", "author_removed", map[string]any{"id": id, "libraryId": libraryID})
		}
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id FROM series WHERE libraryId=? AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.seriesId=series.id)`, libraryID)
	if err != nil {
		return err
	}
	var series []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		series = append(series, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range series {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM series WHERE id=?`, id); err != nil {
			return err
		}
		if s.emit != nil {
			s.emit("*", "series_removed", map[string]any{"id": id, "libraryId": libraryID})
		}
	}
	return nil
}

func mustRel(root, p string) string { r, _ := filepath.Rel(root, p); return r }
