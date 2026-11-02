package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/store"
)

// removeLibraryItemsWithIssues is DELETE /api/libraries/:id/issues: it removes
// every item flagged missing or invalid, then the authors and series those
// items leave empty (LibraryController.js:650-722).
func (h *Handler) removeLibraryItemsWithIssues(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var found string
	if err := h.db.QueryRowContext(r.Context(), `SELECT id FROM libraries WHERE id=?`, libraryID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !adminUser(user) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM libraryItems WHERE libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	var itemIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		itemIDs = append(itemIDs, id)
	}
	if err := rows.Close(); err != nil || rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	var linked itemLinks
	for _, id := range itemIDs {
		if status, message := h.removeLibraryItem(r.Context(), nil, id, false, &linked); status != http.StatusOK {
			writeError(w, status, message)
			return
		}
	}
	// The cleanup is racy upstream: the author of a removed book is sometimes
	// still counted as having a book when checkRemoveAuthorsWithNoBooks runs
	// (seen in differential runs; LibraryController.js:710-717). The
	// source-intended outcome is kept.
	h.checkRemoveAuthorsWithNoBooks(r.Context(), linked.authors)
	h.checkRemoveEmptySeries(r.Context(), linked.series)
	h.filters.StoreNumIssues(libraryID, 0) // LibraryController.js:727-730
	writeStatus(w, http.StatusOK)
}

// checkRemoveAuthorsWithNoBooks removes the given authors that have no books
// and no asin, description or image, announcing each with author_removed
// (ApiRouter.js:480-520).
func (h *Handler) checkRemoveAuthorsWithNoBooks(ctx context.Context, authorIDs []string) {
	h.removeOrphans(ctx, authorIDs, `SELECT id,libraryId FROM authors WHERE id IN (%s) AND COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.authorId=authors.id) ORDER BY rowid`, `DELETE FROM authors WHERE id=?`, "author_removed", h.filters.RemoveAuthor)
}

// checkRemoveEmptySeries removes the given series that have no books, announcing
// each with series_removed (ApiRouter.js:417-478).
func (h *Handler) checkRemoveEmptySeries(ctx context.Context, seriesIDs []string) {
	h.removeOrphans(ctx, seriesIDs, `SELECT id,libraryId FROM series WHERE id IN (%s) AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.seriesId=series.id) ORDER BY rowid`, `DELETE FROM series WHERE id=?`, "series_removed", h.filters.RemoveSeries)
}

func (h *Handler) removeOrphans(ctx context.Context, ids []string, selectFmt, deleteSQL, event string, forget func(libraryID, id string)) {
	seen := map[string]bool{}
	var unique []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return
	}
	rows, err := h.db.QueryContext(ctx, fmt.Sprintf(selectFmt, sqlMarks(len(unique))), stringArgs(unique)...)
	if err != nil {
		return
	}
	type orphan struct{ id, libraryID string }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if rows.Scan(&o.id, &o.libraryID) == nil {
			orphans = append(orphans, o)
		}
	}
	rows.Close()
	for _, o := range orphans {
		if _, err := h.db.ExecContext(ctx, deleteSQL, o.id); err != nil {
			continue
		}
		forget(o.libraryID, o.id)
		if h.emit != nil {
			h.emit("*", event, map[string]any{"id": o.id, "libraryId": o.libraryID})
		}
	}
}
