package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// librarySettings are the settings keys catalog routes read from a library row.
type librarySettings struct {
	HideSingleBookSeries               bool `json:"hideSingleBookSeries"`
	OnlyShowLaterBooksInContinueSeries bool `json:"onlyShowLaterBooksInContinueSeries"`
	SkipMatchingMediaWithAsin          bool `json:"skipMatchingMediaWithAsin"`
	SkipMatchingMediaWithIsbn          bool `json:"skipMatchingMediaWithIsbn"`
}

// libraryScope is the library of a /api/libraries/:id/* request after
// LibraryController.middleware, with the requesting user and the request's
// limit and page (0 when absent, as the middleware converts them).
type libraryScope struct {
	id, name, mediaType, provider string
	settings                      librarySettings
	user                          store.User
	limit, page                   int
	hasPage                       bool
}

// inLibrary is LibraryController.middleware (LibraryController.js:1475-1499):
// 403 for a library the user cannot access, 404 'Library not found', and 400
// when limit or page is not a non-negative integer. Every route under
// /api/libraries/:id passes through it.
func (h *Handler) inLibrary(fn func(http.ResponseWriter, *http.Request, libraryScope) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		scope := libraryScope{id: r.PathValue("id"), user: requestUser(r)}
		if !access.For(r.Context(), scope.user).Library(scope.id) {
			logging.Warnf("[LibraryController] Library %s not accessible to user %s", scope.id, scope.user.Username)
			return httputil.Status(http.StatusForbidden, "")
		}
		var settings sql.NullString
		err := h.db.QueryRowContext(r.Context(), `SELECT name,mediaType,provider,settings FROM libraries WHERE id=?`, scope.id).Scan(&scope.name, &scope.mediaType, &scope.provider, &settings)
		if errors.Is(err, sql.ErrNoRows) {
			return httputil.Status(http.StatusNotFound, "Library not found")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		_ = json.Unmarshal([]byte(settings.String), &scope.settings)
		for _, key := range []string{"limit", "page"} {
			raw, present := r.URL.Query()[key]
			if !present {
				continue
			}
			// `!isNaN(v) ? Number(v) : 0`, then it must be a non-negative integer.
			n, parseErr := strconv.ParseFloat(strings.TrimSpace(raw[0]), 64)
			if parseErr != nil {
				n = 0
			}
			if n != float64(int64(n)) || n < 0 {
				return httputil.Status(http.StatusBadRequest, "Invalid request. "+key+" must be a positive integer")
			}
			if key == "limit" {
				scope.limit = int(n)
			} else {
				scope.page, scope.hasPage = int(n), true
			}
		}
		return fn(w, r, scope)
	}
}

// mediaTable is the table of the library's media; other types are not served.
func (s libraryScope) mediaTable() (string, error) {
	if table := mediaTableOf(s.mediaType); table != "" {
		return table, nil
	}
	return "", httputil.Status(http.StatusNotImplemented, "unsupported library media type")
}
