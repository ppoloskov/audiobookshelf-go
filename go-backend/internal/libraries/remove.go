package libraries

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
)

// deleteItem removes one item in its own transaction and announces it; the
// caller sweeps the removal.
func (h *Handler) deleteItem(ctx context.Context, itemID string) (item.Removal, error) {
	var rem item.Removal
	err := h.db.Tx(ctx, func(ctx context.Context) (err error) {
		rem, err = h.deps.Items.DeleteRows(ctx, []string{itemID})
		return err
	})
	if err == nil {
		h.deps.Items.Announce(ctx, rem)
	}
	return rem, err
}

// removeFolderItems deletes the items of a removed folder one by one, sweeping
// the authors and series each leaves empty.
func (h *Handler) removeFolderItems(ctx context.Context, folderID string) error {
	itemIDs, err := selectAll[string](ctx, h.db, `SELECT id FROM libraryItems WHERE libraryFolderId = ?`, folderID)
	if err != nil {
		return err
	}
	for _, id := range itemIDs {
		rem, err := h.deleteItem(ctx, id)
		if err != nil {
			return err
		}
		h.deps.Items.Sweep(ctx, rem)
	}
	return nil
}

// remove is DELETE /api/libraries/{id} (LibraryController.delete, js:535-595):
// the library, its items, authors, series, collections and folders go in ONE
// transaction, playback sessions keep their rows without a library, and the
// other libraries are renumbered. Events and file cleanup follow the commit.
func (h *Handler) remove(w http.ResponseWriter, r *http.Request) error {
	ctx, id := r.Context(), r.PathValue("id")
	defer h.deps.Filters.Invalidate(id) // LibraryController.js:591
	view, err := h.view(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	itemIDs, err := selectAll[string](ctx, h.db, `SELECT id FROM libraryItems WHERE libraryId = ?`, id)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	slog.Info("removing library items in library", "items", len(itemIDs), "library", view.Name) // LibraryController.js:557
	var rem item.Removal
	err = h.db.Tx(ctx, func(ctx context.Context) (err error) {
		if rem, err = h.deps.Items.DeleteRows(ctx, itemIDs); err != nil {
			return err
		}
		for _, statement := range []string{
			`DELETE FROM authors WHERE libraryId = ?`,
			`DELETE FROM series WHERE libraryId = ?`,
			`DELETE FROM collectionBooks WHERE collectionId IN (SELECT id FROM collections WHERE libraryId = ?)`,
			`DELETE FROM collections WHERE libraryId = ?`,
			`UPDATE playbackSessions SET libraryId = NULL WHERE libraryId = ?`, // LibraryController.js:580
			`DELETE FROM libraryFolders WHERE libraryId = ?`,
			`DELETE FROM libraries WHERE id = ?`,
		} {
			if _, err = h.db.Exec(ctx, statement, id); err != nil {
				return err
			}
		}
		remaining, err := selectAll[string](ctx, h.db, `SELECT id FROM libraries ORDER BY displayOrder, id`)
		for order, libraryID := range remaining {
			if err == nil {
				_, err = h.db.Exec(ctx, `UPDATE libraries SET displayOrder = ? WHERE id = ?`, order+1, libraryID)
			}
		}
		return err
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	h.changed(id)
	h.deps.Items.Announce(ctx, rem)
	h.emit.ToAll("library_removed", view)
	return httpx.JSON(w, r, view)
}

// removeIssues is DELETE /api/libraries/{id}/issues (admin only): it removes
// every item flagged missing or invalid, then the authors and series those
// items leave empty (LibraryController.js:650-722).
func (h *Handler) removeIssues(w http.ResponseWriter, r *http.Request, scope Scope) error {
	ctx := r.Context()
	if !isAdmin(scope.User) {
		return httpx.Forbidden()
	}
	itemIDs, err := selectAll[string](ctx, h.db, `SELECT id FROM libraryItems WHERE libraryId = ? AND (isMissing=1 OR isInvalid=1)`, scope.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	var all item.Removal
	for _, id := range itemIDs {
		rem, err := h.deleteItem(ctx, id)
		if err != nil {
			return httpx.Internal("database write failed", err)
		}
		all.Add(rem)
	}
	h.deps.Items.Sweep(ctx, all)
	h.deps.Filters.Invalidate(scope.ID) // LibraryController.js:727-730
	return httpx.Status(w, http.StatusOK)
}
