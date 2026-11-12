// Package scan imports media found in the library folders: the library scan,
// the single item rescan, the file watcher and cron schedules that trigger
// them, the metadata sidecar readers and the metadata.json writer.
//
// Every path goes through the same stages: walk the disk into candidate item
// folders, pair them with the stored items (match.go), gather and resolve
// their metadata by the library's precedence, plan, apply in one transaction
// and announce the result (import.go). The watcher re-walks only the folders a
// file event touched and feeds the same stages.
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

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/version"
)

// Result is what a scan changed.
type Result struct {
	Added, Updated, Missing int
	Canceled                bool
	Nothing                 bool // an item rescan that found no files
}

// Scanner scans libraries. Build one with New, call Start once the routes are
// mounted and Close when done.
type Scanner struct {
	db       *db.DB
	settings *settings.Store
	cfg      config.Config
	emit     events.Emitter
	filters  *filterdata.Cache
	items    *item.Handler // author and series links, orphan sweeps, episode removal

	mu    sync.Mutex
	runs  map[string]*run    // the running scan of each library
	stop  context.CancelFunc // ends the watchers and schedules of Start
	watch *watcher
}

// New returns a Scanner. A nil emit discards events.
func New(d *db.DB, st *settings.Store, emit events.Emitter, filters *filterdata.Cache, cfg config.Config) *Scanner {
	return &Scanner{db: d, settings: st, cfg: cfg, emit: emit, filters: filters, runs: map[string]*run{},
		items: item.New(d, st, emit, item.Deps{Filters: filters})}
}

// run is one registered scan of a library; done closes when it ends.
type run struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// begin registers a running scan of the library; at most one runs at a time
// (library scans, item scans and watcher batches alike). The returned func
// ends it and the context ends when Cancel is called.
func (s *Scanner) begin(ctx context.Context, libraryID string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.runs[libraryID]; busy {
		slog.Error("library is already being scanned", "library", libraryID)
		return ctx, nil, errors.New("scan: library scan already running")
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
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.runs, libraryID)
		s.mu.Unlock()
		close(r.done)
	}, nil
}

// Cancel stops the running scan of the library, if any.
func (s *Scanner) Cancel(libraryID string) {
	s.mu.Lock()
	r := s.runs[libraryID]
	s.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// Close stops the watchers, the schedules and every running scan.
func (s *Scanner) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	for _, r := range s.runs {
		r.cancel()
	}
}

// defaultPrecedence is the metadata precedence of a library that sets none.
var defaultPrecedence = []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}

// library is what a scan needs of a libraries row and the server settings.
type library struct {
	id, name, mediaType string
	audiobooksOnly      bool
	precedence          []string
	lastPrecedence      []string // the precedence of the previous scan
	parseSubtitle       bool
	prefixes            []string // sorting prefixes
	cron                string
	disableWatcher      bool
}

func (s *Scanner) loadLibrary(ctx context.Context, id string) (library, error) {
	var row db.Library
	if err := s.db.Get(ctx, &row, `SELECT * FROM libraries WHERE id = ?`, id); err != nil {
		return library{}, err
	}
	return s.libraryOf(row), nil
}

func (s *Scanner) libraryOf(row db.Library) library {
	server := s.settings.Server()
	settings := row.Settings.V
	l := library{id: row.ID, name: db.Val(row.Name), mediaType: db.Val(row.MediaType), audiobooksOnly: db.Val(settings.AudiobooksOnly),
		precedence: settings.MetadataPrecedence, parseSubtitle: server.ScannerParseSubtitle, prefixes: server.SortingPrefixes,
		cron: db.Val(settings.AutoScanCronExpression), disableWatcher: settings.DisableWatcher}
	if len(l.precedence) == 0 {
		l.precedence = slices.Clone(defaultPrecedence)
	}
	if extra := row.ExtraData.V; extra != nil {
		l.lastPrecedence = extra.LastScanMetadataPrecedence
	}
	return l
}

func (s *Scanner) folders(ctx context.Context, libraryID string) ([]db.LibraryFolder, error) {
	var folders []db.LibraryFolder
	err := s.db.Select(ctx, &folders, `SELECT * FROM libraryFolders WHERE libraryId = ? ORDER BY createdAt`, libraryID)
	return folders, err
}

// storedItems are the library's items in the order they were created.
func (s *Scanner) storedItems(ctx context.Context, libraryID string) ([]db.LibraryItem, error) {
	var items []db.LibraryItem
	err := s.db.Select(ctx, &items, `SELECT * FROM libraryItems WHERE libraryId = ? ORDER BY rowid`, libraryID)
	return items, err
}

// view loads an item as the realtime events carry it.
func (s *Scanner) view(ctx context.Context, itemID string) (item.Expanded, error) {
	it, err := item.Get(ctx, s.db, itemID, item.Options{Episodes: true})
	if err != nil {
		return item.Expanded{}, err
	}
	return it.Expanded(s.settings.Server().SortingPrefixes), nil
}

// Scan scans the library; force rescans items even when no file changed.
func (s *Scanner) Scan(ctx context.Context, libraryID string, force bool) (Result, error) {
	ctx, end, err := s.begin(ctx, libraryID)
	if err != nil {
		return Result{}, err
	}
	defer end()
	task := s.startScanTask(ctx, libraryID)
	slog.Info("library scan started", "task", task.ID, "library", libraryID, "forced", force)
	res, err := s.scanLibrary(ctx, libraryID, force)
	res.Canceled = errors.Is(ctx.Err(), context.Canceled)
	slog.Info("library scan finished", "task", task.ID, "after", time.Since(time.UnixMilli(task.StartedAt)).Round(time.Second), "added", res.Added,
		"updated", res.Updated, "missing", res.Missing, "canceled", res.Canceled, "error", err)
	s.finishScanTask(task, res, err)
	s.filters.ResetIssues(ctx, s.db.SQL(), libraryID)
	return res, err
}

// scanLibrary walks every folder of the library, pairs the candidates with the
// stored items, rescans the paired ones (stored order), marks the unpaired
// ones missing and imports the candidates nobody claimed (path order).
func (s *Scanner) scanLibrary(ctx context.Context, libraryID string, force bool) (Result, error) {
	var res Result
	lib, err := s.loadLibrary(ctx, libraryID)
	if err != nil {
		return res, fmt.Errorf("library %q: %w", libraryID, err)
	}
	// A changed metadata precedence rescans every book (Library.js lastScanMetadataPrecedence).
	force = force || (lib.mediaType == "book" && !slices.Equal(lib.precedence, lib.lastPrecedence))
	folders, err := s.folders(ctx, libraryID)
	if err != nil {
		return res, err
	}
	if len(folders) == 0 {
		slog.Warn("library has no folders to scan", "library", lib.name)
		return res, errors.New("scan: library has no folders")
	}
	_, _ = s.filters.Get(ctx, s.db.SQL(), libraryID, lib.mediaType) // loads the cache that AddBook patches; a failure only means a lazy load later
	var cands []*candidate
	for _, folder := range folders {
		root, err := filepath.Abs(db.Val(folder.Path))
		if err != nil {
			return res, err
		}
		folder.Path = &root
		files, err := walkFiles(ctx, root, lib.wantFile)
		if errors.Is(err, context.Canceled) {
			break
		}
		if err != nil {
			slog.Error("walking library folder failed", "folder", root, "error", err)
			continue
		}
		cands = append(cands, lib.candidates(folder, files)...)
	}
	slices.SortFunc(cands, func(a, b *candidate) int { return strings.Compare(a.path, b.path) })
	stored, err := s.storedItems(ctx, libraryID)
	if err != nil {
		return res, err
	}
	matched, fresh := matchItems(stored, cands)
	var tally scanTally
	var updated []item.Expanded
	var newlyMissing []string
	flush := func() {
		if len(updated) > 0 {
			s.emit.ToAll("items_updated", updated)
		}
		updated = nil
	}
	for i := range stored {
		if ctx.Err() != nil {
			break
		}
		ex := &stored[i]
		switch c := matched[i]; {
		case c == nil && lib.mediaType == "podcast" && exists(db.Val(ex.Path)):
			// a podcast folder that is still on disk is never missing
		case c == nil:
			res.Missing++
			if ex.IsMissing {
				break
			}
			if err := s.markMissing(ctx, ex); err != nil {
				return res, err
			}
			newlyMissing = append(newlyMissing, ex.ID)
			if v, err := s.view(ctx, ex.ID); err == nil {
				updated = append(updated, v)
			}
		default:
			out, err := s.importItem(ctx, lib, c, ex, force)
			if err != nil {
				return res, err
			}
			tally.add(out.links)
			if out.updated {
				res.Updated++
				if v, err := s.view(ctx, out.id); err == nil {
					updated = append(updated, v)
				}
			}
		}
		if len(updated) == 10 {
			flush()
		}
	}
	flush()
	if len(newlyMissing) > 0 {
		if _, err := s.db.Exec(ctx, `UPDATE libraryItems SET lastScan = ?, lastScanVersion = ? WHERE id IN (?)`, db.Now(), version.Server, newlyMissing); err != nil {
			return res, err
		}
	}
	var added []item.Expanded
	for _, c := range fresh {
		if ctx.Err() != nil {
			break
		}
		out, err := s.importItem(ctx, lib, c, nil, force)
		if err != nil {
			return res, err
		}
		tally.add(out.links)
		if out.added {
			res.Added++
			if v, err := s.view(ctx, out.id); err == nil {
				added = append(added, v)
			}
		}
		if len(added) == 10 {
			s.emit.ToAll("items_added", added)
			added = nil
		}
	}
	if len(added) > 0 {
		s.emit.ToAll("items_added", added)
	}
	if ctx.Err() != nil {
		return res, nil
	}
	if err := s.recordScan(ctx, lib); err != nil {
		return res, err
	}
	s.finalize(ctx, libraryID, tally)
	return res, nil
}

// recordScan stamps the library with this scan and, for books, the precedence
// it used.
func (s *Scanner) recordScan(ctx context.Context, lib library) error {
	var row db.Library
	if err := s.db.Get(ctx, &row, `SELECT * FROM libraries WHERE id = ?`, lib.id); err != nil {
		return err
	}
	row.LastScan, row.LastScanVersion = db.Now(), db.Ptr(version.Server)
	cols := []string{"lastScan", "lastScanVersion"}
	if lib.mediaType == "book" {
		if row.ExtraData.V == nil {
			row.ExtraData.V = &db.LibraryExtra{}
		}
		row.ExtraData.V.LastScanMetadataPrecedence = lib.precedence
		cols = append(cols, "extraData")
	}
	return s.db.Update(ctx, "libraries", &row, cols...)
}

// markMissing flags a stored item whose files are gone.
func (s *Scanner) markMissing(ctx context.Context, ex *db.LibraryItem) error {
	ex.IsMissing, ex.UpdatedAt = true, db.Now()
	return s.db.Update(ctx, "libraryItems", ex, "isMissing", "updatedAt")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func uniqueIDs(list []string) []string {
	out := slices.Sorted(slices.Values(list))
	return slices.DeleteFunc(slices.Compact(out), func(id string) bool { return id == "" })
}

func slogError(what string, err error) { slog.Error("scan: "+what, "error", err) }
