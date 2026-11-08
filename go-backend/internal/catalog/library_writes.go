package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
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
)

type folderInput struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	FullPath string `json:"fullPath"`
}

func adminUser(user store.User) bool {
	return user.IsActive && access.AdminUser(user)
}

func defaultLibrarySettings(mediaType string) map[string]any {
	if mediaType == "podcast" {
		return map[string]any{"coverAspectRatio": 1, "disableWatcher": false, "autoScanCronExpression": nil, "podcastSearchRegion": "us", "markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10}
	}
	return map[string]any{"coverAspectRatio": 1, "disableWatcher": false, "autoScanCronExpression": nil, "skipMatchingMediaWithAsin": false, "skipMatchingMediaWithIsbn": false, "audiobooksOnly": false, "epubsAllowScriptedContent": false, "hideSingleBookSeries": false, "onlyShowLaterBooksInContinueSeries": false, "metadataPrecedence": serversettings.DefaultMetadataPrecedence(), "markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10}
}

func (h *Handler) createLibrary(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	if !adminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var body map[string]json.RawMessage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	name, ok := requestString(body, "name")
	if !ok || name == nil || name == "" {
		return httputil.Status(400, "Invalid request. Name must be a string")
	}
	mediaType := readStringField(body, "mediaType", "book")
	provider := readStringField(body, "provider", "google")
	icon := readStringField(body, "icon", "database")
	if mediaType == "" || provider == "" || icon == "" {
		return httputil.Status(400, "Invalid request. mediaType, icon and provider must be strings")
	}
	var inputs []folderInput
	if raw, ok := body["folders"]; !ok || json.Unmarshal(raw, &inputs) != nil {
		return httputil.Status(400, "Invalid request. Folders must be an array of objects with path string")
	}
	settings := defaultLibrarySettings(mediaType)
	if raw, ok := body["settings"]; ok {
		var supplied map[string]any
		if json.Unmarshal(raw, &supplied) != nil || supplied == nil {
			return httputil.Status(400, "Invalid request. Settings must be an object")
		}
		for key, value := range supplied {
			old, exists := settings[key]
			if !exists {
				continue
			}
			if key == "metadataPrecedence" {
				if _, ok := value.([]any); !ok {
					return httputil.Status(400, `Invalid request. Settings "metadataPrecedence" must be an array`)
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
					return httputil.Status(400, "Invalid request setting type")
				}
			case float64:
				if _, ok := value.(float64); !ok {
					return httputil.Status(400, "Invalid request setting type")
				}
			case string:
				if _, ok := value.(string); !ok {
					return httputil.Status(400, "Invalid request setting type")
				}
			case nil:
				if _, ok := value.(string); !ok {
					if _, num := value.(float64); !num {
						return httputil.Status(400, "Invalid request setting type")
					}
				}
			}
			settings[key] = value
		}
	}
	if strings.HasPrefix(provider, "custom-") {
		var n int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM customMetadataProviders WHERE slug=?`, provider).Scan(&n); err != nil || n == 0 {
			return httputil.Status(400, "Invalid request. Custom metadata provider does not exist")
		}
	}
	paths := make([]string, 0, len(inputs))
	for _, folder := range inputs {
		target := folder.FullPath
		if target == "" {
			target = folder.Path
		}
		if target == "" {
			return httputil.Status(400, "Invalid request. Folders must be an array of objects with path string")
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			return httputil.Status(400, "Invalid folder directory")
		}
		abs = filepath.ToSlash(filepath.Clean(abs))
		if err := os.MkdirAll(abs, 0755); err != nil {
			return httputil.Status(400, "Invalid folder directory")
		}
		paths = append(paths, abs)
	}
	var order int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(displayOrder),0)+1 FROM libraries`).Scan(&order); err != nil {
		return httputil.Internal("database read failed", err)
	}
	id := ids.Must()
	now := dbtime.Now()
	settingsJSON, _ := json.Marshal(settings)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,extraData,createdAt,updatedAt) VALUES(?,?,?,?,?,?,NULL,NULL,?,'{}',?,?)`, id, name, order, icon, mediaType, provider, string(settingsJSON), now, now); err != nil {
		return httputil.Internal("database write failed", err)
	}
	for _, folderPath := range paths {
		folderID := ids.Must()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?)`, folderID, folderPath, id, now, now); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	view, err := h.libraryView(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	// Upstream create responds with library.toOldJSON() from a freshly built
	// Sequelize instance (LibraryController.create): unpersisted attributes
	// like lastScanVersion are undefined and dropped from the JSON; the
	// database-loaded instances returned by other routes emit them as null.
	result := struct {
		libraryView
		LastScanVersion *string `json:"lastScanVersion,omitempty"` // shadows the embedded field, left out
	}{libraryView: view}
	if h.deps.LibraryChanged != nil {
		h.deps.LibraryChanged(id)
	}
	h.emit.ToAll("library_added", result)
	return httputil.JSON(w, r, result)
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

func (h *Handler) updateLibrary(w http.ResponseWriter, r *http.Request) error {
	user, id := store.RequestUser(r), r.PathValue("id")
	defer h.filters.ResetIssues(r.Context(), h.db, id) // LibraryController.js:513
	if !adminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var existingName, provider, icon, mediaType string
	var existingOrder int
	var settingsRaw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT name,provider,icon,mediaType,displayOrder,settings FROM libraries WHERE id=?`, id).Scan(&existingName, &provider, &icon, &mediaType, &existingOrder, &settingsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var body map[string]json.RawMessage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	fields := map[string]any{}
	oldFields := map[string]any{"name": existingName, "provider": provider, "mediaType": mediaType, "icon": icon}
	for key := range map[string]bool{"name": true, "provider": true, "mediaType": true, "icon": true} {
		if _, ok := body[key]; ok {
			value, valid := requestString(body, key)
			if !valid || value == nil {
				return httputil.Status(400, "Invalid request. "+key+" must be a string")
			}
			if value != "" && value != oldFields[key] {
				fields[key] = value
			}
		}
	}
	if raw, ok := body["displayOrder"]; ok {
		var value json.Number
		if json.Unmarshal(raw, &value) != nil {
			return httputil.Status(400, "Invalid request. displayOrder must be a number")
		}
		n, e := strconv.ParseFloat(value.String(), 64)
		if e != nil {
			return httputil.Status(400, "Invalid request. displayOrder must be a number")
		}
		if n != float64(existingOrder) {
			fields["displayOrder"] = n
		}
	}
	settings := store.DecodeJSONObject(settingsRaw)
	oldSettings := store.DecodeJSONObject(settingsRaw)
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
			return httputil.Status(400, "Invalid request. Settings must be an object")
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
					return httputil.Status(400, `Invalid request. Settings "metadataPrecedence" must be an array`)
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
					return httputil.Status(400, "Invalid request setting type")
				}
			case float64:
				if _, ok := value.(float64); !ok {
					return httputil.Status(400, "Invalid request setting type")
				}
			case string:
				if _, ok := value.(string); !ok {
					return httputil.Status(400, "Invalid request setting type")
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
			return httputil.Status(400, "Invalid request folders")
		}
		current := map[string]bool{}
		rows, e := h.db.QueryContext(r.Context(), `SELECT id FROM libraryFolders WHERE libraryId=?`, id)
		if e != nil {
			return httputil.Internal("database read failed", e)
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
				return httputil.Status(400, "Invalid request folders")
			}
			abs, e := filepath.Abs(folderPath)
			if e != nil {
				return httputil.Status(400, "Invalid folder directory")
			}
			abs = filepath.ToSlash(filepath.Clean(abs))
			if e = os.MkdirAll(abs, 0755); e != nil {
				return httputil.Status(400, "Invalid folder directory")
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
		items, e := h.libraryItemsWhere(r.Context(), `libraryFolderId=?`, folderID)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		for _, item := range items {
			if e = h.removeAndSweep(r.Context(), item, false); e != nil {
				return e
			}
		}
	}
	hasUpdates := len(fields) > 0 || settingsChanged || len(newFolders) > 0
	if !hasUpdates {
		result, e := h.libraryView(r.Context(), id)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		return httputil.JSON(w, r, result)
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
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
	args = append(args, dbtime.Now(), id)
	if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return httputil.Internal("database write failed", err)
	}
	for _, folderPath := range newFolders {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?)`, ids.Must(), folderPath, id, dbtime.Now(), dbtime.Now()); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	for _, folderID := range removedFolders {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM libraryFolders WHERE id=? AND libraryId=?`, folderID, id); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	result, err := h.libraryView(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if h.deps.LibraryChanged != nil && (len(newFolders) > 0 || len(removedFolders) > 0 || disableWatcherChanged) {
		h.deps.LibraryChanged(id)
	}
	h.emit.ToAll("library_updated", result)
	return httputil.JSON(w, r, result)
}

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
	result, err := h.libraryView(r.Context(), id)
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
	if _, _, err := deleteItemRows(r.Context(), tx, items); err != nil {
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
	h.emit.ToAll("library_removed", result)
	for _, item := range items {
		h.emit.ToAll("item_removed", map[string]any{"id": item.id, "libraryId": id})
	}
	if h.cfg.MetadataPath != "" {
		for _, item := range items {
			_ = os.RemoveAll(filepath.Join(h.cfg.Dirs().Items, item.id))
		}
	}
	return httputil.JSON(w, r, result)
}

func (h *Handler) reorderLibraries(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	if !adminUser(user) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var order []struct {
		ID       string  `json:"id"`
		NewOrder float64 `json:"newOrder"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&order) != nil {
		return httputil.Status(400, "Invalid request. Request body must be an array of objects")
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	for _, row := range order {
		if row.ID == "" {
			return httputil.Status(400, "Invalid request. Request body must be an array of objects")
		}
		var old int
		if err := tx.QueryRowContext(r.Context(), `SELECT displayOrder FROM libraries WHERE id=?`, row.ID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
			return httputil.Status(400, "Library not found with id "+row.ID)
		} else if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if float64(old) != row.NewOrder {
			if _, err := tx.ExecContext(r.Context(), `UPDATE libraries SET displayOrder=?,updatedAt=? WHERE id=?`, row.NewOrder, dbtime.Now(), row.ID); err != nil {
				return httputil.Internal("database write failed", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM libraries ORDER BY displayOrder`)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	result := make([]libraryView, 0, len(ids))
	for _, id := range ids {
		library, e := h.libraryView(r.Context(), id)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		result = append(result, library)
	}
	return httputil.JSON(w, r, map[string]any{"libraries": result})
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
