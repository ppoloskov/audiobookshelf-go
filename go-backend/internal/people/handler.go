// Package people implements author/series identity detail, CRUD, merge/delete
// and metadata matching. Library-scoped author/series lists remain in catalog
// until the library packet.
package people

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/wire"
)

// AuthorFinder is AuthorFinder.js: the metadata provider lookups behind
// POST /api/authors/:id/match.
type AuthorFinder interface {
	FindAuthorByASIN(ctx context.Context, asin, region string) *wire.AuthorMatch
	FindAuthorByName(ctx context.Context, name, region string) *wire.AuthorMatch
}

// Deps are the collaborators owned outside the people domain.
type Deps struct {
	Filters         *filterdata.Cache
	Authors         AuthorFinder
	WriteMetadata   func(context.Context, string) error
	PurgeImageCache func(string)
	SaveAuthorImage func(context.Context, string, string) (string, string)
}

type Handler struct {
	db      *sql.DB
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

func New(db *sql.DB, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, emit: emit, filters: deps.Filters, deps: deps}
}

// Routes registers the author/series entity routes. Author image and
// library-scoped list routes remain in catalog for the next slice.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/authors/{id}", httputil.Handler(h.authorFindOne))
	mux.Handle("PATCH /api/authors/{id}", httputil.Handler(h.authorUpdate))
	mux.Handle("DELETE /api/authors/{id}", httputil.Handler(h.authorDelete))
	mux.Handle("POST /api/authors/{id}/match", httputil.Handler(h.authorMatch))
	mux.Handle("GET /api/series/{id}", httputil.Handler(h.seriesFindOne))
	mux.Handle("PATCH /api/series/{id}", httputil.Handler(h.seriesUpdate))
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

func (h *Handler) expandedItems(ctx context.Context, ids []string) ([]items.Expanded, error) {
	loaded, err := items.Load(ctx, h.db, ids, items.Options{Episodes: true})
	if err != nil {
		return nil, err
	}
	prefixes := h.sortingPrefixes(ctx)
	out := make([]items.Expanded, len(loaded))
	for i, item := range loaded {
		out[i] = item.Expanded(prefixes)
	}
	return out, nil
}

func (h *Handler) saveMetadataFiles(ctx context.Context, itemIDs []string) error {
	if h.deps.WriteMetadata == nil {
		return nil
	}
	for _, id := range itemIDs {
		if err := h.deps.WriteMetadata(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func feedMinified(ctx context.Context, db *sql.DB, entityType, entityID string) (feeds.Minified, bool, error) {
	feed, err := sqlc.New(db).GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityType: entityType, EntityID: entityID})
	if errors.Is(err, sql.ErrNoRows) {
		return feeds.Minified{}, false, nil
	}
	if err != nil {
		return feeds.Minified{}, false, err
	}
	return feeds.FromSummary(feed), true, nil
}
