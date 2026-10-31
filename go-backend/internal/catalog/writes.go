package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
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

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/store"
)

type itemScope struct{ libraryID, mediaID, mediaType string }

func (h *Handler) writableItem(ctx context.Context, id string, user store.User) (itemScope, bool, error) {
	var scope itemScope
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

func (h *Handler) updateMedia(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var payload map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var metadata map[string]json.RawMessage
	if raw := payload["metadata"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid metadata")
			return
		}
	}
	prefixes := []string{"the", "a"}
	var settingsRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsRaw); err == nil {
		if values, ok := decodeObject(settingsRaw)["sortingPrefixes"].([]any); ok {
			prefixes = make([]string, 0, len(values))
			for _, value := range values {
				if s, ok := value.(string); ok {
					prefixes = append(prefixes, s)
				}
			}
		}
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	defer tx.Rollback()
	columns := map[string]any{}
	if scope.mediaType == "book" {
		columns = bookUpdateColumns(metadata, payload)
	} else if scope.mediaType == "podcast" {
		columns = podcastUpdateColumns(metadata, payload)
	} else {
		writeError(w, http.StatusBadRequest, "")
		return
	}
	updated := len(columns) > 0
	if scope.mediaType == "book" {
		if raw, ok := metadata["authors"]; ok {
			changed, e := h.syncBookAuthors(r.Context(), tx, scope.mediaID, scope.libraryID, raw)
			if e != nil {
				writeError(w, http.StatusInternalServerError, "database write failed")
				return
			}
			updated = updated || changed
		}
		if raw, ok := metadata["series"]; ok {
			changed, e := h.syncBookSeries(r.Context(), tx, scope.mediaID, scope.libraryID, raw, prefixes)
			if e != nil {
				writeError(w, http.StatusInternalServerError, "database write failed")
				return
			}
			updated = updated || changed
		}
	}
	if updated {
		sets := make([]string, 0, len(columns))
		args := make([]any, 0, len(columns)+2)
		for col, value := range columns {
			sets = append(sets, `"`+col+`"=?`)
			args = append(args, value)
		}
		sets = append(sets, `"updatedAt"=?`)
		args = append(args, dbNow())
		args = append(args, scope.mediaID)
		table := "books"
		if scope.mediaType == "podcast" {
			table = "podcasts"
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE `+table+` SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
			writeError(w, http.StatusInternalServerError, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET updatedAt=? WHERE id=?`, dbNow(), itemID); err != nil {
			writeError(w, http.StatusInternalServerError, "database write failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "database write failed")
		return
	}
	if updated && h.itemChanged != nil {
		if err := h.itemChanged(r.Context(), itemID); err != nil {
			writeError(w, http.StatusInternalServerError, "metadata file write failed")
			return
		}
	}
	item, err := h.ItemJSON(r.Context(), itemID, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"updated": updated, "libraryItem": item})
	if updated && h.emit != nil {
		if expanded, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
			h.emit("*", "item_updated", expanded)
		}
	}
}

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	status, message := h.deleteLibraryItem(r.Context(), user, itemID, r.URL.Query().Get("hard") == "1")
	if status != http.StatusOK {
		writeError(w, status, message)
		return
	}
	w.WriteHeader(status)
}

func (h *Handler) deleteLibraryItem(ctx context.Context, user store.User, itemID string, hard bool) (int, string) {
	var scope itemScope
	var itemPath string
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,mediaId,mediaType,path FROM libraryItems WHERE id=?`, itemID).Scan(&scope.libraryID, &scope.mediaID, &scope.mediaType, &itemPath)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, ""
	}
	if err != nil {
		return http.StatusInternalServerError, "database read failed"
	}
	if !userCanAccessLibrary(user, scope.libraryID) {
		return http.StatusForbidden, ""
	}
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	if !user.IsActive || permissions["delete"] != true {
		return http.StatusForbidden, ""
	}
	mediaIDs := []string{scope.mediaID}
	authorIDs := []string{}
	seriesIDs := []string{}
	if scope.mediaType == "podcast" {
		rows, e := h.db.QueryContext(ctx, `SELECT id FROM podcastEpisodes WHERE podcastId=?`, scope.mediaID)
		if e != nil {
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
				return http.StatusInternalServerError, "database read failed"
			}
			rows.Close()
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
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
			return http.StatusInternalServerError, "database write failed"
		}
		if err := deletePlaylistMedia(ctxTx{tx, ctx}, mediaIDs); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM feeds WHERE entityType='libraryItem' AND entityId=?`, itemID); err != nil {
		return http.StatusInternalServerError, "database write failed"
	}
	if scope.mediaType == "book" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE bookId=?`, scope.mediaID); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=?`, scope.mediaID); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM mediaItemShares WHERE mediaItemType='book' AND mediaItemId=?`, scope.mediaID); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id=?`, scope.mediaID); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
	} else if scope.mediaType == "podcast" {
		if len(mediaIDs) > 0 {
			marks := strings.TrimRight(strings.Repeat("?,", len(mediaIDs)), ",")
			args := make([]any, len(mediaIDs))
			for i, id := range mediaIDs {
				args[i] = id
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM mediaItemShares WHERE mediaItemType='podcastEpisode' AND mediaItemId IN (`+marks+`)`, args...); err != nil {
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
				return http.StatusInternalServerError, "database write failed"
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM podcasts WHERE id=?`, scope.mediaID); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
	} else {
		return http.StatusBadRequest, ""
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM libraryItems WHERE id=?`, itemID); err != nil {
		return http.StatusInternalServerError, "database write failed"
	}
	for _, id := range authorIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM authors WHERE id=? AND NOT EXISTS(SELECT 1 FROM bookAuthors WHERE authorId=?)`, id, id); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
	}
	for _, id := range seriesIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id=? AND NOT EXISTS(SELECT 1 FROM bookSeries WHERE seriesId=?)`, id, id); err != nil {
			return http.StatusInternalServerError, "database write failed"
		}
	}
	if err := tx.Commit(); err != nil {
		return http.StatusInternalServerError, "database write failed"
	}
	if h.cfg.MetadataPath != "" {
		_ = os.RemoveAll(filepath.Join(h.cfg.MetadataPath, "items", itemID))
	}
	if hard && itemPath != "" && h.pathAllowed(ctx, itemPath, scope.libraryID, false) {
		_ = os.RemoveAll(itemPath)
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
	for _, id := range mediaIDs {
		if _, err := db.tx.ExecContext(db.ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId=?`, id); err != nil {
			return err
		}
	}
	rows, err := db.tx.QueryContext(db.ctx, `SELECT id FROM playlists`)
	if err != nil {
		return err
	}
	playlistIDs := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			playlistIDs = append(playlistIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, playlistID := range playlistIDs {
		items, e := db.tx.QueryContext(db.ctx, `SELECT id FROM playlistMediaItems WHERE playlistId=? ORDER BY "order"`, playlistID)
		if e != nil {
			return e
		}
		ids := []string{}
		for items.Next() {
			var id string
			if items.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		if e = items.Err(); e != nil {
			items.Close()
			return e
		}
		items.Close()
		if len(ids) == 0 {
			if _, e = db.tx.ExecContext(db.ctx, `DELETE FROM playlists WHERE id=?`, playlistID); e != nil {
				return e
			}
			continue
		}
		for order, id := range ids {
			if _, e = db.tx.ExecContext(db.ctx, `UPDATE playlistMediaItems SET "order"=? WHERE id=?`, order+1, id); e != nil {
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

func (h *Handler) syncBookAuthors(ctx context.Context, tx *sql.Tx, bookID, libraryID string, raw json.RawMessage) (bool, error) {
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
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=?`, bookID)
	if err != nil {
		return false, err
	}
	current := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return false, err
		}
		current[strings.ToLower(name)] = id
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	changed := false
	keep := map[string]bool{}
	now := dbNow()
	for _, name := range names {
		key := strings.ToLower(name)
		if keep[key] {
			continue
		}
		keep[key] = true
		if _, ok := current[key]; ok {
			continue
		}
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM authors WHERE libraryId=? AND name=? COLLATE NOCASE LIMIT 1`, libraryID, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = makeUUID()
			if id == "" {
				return false, fmt.Errorf("generate author id")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, lastFirst(name), libraryID, now, now); err != nil {
				return false, err
			}
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
		changed = true
	}
	for key, id := range current {
		if keep[key] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE bookId=? AND authorId=?`, bookID, id); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM authors WHERE id=? AND NOT EXISTS(SELECT 1 FROM bookAuthors WHERE authorId=?)`, id, id); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func (h *Handler) syncBookSeries(ctx context.Context, tx *sql.Tx, bookID, libraryID string, raw json.RawMessage, prefixes []string) (bool, error) {
	var incoming []map[string]any
	if json.Unmarshal(raw, &incoming) != nil {
		return false, nil
	}
	for _, s := range incoming {
		if name, ok := s["name"].(string); !ok || strings.TrimSpace(name) == "" {
			return false, nil
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=?`, bookID)
	if err != nil {
		return false, err
	}
	type currentSeries struct {
		id       string
		sequence sql.NullString
	}
	current := map[string]currentSeries{}
	for rows.Next() {
		var id, name string
		var seq sql.NullString
		if err := rows.Scan(&id, &name, &seq); err != nil {
			rows.Close()
			return false, err
		}
		current[strings.ToLower(name)] = currentSeries{id: id, sequence: seq}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	changed := false
	keep := map[string]bool{}
	now := dbNow()
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
		err := tx.QueryRowContext(ctx, `SELECT id FROM series WHERE libraryId=? AND name=? COLLATE NOCASE LIMIT 1`, libraryID, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			id = makeUUID()
			if id == "" {
				return false, fmt.Errorf("generate series id")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, catalogTitlePrefixAtEnd(name, prefixes), libraryID, now, now); err != nil {
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
		changed = true
	}
	for key, row := range current {
		if keep[key] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=? AND seriesId=?`, bookID, row.id); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id=? AND NOT EXISTS(SELECT 1 FROM bookSeries WHERE seriesId=?)`, row.id, row.id); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func bookUpdateColumns(metadata, payload map[string]json.RawMessage) map[string]any {
	columns := map[string]any{}
	for _, key := range []string{"title", "subtitle", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language"} {
		if value, ok := requestString(metadata, key); ok {
			if key == "description" && value != nil {
				value = auth.SanitizeHTML(fmt.Sprint(value))
			}
			if value == "" {
				value = nil
			}
			columns[key] = value
		}
	}
	for _, key := range []string{"explicit", "abridged"} {
		if value, ok := requestBool(metadata, key); ok {
			columns[key] = value
		}
	}
	for _, key := range []string{"narrators", "genres"} {
		if value, ok := requestStringArray(metadata, key); ok {
			columns[key] = jsonValue(value)
		}
	}
	if value, ok := requestStringArray(payload, "tags"); ok {
		columns["tags"] = jsonValue(value)
	}
	return columns
}

func podcastUpdateColumns(metadata, payload map[string]json.RawMessage) map[string]any {
	columns := map[string]any{}
	mapKeys := map[string]string{"title": "title", "author": "author", "releaseDate": "releaseDate", "feedUrl": "feedURL", "imageUrl": "imageURL", "description": "description", "itunesPageUrl": "itunesPageURL", "itunesId": "itunesId", "itunesArtistId": "itunesArtistId", "language": "language", "type": "podcastType"}
	for key, col := range mapKeys {
		if value, ok := requestString(metadata, key); ok {
			if key == "description" && value != nil {
				value = auth.SanitizeHTML(fmt.Sprint(value))
			}
			if value == "" {
				value = nil
			}
			columns[col] = value
		}
	}
	if value, ok := requestBool(metadata, "explicit"); ok {
		columns["explicit"] = value
	}
	if value, ok := requestStringArray(metadata, "genres"); ok {
		columns["genres"] = jsonValue(value)
	}
	if value, ok := requestStringArray(payload, "tags"); ok {
		columns["tags"] = jsonValue(value)
	}
	if value, ok := requestBool(payload, "autoDownloadEpisodes"); ok {
		columns["autoDownloadEpisodes"] = value
	}
	if raw, ok := payload["autoDownloadSchedule"]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			columns["autoDownloadSchedule"] = value
		}
	}
	for _, key := range []string{"maxEpisodesToKeep", "maxNewEpisodesToDownload"} {
		if raw, ok := payload[key]; ok {
			var v json.Number
			if json.Unmarshal(raw, &v) == nil {
				if n, e := strconv.ParseInt(v.String(), 10, 64); e == nil {
					columns[key] = n
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

func (h *Handler) removeCover(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "")
		return
	}
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if !allowed {
		writeError(w, 403, "")
		return
	}
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=NULL,updatedAt=? WHERE id=? AND coverPath IS NOT NULL`, dbNow(), scope.mediaID)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
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
	writeStatus(w, http.StatusOK)
}

func (h *Handler) updateCover(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "")
		return
	}
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if !allowed {
		writeError(w, 403, "")
		return
	}
	var body struct {
		Cover string `json:"cover"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 10_000_000)).Decode(&body) != nil || body.Cover == "" {
		sendTextStatus(w, 400, "Invalid request no cover path")
		return
	}
	if strings.HasPrefix(body.Cover, "http:") || strings.HasPrefix(body.Cover, "https:") {
		sendTextStatus(w, 500, "Invalid cover path")
		return
	}
	var raw []byte
	var itemPath string
	var isFile int
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryFiles,path,COALESCE(isFile,0) FROM libraryItems WHERE id=?`, itemID).Scan(&raw, &itemPath, &isFile); err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	valid := false
	for _, file := range decodeArray(raw) {
		if filePath(file) == body.Cover {
			valid = true
			break
		}
	}
	if !valid {
		sendTextStatus(w, 500, "Invalid cover path")
		return
	}
	table := "books"
	if scope.mediaType == "podcast" {
		table = "podcasts"
	}
	var currentCover sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM `+table+` WHERE id=?`, scope.mediaID).Scan(&currentCover); err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if currentCover.Valid && currentCover.String == body.Cover {
		writeExpressJSON(w, http.StatusOK, struct {
			Success bool   `json:"success"`
			Cover   string `json:"cover"`
		}{true, body.Cover})
		return
	}
	info, err := os.Stat(body.Cover)
	if err != nil {
		sendTextStatus(w, 500, "Cover path does not exist")
		return
	}
	if !info.Mode().IsRegular() {
		sendTextStatus(w, 500, "Cover path is not a file")
		return
	}
	ext, validationError := validateCoverImage(body.Cover)
	if validationError != "" {
		sendTextStatus(w, 500, validationError)
		return
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
			sendTextStatus(w, 500, "Failed to copy cover to dir")
			return
		}
		coverPath = filepath.Join(coverDir, "cover."+ext)
		source, err := os.Open(body.Cover)
		if err != nil {
			sendTextStatus(w, 500, "Failed to copy cover to dir")
			return
		}
		target, err := os.Create(coverPath)
		if err != nil {
			_ = source.Close()
			sendTextStatus(w, 500, "Failed to copy cover to dir")
			return
		}
		_, copyErr := io.Copy(target, source)
		targetErr := target.Close()
		sourceErr := source.Close()
		if copyErr != nil || targetErr != nil || sourceErr != nil {
			sendTextStatus(w, 500, "Failed to copy cover to dir")
			return
		}
		removeOldCovers(coverDir, "."+ext)
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE `+table+` SET coverPath=?,updatedAt=? WHERE id=? AND (coverPath IS NULL OR coverPath!=?)`, coverPath, dbNow(), scope.mediaID, coverPath)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
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
	writeExpressJSON(w, http.StatusOK, struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}{true, coverPath})
}

func (h *Handler) deleteLibraryFile(w http.ResponseWriter, r *http.Request, user store.User, itemID, fileID string) {
	scope, allowed, err := h.writableItem(r.Context(), itemID, user)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "")
		return
	}
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if !allowed {
		writeError(w, 403, "")
		return
	}
	var libraryFilesRaw, audioRaw, ebookRaw []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT li.libraryFiles,b.audioFiles,b.ebookFile FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId WHERE li.id=?`, itemID).Scan(&libraryFilesRaw, &audioRaw, &ebookRaw); err != nil {
		writeError(w, 500, "database read failed")
		return
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
		writeError(w, 404, "")
		return
	}
	target := filePath(removed)
	if target == "" || !h.pathAllowed(r.Context(), target, scope.libraryID, false) {
		writeError(w, 404, "")
		return
	}
	_ = os.Remove(target)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
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
				writeError(w, 500, "database write failed")
				return
			}
		}
		missing := len(nextAudio) == 0 && (len(ebookRaw) == 0 || ebookChanged || string(ebookRaw) == "null")
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,isMissing=?,updatedAt=? WHERE id=?`, jsonValue(remaining), missing, now, itemID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	} else if scope.mediaType == "podcast" {
		var episodeID string
		err := tx.QueryRowContext(r.Context(), `SELECT id FROM podcastEpisodes WHERE podcastId=? AND json_extract(audioFile,'$.ino')=?`, scope.mediaID, fileID).Scan(&episodeID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, 404, "")
			return
		}
		if err != nil {
			writeError(w, 500, "database read failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlistMediaItems WHERE mediaItemId=? AND mediaItemType='podcastEpisode'`, episodeID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId=? AND mediaItemType='podcastEpisode'`, episodeID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE id=?`, episodeID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, jsonValue(remaining), now, itemID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	} else {
		writeError(w, 400, "")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if h.emit != nil {
		if item, e := h.ItemJSON(r.Context(), itemID, true); e == nil {
			h.emit("*", "item_updated", item)
		}
	}
	w.WriteHeader(http.StatusOK)
}
