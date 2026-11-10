package catalog

import (
	"context"
	"net/http"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
)

func adminUser(user store.User) bool { return user.IsActive && access.AdminUser(user) }

func (h *Handler) deleteLibrary(w http.ResponseWriter, r *http.Request) error {
	user, id := store.RequestUser(r), r.PathValue("id")
	defer h.filters.Invalidate(id) // LibraryController.js:591
	if !adminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var count int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=?`, id).Scan(&count); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if count == 0 {
		return httputil.Status(404, "Library not found")
	}
	result, err := library.ReadView(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	items, err := h.libraryItemsWhere(r.Context(), `libraryId=?`, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	logging.Infof("[LibraryController] Removing %d library items in library %q", len(items), result.Name) // LibraryController.js:557
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	targets := make([]itemwrite.DeleteTarget, len(items))
	for i, item := range items {
		targets[i] = deletionTarget(item)
	}
	changes, err := itemwrite.DeleteRows(r.Context(), tx, targets)
	playlistChanges, closedFeeds := changes.PlaylistChanges, changes.ClosedFeeds
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM authors WHERE libraryId=?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM series WHERE libraryId=?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := sqlc.New(tx).DeleteCollectionBooksByLibraryID(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := sqlc.New(tx).DeleteCollectionsByLibraryID(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if res, err := tx.ExecContext(r.Context(), `UPDATE playbackSessions SET libraryId=NULL WHERE libraryId=?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	} else if n, rowsErr := res.RowsAffected(); rowsErr == nil {
		logging.Infof("[LibraryController] Updated %d playback sessions to remove library id", n) // LibraryController.js:580
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraryFolders WHERE libraryId=?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraries WHERE id=?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	orderRows, err := tx.QueryContext(r.Context(), `SELECT id FROM libraries ORDER BY displayOrder,id`)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	remainingLibraries := []string{}
	for orderRows.Next() {
		var libraryID string
		if orderRows.Scan(&libraryID) == nil {
			remainingLibraries = append(remainingLibraries, libraryID)
		}
	}
	if err := orderRows.Err(); err != nil {
		orderRows.Close()
		return httputil.Internal("database read failed", err)
	}
	orderRows.Close()
	for order, libraryID := range remainingLibraries {
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET displayOrder=? WHERE id=?`, order+1, libraryID); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if h.deps.LibraryChanged != nil {
		h.deps.LibraryChanged(id)
	}
	playlistview.Announce(r.Context(), h.db, h.emit, playlistChanges)
	for _, item := range items {
		for _, feed := range closedFeeds {
			if feed.EntityID != nil && *feed.EntityID == item.id {
				h.emit.ToAll("rss_feed_closed", feed)
			}
		}
		h.deleter().RemoveItemArtifacts(r.Context(), deletionTarget(item))
		h.deleter().AnnounceItemRemoved(deletionTarget(item))
	}
	h.emit.ToAll("library_removed", result)
	return httputil.JSON(w, r, result)
}

// libraryItemsWhere lists the items matching one column condition.
func (h *Handler) libraryItemsWhere(ctx context.Context, where string, arg any) ([]itemScope, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT `+itemScopeColumns+` FROM libraryItems li WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []itemScope
	for rows.Next() {
		item, err := scanItemScope(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
