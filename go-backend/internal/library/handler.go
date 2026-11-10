// Package library owns library identity, request scoping, the filter/sort
// compiler, library metadata reads and series browsing. Item browsing and
// discovery move here in the next slices; writes/admin remain in catalog.
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

// Deps are the collaborators owned outside the library read domain.
type Deps struct {
	Filters *filterdata.Cache
}

// Handler serves library-scoped reads over db.
type Handler struct {
	db      *sql.DB
	emit    events.Emitter
	filters *filterdata.Cache
}

// New builds the library handler.
func New(db *sql.DB, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, emit: emit, filters: deps.Filters}
}

// Routes registers the library-owned reads.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/libraries", httputil.Handler(h.libraries))
	mux.Handle("GET /api/libraries/{id}", InLibrary(h.db, h.library))
	mux.Handle("GET /api/libraries/{id}/stats", InLibrary(h.db, h.libraryStats))
	mux.Handle("GET /api/libraries/{id}/filterdata", InLibrary(h.db, h.libraryFilterData))
	mux.Handle("GET /api/libraries/{id}/narrators", InLibrary(h.db, h.narrators))
	mux.Handle("GET /api/libraries/{id}/podcast-titles", InLibrary(h.db, h.podcastTitles))
	mux.Handle("GET /api/series/{id}", httputil.Handler(h.seriesFindOne))
	mux.Handle("PATCH /api/series/{id}", httputil.Handler(h.seriesUpdate))
	mux.Handle("GET /api/libraries/{id}/series", InLibrary(h.db, h.series))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", InLibrary(h.db, h.seriesByID))
}

func (h *Handler) sortingPrefixes(ctx context.Context) []string {
	return serversettings.Load(ctx, h.db).SortingPrefixes()
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
