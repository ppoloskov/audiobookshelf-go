package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/serversettings"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

type itemScope struct{ itemID, libraryID, mediaID, mediaType string }

func (h *Handler) writableItem(ctx context.Context, id string, user store.User) (itemScope, bool, error) {
	scope := itemScope{itemID: id}
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,mediaId,mediaType FROM libraryItems WHERE id=?`, id).Scan(&scope.libraryID, &scope.mediaID, &scope.mediaType)
	if err != nil {
		return scope, false, err
	}
	if !access.Of(user).Library(scope.libraryID) {
		return scope, false, nil
	}
	return scope, access.Of(user).Update(), nil
}

func (h *Handler) updateMedia(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	itemID := r.PathValue("id")
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&payload); err != nil {
		return httputil.Status(http.StatusBadRequest, "Invalid request body")
	}
	var metadata map[string]json.RawMessage
	if raw := payload["metadata"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return httputil.Status(http.StatusBadRequest, "Invalid metadata")
		}
	}
	if scope.mediaType == "podcast" {
		// LibraryItemController.updateMedia (LibraryItemController.js:225).
		if raw, ok := payload["autoDownloadSchedule"]; ok {
			var schedule string
			if json.Unmarshal(raw, &schedule) == nil && schedule != "" && !cronspec.Valid(schedule) {
				return httputil.Status(http.StatusBadRequest, "Invalid auto download schedule cron expression")
			}
		}
	}
	result, err := h.applyMedia(r.Context(), scope, payload, metadata)
	if errors.Is(err, errUnsupportedMedia) {
		return httputil.Status(http.StatusBadRequest, "")
	}
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	// LibraryItemController.updateMedia order: series cleanup, then the
	// author events from updateAuthorsFromRequest, then the empty-author check
	// (LibraryItemController.js:233-262).
	env := h.env()
	env.SweepSeries(r.Context(), result.changes.SeriesUnlinked)
	env.Announce(scope.libraryID, &result.changes)
	env.SweepAuthors(r.Context(), result.changes.AuthorsUnlinked)
	if result.updated {
		if err := h.ItemChanged(r.Context(), itemID); err != nil {
			return httputil.Internal("metadata file write failed", err)
		}
	}
	item, err := h.ItemJSON(r.Context(), itemID, false)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"updated": result.updated, "libraryItem": item}, httputil.ForRequest(r))
	return nil
}

var errUnsupportedMedia = errors.New("unsupported media type")

// mediaUpdateResult is what applying a media payload to an item changed.
type mediaUpdateResult struct {
	updated bool
	changes itemwrite.Changes
}

// applyMedia is the database part of LibraryItemController.updateMedia
// (LibraryItemController.js:208-291) and of each entry of batchUpdate
// (LibraryItemController.js:630-735): media.updateFromRequest, then for books
// updateSeriesFromRequest and updateAuthorsFromRequest, saved in one
// transaction. The caller runs the follow-ups (empty series and author
// cleanup, filter data, events, the metadata file).
func (h *Handler) applyMedia(ctx context.Context, scope itemScope, payload, metadata map[string]json.RawMessage) (mediaUpdateResult, error) {
	var result mediaUpdateResult
	table := map[string]string{"book": "books", "podcast": "podcasts"}[scope.mediaType]
	if table == "" {
		return result, errUnsupportedMedia
	}
	prefixes := h.sortingPrefixes(ctx)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current, err := rowColumns(ctx, tx, table, scope.mediaID)
	if err != nil {
		return result, err
	}
	columns := newColumnSet(current)
	if scope.mediaType == "book" {
		columns.bookFields(metadata, payload)
	} else {
		columns.podcastFields(metadata, payload)
	}
	// A new title carries its sort form (Book.js:394-396, Podcast.js:243-245);
	// the libraryItems copy follows through the database trigger.
	if title, ok := columns.set["title"]; ok {
		text, _ := title.(string)
		columns.set["titleIgnorePrefix"] = itemwrite.TitleIgnorePrefix(text, prefixes)
	}
	result.updated = len(columns.set) > 0
	if scope.mediaType == "book" {
		rules := itemwrite.Rules{Replace: true, AuthorUpdates: true}
		if names, ok := authorNames(metadata["authors"]); ok {
			changed, e := result.changes.LinkAuthors(ctx, tx, scope.libraryID, scope.mediaID, names, rules)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
		if series, ok := seriesEntries(metadata["series"]); ok {
			changed, e := result.changes.LinkSeries(ctx, tx, scope.libraryID, scope.mediaID, series, prefixes, rules)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
	}
	if result.updated {
		if err := columns.update(ctx, tx, table, scope.mediaID); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), scope.itemID); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

// authorNames are the names of an authors payload; ok is false when it is not
// an array.
func authorNames(raw json.RawMessage) ([]string, bool) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return nil, false
	}
	var names []string
	for _, a := range incoming {
		if name, ok := a["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names, true
}

// seriesEntries are the entries of a series payload; ok is false when it is
// not an array or an entry has no name.
func seriesEntries(raw json.RawMessage) ([]itemwrite.Series, bool) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return nil, false
	}
	series := make([]itemwrite.Series, len(incoming))
	for i, s := range incoming {
		name, _ := s["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, false
		}
		sequence, _ := s["sequence"].(string)
		series[i] = itemwrite.Series{Name: name, Sequence: sequence}
	}
	return series, true
}

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
	h.resetIssues(ctx, linked.libraryID)
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
		if !access.Of(*user).Library(scope.libraryID) {
			return http.StatusForbidden, ""
		}
		if !access.Of(*user).Delete() {
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
	if hard && itemPath != "" && h.pathAllowed(ctx, itemPath, scope.libraryID, false) {
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

func requestString(data map[string]json.RawMessage, key string) (any, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String(), true
	}
	return nil, false
}
func requestBool(data map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := data[key]
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return b, true
}
func requestStringArray(data map[string]json.RawMessage, key string) ([]string, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	if values == nil {
		return nil, false
	}
	return values, true
}
func jsonValue(value any) string { raw, _ := json.Marshal(value); return string(raw) }

// Sequelize's SQLite DATE binding stores DATETIME as a space-separated UTC
// timestamp with milliseconds and an explicit +00:00 offset.
func dbNow() string { return dbtime.Now() }

func (h *Handler) removeCover(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	itemID := r.PathValue("id")
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=NULL,updatedAt=? WHERE id=? AND coverPath IS NOT NULL`, dbNow(), scope.mediaID)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = h.db.ExecContext(r.Context(), `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), itemID)
		h.purgeCoverCache(itemID)
		if h.emit != nil {
			if item, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
				h.emit("*", "item_updated", item)
			}
		}
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateCover(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	itemID := r.PathValue("id")
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	var body struct {
		Cover string `json:"cover"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 10_000_000)).Decode(&body) != nil || body.Cover == "" {
		return httputil.Status(400, "Invalid request no cover path")
	}
	if strings.HasPrefix(body.Cover, "http:") || strings.HasPrefix(body.Cover, "https:") {
		logging.RequestDebug(r, "responding 500: cover path is a URL, a local file is required")
		return httputil.Status(500, "Invalid cover path")
	}
	var raw []byte
	var itemPath string
	var isFile int
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryFiles,path,COALESCE(isFile,0) FROM libraryItems WHERE id=?`, itemID).Scan(&raw, &itemPath, &isFile); err != nil {
		return httputil.Internal("database read failed", err)
	}
	valid := false
	for _, file := range decodeArray(raw) {
		if filePath(file) == body.Cover {
			valid = true
			break
		}
	}
	if !valid {
		logging.RequestDebug(r, "responding 500: cover path is not one of the library item files")
		return httputil.Status(500, "Invalid cover path")
	}
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	}
	var currentCover sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM `+table+` WHERE id=?`, scope.mediaID).Scan(&currentCover); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if currentCover.Valid && currentCover.String == body.Cover {
		return httputil.JSON(w, r, struct {
			Success bool   `json:"success"`
			Cover   string `json:"cover"`
		}{true, body.Cover})
	}
	info, err := os.Stat(body.Cover)
	if err != nil {
		return httputil.Internal("Cover path does not exist", err)
	}
	if !info.Mode().IsRegular() {
		logging.RequestDebug(r, "responding 500: cover path is not a regular file")
		return httputil.Status(500, "Cover path is not a file")
	}
	ext, validationError := validateCoverImage(body.Cover)
	if validationError != "" {
		logging.RequestDebug(r, "responding 500: "+validationError)
		return httputil.Status(500, validationError)
	}
	coverDir := filepath.Join(h.cfg.MetadataPath, "items", itemID)
	if storeWithItem, _ := serversettings.Load(r.Context(), h.db).Doc()["storeCoverWithItem"].(bool); storeWithItem && isFile == 0 {
		coverDir = itemPath
	}
	coverPath := body.Cover
	if !strings.HasPrefix(coverPath, coverDir) {
		if err := os.MkdirAll(coverDir, 0o755); err != nil {
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		coverPath = filepath.Join(coverDir, "cover."+ext)
		source, err := os.Open(body.Cover)
		if err != nil {
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		target, err := os.Create(coverPath)
		if err != nil {
			_ = source.Close()
			return httputil.Internal("Failed to copy cover to dir", err)
		}
		_, copyErr := io.Copy(target, source)
		targetErr := target.Close()
		sourceErr := source.Close()
		if copyErr != nil || targetErr != nil || sourceErr != nil {
			return httputil.Internal("Failed to copy cover to dir", sourceErr)
		}
		removeOldCovers(coverDir, "."+ext)
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=?,updatedAt=? WHERE id=? AND (coverPath IS NULL OR coverPath!=?)`, coverPath, dbNow(), scope.mediaID, coverPath)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = h.db.ExecContext(r.Context(), `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), itemID)
		h.purgeCoverCache(itemID)
		if h.emit != nil {
			if item, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
				h.emit("*", "item_updated", item)
			}
		}
	}
	return httputil.JSON(w, r, struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}{true, coverPath})
}

func (h *Handler) deleteLibraryFile(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	itemID := r.PathValue("id")
	fileID := r.PathValue("ino")
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	var libraryFilesRaw, audioRaw, ebookRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryFiles,b.audioFiles,b.ebookFile FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&libraryFilesRaw, &audioRaw, &ebookRaw); err != nil {
		return httputil.Internal("database read failed", err)
	}
	files := decodeArray(libraryFilesRaw)
	var removed map[string]any
	remaining := make([]map[string]any, 0, len(files))
	for _, f := range files {
		if stringValue(f["ino"]) == fileID {
			removed = f
		} else {
			remaining = append(remaining, f)
		}
	}
	if removed == nil {
		return httputil.Status(404, "")
	}
	target := filePath(removed)
	if target == "" || !h.pathAllowed(r.Context(), target, scope.libraryID, false) {
		return httputil.Status(404, "")
	}
	_ = os.Remove(target)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	now := dbNow()
	if scope.mediaType == "book" {
		audio := decodeArray(audioRaw)
		nextAudio := make([]map[string]any, 0, len(audio))
		for _, f := range audio {
			if stringValue(f["ino"]) != fileID {
				nextAudio = append(nextAudio, f)
			}
		}
		ebook := decodeObject(ebookRaw)
		ebookChanged := stringValue(ebook["ino"]) == fileID
		if len(nextAudio) != len(audio) || ebookChanged {
			var ebookValue any
			if !ebookChanged {
				ebookValue = string(ebookRaw)
			}
			if _, err := tx.ExecContext(r.Context(), `UPDATE books SET audioFiles=?,ebookFile=?,updatedAt=? WHERE id=?`, jsonValue(nextAudio), ebookValue, now, scope.mediaID); err != nil {
				return httputil.Internal("database write failed", err)
			}
		}
		missing := len(nextAudio) == 0 && (len(ebookRaw) == 0 || ebookChanged || string(ebookRaw) == "null")
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,isMissing=?,updatedAt=? WHERE id=?`, jsonValue(remaining), missing, now, itemID); err != nil {
			return httputil.Internal("database write failed", err)
		}
	} else if scope.mediaType == "podcast" {
		var episodeID string
		err := tx.QueryRowContext(r.Context(), `SELECT id FROM podcastEpisodes WHERE podcastId=? AND json_extract(audioFile,'$.ino')=?`, scope.mediaID, fileID).Scan(&episodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return httputil.Status(404, "")
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if err := sqlc.New(tx).DeletePodcastEpisodePlaylistEntries(r.Context(), episodeID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId=? AND mediaItemType='podcastEpisode'`, episodeID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE id=?`, episodeID); err != nil {
			return httputil.Internal("database write failed", err)
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, jsonValue(remaining), now, itemID); err != nil {
			return httputil.Internal("database write failed", err)
		}
	} else {
		return httputil.Status(400, "")
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if h.emit != nil {
		if item, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
			h.emit("*", "item_updated", item)
		}
	}
	w.WriteHeader(http.StatusOK)
	return nil
}
