// Package library owns library identity, scoping and browsing. This first slice
// provides the shared /api/libraries/:id request scope; browse/search/shelves
// operations migrate here in the next slice.
package library

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

// Settings is the library's stored settings object.
type Settings struct {
	HideSingleBookSeries               bool `json:"hideSingleBookSeries"`
	OnlyShowLaterBooksInContinueSeries bool `json:"onlyShowLaterBooksInContinueSeries"`
	SkipMatchingMediaWithAsin          bool `json:"skipMatchingMediaWithAsin"`
	SkipMatchingMediaWithIsbn          bool `json:"skipMatchingMediaWithIsbn"`
}

// Scope is the library of a /api/libraries/:id/* request after
// LibraryController.middleware, with the requesting user and the request's
// limit and page (0 when absent, as the middleware converts them).
type Scope struct {
	ID, Name, MediaType, Provider string
	Settings                      Settings
	User                          store.User
	Limit, Page                   int
	HasPage                       bool
}

// InLibrary runs httputil.OpenLibrary (LibraryController.middleware,
// LibraryController.js:1475-1499) for every route under /api/libraries/:id.
func InLibrary(db *sql.DB, fn func(http.ResponseWriter, *http.Request, Scope) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user := store.RequestUser(r)
		lib, err := httputil.OpenLibrary(r, db, access.For(r.Context(), user).Library, r.PathValue("id"))
		if err != nil {
			return err
		}
		scope := Scope{ID: lib.ID, Name: lib.Name, MediaType: lib.MediaType, Provider: lib.Provider, User: user, Limit: lib.Limit, Page: lib.Page, HasPage: lib.HasPage}
		_ = json.Unmarshal([]byte(lib.Settings), &scope.Settings)
		return fn(w, r, scope)
	}
}

// MediaTable is the table of the library's media; other types are not served.
func (s Scope) MediaTable() (string, error) {
	if table := MediaTableOf(s.MediaType); table != "" {
		return table, nil
	}
	return "", httputil.Status(http.StatusNotImplemented, "unsupported library media type")
}

// MediaTableOf is the storage table of a media type.
func MediaTableOf(mediaType string) string {
	return map[string]string{"book": "books", "podcast": "podcasts"}[mediaType]
}
