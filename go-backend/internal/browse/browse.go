// Package browse serves the read side of a library: the filtered and sorted
// item list, the personalized shelves, search, and the series list and detail
// (plus the series PATCH). Items are rendered by internal/item; this package
// only decides which items, in which order, with which extras.
package browse

import (
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
)

// Handler serves the library browsing routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
}

// New returns a Handler. Only series_updated is emitted.
func New(d *db.DB, s *settings.Store, emit events.Emitter) *Handler {
	return &Handler{db: d, settings: s, emit: emit}
}

// Routes registers the routes. Authorisation: every /api/libraries/{id}/...
// route needs access to the library (403) and an existing library (404);
// /api/series/{id} needs an authenticated user who may see one of the series'
// books (else 404) and PATCH also the update permission.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/libraries/{id}/items", h.inLibrary(h.items))
	mux.Handle("GET /api/libraries/{id}/personalized", h.inLibrary(h.personalized))
	mux.Handle("GET /api/libraries/{id}/search", h.inLibrary(h.search))
	mux.Handle("GET /api/libraries/{id}/series", h.inLibrary(h.seriesList))
	mux.Handle("GET /api/libraries/{id}/series/{seriesId}", h.inLibrary(h.seriesInLibrary))
	mux.Handle("GET /api/series/{id}", h.authed(h.seriesGet))
	mux.Handle("PATCH /api/series/{id}", h.authed(h.seriesPatch))
}

// scope is the library of a /api/libraries/{id}/... request with the
// requesting user and the request's limit and page (0 when absent).
// TODO(cleanup): use libraries.Scope
type scope struct {
	lib         db.Library
	user        *auth.Principal
	limit, page int
}

func (s scope) podcast() bool { return db.Val(s.lib.MediaType) == "podcast" }

// mediaTable is the table of the library's media; other media types are not
// served (501).
func (s scope) mediaTable() (string, error) {
	switch db.Val(s.lib.MediaType) {
	case "book":
		return "books", nil
	case "podcast":
		return "podcasts", nil
	}
	return "", httpx.Error(http.StatusNotImplemented, "unsupported library media type")
}

// inLibrary is LibraryController.middleware (js:1475-1499): 403 for a library
// the user cannot access, 404 for an unknown one, then limit and page must be
// non-negative integers (400).
func (h *Handler) inLibrary(next func(http.ResponseWriter, *http.Request, scope) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, id := auth.User(r.Context()), r.PathValue("id")
		if !user.CanAccessLibrary(id) {
			slog.Warn("library not accessible to the requesting user", "library", id)
			return httpx.Forbidden()
		}
		s := scope{user: user}
		switch err := h.db.Get(r.Context(), &s.lib, `SELECT * FROM libraries WHERE id = ?`, id); {
		case errors.Is(err, sql.ErrNoRows):
			return httpx.Error(http.StatusNotFound, "Library not found")
		case err != nil:
			return httpx.Internal("database read failed", err)
		}
		var err error
		if s.limit, err = countParam(r, "limit"); err != nil {
			return err
		}
		if s.page, err = countParam(r, "page"); err != nil {
			return err
		}
		return next(w, r, s)
	}
}

// countParam reads a non-negative integer query parameter: absent or
// non-numeric is 0 (as upstream's Number()), a fraction or negative is a 400.
func countParam(r *http.Request, key string) (int, error) {
	raw, present := r.URL.Query()[key]
	if !present {
		return 0, nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(raw[0]), 64)
	if err != nil || math.IsNaN(n) {
		return 0, nil
	}
	if n != math.Trunc(n) || n < 0 || math.IsInf(n, 0) {
		return 0, httpx.BadRequest("Invalid request. " + key + " must be a positive integer")
	}
	return int(n), nil
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
