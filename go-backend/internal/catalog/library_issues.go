package catalog

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/library"
	"net/http"
)

// removeLibraryItemsWithIssues is DELETE /api/libraries/:id/issues: it removes
// every item flagged missing or invalid, then the authors and series those
// items leave empty (LibraryController.js:650-722).
func (h *Handler) removeLibraryItemsWithIssues(w http.ResponseWriter, r *http.Request, lib library.Scope) error {
	libraryID := lib.ID
	if !adminUser(lib.User) {
		return httputil.Status(http.StatusForbidden, "")
	}
	items, err := h.libraryItemsWhere(r.Context(), `libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var linked itemLinks
	for _, item := range items {
		if err := h.removeLibraryItem(r.Context(), item, false, &linked); err != nil {
			return err
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
