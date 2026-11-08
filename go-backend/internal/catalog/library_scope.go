package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
	"encoding/json"
	"net/http"
)

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

// inLibrary runs httputil.OpenLibrary (LibraryController.middleware,
// LibraryController.js:1475-1499) for every route under /api/libraries/:id.
func (h *Handler) inLibrary(fn func(http.ResponseWriter, *http.Request, libraryScope) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user := store.RequestUser(r)
		lib, err := httputil.OpenLibrary(r, h.db, access.For(r.Context(), user).Library, r.PathValue("id"))
		if err != nil {
			return err
		}
		scope := libraryScope{id: lib.ID, name: lib.Name, mediaType: lib.MediaType, provider: lib.Provider, user: user, limit: lib.Limit, page: lib.Page, hasPage: lib.HasPage}
		_ = json.Unmarshal([]byte(lib.Settings), &scope.settings)
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
