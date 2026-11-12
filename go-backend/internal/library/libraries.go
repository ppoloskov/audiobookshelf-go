// Package library serves the library management routes (LibraryController.js,
// v2.37.2): the library list, create, update, order and delete, a library's
// stats, filter data, authors, narrators, podcast titles, OPML export, zip
// download, match-all and the cleanup of metadata files and items with issues.
// Item browsing (items, shelves, search, series) lives elsewhere; it uses Scope
// and InLibrary to open a library.
package library

import (
	"context"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

// Deps are the collaborators the library routes use outside this package.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner. Required.
	Filters *filterdata.Cache
	// LibraryChanged refreshes the scanner's watchers and schedules after a
	// committed change of a library's folders or watcher setting. May be nil.
	LibraryChanged func(libraryID string)
	// QuickMatch quick matches one item for the match-all task. May be nil
	// (every item then counts as an error).
	QuickMatch func(ctx context.Context, itemID string, opts item.MatchOptions) (item.MatchResult, error)
	// Items deletes items (item.Handler.DeleteRows) for library delete, issue
	// cleanup and folder removal. It must share Filters. Required.
	Items *item.Handler
}

// Handler serves the library routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	deps     Deps
}

// New returns a Handler.
func New(d *db.DB, settings *settings.Store, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: d, settings: settings, emit: emit, deps: deps}
}

// Routes registers the routes. Authorisation: POST /api/libraries, POST
// .../order, PATCH and DELETE /api/libraries/{id} are admin-only (403 before
// anything else). Every other /api/libraries/{id}/... route opens the library
// first (403 without access to it, 404 when unknown); matchall, issues,
// remove-metadata and podcast-titles then need an admin, narrator edits the
// update permission and download the download permission.
func (h *Handler) Routes(mux *http.ServeMux) {
	inLibrary := func(fn func(http.ResponseWriter, *http.Request, Scope) error) httpx.Handler {
		return InLibrary(h.db, fn)
	}
	mux.Handle("GET /api/libraries", h.authed(h.list))
	mux.Handle("POST /api/libraries", auth.Admin(httpx.Handler(h.create)))
	mux.Handle("POST /api/libraries/order", auth.Admin(httpx.Handler(h.reorder)))
	mux.Handle("PATCH /api/libraries/{id}", auth.Admin(httpx.Handler(h.update)))
	mux.Handle("DELETE /api/libraries/{id}", auth.Admin(httpx.Handler(h.remove)))
	mux.Handle("GET /api/libraries/{id}", inLibrary(h.get))
	mux.Handle("GET /api/libraries/{id}/stats", inLibrary(h.stats))
	mux.Handle("GET /api/libraries/{id}/filterdata", inLibrary(h.filterData))
	mux.Handle("GET /api/libraries/{id}/opml", inLibrary(h.opml))
	mux.Handle("POST /api/libraries/{id}/remove-metadata", inLibrary(h.removeMetadata))
	mux.Handle("GET /api/libraries/{id}/narrators", inLibrary(h.narrators))
	mux.Handle("PATCH /api/libraries/{id}/narrators/{narratorId}", inLibrary(h.renameNarrator))
	mux.Handle("DELETE /api/libraries/{id}/narrators/{narratorId}", inLibrary(h.removeNarrator))
	mux.Handle("GET /api/libraries/{id}/podcast-titles", inLibrary(h.podcastTitles))
	mux.Handle("GET /api/libraries/{id}/authors", inLibrary(h.authors))
	mux.Handle("DELETE /api/libraries/{id}/issues", inLibrary(h.removeIssues))
	mux.Handle("GET /api/libraries/{id}/matchall", inLibrary(h.matchAll))
	mux.Handle("GET /api/libraries/{id}/download", inLibrary(h.download))
}

// authed runs next for the authenticated principal (401 without one).
func (h *Handler) authed(next func(http.ResponseWriter, *http.Request, *auth.Principal) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user := auth.User(r.Context()); user != nil {
			return next(w, r, user)
		}
		return httpx.Error(http.StatusUnauthorized, "")
	}
}

// changed tells the scanner to refresh its watchers and schedules.
func (h *Handler) changed(libraryID string) {
	if h.deps.LibraryChanged != nil {
		h.deps.LibraryChanged(libraryID)
	}
}

// isAdmin is the admin check of the routes that open the library first
// (auth.Admin cannot run before the library check there).
func isAdmin(user *auth.Principal) bool { return user.IsAdmin() && bool(user.IsActive) }

// selectAll runs a query into a non-nil slice, so an empty result encodes as [].
func selectAll[T any](ctx context.Context, d *db.DB, query string, args ...any) ([]T, error) {
	rows := []T{}
	err := d.Select(ctx, &rows, query, args...)
	return rows, err
}
