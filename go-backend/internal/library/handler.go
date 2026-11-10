// Package library owns library identity, request scoping, the filter/sort
// compiler, library reads/writes and series/item browsing. Item detail,
// cover/file routes and item mutations remain in catalog.
package library

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/serversettings"
)

// Deps are the collaborators owned outside the library domain.
type Deps struct {
	Filters *filterdata.Cache
	// LibraryChanged refreshes watchers after a committed library change.
	LibraryChanged func(string)
	// RemoveFolderItems removes the items of a folder before that folder is
	// deleted. It stays a composition callback until item deletion moves.
	RemoveFolderItems func(context.Context, string, string) error
}

// Handler serves library reads and writes over db.
type Handler struct {
	db      *sql.DB
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

// New builds the library handler.
func New(db *sql.DB, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, emit: emit, filters: deps.Filters, deps: deps}
}

// Routes registers library-owned reads and writes.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/libraries", httputil.Handler(h.libraries))
	mux.Handle("POST /api/libraries", httputil.Handler(h.createLibrary))
	mux.Handle("POST /api/libraries/order", httputil.Handler(h.reorderLibraries))
	mux.Handle("PATCH /api/libraries/{id}", httputil.Handler(h.updateLibrary))
	mux.Handle("GET /api/libraries/{id}", InLibrary(h.db, h.library))
	mux.Handle("GET /api/libraries/{id}/stats", InLibrary(h.db, h.libraryStats))
	mux.Handle("GET /api/libraries/{id}/filterdata", InLibrary(h.db, h.libraryFilterData))
	mux.Handle("GET /api/libraries/{id}/opml", InLibrary(h.db, h.libraryOPML))
	mux.Handle("POST /api/libraries/{id}/remove-metadata", InLibrary(h.db, h.libraryRemoveMetadata))
	mux.Handle("GET /api/libraries/{id}/narrators", InLibrary(h.db, h.narrators))
	mux.Handle("PATCH /api/libraries/{id}/narrators/{narratorId}", InLibrary(h.db, h.updateNarrator))
	mux.Handle("DELETE /api/libraries/{id}/narrators/{narratorId}", InLibrary(h.db, h.updateNarrator))
	mux.Handle("GET /api/libraries/{id}/podcast-titles", InLibrary(h.db, h.podcastTitles))
	mux.Handle("GET /api/series/{id}", httputil.Handler(h.seriesFindOne))
	mux.Handle("PATCH /api/series/{id}", httputil.Handler(h.seriesUpdate))
	mux.Handle("GET /api/libraries/{id}/authors", InLibrary(h.db, h.authors))
	mux.Handle("GET /api/libraries/{id}/items", InLibrary(h.db, h.libraryItems))
	mux.Handle("GET /api/libraries/{id}/personalized", InLibrary(h.db, h.personalized))
	mux.Handle("GET /api/libraries/{id}/search", InLibrary(h.db, h.search))
	mux.Handle("GET /api/libraries/{id}/series", InLibrary(h.db, h.series))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", InLibrary(h.db, h.seriesByID))
}

func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
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

func (h *Handler) minifiedItems(ctx context.Context, ids []string) ([]items.Minified, error) {
	loaded, err := items.Load(ctx, h.db, ids, items.Options{})
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]items.Minified, len(loaded))
	for i, item := range loaded {
		out[i] = item.Minified(prefixes)
	}
	return out, nil
}

func (h *Handler) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func pageOf[T any](list []T, page, limit int) []T {
	start := page * limit
	if start > len(list) {
		return []T{}
	}
	return list[start:min(start+limit, len(list))]
}

func naturalLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
