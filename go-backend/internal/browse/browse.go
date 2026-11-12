// Package browse serves the read side of a library: the filtered and sorted
// item list, the personalized shelves, search, and the series list and detail
// (plus the series PATCH). Items are rendered by internal/item; this package
// only decides which items, in which order, with which extras.
package browse

import (
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/settings"
)

// Handler serves the library browsing routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
	filters  *filterdata.Cache
}

// New returns a Handler. Only series_updated is emitted. filters is the filter
// menu cache the series rename invalidates.
func New(d *db.DB, s *settings.Store, emit events.Emitter, filters *filterdata.Cache) *Handler {
	return &Handler{db: d, settings: s, emit: emit, filters: filters}
}

// Routes registers the routes. Authorisation: every /api/libraries/{id}/...
// route needs access to the library (403) and an existing library (404);
// /api/series/{id} needs an authenticated user who may see one of the series'
// books (else 404) and PATCH also the update permission.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/libraries/{id}/items", library.InLibrary(h.db, h.items))
	mux.Handle("GET /api/libraries/{id}/personalized", library.InLibrary(h.db, h.personalized))
	mux.Handle("GET /api/libraries/{id}/search", library.InLibrary(h.db, h.search))
	mux.Handle("GET /api/libraries/{id}/series", library.InLibrary(h.db, h.seriesList))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", library.InLibrary(h.db, h.seriesInLibrary))
	mux.Handle("GET /api/series/{id}", h.authed(h.seriesGet))
	mux.Handle("PATCH /api/series/{id}", h.authed(h.seriesPatch))
}

type userHandler func(w http.ResponseWriter, r *http.Request, user *auth.Principal) error

// authed runs next for the authenticated principal (401 without one).
func (h *Handler) authed(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user := auth.User(r.Context()); user != nil {
			return next(w, r, user)
		}
		return httpx.Error(http.StatusUnauthorized, "")
	}
}

// includes is the normalized include= query: lower case, trimmed, no empties.
func includes(r *http.Request) []string {
	var out []string
	for value := range strings.SplitSeq(r.URL.Query().Get("include"), ",") {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func page[T any](list []T, pageNo, limit int) []T {
	start := min(pageNo*limit, len(list))
	return list[start:min(start+limit, len(list))]
}

// isAdmin is an active root or admin user (the web client's admin checks).
func isAdmin(user *auth.Principal) bool { return user.IsAdmin() && bool(user.IsActive) }
