package library

import (
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
)

func (h *Handler) narrators(w http.ResponseWriter, r *http.Request, lib Scope) error {
	libraryID := lib.ID
	rows, err := h.db.QueryContext(r.Context(), `SELECT b.narrators FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=? AND json_array_length(COALESCE(b.narrators,'[]'))>0`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	counts := map[string]int{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		seen := map[string]bool{}
		for _, narrator := range store.DecodeJSONSlice[string](raw) {
			if narrator != "" && !seen[narrator] {
				counts[narrator]++
				seen[narrator] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return textsort.NaturalBaseCompare(names[i], names[j]) < 0 })
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"id": url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name))), "name": name, "numBooks": counts[name]})
	}
	return httputil.JSON(w, r, map[string]any{"narrators": result})
}

func (h *Handler) podcastTitles(w http.ResponseWriter, r *http.Request, lib Scope) error {
	libraryID := lib.ID
	if !access.AdminUser(lib.User) {
		return httputil.Status(http.StatusForbidden, "")
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT p.title,p.itunesId,li.id,li.libraryId FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=?`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		var title, itunesID sql.NullString
		var itemID, libID string
		if err := rows.Scan(&title, &itunesID, &itemID, &libID); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		result = append(result, map[string]any{"title": store.NullString(title), "itunesId": store.NullString(itunesID), "libraryItemId": itemID, "libraryId": libID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	return httputil.JSON(w, r, map[string]any{"podcasts": result})
}
