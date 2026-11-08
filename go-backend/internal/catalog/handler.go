// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/wire"
	"context"
	"database/sql"
	"strings"
)

// DownloadQueue reports a library item's podcast episode downloads: the ones
// queued and the one running now when it belongs to the item.
type DownloadQueue interface {
	EpisodeDownloads(libraryItemID string) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload)
}

type emitFunc func(userID, event string, payload any)

// Handler reads library and media catalog data from an existing database.
type Handler struct {
	db             *sql.DB
	cfg            config.Config
	emit           emitFunc
	libraryChanged func(libraryID string)
	itemChanged    func(context.Context, string) error
	filters        *filterdata.Cache
	downloads      DownloadQueue
	authorFinder   AuthorFinder
	finder         BookFinder
	episodes       func(ctx context.Context, itemID string, overrideDetails bool) (int, error)
	// podcastScheduleChanged is CronManager.checkUpdatePodcastCron.
	podcastScheduleChanged func(ctx context.Context, itemID string)
}

// Option configures optional collaborators of New.
type Option func(*Handler)

// WithDownloadQueue supplies the podcast download queue that
// GET /api/items/:id?include=downloads reports.
func WithDownloadQueue(queue DownloadQueue) Option {
	return func(h *Handler) { h.downloads = queue }
}

// New constructs a catalog handler. The emit callback is used for events from
// catalog write routes.
func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any), options ...Option) *Handler {
	h := &Handler{db: db, cfg: cfg, emit: emit, filters: filterdata.NewCache()}
	for _, option := range options {
		option(h)
	}
	return h
}

// FilterCache exposes the library filter data cache to the scanner, which
// patches it the way upstream's scanner does.
func (h *Handler) FilterCache() *filterdata.Cache { return h.filters }

// EnsureFilterData loads a library's filter data when none is cached, as
// libraryFilters.getFilterData does at the start of every scan.
func (h *Handler) EnsureFilterData(ctx context.Context, libraryID, mediaType string) error {
	_, err := h.filters.Get(ctx, h.db, libraryID, mediaType)
	return err
}

// SetLibraryChanged installs the scanner watcher refresh callback. It runs
// only after a catalog library mutation commits successfully.
func (h *Handler) SetLibraryChanged(callback func(libraryID string)) { h.libraryChanged = callback }

// SetItemChanged installs the source-compatible metadata sidecar callback for
// media mutations. It runs only after the catalog row has been saved.
func (h *Handler) SetItemChanged(callback func(context.Context, string) error) {
	h.itemChanged = callback
}

// loadItems loads ids with one libitem.Load; missing ids are skipped.
func (h *Handler) loadItems(ctx context.Context, ids []string, episodes bool) ([]libitem.Item, error) {
	return libitem.Load(ctx, h.db, ids, libitem.Options{Episodes: episodes})
}

// minifiedItems renders ids in the shelf shape in id order; a missing id is
// sql.ErrNoRows.
func (h *Handler) minifiedItems(ctx context.Context, ids []string) ([]libitem.Minified, error) {
	items, err := h.loadItems(ctx, ids, false)
	if err != nil {
		return nil, err
	}
	if len(items) != len(ids) {
		return nil, sql.ErrNoRows
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]libitem.Minified, len(items))
	for i, it := range items {
		out[i] = it.Minified(prefixes)
	}
	return out, nil
}

// expandedItems renders ids in the expanded shape in id order; missing ids are
// skipped.
func (h *Handler) expandedItems(ctx context.Context, ids []string) ([]libitem.Expanded, error) {
	items, err := h.loadItems(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]libitem.Expanded, len(items))
	for i, it := range items {
		out[i] = it.Expanded(prefixes)
	}
	return out, nil
}

// sortingPrefixes reads the server's sortingPrefixes setting.
func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
}

func stringValue(v any) string { s, _ := v.(string); return s }

func naturalLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }

// pageOf is the page of list that starts at page*limit.
func pageOf[T any](list []T, page, limit int) []T {
	start := page * limit
	if start > len(list) {
		return []T{}
	}
	return list[start:min(start+limit, len(list))]
}
