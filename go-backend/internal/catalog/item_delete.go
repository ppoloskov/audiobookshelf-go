package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
)

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) error {
	user, ctx := store.RequestUser(r), r.Context()
	item, err := h.loadItem(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if perms := access.For(ctx, user); !perms.Library(item.libraryID) || !perms.Delete() {
		return httputil.Status(http.StatusForbidden, "")
	}
	if err := h.removeAndSweep(ctx, item, r.URL.Query().Get("hard") == "1"); err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

// removeAndSweep is LibraryItemController.delete (LibraryItemController.js:
// 139-148): the item goes, then the authors and series it leaves empty.
func (h *Handler) removeAndSweep(ctx context.Context, item itemScope, hard bool) error {
	var linked itemLinks
	if err := h.removeLibraryItem(ctx, item, hard, &linked); err != nil {
		return err
	}
	h.env().Sweep(ctx, linked.authors, linked.series)
	h.filters.ResetIssues(ctx, h.db, item.libraryID)
	return nil
}

type itemLinks struct{ authors, series []string }

// removeLibraryItem is ApiRouter.handleDeleteLibraryItem plus the optional file
// removal. It checks no permission: every route authorizes before calling. The
// caller drops the linked authors and series left empty.
func (h *Handler) removeLibraryItem(ctx context.Context, item itemScope, hard bool, linked *itemLinks) error {
	if mediaTableOf(item.mediaType) == "" {
		return httputil.Status(http.StatusBadRequest, "")
	}
	deleteKind := "Soft"
	if hard {
		deleteKind = "Hard"
	}
	logging.Infof("[LibraryItemController] (%s) deleting Library Item at %q with id %q", deleteKind, item.path, item.id) // LibraryItemController.js:587
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	authors, series, err := deleteItemRows(ctx, tx, []itemScope{item})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	linked.authors = append(linked.authors, authors...)
	linked.series = append(linked.series, series...)
	if h.cfg.MetadataPath != "" {
		_ = os.RemoveAll(filepath.Join(h.cfg.Dirs().Items, item.id))
	}
	if hard && item.path != "" && h.pathAllowed(ctx, item.path, item.libraryID) {
		logging.Infof("[LibraryItemController] Deleting library item from file system at %q", item.path) // LibraryItemController.js:604
		if err := os.RemoveAll(item.path); err != nil {
			logging.Errorf("[LibraryItemController] Failed to delete library item from file system at %q: %v", item.path, err)
		}
	}
	h.emit.ToAll("item_removed", map[string]any{"id": item.id, "libraryId": item.libraryID})
	return nil
}

// deleteItemRows removes the items and everything that references them (the
// one list of tables to extend when a table starts referencing a book or an
// episode). It returns the authors and series the books were linked to.
func deleteItemRows(ctx context.Context, tx *sql.Tx, items []itemScope) (authors, series []string, err error) {
	var itemIDs, bookIDs, podcastIDs []string
	for _, item := range items {
		itemIDs = append(itemIDs, item.id)
		switch item.mediaType {
		case "book":
			bookIDs = append(bookIDs, item.mediaID)
		case "podcast":
			podcastIDs = append(podcastIDs, item.mediaID)
		}
	}
	list := func(column, table, by string, ids []string) []string {
		if err != nil || len(ids) == 0 {
			return nil
		}
		var rows *sql.Rows
		if rows, err = tx.QueryContext(ctx, `SELECT `+column+` FROM `+table+` WHERE `+by+` IN (`+store.Placeholders(len(ids))+`)`, stringArgs(ids)...); err != nil {
			return nil
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return nil
			}
			out = append(out, id)
		}
		err = rows.Err()
		return out
	}
	episodeIDs := list("id", "podcastEpisodes", "podcastId", podcastIDs)
	authors = list("authorId", "bookAuthors", "bookId", bookIDs)
	series = list("seriesId", "bookSeries", "bookId", bookIDs)
	if err != nil {
		return nil, nil, err
	}
	exec := func(table, by string, ids []string) {
		if err != nil || len(ids) == 0 {
			return
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+by+` IN (`+store.Placeholders(len(ids))+`)`, stringArgs(ids)...)
	}
	q := sqlc.New(tx)
	mediaIDs := append(append([]string{}, bookIDs...), episodeIDs...)
	exec("mediaProgresses", "mediaItemId", mediaIDs)
	if err == nil && len(mediaIDs) > 0 {
		err = store.RemoveFromPlaylists(ctx, tx, mediaIDs)
	}
	if err == nil && len(itemIDs) > 0 {
		err = q.DeleteLibraryItemFeedsByItemIDs(ctx, store.Texts(itemIDs))
	}
	exec("bookAuthors", "bookId", bookIDs)
	exec("bookSeries", "bookId", bookIDs)
	if err == nil && len(bookIDs) > 0 {
		err = q.DeleteBookSharesByMediaIDs(ctx, store.Texts(bookIDs))
	}
	exec("books", "id", bookIDs)
	if err == nil && len(episodeIDs) > 0 {
		err = q.DeletePodcastEpisodeSharesByMediaIDs(ctx, store.Texts(episodeIDs))
	}
	exec("podcastEpisodes", "id", episodeIDs)
	exec("podcasts", "id", podcastIDs)
	exec("libraryItems", "id", itemIDs)
	return authors, series, err
}
