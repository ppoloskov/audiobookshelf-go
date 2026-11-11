// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
)

// Deps are the collaborators catalog routes use outside this package. They are
// interfaces and functions defined here and satisfied by feature packages,
// because feature packages do not import each other; the composition root
// supplies them to New. Filters is required; every other field is optional.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner.
	Filters *filterdata.Cache
	// ItemChanged ends a media mutation: the metadata sidecar file and the
	// item_updated event of one library item.
	ItemChanged func(context.Context, string) error
	// LibraryChanged refreshes the scanner's watcher and schedules after a
	// library mutation committed.
	LibraryChanged func(libraryID string)
	// QuickMatch quick matches one library item for the library match-all task.
	QuickMatch func(ctx context.Context, itemID string, opts item.MatchOptions) (item.MatchResult, error)
}

type Handler struct {
	db      *sql.DB
	cfg     config.Config
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

// New's emit callback sends events from catalog write routes.
func New(db *sql.DB, cfg config.Config, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, filters: deps.Filters, deps: deps}
}

func (h *Handler) loadItems(ctx context.Context, ids []string, episodes bool) ([]items.Item, error) {
	return items.Load(ctx, h.db, ids, items.Options{Episodes: episodes})
}

// expandedItems renders ids in the expanded shape in id order; missing ids are
// skipped.
func (h *Handler) expandedItems(ctx context.Context, ids []string) ([]items.Expanded, error) {
	loaded, err := h.loadItems(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]items.Expanded, len(loaded))
	for i, it := range loaded {
		out[i] = it.Expanded(prefixes)
	}
	return out, nil
}

func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
}
