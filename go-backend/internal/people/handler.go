// Package people owns author/series identity, projections, CRUD, merge/delete,
// metadata matching and author image handling.
package people

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
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
	Filters             *filterdata.Cache
	Authors             AuthorFinder
	WriteMetadata       func(context.Context, string) error
	SeriesMatchesFilter func(context.Context, string, string, store.User) (bool, error)
}

type Handler struct {
	db      *sql.DB
	cfg     config.Config
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

func New(db *sql.DB, cfg config.Config, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, filters: deps.Filters, deps: deps}
}

// Routes registers author/series entity, image and library-scoped list routes.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/authors/{id}", httputil.Handler(h.authorFindOne))
	mux.Handle("PATCH /api/authors/{id}", httputil.Handler(h.authorUpdate))
	mux.Handle("DELETE /api/authors/{id}", httputil.Handler(h.authorDelete))
	mux.Handle("POST /api/authors/{id}/match", httputil.Handler(h.authorMatch))
	mux.Handle("GET /api/series/{id}", httputil.Handler(h.seriesFindOne))
	mux.Handle("PATCH /api/series/{id}", httputil.Handler(h.seriesUpdate))
	mux.Handle("GET /api/libraries/{id}/authors", library.InLibrary(h.db, h.authors))
	mux.Handle("GET /api/libraries/{id}/series", library.InLibrary(h.db, h.series))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", library.InLibrary(h.db, h.seriesByID))
	mux.Handle("GET /api/authors/{id}/image", httputil.Handler(h.authorImage))
	mux.Handle("POST /api/authors/{id}/image", httputil.Handler(h.authorUploadImage))
	mux.Handle("DELETE /api/authors/{id}/image", httputil.Handler(h.authorDeleteImage))
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

func (h *Handler) seriesMatchesFilter(ctx context.Context, seriesID, raw string, user store.User) (bool, error) {
	if h.deps.SeriesMatchesFilter == nil {
		return true, nil
	}
	return h.deps.SeriesMatchesFilter(ctx, seriesID, raw, user)
}

func (h *Handler) feedMinified(ctx context.Context, entityType, entityID string) (feeds.Minified, bool, error) {
	return feedMinified(ctx, h.db, entityType, entityID)
}

func pageOf[T any](list []T, page, limit int) []T {
	start := page * limit
	if start > len(list) {
		return []T{}
	}
	return list[start:min(start+limit, len(list))]
}

func naturalLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }

func normalizedInclude(r *http.Request) string {
	values := strings.Split(r.URL.Query().Get("include"), ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, ",")
}

func includeHas(r *http.Request, value string) bool {
	return slices.Contains(strings.Split(normalizedInclude(r), ","), value)
}
