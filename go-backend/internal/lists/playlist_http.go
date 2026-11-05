package lists

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"

	"audiobookshelf-compat/internal/db/sqlc"
	itemapi "audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
)

func (h *Handler) listPlaylists(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	ids, err := h.queries().ListUserPlaylistIDs(r.Context(), user.ID)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item, libraryID, err := Playlist(r.Context(), h.db, h.items, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !h.canAccessLibrary(user, libraryID)) {
			continue
		}
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		out = append(out, item)
	}
	// Playlist.getOldPlaylistsForUserAndLibrary: name.localeCompare
	// (server/models/Playlist.js:85).
	sort.SliceStable(out, func(i, j int) bool {
		return textsort.LocaleCompare(jsonvalue.String(out[i]["name"]), jsonvalue.String(out[j]["name"])) < 0
	})
	httputil.WriteJSON(w, 200, map[string]any{"playlists": out})
}

func (h *Handler) ownedPlaylist(r *http.Request, id string, user store.User) (map[string]any, string, int, error) {
	playlist, err := h.queries().GetPlaylist(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", http.StatusNotFound, nil
	} else if err != nil {
		return nil, "", 0, err
	}
	libraryID := playlist.Libraryid.String
	if playlist.Userid.String != user.ID {
		return nil, "", http.StatusForbidden, nil
	}
	if !h.canAccessLibrary(user, libraryID) {
		return nil, "", http.StatusNotFound, nil
	}
	item, _, err := Playlist(r.Context(), h.db, h.items, id)
	return item, libraryID, 0, err
}

func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	body, err := httputil.DecodeObjectRequest(r)
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
	if jsonvalue.Truthy(body["description"]) && !descOK {
		http.Error(w, "Invalid playlist description", 400)
		return
	}
	if !h.canAccessLibrary(user, libraryID) {
		httputil.SendStatus(w, 403)
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
		if jsonvalue.Truthy(item["episodeId"]) && ep == "" {
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
	id, err := ids.New()
	if err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err := q.InsertPlaylist(r.Context(), sqlc.InsertPlaylistParams{ID: id, Name: name, Description: store.TextOrNull(description), LibraryID: libraryID, UserID: user.ID, Createdat: now, Updatedat: now}); err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	for i, ref := range refs {
		entryID, err := ids.New()
		if err != nil {
			writeInternalError(w, r, "Failed to create playlist", err)
			return
		}
		if err := q.InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: ref.mediaID, MediaType: ref.mediaType, Position: int64(i + 1), PlaylistID: id, Createdat: now}); err != nil {
			writeInternalError(w, r, "Failed to create playlist", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	item, _, err := Playlist(r.Context(), h.db, h.items, id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_added", item)
	}
	httputil.WriteJSON(w, 200, item)
}

func (h *Handler) updatePlaylist(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	if jsonvalue.Truthy(body["libraryId"]) || jsonvalue.Truthy(body["userId"]) {
		http.Error(w, "Invalid playlist data. Cannot update libraryId or userId", 400)
		return
	}
	if jsonvalue.Truthy(body["name"]) {
		name := body["name"]
		if _, ok := name.(string); !ok {
			http.Error(w, "Invalid playlist name", 400)
			return
		}
	}
	if jsonvalue.Truthy(body["description"]) {
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
			if jsonvalue.Truthy(item["episodeId"]) {
				ep := item["episodeId"]
				if _, ok := ep.(string); !ok {
					http.Error(w, "Invalid playlist items", 400)
					return
				}
			}
		}
	}
	old, err := h.queries().GetPlaylist(r.Context(), id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	oldName, oldDescription := old.Name.String, old.Description
	newName := oldName
	if raw, exists := body["name"]; exists {
		if s, _ := raw.(string); cleanName(s) != "" {
			newName = cleanName(s)
		}
	}
	newDescription := oldDescription
	if raw, exists := body["description"]; exists {
		if s, ok := raw.(string); ok && s != "" {
			newDescription = store.Text(s)
		}
	}
	changed := newName != oldName || (body["description"] != nil && jsonvalue.String(body["description"]) != oldDescription.String)
	if changed {
		if err := h.queries().UpdatePlaylist(r.Context(), sqlc.UpdatePlaylistParams{Name: newName, Description: newDescription, Updatedat: dbtime.Now(), ID: id}); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
	}
	itemsChanged := false
	if len(requestItems) > 0 {
		refs := make([]string, 0, len(requestItems))
		for _, rawItem := range requestItems {
			item := rawItem.(map[string]any)
			li := jsonvalue.String(item["libraryItemId"])
			var mediaID string
			if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, li).Scan(&mediaID); err != nil {
				http.Error(w, "Invalid playlist items. Items not found", 400)
				return
			}
			if episode := jsonvalue.String(item["episodeId"]); episode != "" {
				mediaID = episode
			}
			refs = append(refs, mediaID)
		}
		entries, err := h.queries().ListPlaylistMediaItems(r.Context(), id)
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		type existing struct {
			id, mediaID string
			order       int
		}
		existingItems := make([]existing, 0, len(entries))
		for _, e := range entries {
			existingItems = append(existingItems, existing{id: e.ID, mediaID: e.Mediaitemid.String, order: int(e.Order.Int64)})
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
				if err := h.queries().UpdatePlaylistMediaItemOrder(r.Context(), sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(want), ID: entry.id}); err != nil {
					writeInternalError(w, r, "database write failed", err)
					return
				}
				itemsChanged = true
			}
		}
	}
	item, _, err := Playlist(r.Context(), h.db, h.items, id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if (changed || itemsChanged) && h.emit != nil {
		h.emit(user.ID, "playlist_updated", item)
	}
	httputil.WriteJSON(w, 200, item)
}

func (h *Handler) deletePlaylist(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	item, _, code, err := h.ownedPlaylist(r, id, user)
	if code != 0 {
		respondPlaylistError(w, code)
		return
	}
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err := q.DeletePlaylistMediaItems(r.Context(), id); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	if err := q.DeletePlaylist(r.Context(), id); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_removed", item)
	}
	httputil.SendStatus(w, 200)
}

func (h *Handler) addPlaylistItem(w http.ResponseWriter, r *http.Request, id string, user store.User, batch bool) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		httputil.SendText(w, 400, "Invalid request body items")
		return
	}
	type ref struct{ libraryItemID, episodeID string }
	refs := make([]ref, 0)
	if batch {
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 {
			httputil.SendText(w, 400, "Invalid request body items")
			return
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok || jsonvalue.String(item["libraryItemId"]) == "" {
				httputil.SendText(w, 400, "Invalid request body items")
				return
			}
			if jsonvalue.Truthy(item["episodeId"]) && jsonvalue.String(item["episodeId"]) == "" {
				httputil.SendText(w, 400, "Invalid request body items")
				return
			}
			refs = append(refs, ref{jsonvalue.String(item["libraryItemId"]), jsonvalue.String(item["episodeId"])})
		}
	} else {
		if jsonvalue.String(body["libraryItemId"]) == "" {
			http.Error(w, "Request body has no libraryItemId", 400)
			return
		}
		if jsonvalue.Truthy(body["episodeId"]) && jsonvalue.String(body["episodeId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		refs = append(refs, ref{jsonvalue.String(body["libraryItemId"]), jsonvalue.String(body["episodeId"])})
	}
	playlist, err := h.queries().GetPlaylist(r.Context(), id)
	if err != nil {
		sendEntityNotFound(w, "Playlist not found")
		return
	}
	libraryID := playlist.Libraryid.String
	entryCount, err := h.queries().CountPlaylistMediaItems(r.Context(), id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	count := int(entryCount)
	changed := false
	for _, ref := range refs {
		var mediaID, typ, lib string
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, ref.libraryItemID).Scan(&mediaID, &typ, &lib); err != nil {
			if batch {
				httputil.SendText(w, 400, "Invalid request body items")
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
		exists, err := h.queries().CountPlaylistMediaItem(r.Context(), sqlc.CountPlaylistMediaItemParams{PlaylistID: id, MediaID: mediaID})
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		if exists > 0 {
			if !batch {
				http.Error(w, "Item already in playlist", 400)
				return
			}
			continue
		}
		entryID, err := ids.New()
		if err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		if err := h.queries().InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: mediaID, MediaType: mediaType, Position: int64(count + 1), PlaylistID: id, Createdat: dbtime.Now()}); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		count++
		changed = true
	}
	item, _, err := Playlist(r.Context(), h.db, h.items, id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if !batch && changed && refs[0].episodeID != "" {
		// PlaylistController.addItem (PlaylistController.js:321-328) appends the
		// added episode with the library item it already loaded WITH episodes,
		// unlike a reloaded playlist whose podcasts have none.
		entries, _ := item["items"].([]any)
		if len(entries) > 0 {
			if entry, ok := entries[len(entries)-1].(map[string]any); ok && entry["episodeId"] == refs[0].episodeID {
				if full, e := h.items.ItemJSON(r.Context(), refs[0].libraryItemID, true); e == nil {
					if media, ok := full["media"].(map[string]any); ok {
						if minified, ok := entry["libraryItem"].(map[string]any); ok {
							if minifiedMedia, ok := minified["media"].(map[string]any); ok {
								minifiedMedia["numEpisodes"] = media["numEpisodes"]
								minifiedMedia["size"] = media["size"]
							}
						}
					}
				}
			}
		}
	}
	if changed && h.emit != nil {
		h.emit(user.ID, "playlist_updated", item)
	}
	httputil.WriteJSON(w, 200, item)
}

func (h *Handler) removePlaylistItems(w http.ResponseWriter, r *http.Request, id string, user store.User) {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	items, ok := body["items"].([]any)
	if err != nil || !ok || len(items) == 0 {
		http.Error(w, "Invalid request body items", 400)
		return
	}
	remove := make(map[string]bool)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || jsonvalue.String(item["libraryItemId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		if jsonvalue.Truthy(item["episodeId"]) && jsonvalue.String(item["episodeId"]) == "" {
			http.Error(w, "Invalid request body items", 400)
			return
		}
		if episode := jsonvalue.String(item["episodeId"]); episode != "" {
			remove[episode] = true
		} else {
			var mediaID string
			if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, jsonvalue.String(item["libraryItemId"])).Scan(&mediaID); err == nil {
				remove[mediaID] = true
			}
		}
	}
	h.deletePlaylistMediaIDs(w, r, id, user, remove)
}

func (h *Handler) removePlaylistItem(w http.ResponseWriter, r *http.Request) {
	id, user := r.PathValue("id"), requestUser(r)
	parts := strings.Split(r.PathValue("itemPath"), "/")
	libraryItemID, episodeID := parts[0], ""
	if len(parts) > 1 {
		episodeID = parts[1]
	}
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		respondPlaylistError(w, code)
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	mediaID := episodeID
	if mediaID == "" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, libraryItemID).Scan(&mediaID); err != nil {
			http.Error(w, "Media item not found in playlist", 404)
			return
		}
	}
	if exists, err := h.queries().CountPlaylistMediaItem(r.Context(), sqlc.CountPlaylistMediaItemParams{PlaylistID: id, MediaID: mediaID}); err != nil || exists == 0 {
		http.Error(w, "Media item not found in playlist", 404)
		return
	}
	h.deletePlaylistMediaIDs(w, r, id, user, map[string]bool{mediaID: true})
}

func (h *Handler) deletePlaylistMediaIDs(w http.ResponseWriter, r *http.Request, id string, user store.User, remove map[string]bool) {
	finalDTO, _, preErr := Playlist(r.Context(), h.db, h.items, id)
	if preErr != nil {
		writeInternalError(w, r, "database read failed", preErr)
		return
	}
	if currentItems, ok := finalDTO["items"].([]any); ok {
		remaining := make([]any, 0, len(currentItems))
		for _, raw := range currentItems {
			entry, _ := raw.(map[string]any)
			mediaID := jsonvalue.String(entry["episodeId"])
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
		writeInternalError(w, r, "database write failed", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	rows, err := q.ListPlaylistMediaItems(r.Context(), id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	type entry struct{ id, mediaID string }
	entries := make([]entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, entry{id: row.ID, mediaID: row.Mediaitemid.String})
	}
	order, changed := 1, false
	for _, e := range entries {
		if remove[e.mediaID] {
			if err := q.DeletePlaylistMediaItem(r.Context(), e.id); err != nil {
				writeInternalError(w, r, "database write failed", err)
				return
			}
			changed = true
			continue
		}
		if err := q.UpdatePlaylistMediaItemOrder(r.Context(), sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(order), ID: e.id}); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		order++
	}
	if changed && order == 1 {
		if err := q.DeletePlaylist(r.Context(), id); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	if changed && h.emit != nil {
		event := "playlist_updated"
		if order == 1 {
			event = "playlist_removed"
		}
		h.emit(user.ID, event, finalDTO)
	}
	httputil.WriteJSON(w, 200, finalDTO)
}

func (h *Handler) createPlaylistFromCollection(w http.ResponseWriter, r *http.Request, user store.User, collectionID string) {
	dto, libraryID, err := h.collection(r.Context(), collectionID, user)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !h.canAccessLibrary(user, libraryID)) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	books, _ := dto["books"].([]any)
	if len(books) == 0 {
		http.Error(w, "Collection has no books", 400)
		return
	}
	name, _ := dto["name"].(string)
	desc, _ := dto["description"].(string)
	id, err := ids.New()
	if err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err = q.InsertPlaylist(r.Context(), sqlc.InsertPlaylistParams{ID: id, Name: name, Description: store.TextOrNull(desc), LibraryID: libraryID, UserID: user.ID, Createdat: now, Updatedat: now}); err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
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
		entryID, genErr := ids.New()
		if genErr != nil {
			writeInternalError(w, r, "Failed to create playlist", genErr)
			return
		}
		if err = q.InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: mediaID, MediaType: "book", Position: int64(i + 1), PlaylistID: id, Createdat: now}); err != nil {
			writeInternalError(w, r, "Failed to create playlist", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeInternalError(w, r, "Failed to create playlist", err)
		return
	}
	item, _, err := Playlist(r.Context(), h.db, h.items, id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit(user.ID, "playlist_added", item)
	}
	httputil.WriteJSON(w, 200, item)
}
