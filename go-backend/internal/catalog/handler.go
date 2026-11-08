// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
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

// Deps are the collaborators catalog routes use outside this package. They are
// interfaces and functions defined here and satisfied by feature packages,
// because feature packages do not import each other; the composition root
// supplies them to New. Every field is optional.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner; New
	// makes its own when nil.
	Filters *filterdata.Cache
	// Downloads reports the podcast downloads GET /api/items/:id?include=downloads lists.
	Downloads DownloadQueue
	// ItemChanged ends a media mutation: the metadata sidecar file and the
	// item_updated event of one library item.
	ItemChanged func(context.Context, string) error
	// LibraryChanged refreshes the scanner's watcher and schedules after a
	// library mutation committed.
	LibraryChanged func(libraryID string)
	// Authors looks authors up at the metadata provider (POST /api/authors/:id/match).
	Authors AuthorFinder
	// Books searches the metadata providers for a quick match.
	Books BookFinder
	// PodcastEpisodes quick matches a podcast's unmatched episodes against its
	// feed (Scanner.quickMatchPodcastEpisodes); it returns the number updated.
	PodcastEpisodes func(ctx context.Context, itemID string, overrideDetails bool) (int, error)
	// PodcastScheduleChanged re-evaluates a podcast's auto-download cron entry
	// after PATCH /api/items/:id/media changed its schedule or enabled flag
	// (CronManager.checkUpdatePodcastCron, LibraryItemController.js:276).
	PodcastScheduleChanged func(ctx context.Context, itemID string)
}

type Handler struct {
	db      *sql.DB
	cfg     config.Config
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

type Option func(*Handler)

func WithDeps(deps Deps) Option {
	return func(h *Handler) {
		h.deps = deps
		if deps.Filters != nil {
			h.filters = deps.Filters
		}
	}
}

// New's emit callback sends events from catalog write routes.
func New(db *sql.DB, cfg config.Config, emit events.Emitter, options ...Option) *Handler {
	h := &Handler{db: db, cfg: cfg, emit: emit, filters: filterdata.NewCache()}
	for _, option := range options {
		option(h)
	}
	return h
}

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

func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
}

func stringValue(v any) string { s, _ := v.(string); return s }

func naturalLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }

func pageOf[T any](list []T, page, limit int) []T {
	start := page * limit
	if start > len(list) {
		return []T{}
	}
	return list[start:min(start+limit, len(list))]
}
