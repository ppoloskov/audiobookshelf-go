package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/store"
)

type folderInput struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	FullPath string `json:"fullPath"`
}

func adminUser(user store.User) bool {
	return user.IsActive && (user.Type == "root" || user.Type == "admin")
}

func defaultLibrarySettings(mediaType string) map[string]any {
	if mediaType == "podcast" {
		return map[string]any{"coverAspectRatio": 1, "disableWatcher": false, "autoScanCronExpression": nil, "podcastSearchRegion": "us", "markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10}
	}
	return map[string]any{"coverAspectRatio": 1, "disableWatcher": false, "autoScanCronExpression": nil, "skipMatchingMediaWithAsin": false, "skipMatchingMediaWithIsbn": false, "audiobooksOnly": false, "epubsAllowScriptedContent": false, "hideSingleBookSeries": false, "onlyShowLaterBooksInContinueSeries": false, "metadataPrecedence": []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}, "markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10}
}

func (h *Handler) createLibrary(w http.ResponseWriter, r *http.Request, user store.User) {
	if !adminUser(user) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body map[string]json.RawMessage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body) != nil {
		writeError(w, 400, "Invalid request body")
		return
	}
	name, ok := requestString(body, "name")
	if !ok || name == nil || name == "" {
		writeError(w, 400, "Invalid request. Name must be a string")
		return
	}
	mediaType := readStringField(body, "mediaType", "book")
	provider := readStringField(body, "provider", "google")
	icon := readStringField(body, "icon", "database")
	if mediaType == "" || provider == "" || icon == "" {
		writeError(w, 400, "Invalid request. mediaType, icon and provider must be strings")
		return
	}
	var inputs []folderInput
	if raw, ok := body["folders"]; !ok || json.Unmarshal(raw, &inputs) != nil {
		writeError(w, 400, "Invalid request. Folders must be an array of objects with path string")
		return
	}
	settings := defaultLibrarySettings(mediaType)
	if raw, ok := body["settings"]; ok {
		var supplied map[string]any
		if json.Unmarshal(raw, &supplied) != nil || supplied == nil {
			writeError(w, 400, "Invalid request. Settings must be an object")
			return
		}
		for key, value := range supplied {
			old, exists := settings[key]
			if !exists {
				continue
			}
			if key == "metadataPrecedence" {
				if _, ok := value.([]any); !ok {
					writeError(w, 400, `Invalid request. Settings "metadataPrecedence" must be an array`)
					return
				}
				settings[key] = value
				continue
			}
			if value == nil && (key == "autoScanCronExpression" || key == "markAsFinishedPercentComplete" || key == "markAsFinishedTimeRemaining") {
				settings[key] = nil
				continue
			}
			switch old.(type) {
			case bool:
				if _, ok := value.(bool); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			case float64:
				if _, ok := value.(float64); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			case string:
				if _, ok := value.(string); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			case nil:
				if _, ok := value.(string); !ok {
					if _, num := value.(float64); !num {
						writeError(w, 400, "Invalid request setting type")
						return
					}
				}
			}
			settings[key] = value
		}
	}
	if strings.HasPrefix(provider, "custom-") {
		var n int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM customMetadataProviders WHERE slug=?`, provider).Scan(&n); err != nil || n == 0 {
			writeError(w, 400, "Invalid request. Custom metadata provider does not exist")
			return
		}
	}
	paths := make([]string, 0, len(inputs))
	for _, folder := range inputs {
		target := folder.FullPath
		if target == "" {
			target = folder.Path
		}
		if target == "" {
			writeError(w, 400, "Invalid request. Folders must be an array of objects with path string")
			return
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			writeError(w, 400, "Invalid folder directory")
			return
		}
		abs = filepath.ToSlash(filepath.Clean(abs))
		if err := os.MkdirAll(abs, 0755); err != nil {
			writeError(w, 400, "Invalid folder directory")
			return
		}
		paths = append(paths, abs)
	}
	var order int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(displayOrder),0)+1 FROM libraries`).Scan(&order); err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	id := makeUUID()
	if id == "" {
		writeError(w, 500, "failed to create library")
		return
	}
	now := dbNow()
	settingsJSON, _ := json.Marshal(settings)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,extraData,createdAt,updatedAt) VALUES(?,?,?,?,?,?,NULL,NULL,?,'{}',?,?)`, id, name, order, icon, mediaType, provider, string(settingsJSON), now, now); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	for _, folderPath := range paths {
		folderID := makeUUID()
		if folderID == "" {
			writeError(w, 500, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?)`, folderID, folderPath, id, now, now); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	result, err := h.libraryJSON(r.Context(), id)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	// Upstream create responds with library.toOldJSON() from a freshly built
	// Sequelize instance (LibraryController.create): unpersisted attributes
	// like lastScanVersion are undefined and dropped from the JSON; the
	// database-loaded instances returned by other routes emit them as null.
	delete(result, "lastScanVersion")
	if h.libraryChanged != nil {
		h.libraryChanged(id)
	}
	if h.emit != nil {
		h.emit("*", "library_added", result)
	}
	writeJSON(w, r, 200, result)
}

func readStringField(body map[string]json.RawMessage, key, fallback string) string {
	if raw, ok := body[key]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil && value != "" {
			return value
		}
		return ""
	}
	return fallback
}

func (h *Handler) updateLibrary(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if !adminUser(user) {
		w.WriteHeader(403)
		return
	}
	var existingName, provider, icon, mediaType string
	var existingOrder int
	var settingsRaw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT name,provider,icon,mediaType,displayOrder,settings FROM libraries WHERE id=?`, id).Scan(&existingName, &provider, &icon, &mediaType, &existingOrder, &settingsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "Library not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	var body map[string]json.RawMessage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body) != nil {
		writeError(w, 400, "Invalid request body")
		return
	}
	fields := map[string]any{}
	oldFields := map[string]any{"name": existingName, "provider": provider, "mediaType": mediaType, "icon": icon}
	for key := range map[string]bool{"name": true, "provider": true, "mediaType": true, "icon": true} {
		if _, ok := body[key]; ok {
			value, valid := requestString(body, key)
			if !valid || value == nil {
				writeError(w, 400, "Invalid request. "+key+" must be a string")
				return
			}
			if value != "" && value != oldFields[key] {
				fields[key] = value
			}
		}
	}
	if raw, ok := body["displayOrder"]; ok {
		var value json.Number
		if json.Unmarshal(raw, &value) != nil {
			writeError(w, 400, "Invalid request. displayOrder must be a number")
			return
		}
		n, e := strconv.ParseFloat(value.String(), 64)
		if e != nil {
			writeError(w, 400, "Invalid request. displayOrder must be a number")
			return
		}
		if n != float64(existingOrder) {
			fields["displayOrder"] = n
		}
	}
	settings := decodeObject(settingsRaw)
	oldSettings := decodeObject(settingsRaw)
	if settings == nil {
		settings = defaultLibrarySettings(mediaType)
	}
	defaults := defaultLibrarySettings(mediaType)
	for key, value := range defaults {
		if _, ok := settings[key]; !ok {
			settings[key] = value
		}
	}
	settingsChanged := false
	if raw, ok := body["settings"]; ok {
		var values map[string]any
		if json.Unmarshal(raw, &values) != nil || values == nil {
			writeError(w, 400, "Invalid request. Settings must be an object")
			return
		}
		for key, value := range values {
			old, exists := defaults[key]
			if !exists {
				continue
			}
			previous := settings[key]
			if value == nil && (key == "autoScanCronExpression" || key == "markAsFinishedPercentComplete" || key == "markAsFinishedTimeRemaining") {
				settings[key] = nil
				if !reflect.DeepEqual(previous, nil) {
					settingsChanged = true
				}
				continue
			}
			if key == "metadataPrecedence" {
				if _, ok := value.([]any); !ok {
					writeError(w, 400, `Invalid request. Settings "metadataPrecedence" must be an array`)
					return
				}
				settings[key] = value
				if !reflect.DeepEqual(previous, value) {
					settingsChanged = true
				}
				continue
			}
			switch old.(type) {
			case bool:
				if _, ok := value.(bool); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			case float64:
				if _, ok := value.(float64); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			case string:
				if _, ok := value.(string); !ok {
					writeError(w, 400, "Invalid request setting type")
					return
				}
			}
			settings[key] = value
			if !reflect.DeepEqual(previous, value) {
				settingsChanged = true
			}
		}
	}
	disableWatcherChanged := settingsChanged && !reflect.DeepEqual(oldSettings["disableWatcher"], settings["disableWatcher"])
	newFolders := make([]string, 0)
	removedFolders := make([]string, 0)
	if raw, ok := body["folders"]; ok {
		var requested []folderInput
		if json.Unmarshal(raw, &requested) != nil {
			writeError(w, 400, "Invalid request folders")
			return
		}
		current := map[string]bool{}
		rows, e := h.db.QueryContext(r.Context(), `SELECT id FROM libraryFolders WHERE libraryId=?`, id)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		for rows.Next() {
			var folderID string
			if rows.Scan(&folderID) == nil {
				current[folderID] = true
			}
		}
		rows.Close()
		seen := map[string]bool{}
		for _, folder := range requested {
			if folder.ID != "" {
				seen[folder.ID] = true
				continue
			}
			folderPath := folder.FullPath
			if folderPath == "" {
				folderPath = folder.Path
			}
			if folderPath == "" {
				writeError(w, 400, "Invalid request folders")
				return
			}
			abs, e := filepath.Abs(folderPath)
			if e != nil {
				writeError(w, 400, "Invalid folder directory")
				return
			}
			abs = filepath.ToSlash(filepath.Clean(abs))
			if e = os.MkdirAll(abs, 0755); e != nil {
				writeError(w, 400, "Invalid folder directory")
				return
			}
			newFolders = append(newFolders, abs)
		}
		for existing := range current {
			if !seen[existing] {
				removedFolders = append(removedFolders, existing)
			}
		}
	}
	for _, folderID := range removedFolders {
		rows, e := h.db.QueryContext(r.Context(), `SELECT id FROM libraryItems WHERE libraryFolderId=?`, folderID)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		itemIDs := []string{}
		for rows.Next() {
			var itemID string
			if rows.Scan(&itemID) == nil {
				itemIDs = append(itemIDs, itemID)
			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			writeError(w, 500, "database read failed")
			return
		}
		rows.Close()
		cleanupUser := user
		cleanupUser.Permissions = json.RawMessage(`{"accessAllLibraries":true,"delete":true}`)
		for _, itemID := range itemIDs {
			status, _ := h.deleteLibraryItem(r.Context(), cleanupUser, itemID, false)
			if status != http.StatusOK {
				writeError(w, status, "library folder item cleanup failed")
				return
			}
		}
	}
	hasUpdates := len(fields) > 0 || settingsChanged || len(newFolders) > 0
	if !hasUpdates {
		result, e := h.libraryJSON(r.Context(), id)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		writeJSON(w, r, 200, result)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	defer tx.Rollback()
	sets := make([]string, 0)
	args := make([]any, 0)
	for key, value := range fields {
		sets = append(sets, `"`+key+`"=?`)
		args = append(args, value)
	}
	if settingsChanged {
		settingsJSON, _ := json.Marshal(settings)
		sets = append(sets, `"settings"=?`)
		args = append(args, string(settingsJSON))
	}
	sets = append(sets, `"updatedAt"=?`)
	args = append(args, dbNow(), id)
	if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	for _, folderPath := range newFolders {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?)`, makeUUID(), folderPath, id, dbNow(), dbNow()); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	for _, folderID := range removedFolders {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraryFolders WHERE id=? AND libraryId=?`, folderID, id); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	result, err := h.libraryJSON(r.Context(), id)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if h.libraryChanged != nil && (len(newFolders) > 0 || len(removedFolders) > 0 || disableWatcherChanged) {
		h.libraryChanged(id)
	}
	if h.emit != nil {
		h.emit("*", "library_updated", result)
	}
	writeJSON(w, r, 200, result)
}

func (h *Handler) deleteLibrary(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if !adminUser(user) {
		w.WriteHeader(403)
		return
	}
	var count int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=?`, id).Scan(&count); err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	if count == 0 {
		writeError(w, 404, "Library not found")
		return
	}
	result, err := h.libraryJSON(r.Context(), id)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,mediaId,mediaType FROM libraryItems WHERE libraryId=?`, id)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	type deletedItem struct{ id, mediaID, mediaType string }
	items := make([]deletedItem, 0)
	bookIDs := make([]string, 0)
	podcastIDs := make([]string, 0)
	itemIDs := make([]string, 0)
	for rows.Next() {
		var item deletedItem
		if err := rows.Scan(&item.id, &item.mediaID, &item.mediaType); err != nil {
			rows.Close()
			writeError(w, 500, "database read failed")
			return
		}
		items = append(items, item)
		itemIDs = append(itemIDs, item.id)
		if item.mediaType == "book" {
			bookIDs = append(bookIDs, item.mediaID)
		} else if item.mediaType == "podcast" {
			podcastIDs = append(podcastIDs, item.mediaID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "database read failed")
		return
	}
	rows.Close()
	mediaIDs := append([]string{}, bookIDs...)
	podcastEpisodeIDs := make([]string, 0)
	for _, podcastID := range podcastIDs {
		eps, e := h.db.QueryContext(r.Context(), `SELECT id FROM podcastEpisodes WHERE podcastId=?`, podcastID)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		for eps.Next() {
			var episodeID string
			if eps.Scan(&episodeID) == nil {
				mediaIDs = append(mediaIDs, episodeID)
				podcastEpisodeIDs = append(podcastEpisodeIDs, episodeID)
			}
		}
		if e = eps.Err(); e != nil {
			eps.Close()
			writeError(w, 500, "database read failed")
			return
		}
		eps.Close()
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	defer tx.Rollback()
	if len(mediaIDs) > 0 {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE mediaItemId IN (`+sqlMarks(len(mediaIDs))+`)`, stringArgs(mediaIDs)...); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
		if err := deletePlaylistMedia(ctxTx{tx, r.Context()}, mediaIDs); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if len(itemIDs) > 0 {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM feeds WHERE entityType='libraryItem' AND entityId IN (`+sqlMarks(len(itemIDs))+`)`, stringArgs(itemIDs)...); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if len(bookIDs) > 0 {
		for _, q := range []string{`DELETE FROM bookAuthors WHERE bookId IN (` + sqlMarks(len(bookIDs)) + `)`, `DELETE FROM bookSeries WHERE bookId IN (` + sqlMarks(len(bookIDs)) + `)`, `DELETE FROM mediaItemShares WHERE mediaItemType='book' AND mediaItemId IN (` + sqlMarks(len(bookIDs)) + `)`, `DELETE FROM books WHERE id IN (` + sqlMarks(len(bookIDs)) + `)`} {
			if _, err := tx.ExecContext(r.Context(), q, stringArgs(bookIDs)...); err != nil {
				writeError(w, 500, "database write failed")
				return
			}
		}
	}
	if len(podcastIDs) > 0 {
		if len(podcastEpisodeIDs) > 0 {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM mediaItemShares WHERE mediaItemType='podcastEpisode' AND mediaItemId IN (`+sqlMarks(len(podcastEpisodeIDs))+`)`, stringArgs(podcastEpisodeIDs)...); err != nil {
				writeError(w, 500, "database write failed")
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM podcastEpisodes WHERE podcastId IN (`+sqlMarks(len(podcastIDs))+`)`, stringArgs(podcastIDs)...); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM podcasts WHERE id IN (`+sqlMarks(len(podcastIDs))+`)`, stringArgs(podcastIDs)...); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraryItems WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM authors WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM series WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM collectionBooks WHERE collectionId IN (SELECT id FROM collections WHERE libraryId=?)`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM collections WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE playbackSessions SET libraryId=NULL WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraryFolders WHERE libraryId=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraries WHERE id=?`, id); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	orderRows, err := tx.QueryContext(r.Context(), `SELECT id FROM libraries ORDER BY displayOrder,id`)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
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
		writeError(w, 500, "database read failed")
		return
	}
	orderRows.Close()
	for order, libraryID := range remainingLibraries {
		if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET displayOrder=? WHERE id=?`, order+1, libraryID); err != nil {
			writeError(w, 500, "database write failed")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	if h.libraryChanged != nil {
		h.libraryChanged(id)
	}
	if h.emit != nil {
		h.emit("*", "library_removed", result)
		for _, item := range items {
			h.emit("*", "item_removed", map[string]any{"id": item.id, "libraryId": id})
		}
	}
	if h.cfg.MetadataPath != "" {
		for _, item := range items {
			_ = os.RemoveAll(filepath.Join(h.cfg.MetadataPath, "items", item.id))
		}
	}
	writeJSON(w, r, 200, result)
}

func sqlMarks(n int) string { return strings.TrimRight(strings.Repeat("?,", n), ",") }
func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func (h *Handler) reorderLibraries(w http.ResponseWriter, r *http.Request, user store.User) {
	if !adminUser(user) {
		w.WriteHeader(403)
		return
	}
	var order []struct {
		ID       string  `json:"id"`
		NewOrder float64 `json:"newOrder"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&order) != nil {
		writeError(w, 400, "Invalid request. Request body must be an array of objects")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	defer tx.Rollback()
	for _, row := range order {
		if row.ID == "" {
			writeError(w, 400, "Invalid request. Request body must be an array of objects")
			return
		}
		var old int
		if err := tx.QueryRowContext(r.Context(), `SELECT displayOrder FROM libraries WHERE id=?`, row.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			writeError(w, 400, "Library not found with id "+row.ID)
			return
		} else if err != nil {
			writeError(w, 500, "database read failed")
			return
		}
		if float64(old) != row.NewOrder {
			if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET displayOrder=?,updatedAt=? WHERE id=?`, row.NewOrder, dbNow(), row.ID); err != nil {
				writeError(w, 500, "database write failed")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "database write failed")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM libraries ORDER BY displayOrder`)
	if err != nil {
		writeError(w, 500, "database read failed")
		return
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	result := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		library, e := h.libraryJSON(r.Context(), id)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		result = append(result, library)
	}
	writeJSON(w, r, 200, map[string]any{"libraries": result})
}

func (h *Handler) libraryJSON(ctx context.Context, id string) (map[string]any, error) {
	var name, icon, mediaType, provider, lastVersion sql.NullString
	var order sql.NullInt64
	var lastScan, created, updated any
	var settingsRaw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries WHERE id=?`, id).Scan(&name, &order, &icon, &mediaType, &provider, &lastScan, &lastVersion, &settingsRaw, &created, &updated); err != nil {
		return nil, err
	}
	folders, err := h.db.QueryContext(ctx, `SELECT id,path,createdAt FROM libraryFolders WHERE libraryId=? ORDER BY createdAt`, id)
	if err != nil {
		return nil, err
	}
	folderList := make([]map[string]any, 0)
	for folders.Next() {
		var folderID, folderPath string
		var added any
		if err := folders.Scan(&folderID, &folderPath, &added); err != nil {
			folders.Close()
			return nil, err
		}
		ms, e := dateMillis(added)
		if e != nil {
			folders.Close()
			return nil, e
		}
		folderList = append(folderList, map[string]any{"id": folderID, "fullPath": folderPath, "libraryId": id, "addedAt": deref(ms)})
	}
	if err := folders.Err(); err != nil {
		folders.Close()
		return nil, err
	}
	folders.Close()
	createdMs, err := dateMillis(created)
	if err != nil {
		return nil, err
	}
	updatedMs, err := dateMillis(updated)
	if err != nil {
		return nil, err
	}
	scanMs, err := dateMillis(lastScan)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "name": nullString(name), "folders": folderList, "displayOrder": order.Int64, "icon": nullString(icon), "mediaType": nullString(mediaType), "provider": nullString(provider), "settings": decodeObject(settingsRaw), "lastScan": deref(scanMs), "lastScanVersion": nullString(lastVersion), "createdAt": deref(createdMs), "lastUpdate": deref(updatedMs)}
	return result, nil
}
