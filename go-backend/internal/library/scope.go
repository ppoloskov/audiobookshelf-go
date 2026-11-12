package library

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
	"audiobookshelf-compat/internal/httpx"
)

// Scope is the library of a /api/libraries/{id}/... request after
// LibraryController.middleware: the library row (settings decoded in
// Settings.V), the requesting user and the request's limit and page.
type Scope struct {
	db.Library
	User *auth.Principal
	// Limit and Page are the query values, 0 when absent. HasPage tells that
	// page was present.
	Limit, Page int
	HasPage     bool
}

// MediaTable is the table of the library's media: books or podcasts. Other
// media types are not served (501).
func (s Scope) MediaTable() (string, error) {
	switch db.Val(s.MediaType) {
	case "book":
		return "books", nil
	case "podcast":
		return "podcasts", nil
	}
	return "", httpx.Error(http.StatusNotImplemented, "unsupported library media type")
}

// InLibrary runs Open before fn.
func InLibrary(d *db.DB, fn func(http.ResponseWriter, *http.Request, Scope) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		scope, err := Open(d, r)
		if err != nil {
			return err
		}
		return fn(w, r, scope)
	}
}

// Open is LibraryController.middleware (js:1475-1499) for the library named by
// the {id} path value: 401 without a user, 403 when the user may not access
// the library, 404 when it does not exist, then the optional limit and page
// query values must be non-negative integers (the text read as a number,
// non-numeric text as 0), else 400.
func Open(d *db.DB, r *http.Request) (Scope, error) {
	user := auth.User(r.Context())
	if user == nil {
		return Scope{}, httpx.Error(http.StatusUnauthorized, "")
	}
	id := r.PathValue("id")
	scope := Scope{User: user}
	if !user.CanAccessLibrary(id) {
		slog.Warn("library not accessible to the requesting user", "library", id)
		return scope, httpx.Forbidden()
	}
	switch err := d.Get(r.Context(), &scope.Library, `SELECT * FROM libraries WHERE id = ?`, id); {
	case errors.Is(err, sql.ErrNoRows):
		return scope, httpx.Error(http.StatusNotFound, "Library not found")
	case err != nil:
		return scope, httpx.Internal("database read failed", err)
	}
	for _, q := range []struct {
		key string
		dst *int
	}{{"limit", &scope.Limit}, {"page", &scope.Page}} {
		raw, present := r.URL.Query()[q.key]
		if !present {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(raw[0]), 64)
		if err != nil || math.IsNaN(n) {
			n = 0
		}
		if n != math.Trunc(n) || n < 0 || math.IsInf(n, 0) {
			return scope, httpx.BadRequest("Invalid request. " + q.key + " must be a positive integer")
		}
		*q.dst = int(n)
		scope.HasPage = scope.HasPage || q.key == "page"
	}
	return scope, nil
}

// Podcast reports whether the library holds podcasts.
func (s Scope) Podcast() bool { return db.Val(s.MediaType) == "podcast" }
