package catalog

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) narrators(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, http.StatusText(http.StatusForbidden))
	}
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
		for _, narrator := range decodeStrings(raw) {
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
	sort.SliceStable(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]any{"id": url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name))), "name": name, "numBooks": counts[name]})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"narrators": result}, httputil.Streamed(), httputil.ForRequest(r))
	return nil
}

func (h *Handler) updateNarrator(w http.ResponseWriter, r *http.Request) error {
	remove := r.Method == http.MethodDelete
	user, libraryID, encodedID := requestUser(r), r.PathValue("id"), r.PathValue("narratorId")
	// Narrator renames and removals patch the filter data upstream
	// (LibraryController.js:1170,1214); reloading it keeps the same content.
	defer h.filters.Invalidate(libraryID)
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, http.StatusText(http.StatusForbidden))
	}
	if !canUpdateCatalog(user) {
		return httputil.Status(http.StatusForbidden, http.StatusText(http.StatusForbidden))
	}
	decoded, err := url.QueryUnescape(encodedID)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	nameBytes, err := base64.StdEncoding.DecodeString(decoded)
	if err != nil {
		nameBytes, err = base64.RawStdEncoding.DecodeString(decoded)
	}
	if err != nil {
		return httputil.Status(http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
	}
	oldName := string(nameBytes)
	newName := ""
	if !remove {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || body.Name == "" {
			return httputil.Status(http.StatusBadRequest, "Invalid request payload. Name not specified.")
		}
		newName = body.Name
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT li.id,b.id,b.narrators FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=? AND EXISTS(SELECT 1 FROM json_each(COALESCE(b.narrators,'[]')) WHERE json_each.value=?)`, libraryID, oldName)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	type bookRow struct {
		itemID, bookID string
		narrators      []string
	}
	books := []bookRow{}
	for rows.Next() {
		var row bookRow
		var raw []byte
		if err := rows.Scan(&row.itemID, &row.bookID, &raw); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		row.narrators = decodeStrings(raw)
		books = append(books, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	updatedItems := make([]map[string]any, 0, len(books))
	for _, row := range books {
		next := make([]string, 0, len(row.narrators)+1)
		for _, narrator := range row.narrators {
			if narrator != oldName {
				next = append(next, narrator)
			}
		}
		if !remove {
			found := false
			for _, narrator := range next {
				if narrator == newName {
					found = true
					break
				}
			}
			if !found {
				next = append(next, newName)
			}
		}
		encoded, _ := json.Marshal(next)
		if _, err := h.db.ExecContext(r.Context(), `UPDATE books SET narrators=?,updatedAt=? WHERE id=?`, string(encoded), dbNow(), row.bookID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if item, err := h.ItemJSON(r.Context(), row.itemID, true); err == nil {
			updatedItems = append(updatedItems, item)
		}
	}
	if len(updatedItems) > 0 && h.emit != nil {
		h.emit("*", "items_updated", updatedItems)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"updated": len(books)}, httputil.Streamed(), httputil.ForRequest(r))
	return nil
}

func (h *Handler) podcastTitles(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, http.StatusText(http.StatusForbidden))
	}
	if user.Type != "root" && user.Type != "admin" {
		return httputil.Status(http.StatusForbidden, http.StatusText(http.StatusForbidden))
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
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"podcasts": result}, httputil.Streamed(), httputil.ForRequest(r))
	return nil
}
