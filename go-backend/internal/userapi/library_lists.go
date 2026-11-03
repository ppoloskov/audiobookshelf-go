package userapi

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
)

// libraryListPagination is LibraryController.middleware's access check and
// pagination coercion (server/controllers/LibraryController.js:1475-1497):
// 403 before the existence lookup, 404 "Library not found", then limit and
// page must be non-negative integers. It reports false after writing an
// error response.
func (h *Handler) libraryListPagination(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) (limit, page int, ok bool) {
	if !canAccessLibrary(permissions(user), libraryID) {
		httputil.SendStatus(w, http.StatusForbidden)
		return 0, 0, false
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=?`, libraryID).Scan(&exists); err != nil {
		logging.RequestError(r, "database read failed", err)
		http.Error(w, "database read failed", 500)
		return 0, 0, false
	}
	if exists == 0 {
		httputil.SendText(w, http.StatusNotFound, "Library not found")
		return 0, 0, false
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
			httputil.SendText(w, http.StatusBadRequest, "Invalid request. "+key+" must be a positive integer")
			return 0, 0, false
		}
		values[key] = int(n)
	}
	return values["limit"], values["page"], true
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
func (h *Handler) libraryCollections(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	limit, page, ok := h.libraryListPagination(w, r, user, libraryID)
	if !ok {
		return
	}
	include := make([]string, 0)
	for _, v := range strings.Split(r.URL.Query().Get("include"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			include = append(include, v)
		}
	}
	ids, err := h.queryIDs(r, `SELECT id FROM collections WHERE libraryId=? ORDER BY createdAt ASC`, libraryID)
	if err != nil {
		logging.RequestError(r, "database read failed", err)
		http.Error(w, "database read failed", 500)
		return
	}
	collections := make([]any, 0, len(ids))
	for _, id := range ids {
		dto, _, hasVisible, err := h.collectionDTO(r.Context(), id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			logging.RequestError(r, "database read failed", err)
			http.Error(w, "database read failed", 500)
			return
		}
		if !hasVisible {
			continue
		}
		// Collection.getOldCollectionsJsonExpanded attaches the full feed
		// (feed.toOldJSON), not the minified one.
		if slices.Contains(include, "rssfeed") {
			feed, err := h.collectionRSSFeed(r, id)
			if err != nil {
				logging.RequestError(r, "database read failed", err)
				http.Error(w, "database read failed", 500)
				return
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
	httputil.WriteJSON(w, 200, payload)
}

// libraryPlaylists serves GET /api/libraries/:id/playlists
// (LibraryController.getUserPlaylistsForLibrary, server/controllers/LibraryController.js:853).
func (h *Handler) libraryPlaylists(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	limit, page, ok := h.libraryListPagination(w, r, user, libraryID)
	if !ok {
		return
	}
	ids, err := h.queryIDs(r, `SELECT id FROM playlists WHERE userId=? AND libraryId=?`, user.ID, libraryID)
	if err != nil {
		logging.RequestError(r, "database read failed", err)
		http.Error(w, "database read failed", 500)
		return
	}
	type namedPlaylist struct {
		name string
		dto  any
	}
	playlists := make([]namedPlaylist, 0, len(ids))
	for _, id := range ids {
		item, _, err := h.playlistJSON(r, id, user)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			logging.RequestError(r, "database read failed", err)
			http.Error(w, "database read failed", 500)
			return
		}
		playlists = append(playlists, namedPlaylist{name: stringValue(item["name"]), dto: item})
	}
	// Playlist.getOldPlaylistsForUserAndLibrary sorts by name.localeCompare
	// (server/models/Playlist.js:85); V8's sort is stable.
	sort.SliceStable(playlists, func(i, j int) bool { return textsort.LocaleCompare(playlists[i].name, playlists[j].name) < 0 })
	results := make([]any, 0, len(playlists))
	for _, p := range paginate(playlists, limit, page) {
		results = append(results, p.dto)
	}
	httputil.WriteJSON(w, 200, wire.LibraryPlaylistsPage{Results: results, Total: len(playlists), Limit: limit, Page: page})
}

// queryIDs materializes a single-column id query and closes the cursor before
// callers open further queries on the one-connection pool.
func (h *Handler) queryIDs(r *http.Request, query string, args ...any) ([]string, error) {
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
