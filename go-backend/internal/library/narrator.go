package library

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) updateNarrator(w http.ResponseWriter, r *http.Request, lib Scope) error {
	remove := r.Method == http.MethodDelete
	libraryID, encodedID := lib.ID, r.PathValue("narratorId")
	// Narrator renames and removals patch the filter data upstream
	// (LibraryController.js:1170,1214); reloading it keeps the same content.
	defer h.filters.Invalidate(libraryID)
	if !access.For(r.Context(), lib.User).Update() {
		return httputil.Status(http.StatusForbidden, "")
	}
	decoded, err := url.QueryUnescape(encodedID)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, "")
	}
	nameBytes, err := base64.StdEncoding.DecodeString(decoded)
	if err != nil {
		nameBytes, err = base64.RawStdEncoding.DecodeString(decoded)
	}
	if err != nil {
		return httputil.Status(http.StatusBadRequest, "")
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
		row.narrators = store.DecodeJSONSlice[string](raw)
		books = append(books, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	rows.Close()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	updatedIDs := make([]string, 0, len(books))
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
		if _, err := tx.ExecContext(r.Context(), `UPDATE books SET narrators=?,updatedAt=? WHERE id=?`, string(encoded), dbtime.Now(), row.bookID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		updatedIDs = append(updatedIDs, row.itemID)
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if len(updatedIDs) > 0 {
		if updatedItems, err := h.expandedItems(r.Context(), updatedIDs); err == nil && len(updatedItems) > 0 {
			h.emit.ToAll("items_updated", updatedItems)
		}
	}
	return httputil.JSON(w, r, map[string]any{"updated": len(books)})
}
