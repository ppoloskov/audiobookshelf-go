package catalog

import (
	"audiobookshelf-compat/internal/httputil"
	"cmp"
	"net/http"
)

// removeLibraryItemsWithIssues is DELETE /api/libraries/:id/issues: it removes
// every item flagged missing or invalid, then the authors and series those
// items leave empty (LibraryController.js:650-722).
func (h *Handler) removeLibraryItemsWithIssues(w http.ResponseWriter, r *http.Request, lib libraryScope) error {
	libraryID := lib.id
	if !adminUser(lib.user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM libraryItems WHERE libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var itemIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		itemIDs = append(itemIDs, id)
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		if err == nil {
			err = rows.Err()
		}
		return httputil.Internal("database read failed", err)
	}
	var linked itemLinks
	for _, id := range itemIDs {
		if status, message := h.removeLibraryItem(r.Context(), nil, id, false, &linked); status != http.StatusOK {
			return httputil.Status(status, cmp.Or(message, http.StatusText(status)))
		}
	}
	// The cleanup is racy upstream: the author of a removed book is sometimes
	// still counted as having a book when the author sweep runs
	// (seen in differential runs; LibraryController.js:710-717). The
	// source-intended outcome is kept.
	h.env().Sweep(r.Context(), linked.authors, linked.series)
	h.filters.StoreNumIssues(libraryID, 0) // LibraryController.js:727-730
	return httputil.OK(w, r)
}
