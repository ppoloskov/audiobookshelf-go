package userapi

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/dbtime"

	itemapi "audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
)

// playlistRoute implements the legacy playlist API. Playlist ownership is
// enforced by the upstream middleware: only the creating user can access an
// individual playlist, and that user must still have library access.
func (h *Handler) playlistRoute(w http.ResponseWriter, r *http.Request, user store.User, tail string) bool {
	if tail == "" || tail == "/" {
		switch r.Method {
		case http.MethodGet:
			rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM playlists WHERE userId=?`, user.ID)
			if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			ids := make([]string, 0)
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					http.Error(w, "database read failed", 500)
					return true
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			out := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				item, libraryID, err := h.playlistJSON(r, id, user)
				if errors.Is(err, sql.ErrNoRows) || (err == nil && !canAccessLibrary(permissions(user), libraryID)) {
					continue
				}
				if err != nil {
					http.Error(w, "database read failed", 500)
					return true
				}
				out = append(out, item)
			}
			sort.SliceStable(out, func(i, j int) bool { return stringValue(out[i]["name"]) < stringValue(out[j]["name"]) })
			writeJSON(w, 200, map[string]any{"playlists": out})
			return true
		case http.MethodPost:
			h.createPlaylist(w, r, user)
			return true
		default:
			return false
		}
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	if len(parts) == 2 && parts[0] == "collection" && r.Method == http.MethodPost {
		h.createPlaylistFromCollection(w, r, user, parts[1])
		return true
	}
	if len(parts) < 1 || parts[0] == "" {
		return false
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, _, code, err := h.ownedPlaylist(r, id, user)
			if code != 0 {
				respondPlaylistError(w, code)
				return true
			}
			if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			writeJSON(w, 200, item)
			return true
		case http.MethodPatch:
			h.updatePlaylist(w, r, id, user)
			return true
		case http.MethodDelete:
			h.deletePlaylist(w, r, id, user)
			return true
		}
	}
	if len(parts) == 2 && parts[1] == "item" {
		if r.Method == http.MethodPost {
			h.addPlaylistItem(w, r, id, user, false)
			return true
		}
	}
	if len(parts) == 3 && parts[1] == "batch" && r.Method == http.MethodPost {
		switch parts[2] {
		case "add":
			h.addPlaylistItem(w, r, id, user, true)
			return true
		case "remove":
			h.removePlaylistItems(w, r, id, user)
			return true
		}
	}
	if len(parts) >= 3 && parts[1] == "item" && r.Method == http.MethodDelete {
		libraryItemID := parts[2]
		episodeID := ""
		if len(parts) > 3 {
			episodeID = parts[3]
		}
		h.removePlaylistItem(w, r, id, user, libraryItemID, episodeID)
		return true
	}
	return false
}

func (h *Handler) ownedPlaylist(r *http.Request, id string, user store.User) (map[string]any, string, int, error) {
	var ownerID, libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT userId,libraryId FROM playlists WHERE id=?`, id).Scan(&ownerID, &libraryID); errors.Is(err, sql.ErrNoRows) {
		return nil, "", http.StatusNotFound, nil
	} else if err != nil {
		return nil, "", 0, err
	}
	if ownerID != user.ID {
		return nil, "", http.StatusForbidden, nil
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		return nil, "", http.StatusNotFound, nil
	}
	item, _, err := h.playlistJSON(r, id, user)
	return item, libraryID, 0, err
}

func (h *Handler) playlistJSON(r *http.Request, id string, user store.User) (map[string]any, string, error) {
	var name string
	var description sql.NullString
	var libraryID, ownerID string
	var created, updated any
	if err := h.db.QueryRowContext(r.Context(), `SELECT name,description,libraryId,userId,createdAt,updatedAt FROM playlists WHERE id=?`, id).Scan(&name, &description, &libraryID, &ownerID, &created, &updated); err != nil {
		return nil, "", err
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT mediaItemId,mediaItemType FROM playlistMediaItems WHERE playlistId=? ORDER BY "order" ASC`, id)
	if err != nil {
		return nil, "", err
	}
	type mediaRef struct{ id, typ string }
	refs := make([]mediaRef, 0)
	for rows.Next() {
		var ref mediaRef
		if err := rows.Scan(&ref.id, &ref.typ); err != nil {
			rows.Close()
			return nil, "", err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	items := make([]any, 0, len(refs))
	for _, ref := range refs {
		var libraryItemID string
		if ref.typ == "podcastEpisode" {
			err = h.db.QueryRowContext(r.Context(), `SELECT li.id FROM podcastEpisodes pe JOIN libraryItems li ON li.mediaId=pe.podcastId AND li.mediaType='podcast' WHERE pe.id=?`, ref.id).Scan(&libraryItemID)
		} else {
			err = h.db.QueryRowContext(r.Context(), `SELECT id FROM libraryItems WHERE mediaId=? AND mediaType='book'`, ref.id).Scan(&libraryItemID)
		}
		if err != nil {
			continue // upstream reconciliation removes orphaned playlist records
		}
		if ref.typ == "podcastEpisode" {
			// Upstream playlist entries use libraryItem.toOldJSONExpanded() and
			// episode.toOldJSONExpanded(libraryItem.id), so the embedded item is
			// the expanded representation, not the minified one.
			li, err := h.items.ItemJSON(r.Context(), libraryItemID, true)
			if err != nil {
				return nil, "", err
			}
			media, _ := li["media"].(map[string]any)
			episodes, _ := media["episodes"].([]map[string]any)
			var episode map[string]any
			for _, candidate := range episodes {
				if candidate["id"] == ref.id {
					episode = candidate
					break
				}
			}
			if episode == nil {
				continue
			}
			minified, err := h.items.ItemMinifiedJSON(r.Context(), libraryItemID)
			if err != nil {
				return nil, "", err
			}
			items = append(items, map[string]any{"episodeId": ref.id, "episode": episode, "libraryItemId": libraryItemID, "libraryItem": minified})
		} else {
			li, err := h.items.ItemJSON(r.Context(), libraryItemID, true)
			if err != nil {
				return nil, "", err
			}
			items = append(items, map[string]any{"libraryItemId": libraryItemID, "libraryItem": li})
		}
	}
	return map[string]any{"id": id, "name": name, "libraryId": libraryID, "userId": ownerID, "description": nullableValue(description), "lastUpdate": millis(updated), "createdAt": millis(created), "items": items}, libraryID, nil
}

func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request, user store.User) {
	body, err := decodeBody(r)
	if err != nil {
		http.Error(w, "Invalid playlist data", 400)
		return
	}
	name, _ := body["name"].(string)
	name = cleanName(name)
	libraryID, _ := body["libraryId"].(string)
	description, descOK := body["description"].(string)
	if name == "" || libraryID == "" {
		http.Error(w, "Invalid playlist data", 400)
		return
	}
	if jsTruthy(body["description"]) && !descOK {
		http.Error(w, "Invalid playlist description", 400)
		return
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		sendStatus(w, 403)
		return
	}
	items, ok := body["items"].([]any)
	if !ok {
		items = []any{}
	}
	type itemRef struct{ libraryItemID, episodeID, mediaID, mediaType string }
	refs := make([]itemRef, 0, len(items))
	isPodcast := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			http.Error(w, "Invalid playlist item", 400)
			return
		}
		li, _ := item["libraryItemId"].(string)
		ep, _ := item["episodeId"].(string)
		if jsTruthy(item["episodeId"]) && ep == "" {
			http.Error(w, "Invalid playlist item episodeId", 400)
			return
		}
		if li == "" {
			http.Error(w, "Invalid playlist item", 400)
			return
		}
		if ep != "" {
			isPodcast = true
		}
		refs = append(refs, itemRef{libraryItemID: li, episodeID: ep})
	}
	for i := range refs {
		ref := &refs[i]
		var mediaID, mediaType, lib string
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, ref.libraryItemID).Scan(&mediaID, &mediaType, &lib); err != nil || lib != libraryID || (isPodcast && mediaType != "podcast") || (!isPodcast && mediaType != "book") {
			http.Error(w, "Invalid playlist data. Invalid items", 400)
			return
		}
		if isPodcast && ref.episodeID == "" {
			http.Error(w, "Invalid playlist item episodeId", 400)
			return
		}
		if !isPodcast && ref.episodeID != "" {
			http.Error(w, "Invalid playlist item episodeId", 400)
			return
		}
		ref.mediaID, ref.mediaType = mediaID, "book"
		if isPodcast {
			ref.mediaID, ref.mediaType = ref.episodeID, "podcastEpisode"
			var exists int
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, ref.episodeID, mediaID).Scan(&exists); err != nil || exists == 0 {
				http.Error(w, "Invalid playlist data. Invalid podcast episodes", 400)
				return
			}
		}
	}
	id, now := newUUID(), dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO playlists(id,name,description,libraryId,userId,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?)`, id, name, nullableStringForDB(description), libraryID, user.ID, now, now); err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	for i, ref := range refs {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES(?,?,?,?,?,?)`, newUUID(), ref.mediaID, ref.mediaType, i+1, id, now); err != nil {
			http.Error(w, "Failed to create playlist", 500)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	item, _, err := h.playlistJSON(r, id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_added", item)
	}
	writeJSON(w, 200, item)
}

func nullableStringForDB(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h *Handler) updatePlaylist(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	if jsTruthy(body["libraryId"]) || jsTruthy(body["userId"]) {
		http.Error(w, "Invalid playlist data. Cannot update libraryId or userId", 400)
		return
	}
	if jsTruthy(body["name"]) {
		name := body["name"]
		if _, ok := name.(string); !ok {
			http.Error(w, "Invalid playlist name", 400)
			return
		}
	}
	if jsTruthy(body["description"]) {
		desc := body["description"]
		if _, ok := desc.(string); !ok {
			http.Error(w, "Invalid playlist description", 400)
			return
		}
	}
	var requestItems []any
	if raw, exists := body["items"]; exists && raw != nil {
		var ok bool
		requestItems, ok = raw.([]any)
		if !ok {
			http.Error(w, "Invalid playlist items", 400)
			return
		}
		for _, rawItem := range requestItems {
			item, ok := rawItem.(map[string]any)
			if !ok {
				http.Error(w, "Invalid playlist items", 400)
				return
			}
			li, _ := item["libraryItemId"].(string)
			if li == "" {
				http.Error(w, "Invalid playlist items", 400)
				return
			}
			if jsTruthy(item["episodeId"]) {
				ep := item["episodeId"]
				if _, ok := ep.(string); !ok {
					http.Error(w, "Invalid playlist items", 400)
					return
				}
			}
		}
	}
	var oldName string
	var oldDescription sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT name,description FROM playlists WHERE id=?`, id).Scan(&oldName, &oldDescription); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	newName := oldName
	if raw, exists := body["name"]; exists {
		if s, _ := raw.(string); cleanName(s) != "" {
			newName = cleanName(s)
		}
	}
	var newDescription any
	if oldDescription.Valid {
		newDescription = oldDescription.String
	}
	if raw, exists := body["description"]; exists {
		if s, ok := raw.(string); ok && s != "" {
			newDescription = s
		}
	}
	changed := newName != oldName || (body["description"] != nil && stringValue(body["description"]) != oldDescription.String)
	if changed {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE playlists SET name=?,description=?,updatedAt=? WHERE id=?`, newName, newDescription, dbtime.Now(), id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
	}
	itemsChanged := false
	if len(requestItems) > 0 {
		refs := make([]string, 0, len(requestItems))
		for _, rawItem := range requestItems {
			item := rawItem.(map[string]any)
			li := stringValue(item["libraryItemId"])
			var mediaID string
			if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, li).Scan(&mediaID); err != nil {
				http.Error(w, "Invalid playlist items. Items not found", 400)
				return
			}
			if episode := stringValue(item["episodeId"]); episode != "" {
				mediaID = episode
			}
			refs = append(refs, mediaID)
		}
		rows, err := h.db.QueryContext(r.Context(), `SELECT id,mediaItemId,"order" FROM playlistMediaItems WHERE playlistId=? ORDER BY "order" ASC`, id)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		type existing struct {
			id, mediaID string
			order       int
		}
		existingItems := make([]existing, 0)
		for rows.Next() {
			var entry existing
			if err := rows.Scan(&entry.id, &entry.mediaID, &entry.order); err != nil {
				rows.Close()
				http.Error(w, "database read failed", 500)
				return
			}
			existingItems = append(existingItems, entry)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if len(existingItems) != len(requestItems) {
			http.Error(w, "Invalid playlist items. Length mismatch", 400)
			return
		}
		sort.SliceStable(existingItems, func(i, j int) bool {
			index := func(id string) int {
				for n, wanted := range refs {
					if wanted == id {
						return n
					}
				}
				return len(refs)
			}
			return index(existingItems[i].mediaID) < index(existingItems[j].mediaID)
		})
		for i, entry := range existingItems {
			want := i + 1
			if entry.order != want {
				if _, err := h.db.ExecContext(r.Context(), `UPDATE playlistMediaItems SET "order"=? WHERE id=?`, want, entry.id); err != nil {
					http.Error(w, "database write failed", 500)
					return
				}
				itemsChanged = true
			}
		}
	}
	item, _, err := h.playlistJSON(r, id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if (changed || itemsChanged) && h.emit != nil {
		h.emit(user.ID, "playlist_updated", item)
	}
	writeJSON(w, 200, item)
}

func (h *Handler) deletePlaylist(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	item, _, code, err := h.ownedPlaylist(r, id, user)
	if code != 0 {
		respondPlaylistError(w, code)
		return
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlistMediaItems WHERE playlistId=?`, id); err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlists WHERE id=?`, id); err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_removed", item)
	}
	sendStatus(w, 200)
}

func (h *Handler) addPlaylistItem(w http.ResponseWriter, r *http.Request, id string, user store.User, batch bool) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		sendText(w, 400, "Invalid request body items")
		return
	}
	type ref struct{ libraryItemID, episodeID string }
	refs := make([]ref, 0)
	if batch {
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 {
			sendText(w, 400, "Invalid request body items")
			return
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok || stringValue(item["libraryItemId"]) == "" {
				sendText(w, 400, "Invalid request body items")
				return
			}
			if jsTruthy(item["episodeId"]) && stringValue(item["episodeId"]) == "" {
				sendText(w, 400, "Invalid request body items")
				return
			}
			refs = append(refs, ref{stringValue(item["libraryItemId"]), stringValue(item["episodeId"])})
		}
	} else {
		if stringValue(body["libraryItemId"]) == "" {
			http.Error(w, "Request body has no libraryItemId", 400)
			return
		}
		if jsTruthy(body["episodeId"]) && stringValue(body["episodeId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		refs = append(refs, ref{stringValue(body["libraryItemId"]), stringValue(body["episodeId"])})
	}
	var libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM playlists WHERE id=?`, id).Scan(&libraryID); err != nil {
		sendEntityNotFound(w, "Playlist not found")
		return
	}
	var count int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId=?`, id).Scan(&count); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	changed := false
	for _, ref := range refs {
		var mediaID, typ, lib string
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, ref.libraryItemID).Scan(&mediaID, &typ, &lib); err != nil {
			if batch {
				sendText(w, 400, "Invalid request body items")
				return
			}
			http.Error(w, "Library item not found", 400)
			return
		}
		if lib != libraryID {
			http.Error(w, "Library item in different library", 400)
			return
		}
		mediaType := "book"
		if typ == "podcast" && ref.episodeID != "" {
			var exists int
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, ref.episodeID, mediaID).Scan(&exists); err != nil || exists == 0 {
				http.Error(w, "Episode not found in library item", 400)
				return
			}
			mediaID, mediaType = ref.episodeID, "podcastEpisode"
		} else if typ == "podcast" || ref.episodeID != "" {
			http.Error(w, "Invalid item to add for this library type", 400)
			return
		}
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId=? AND mediaItemId=?`, id, mediaID).Scan(&exists); err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if exists > 0 {
			if !batch {
				http.Error(w, "Item already in playlist", 400)
				return
			}
			continue
		}
		if _, err := h.db.ExecContext(r.Context(), `INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES(?,?,?,?,?,?)`, newUUID(), mediaID, mediaType, count+1, id, dbtime.Now()); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		count++
		changed = true
	}
	item, _, err := h.playlistJSON(r, id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if changed && h.emit != nil {
		h.emit(user.ID, "playlist_updated", item)
	}
	writeJSONKeys(w, 200, item, []string{"id", "name", "libraryId", "userId", "description", "lastUpdate", "createdAt", "items"})
}

func (h *Handler) removePlaylistItems(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	body, err := decodeBody(r)
	items, ok := body["items"].([]any)
	if err != nil || !ok || len(items) == 0 {
		http.Error(w, "Invalid request body items", 400)
		return
	}
	remove := make(map[string]bool)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || stringValue(item["libraryItemId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		if jsTruthy(item["episodeId"]) && stringValue(item["episodeId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		if episode := stringValue(item["episodeId"]); episode != "" {
			remove[episode] = true
		} else {
			var mediaID string
			if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, stringValue(item["libraryItemId"])).Scan(&mediaID); err == nil {
				remove[mediaID] = true
			}
		}
	}
	h.deletePlaylistMediaIDs(w, r, id, user, remove)
}

func (h *Handler) removePlaylistItem(w http.ResponseWriter, r *http.Request, id string, user store.User, libraryItemID, episodeID string) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	mediaID := episodeID
	if mediaID == "" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, libraryItemID).Scan(&mediaID); err != nil {
			http.Error(w, "Media item not found in playlist", 404)
			return
		}
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId=? AND mediaItemId=?`, id, mediaID).Scan(&exists); err != nil || exists == 0 {
		http.Error(w, "Media item not found in playlist", 404)
		return
	}
	h.deletePlaylistMediaIDs(w, r, id, user, map[string]bool{mediaID: true})
}

func (h *Handler) deletePlaylistMediaIDs(w http.ResponseWriter, r *http.Request, id string, user store.User, remove map[string]bool) {
	finalDTO, _, preErr := h.playlistJSON(r, id, user)
	if preErr != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if currentItems, ok := finalDTO["items"].([]any); ok {
		remaining := make([]any, 0, len(currentItems))
		for _, raw := range currentItems {
			entry, _ := raw.(map[string]any)
			mediaID := stringValue(entry["episodeId"])
			if mediaID == "" {
				if libraryItem, ok := entry["libraryItem"].(map[string]any); ok {
					if access, ok := itemapi.AccessMedia(libraryItem["media"]); ok {
						mediaID = access.ID
					}
				}
			}
			if !remove[mediaID] {
				remaining = append(remaining, raw)
			}
		}
		finalDTO["items"] = remaining
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(r.Context(), `SELECT id,mediaItemId FROM playlistMediaItems WHERE playlistId=? ORDER BY "order" ASC`, id)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	type entry struct{ id, mediaID string }
	entries := make([]entry, 0)
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.mediaID); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	order, changed := 1, false
	for _, e := range entries {
		if remove[e.mediaID] {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlistMediaItems WHERE id=?`, e.id); err != nil {
				http.Error(w, "database write failed", 500)
				return
			}
			changed = true
			continue
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE playlistMediaItems SET "order"=? WHERE id=?`, order, e.id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		order++
	}
	if changed && order == 1 {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM playlists WHERE id=?`, id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	if changed && h.emit != nil {
		event := "playlist_updated"
		if order == 1 {
			event = "playlist_removed"
		}
		h.emit(user.ID, event, finalDTO)
	}
	writeJSON(w, 200, finalDTO)
}

func (h *Handler) createPlaylistFromCollection(w http.ResponseWriter, r *http.Request, user store.User, collectionID string) {
	dto, libraryID, visible, err := h.collectionDTO(r.Context(), collectionID, user)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !canAccessLibrary(permissions(user), libraryID)) || (err == nil && !visible) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	books, _ := dto["books"].([]any)
	if len(books) == 0 {
		http.Error(w, "Collection has no books", 400)
		return
	}
	name, _ := dto["name"].(string)
	desc, _ := dto["description"].(string)
	id, now := newUUID(), dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO playlists(id,name,description,libraryId,userId,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?)`, id, name, nullableStringForDB(desc), libraryID, user.ID, now, now); err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	for i, raw := range books {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mediaID := ""
		if access, ok := itemapi.AccessMedia(item["media"]); ok {
			mediaID = access.ID
		}
		if mediaID == "" {
			continue
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId,createdAt) VALUES(?,?,?,?,?,?)`, newUUID(), mediaID, "book", i+1, id, now); err != nil {
			http.Error(w, "Failed to create playlist", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Failed to create playlist", 500)
		return
	}
	item, _, err := h.playlistJSON(r, id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_added", item)
	}
	writeJSON(w, 200, item)
}
