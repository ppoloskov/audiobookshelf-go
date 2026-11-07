package lists

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
)

// libraryListPagination is LibraryController.middleware's access check and
// pagination coercion (server/controllers/LibraryController.js:1475-1497):
// 403 before the existence lookup, 404 "Library not found", then limit and
// page must be non-negative integers.
func (h *Handler) libraryListPagination(r *http.Request, user store.User, libraryID string) (limit, page int, err error) {
	if !h.canAccessLibrary(user, libraryID) {
		return 0, 0, httputil.Status(http.StatusForbidden, "")
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=?`, libraryID).Scan(&exists); err != nil {
		return 0, 0, httputil.Internal("database read failed", err)
	}
	if exists == 0 {
		return 0, 0, httputil.Status(http.StatusNotFound, "Library not found")
	}
	values := map[string]int{}
	for _, key := range []string{"limit", "page"} {
		raw, present := r.URL.Query()[key]
		if !present {
			continue
		}
		// Number(v) with isNaN -> 0; then Number.isInteger and >= 0.
		n, err := strconv.ParseFloat(strings.TrimSpace(raw[0]), 64)
		if strings.TrimSpace(raw[0]) == "" {
			n, err = 0, nil
		}
		if err != nil || math.IsNaN(n) {
			n = 0
		}
		if n != math.Trunc(n) || n < 0 || math.IsInf(n, 0) {
			return 0, 0, httputil.Status(http.StatusBadRequest, "Invalid request. "+key+" must be a positive integer")
		}
		values[key] = int(n)
	}
	return values["limit"], values["page"], nil
}

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
	limit, page, err := h.libraryListPagination(r, user, libraryID)
	if err != nil {
		return err
	}
	include := make([]string, 0)
	for _, v := range strings.Split(r.URL.Query().Get("include"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			include = append(include, v)
		}
	}
	ids, err := h.queries().ListLibraryCollectionIDs(r.Context(), libraryID)
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
			feed, err := h.collectionRSSFeed(r, id)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			if feed != nil {
				dto["rssFeed"] = feed
			}
		}
		collections = append(collections, dto)
	}
	payload := wire.LibraryCollectionsPage{
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
	limit, page, err := h.libraryListPagination(r, user, libraryID)
	if err != nil {
		return err
	}
	ids, err := h.queries().ListUserLibraryPlaylistIDs(r.Context(), sqlc.ListUserLibraryPlaylistIDsParams{UserID: user.ID, LibraryID: libraryID})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type namedPlaylist struct {
		name string
		dto  any
	}
	playlists := make([]namedPlaylist, 0, len(ids))
	for _, id := range ids {
		item, _, err := Playlist(r.Context(), h.db, h.items, id)
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
	return httputil.JSON(w, r, wire.LibraryPlaylistsPage{Results: results, Total: len(playlists), Limit: limit, Page: page})
}
