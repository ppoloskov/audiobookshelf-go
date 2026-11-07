package catalog

import (
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
)

type itemScope struct{ itemID, libraryID, mediaID, mediaType string }

func (h *Handler) writableItem(ctx context.Context, id string, user store.User) (itemScope, bool, error) {
	scope := itemScope{itemID: id}
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,mediaId,mediaType FROM libraryItems WHERE id=?`, id).Scan(&scope.libraryID, &scope.mediaID, &scope.mediaType)
	if err != nil {
		return scope, false, err
	}
	if !userCanAccessLibrary(user, scope.libraryID) {
		return scope, false, nil
	}
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	return scope, user.IsActive && permissions["update"] == true, nil
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
	updated, sync := result.updated, result.sync
	// LibraryItemController.updateMedia order: series cleanup, then the
	// author events from updateAuthorsFromRequest, then the empty-author check
	// (LibraryItemController.js:233-262).
	h.checkRemoveEmptySeries(r.Context(), sync.seriesRemoved)
	for _, se := range sync.seriesAdded { // LibraryItemController.js:240-245
		h.filters.AddSeries(scope.libraryID, se.Name, se.ID)
	}
	if h.emit != nil {
		for _, event := range sync.events {
			h.emit("*", event.name, event.payload)
		}
	}
	h.checkRemoveAuthorsWithNoBooks(r.Context(), sync.authorsRemoved)
	for _, au := range sync.authorsAdded { // LibraryItemController.js:261-265
		h.filters.AddAuthor(scope.libraryID, au.Name, au.ID)
	}
	if updated && h.itemChanged != nil {
		if err := h.itemChanged(r.Context(), itemID); err != nil {
			return httputil.Internal("metadata file write failed", err)
		}
	}
	item, err := h.ItemJSON(r.Context(), itemID, false)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"updated": updated, "libraryItem": item}, httputil.ForRequest(r))
	if updated && h.emit != nil {
		if expanded, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
			h.emit("*", "item_updated", expanded)
		}
	}
	return nil
}

var errUnsupportedMedia = errors.New("unsupported media type")

// titleIgnorePrefix is utils.getTitleIgnorePrefix (utils/index.js:176-202):
// the title without a leading sorting prefix, "" for an empty title.
func titleIgnorePrefix(title string, prefixes []string) string {
	lower := strings.ToLower(title)
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix+" ") {
			return title[len(prefix)+1:]
		}
	}
	return title
}

// storedRow is one row of a table as read from the database.
type storedRow struct {
	names  []string
	values []any
}

// get is the stored value of a column (nil for NULL or an unknown column).
func (r storedRow) get(column string) any {
	for i, name := range r.names {
		if name == column {
			return r.values[i]
		}
	}
	return nil
}

// rowColumns reads one row of table.
func rowColumns(ctx context.Context, tx *sql.Tx, table, id string) (storedRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE id=?`, id)
	if err != nil {
		return storedRow{}, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return storedRow{}, err
	}
	if !rows.Next() {
		return storedRow{}, sql.ErrNoRows
	}
	values := make([]any, len(names))
	targets := make([]any, len(names))
	for i := range values {
		targets[i] = &values[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return storedRow{}, err
	}
	return storedRow{names: names, values: values}, rows.Err()
}

// mediaUpdateResult is what applying a media payload to an item changed.
type mediaUpdateResult struct {
	updated bool
	sync    mediaSync
}

// applyMedia is the database part of LibraryItemController.updateMedia
// (LibraryItemController.js:208-291) and of each entry of batchUpdate
// (LibraryItemController.js:630-735): media.updateFromRequest, then for books
// updateSeriesFromRequest and updateAuthorsFromRequest, saved in one
// transaction. The caller runs the follow-ups (empty series and author
// cleanup, filter data, events, the metadata file).
func (h *Handler) applyMedia(ctx context.Context, scope itemScope, payload, metadata map[string]json.RawMessage) (mediaUpdateResult, error) {
	var result mediaUpdateResult
	prefixes := h.sortingPrefixes(ctx)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	} else if scope.mediaType != "book" {
		return result, errUnsupportedMedia
	}
	current, err := rowColumns(ctx, tx, table, scope.mediaID)
	if err != nil {
		return result, err
	}
	var columns map[string]any
	if scope.mediaType == "book" {
		columns = bookUpdateColumns(metadata, payload, current)
	} else {
		columns = podcastUpdateColumns(metadata, payload, current)
	}
	// A new title carries its sort form (Book.js:394-396, Podcast.js:243-245);
	// the libraryItems copy follows through the database trigger.
	if title, ok := columns["title"]; ok {
		text, _ := title.(string)
		columns["titleIgnorePrefix"] = titleIgnorePrefix(text, prefixes)
	}
	result.updated = len(columns) > 0
	if scope.mediaType == "book" {
		if raw, ok := metadata["authors"]; ok {
			changed, e := h.syncBookAuthors(ctx, tx, scope.mediaID, scope.libraryID, raw, &result.sync)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
		if raw, ok := metadata["series"]; ok {
			changed, e := h.syncBookSeries(ctx, tx, scope.mediaID, scope.libraryID, raw, prefixes, &result.sync)
			if e != nil {
				return result, e
			}
			result.updated = result.updated || changed
		}
	}
	if result.updated {
		sets := make([]string, 0, len(columns))
		args := make([]any, 0, len(columns)+2)
		for col, value := range columns {
			sets = append(sets, `"`+col+`"=?`)
			args = append(args, value)
		}
		sets = append(sets, `"updatedAt"=?`)
		args = append(args, dbNow())
		args = append(args, scope.mediaID)
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), scope.itemID); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
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
	h.checkRemoveAuthorsWithNoBooks(ctx, linked.authors)
	h.checkRemoveEmptySeries(ctx, linked.series)
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
		if !userCanAccessLibrary(*user, scope.libraryID) {
			return http.StatusForbidden, ""
		}
		var permissions map[string]any
		_ = json.Unmarshal(user.Permissions, &permissions)
		if !user.IsActive || permissions["delete"] != true {
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

func makeUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(b[:])
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
}

// mediaSync records what a PATCH /api/items/:id/media changed beyond the book
// row: events Book.updateAuthorsFromRequest emits while it works, and the
// authors/series that lost a book so the controller can drop the empty ones.
type mediaSync struct {
	events         []syncEvent
	authorsRemoved []string
	seriesRemoved  []string
	authorsAdded   []filterdata.Named
	seriesAdded    []filterdata.Named
}

type syncEvent struct {
	name    string
	payload any
}

// authorPayload is Author.toOldJSON (+numBooks for toOldJSONExpanded). A
// freshly created author has no asin/description/imagePath attributes loaded,
// so those keys are absent from its payload (Author.js:177-199).
func authorPayload(ctx context.Context, tx *sql.Tx, id string, created bool, numBooks int) (map[string]any, error) {
	var name, libraryID, createdAt, updatedAt string
	var asin, description, imagePath sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT name,asin,description,imagePath,libraryId,createdAt,updatedAt FROM authors WHERE id=?`, id).Scan(&name, &asin, &description, &imagePath, &libraryID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	msOf := func(value string) int64 {
		if t, ok := dbtime.Parse(value); ok {
			return t.UnixMilli()
		}
		return 0
	}
	payload := map[string]any{"id": id, "name": name, "libraryId": libraryID, "addedAt": msOf(createdAt), "updatedAt": msOf(updatedAt)}
	if !created {
		payload["asin"], payload["description"], payload["imagePath"] = store.NullString(asin), store.NullString(description), store.NullString(imagePath)
		payload["numBooks"] = numBooks
	}
	return payload, nil
}

func authorBookCount(ctx context.Context, tx *sql.Tx, authorID string) int {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return n
}

func (h *Handler) syncBookAuthors(ctx context.Context, tx *sql.Tx, bookID, libraryID string, raw json.RawMessage, out *mediaSync) (bool, error) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return false, nil
	}
	names := make([]string, 0, len(incoming))
	for _, a := range incoming {
		if name, ok := a["name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt,ba.rowid`, bookID)
	if err != nil {
		return false, err
	}
	type linkedAuthor struct{ key, id string }
	var current []linkedAuthor
	known := map[string]bool{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return false, err
		}
		current = append(current, linkedAuthor{strings.ToLower(name), id})
		known[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	changed := false
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.ToLower(name)] = true
	}
	// Book.updateAuthorsFromRequest removes first, then adds
	// (Book.js:434-466).
	for _, link := range current {
		if wanted[link.key] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE bookId=? AND authorId=?`, bookID, link.id); err != nil {
			return false, err
		}
		if n := authorBookCount(ctx, tx, link.id); n > 0 {
			payload, err := authorPayload(ctx, tx, link.id, false, n)
			if err != nil {
				return false, err
			}
			out.events = append(out.events, syncEvent{"author_updated", payload})
		}
		out.authorsRemoved = append(out.authorsRemoved, link.id)
		changed = true
	}
	var clock insertClock
	added := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(name)
		if known[key] || added[key] {
			continue
		}
		added[key] = true
		now := clock.now()
		var id string
		created := false
		err := tx.QueryRowContext(ctx, `SELECT id FROM authors WHERE libraryId=? AND name=? COLLATE NOCASE LIMIT 1`, libraryID, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = makeUUID()
			if id == "" {
				return false, fmt.Errorf("generate author id")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, lastFirst(name), libraryID, now, now); err != nil {
				return false, err
			}
			created = true
		} else if err != nil {
			return false, err
		}
		linkID := makeUUID()
		if linkID == "" {
			return false, fmt.Errorf("generate book author id")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, linkID, bookID, id, now); err != nil {
			return false, err
		}
		payload, err := authorPayload(ctx, tx, id, created, authorBookCount(ctx, tx, id))
		if err != nil {
			return false, err
		}
		out.authorsAdded = append(out.authorsAdded, filterdata.Named{ID: id, Name: name})
		if created {
			out.events = append(out.events, syncEvent{"author_added", payload})
		} else {
			out.events = append(out.events, syncEvent{"author_updated", payload})
		}
		changed = true
	}
	return changed, nil
}

func (h *Handler) syncBookSeries(ctx context.Context, tx *sql.Tx, bookID, libraryID string, raw json.RawMessage, prefixes []string, out *mediaSync) (bool, error) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return false, nil
	}
	for _, s := range incoming {
		if name, ok := s["name"].(string); !ok || strings.TrimSpace(name) == "" {
			return false, nil
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt, bs.rowid`, bookID)
	if err != nil {
		return false, err
	}
	type currentSeries struct {
		id       string
		sequence sql.NullString
	}
	current := map[string]currentSeries{}
	var currentOrder []string
	for rows.Next() {
		var id, name string
		var seq sql.NullString
		if err := rows.Scan(&id, &name, &seq); err != nil {
			rows.Close()
			return false, err
		}
		current[strings.ToLower(name)] = currentSeries{id: id, sequence: seq}
		currentOrder = append(currentOrder, strings.ToLower(name))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	changed := false
	keep := map[string]bool{}
	var clock insertClock
	for _, s := range incoming {
		name := strings.TrimSpace(s["name"].(string))
		key := strings.ToLower(name)
		if keep[key] {
			continue
		}
		keep[key] = true
		sequence := ""
		if v, ok := s["sequence"].(string); ok {
			sequence = v
		}
		old, ok := current[key]
		if ok {
			var oldSeq any
			if old.sequence.Valid {
				oldSeq = old.sequence.String
			}
			var newSeq any
			if sequence != "" {
				newSeq = sequence
			}
			if fmt.Sprint(oldSeq) != fmt.Sprint(newSeq) {
				if _, err := tx.ExecContext(ctx, `UPDATE bookSeries SET sequence=? WHERE bookId=? AND seriesId=?`, newSeq, bookID, old.id); err != nil {
					return false, err
				}
				changed = true
			}
			continue
		}
		var id string
		now := clock.now()
		err := tx.QueryRowContext(ctx, `SELECT id FROM series WHERE libraryId=? AND name=? COLLATE NOCASE LIMIT 1`, libraryID, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = makeUUID()
			if id == "" {
				return false, fmt.Errorf("generate series id")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, libitem.TitleIgnorePrefix(name, prefixes), libraryID, now, now); err != nil {
				return false, err
			}
		} else if err != nil {
			return false, err
		}
		linkID := makeUUID()
		if linkID == "" {
			return false, fmt.Errorf("generate book series id")
		}
		var seq any
		if sequence != "" {
			seq = sequence
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookSeries(id,bookId,seriesId,sequence,createdAt) VALUES(?,?,?,?,?)`, linkID, bookID, id, seq, now); err != nil {
			return false, err
		}
		out.seriesAdded = append(out.seriesAdded, filterdata.Named{ID: id, Name: name})
		changed = true
	}
	for _, key := range currentOrder {
		row := current[key]
		if keep[key] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=? AND seriesId=?`, bookID, row.id); err != nil {
			return false, err
		}
		out.seriesRemoved = append(out.seriesRemoved, row.id)
		changed = true
	}
	return changed, nil
}

// insertClock stamps rows inserted one after another within a request. Sequelize
// stamps every create separately, so books keep their authors and series in
// insertion order when read back by createdAt; a stamp shared by several rows
// would leave that order to the database, so each stamp is a millisecond
// later than the one before.
type insertClock struct{ last time.Time }

func (c *insertClock) now() string {
	t := time.Now().UTC().Truncate(time.Millisecond)
	if !t.After(c.last) {
		t = c.last.Add(time.Millisecond)
	}
	c.last = t
	return dbtime.Format(t)
}

// storedString is a TEXT column value as read into an interface.
func storedString(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return v
	}
}

// storedBool is a BOOLEAN column value.
func storedBool(v any) bool {
	switch x := v.(type) {
	case int64:
		return x != 0
	case bool:
		return x
	case float64:
		return x != 0
	}
	return false
}

// storedJSONText is JSON.stringify of a JSON column's parsed value.
func storedJSONText(v any) string {
	text, ok := storedString(v).(string)
	if !ok {
		return "null"
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(text)) != nil {
		return text
	}
	return compact.String()
}

// stringChanged is `payload !== this[key]` of Book.updateFromRequest
// (Book.js:382): the raw request value against the stored one, before
// sanitizing or turning "" into null.
func stringChanged(current, requested any) bool { return storedString(current) != requested }

// bookUpdateColumns are the columns Book.updateFromRequest (Book.js:370-431)
// would change given the stored row `current`.
func bookUpdateColumns(metadata, payload map[string]json.RawMessage, current storedRow) map[string]any {
	columns := map[string]any{}
	for _, key := range []string{"title", "subtitle", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language"} {
		if value, ok := requestString(metadata, key); ok && stringChanged(current.get(key), value) {
			if key == "description" && value != nil {
				value = textclean.Sanitize(fmt.Sprint(value))
			}
			if value == "" {
				value = nil
			}
			columns[key] = value
		}
	}
	for _, key := range []string{"explicit", "abridged"} {
		if value, ok := requestBool(metadata, key); ok && storedBool(current.get(key)) != value {
			columns[key] = value
		}
	}
	for _, key := range []string{"narrators", "genres"} {
		if value, ok := requestStringArray(metadata, key); ok && storedJSONText(current.get(key)) != jsonValue(value) {
			columns[key] = jsonValue(value)
		}
	}
	if value, ok := requestStringArray(payload, "tags"); ok && storedJSONText(current.get("tags")) != jsonValue(value) {
		columns["tags"] = jsonValue(value)
	}
	return columns
}

// podcastUpdateColumns are the columns Podcast.updateFromRequest
// (Podcast.js:210-292) would change given the stored row `current`.
func podcastUpdateColumns(metadata, payload map[string]json.RawMessage, current storedRow) map[string]any {
	columns := map[string]any{}
	mapKeys := map[string]string{"title": "title", "author": "author", "releaseDate": "releaseDate", "feedUrl": "feedURL", "imageUrl": "imageURL", "description": "description", "itunesPageUrl": "itunesPageURL", "itunesId": "itunesId", "itunesArtistId": "itunesArtistId", "language": "language", "type": "podcastType"}
	for key, col := range mapKeys {
		if value, ok := requestString(metadata, key); ok && stringChanged(current.get(col), value) {
			if key == "description" && value != nil {
				value = textclean.Sanitize(fmt.Sprint(value))
			}
			if value == "" {
				value = nil
			}
			columns[col] = value
		}
	}
	if value, ok := requestBool(metadata, "explicit"); ok && storedBool(current.get("explicit")) != value {
		columns["explicit"] = value
	}
	if value, ok := requestStringArray(metadata, "genres"); ok && storedJSONText(current.get("genres")) != jsonValue(value) {
		columns["genres"] = jsonValue(value)
	}
	if value, ok := requestStringArray(payload, "tags"); ok && storedJSONText(current.get("tags")) != jsonValue(value) {
		columns["tags"] = jsonValue(value)
	}
	if value, ok := requestBool(payload, "autoDownloadEpisodes"); ok && storedBool(current.get("autoDownloadEpisodes")) != value {
		columns["autoDownloadEpisodes"] = value
	}
	if raw, ok := payload["autoDownloadSchedule"]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" && stringChanged(current.get("autoDownloadSchedule"), value) {
			columns["autoDownloadSchedule"] = value
		}
	}
	for _, key := range []string{"maxEpisodesToKeep", "maxNewEpisodesToDownload"} {
		if raw, ok := payload[key]; ok {
			var v json.Number
			if json.Unmarshal(raw, &v) == nil {
				if n, e := strconv.ParseInt(v.String(), 10, 64); e == nil {
					if stored, isInt := current.get(key).(int64); !isInt || stored != n {
						columns[key] = n
					}
				}
			}
		}
	}
	return columns
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
	var settingsRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsRaw); err == nil {
		var settings map[string]any
		_ = json.Unmarshal(settingsRaw, &settings)
		storeWithItem, _ := settings["storeCoverWithItem"].(bool)
		if storeWithItem && isFile == 0 {
			coverDir = itemPath
		}
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
