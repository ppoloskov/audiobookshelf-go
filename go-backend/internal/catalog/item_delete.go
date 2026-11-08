package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	itemID := r.PathValue("id")
	status, message := h.deleteLibraryItem(r.Context(), user, itemID, r.URL.Query().Get("hard") == "1")
	if status != http.StatusOK {
		return httputil.Status(status, message)
	}
	w.WriteHeader(status)
	return nil
}

func (h *Handler) deleteLibraryItem(ctx context.Context, user store.User, itemID string, hard bool) (int, string) {
	var linked itemLinks
	status, message := h.removeLibraryItem(ctx, &user, itemID, hard, &linked)
	if status != http.StatusOK {
		return status, message
	}
	// LibraryItemController.delete (LibraryItemController.js:139-148).
	h.env().Sweep(ctx, linked.authors, linked.series)
	h.filters.ResetIssues(ctx, h.db, linked.libraryID)
	return http.StatusOK, ""
}

// itemLinks are the authors and series a removed book was linked to.
type itemLinks struct {
	authors, series []string
	libraryID       string
}

// removeLibraryItem is ApiRouter.handleDeleteLibraryItem plus the optional file
// removal. The caller drops the linked authors and series left empty. A nil
// user skips the permission checks (admin-only maintenance routes authorize
// before calling).
func (h *Handler) removeLibraryItem(ctx context.Context, user *store.User, itemID string, hard bool, linked *itemLinks) (int, string) {
	var scope itemScope
	var itemPath string
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,mediaId,mediaType,path FROM libraryItems WHERE id=?`, itemID).Scan(&scope.libraryID, &scope.mediaID, &scope.mediaType, &itemPath)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, ""
	}
	if err != nil {
		logging.Errorf("[catalog] removeLibraryItem itemID=%s: database read failed: %v", itemID, err)
		return http.StatusInternalServerError, "database read failed"
	}
	if user != nil {
		if !access.For(ctx, *user).Library(scope.libraryID) {
			return http.StatusForbidden, ""
		}
		if !access.For(ctx, *user).Delete() {
			return http.StatusForbidden, ""
		}
	}
	deleteKind := "Soft"
	if hard {
		deleteKind = "Hard"
	}
	logging.Infof("[LibraryItemController] (%s) deleting Library Item at %q with id %q", deleteKind, itemPath, itemID) // LibraryItemController.js:587
	mediaIDs := []string{scope.mediaID}
	authorIDs := []string{}
	seriesIDs := []string{}
	if scope.mediaType == "podcast" {
		rows, e := h.db.QueryContext(ctx, `SELECT id FROM podcastEpisodes WHERE podcastId=?`, scope.mediaID)
		if e != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database read failed: %v", itemID, e)
			return http.StatusInternalServerError, "database read failed"
		}
		mediaIDs = nil
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				mediaIDs = append(mediaIDs, id)
			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database read failed: %v", itemID, e)
			return http.StatusInternalServerError, "database read failed"
		}
		rows.Close()
	}
	if scope.mediaType == "book" {
		for _, spec := range []struct {
			table, column string
			dst           *[]string
		}{{"bookAuthors", "authorId", &authorIDs}, {"bookSeries", "seriesId", &seriesIDs}} {
			rows, e := h.db.QueryContext(ctx, `SELECT `+spec.column+` FROM `+spec.table+` WHERE bookId=?`, scope.mediaID)
			if e != nil {
				logging.Errorf("[catalog] removeLibraryItem itemID=%s: database read failed: %v", itemID, e)
				return http.StatusInternalServerError, "database read failed"
			}
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					*spec.dst = append(*spec.dst, id)
				}
			}
			if e = rows.Err(); e != nil {
				rows.Close()
				logging.Errorf("[catalog] removeLibraryItem itemID=%s: database read failed: %v", itemID, e)
				return http.StatusInternalServerError, "database read failed"
			}
			rows.Close()
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
		return http.StatusInternalServerError, "database write failed"
	}
	defer tx.Rollback()
	if len(mediaIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(mediaIDs)), ",")
		args := make([]any, len(mediaIDs))
		for i, id := range mediaIDs {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mediaProgresses WHERE mediaItemId IN (`+placeholders+`)`, args...); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
		if err := deletePlaylistMedia(ctxTx{tx, ctx}, mediaIDs); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
	}
	if err := sqlc.New(tx).DeleteLibraryItemFeeds(ctx, itemID); err != nil {
		logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
		return http.StatusInternalServerError, "database write failed"
	}
	if scope.mediaType == "book" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE bookId=?`, scope.mediaID); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=?`, scope.mediaID); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
		if err := sqlc.New(tx).DeleteBookShares(ctx, scope.mediaID); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id=?`, scope.mediaID); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
	} else if scope.mediaType == "podcast" {
		if len(mediaIDs) > 0 {
			if err := sqlc.New(tx).DeletePodcastEpisodeSharesByMediaIDs(ctx, store.Texts(mediaIDs)); err != nil {
				logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
				return http.StatusInternalServerError, "database write failed"
			}
		}
		if len(mediaIDs) > 0 {
			marks := strings.TrimRight(strings.Repeat("?,", len(mediaIDs)), ",")
			args := make([]any, len(mediaIDs))
			for i, id := range mediaIDs {
				args[i] = id
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM podcastEpisodes WHERE id IN (`+marks+`)`, args...); err != nil {
				logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
				return http.StatusInternalServerError, "database write failed"
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM podcasts WHERE id=?`, scope.mediaID); err != nil {
			logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
			return http.StatusInternalServerError, "database write failed"
		}
	} else {
		return http.StatusBadRequest, ""
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM libraryItems WHERE id=?`, itemID); err != nil {
		logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
		return http.StatusInternalServerError, "database write failed"
	}
	if err := tx.Commit(); err != nil {
		logging.Errorf("[catalog] removeLibraryItem itemID=%s: database write failed: %v", itemID, err)
		return http.StatusInternalServerError, "database write failed"
	}
	if linked != nil {
		linked.libraryID = scope.libraryID
		linked.authors = append(linked.authors, authorIDs...)
		linked.series = append(linked.series, seriesIDs...)
	}
	if h.cfg.MetadataPath != "" {
		_ = os.RemoveAll(filepath.Join(h.cfg.MetadataPath, "items", itemID))
	}
	if hard && itemPath != "" && h.pathAllowed(ctx, itemPath, scope.libraryID) {
		logging.Infof("[LibraryItemController] Deleting library item from file system at %q", itemPath) // LibraryItemController.js:604
		if err := os.RemoveAll(itemPath); err != nil {
			logging.Errorf("[LibraryItemController] Failed to delete library item from file system at %q: %v", itemPath, err)
		}
	}
	if h.emit != nil {
		h.emit("*", "item_removed", map[string]any{"id": itemID, "libraryId": scope.libraryID})
	}
	return http.StatusOK, ""

}

type ctxTx struct {
	tx  *sql.Tx
	ctx context.Context
}

func deletePlaylistMedia(db ctxTx, mediaIDs []string) error {
	q := sqlc.New(db.tx)
	for _, id := range mediaIDs {
		if err := q.DeletePlaylistMediaItemsByMediaID(db.ctx, id); err != nil {
			return err
		}
	}
	playlistIDs, err := q.ListAllPlaylistIDs(db.ctx)
	if err != nil {
		return err
	}
	for _, playlistID := range playlistIDs {
		entryIDs, e := q.ListPlaylistMediaItemIDs(db.ctx, playlistID)
		if e != nil {
			return e
		}
		if len(entryIDs) == 0 {
			if e = q.DeletePlaylist(db.ctx, playlistID); e != nil {
				return e
			}
			continue
		}
		for order, id := range entryIDs {
			if e = q.UpdatePlaylistMediaItemOrder(db.ctx, sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(order + 1), ID: id}); e != nil {
				return e
			}
		}
	}
	return nil
}
