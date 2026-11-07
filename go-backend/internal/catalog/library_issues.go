package catalog

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/httputil"
)

// removeLibraryItemsWithIssues is DELETE /api/libraries/:id/issues: it removes
// every item flagged missing or invalid, then the authors and series those
// items leave empty (LibraryController.js:650-722).
func (h *Handler) removeLibraryItemsWithIssues(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var found string
	if err := h.db.QueryRowContext(r.Context(), `SELECT id FROM libraries WHERE id=?`, libraryID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !adminUser(user) {
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
	// still counted as having a book when checkRemoveAuthorsWithNoBooks runs
	// (seen in differential runs; LibraryController.js:710-717). The
	// source-intended outcome is kept.
	h.checkRemoveAuthorsWithNoBooks(r.Context(), linked.authors)
	h.checkRemoveEmptySeries(r.Context(), linked.series)
	h.filters.StoreNumIssues(libraryID, 0) // LibraryController.js:727-730
	return httputil.OK(w, r)
}

// checkRemoveAuthorsWithNoBooks removes the given authors that have no books
// and no asin, description or image, announcing each with author_removed
// (ApiRouter.js:480-520).
func (h *Handler) checkRemoveAuthorsWithNoBooks(ctx context.Context, authorIDs []string) {
	_ = h.removeOrphans(ctx, authorIDs, `SELECT id,libraryId FROM authors WHERE id IN (%s) AND COALESCE(asin,'')='' AND COALESCE(description,'')='' AND COALESCE(imagePath,'')='' AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.authorId=authors.id) ORDER BY rowid`, `DELETE FROM authors WHERE id=?`, "author_removed", h.filters.RemoveAuthor)
}

// checkRemoveEmptySeries removes the given series that have no books, announcing
// each with series_removed (ApiRouter.js:417-478).
func (h *Handler) checkRemoveEmptySeries(ctx context.Context, seriesIDs []string) {
	removed := h.removeOrphans(ctx, seriesIDs, `SELECT id,libraryId FROM series WHERE id IN (%s) AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.seriesId=series.id) ORDER BY rowid`, `DELETE FROM series WHERE id=?`, "series_removed", h.filters.RemoveSeries)
	// A series that is gone loses its open RSS feeds
	// (RssFeedManager.closeFeedsForEntityIds, ApiRouter.js:456-458).
	for _, id := range removed {
		h.closeFeeds(ctx, id)
	}
}

// closeFeeds closes every feed of an entity: the row goes and rss_feed_closed
// carries the feed (RssFeedManager.handleCloseFeed, :355-361).
func (h *Handler) closeFeeds(ctx context.Context, entityID string) {
	for {
		feed, found, err := h.feedMinified(ctx, "", entityID)
		if err != nil || !found {
			return
		}
		feedID, _ := feed["id"].(string)
		if err := h.queries().DeleteFeed(ctx, feedID); err != nil {
			return
		}
		if h.emit != nil {
			h.emit("*", "rss_feed_closed", feed)
		}
	}
}

func (h *Handler) removeOrphans(ctx context.Context, ids []string, selectFmt, deleteSQL, event string, forget func(libraryID, id string)) []string {
	seen := map[string]bool{}
	var unique []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	rows, err := h.db.QueryContext(ctx, fmt.Sprintf(selectFmt, sqlMarks(len(unique))), stringArgs(unique)...)
	if err != nil {
		return nil
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
	var removed []string
	for _, o := range orphans {
		if _, err := h.db.ExecContext(ctx, deleteSQL, o.id); err != nil {
			continue
		}
		forget(o.libraryID, o.id)
		removed = append(removed, o.id)
		if h.emit != nil {
			h.emit("*", event, map[string]any{"id": o.id, "libraryId": o.libraryID})
		}
	}
	return removed
}
