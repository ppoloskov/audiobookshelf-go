package lists

import (
	"audiobookshelf-compat/internal/access"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/feeds"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/textsort"
)

func paginate[T any](items []T, limit, page int) []T {
	if limit <= 0 {
		return items
	}
	start := page * limit
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

// libraryCollections serves GET /api/libraries/:id/collections
// (LibraryController.getCollectionsForLibrary, server/controllers/LibraryController.js:814).
func (h *Handler) libraryCollections(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("libraryID")
	lib, err := httputil.OpenLibrary(r, h.db, access.For(r.Context(), user).Library, libraryID)
	if err != nil {
		return err
	}
	limit, page := lib.Limit, lib.Page
	include := make([]string, 0)
	for _, v := range strings.Split(r.URL.Query().Get("include"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			include = append(include, v)
		}
	}
	ids, err := sqlc.New(h.db).ListLibraryCollectionIDs(r.Context(), libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	collections := make([]any, 0, len(ids))
	for _, id := range ids {
		dto, _, err := h.collection(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		// Collection.getOldCollectionsJsonExpanded attaches the full feed
		// (feed.toOldJSON), not the minified one.
		if slices.Contains(include, "rssfeed") {
			feed, err := feeds.OfEntity(r.Context(), h.db, "collection", id)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			if feed != nil {
				dto["rssFeed"] = feed
			}
		}
		collections = append(collections, dto)
	}
	payload := LibraryCollectionsPage{
		Results: paginate(collections, limit, page), Total: len(collections), Limit: limit, Page: page,
		SortDesc: r.URL.Query().Get("desc") == "1", Minified: r.URL.Query().Get("minified") == "1",
		Include: strings.Join(include, ","),
	}
	if v, present := r.URL.Query()["sort"]; present {
		payload.SortBy = &v[0]
	}
	if v, present := r.URL.Query()["filter"]; present {
		payload.FilterBy = &v[0]
	}
	return httputil.JSON(w, r, payload)
}

// libraryPlaylists serves GET /api/libraries/:id/playlists
// (LibraryController.getUserPlaylistsForLibrary, server/controllers/LibraryController.js:853).
func (h *Handler) libraryPlaylists(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("libraryID")
	lib, err := httputil.OpenLibrary(r, h.db, access.For(r.Context(), user).Library, libraryID)
	if err != nil {
		return err
	}
	limit, page := lib.Limit, lib.Page
	ids, err := sqlc.New(h.db).ListUserLibraryPlaylistIDs(r.Context(), sqlc.ListUserLibraryPlaylistIDsParams{UserID: user.ID, LibraryID: libraryID})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type namedPlaylist struct {
		name string
		dto  any
	}
	playlists := make([]namedPlaylist, 0, len(ids))
	for _, id := range ids {
		item, _, err := Playlist(r.Context(), h.db, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		playlists = append(playlists, namedPlaylist{name: jsonvalue.String(item["name"]), dto: item})
	}
	// Playlist.getOldPlaylistsForUserAndLibrary sorts by name.localeCompare
	// (server/models/Playlist.js:85); V8's sort is stable.
	sort.SliceStable(playlists, func(i, j int) bool { return textsort.LocaleCompare(playlists[i].name, playlists[j].name) < 0 })
	results := make([]any, 0, len(playlists))
	for _, p := range paginate(playlists, limit, page) {
		results = append(results, p.dto)
	}
	return httputil.JSON(w, r, LibraryPlaylistsPage{Results: results, Total: len(playlists), Limit: limit, Page: page})
}
