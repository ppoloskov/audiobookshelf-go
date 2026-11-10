package lists

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"

	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
)

func (h *Handler) listPlaylists(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	ids, err := sqlc.New(h.db).ListUserPlaylistIDs(r.Context(), user.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item, libraryID, err := playlistview.Load(r.Context(), h.db, id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !access.For(r.Context(), user).Library(libraryID)) {
			continue
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		out = append(out, item)
	}
	// Playlist.getOldPlaylistsForUserAndLibrary: name.localeCompare
	// (server/models/Playlist.js:85).
	sort.SliceStable(out, func(i, j int) bool {
		return textsort.LocaleCompare(jsonvalue.String(out[i]["name"]), jsonvalue.String(out[j]["name"])) < 0
	})
	return httputil.JSON(w, r, map[string]any{"playlists": out})
}

func (h *Handler) ownedPlaylist(r *http.Request, id string, user store.User) (map[string]any, string, int, error) {
	playlist, err := sqlc.New(h.db).GetPlaylist(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", http.StatusNotFound, nil
	} else if err != nil {
		return nil, "", 0, err
	}
	libraryID := playlist.Libraryid.String
	if playlist.Userid.String != user.ID {
		return nil, "", http.StatusForbidden, nil
	}
	if !access.For(r.Context(), user).Library(libraryID) {
		return nil, "", http.StatusNotFound, nil
	}
	item, _, err := playlistview.Load(r.Context(), h.db, id)
	return item, libraryID, 0, err
}

func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid playlist data")
	}
	name, _ := body["name"].(string)
	name = cleanName(name)
	libraryID, _ := body["libraryId"].(string)
	description, descOK := body["description"].(string)
	if name == "" || libraryID == "" {
		return httputil.Status(400, "Invalid playlist data")
	}
	if jsonvalue.Truthy(body["description"]) && !descOK {
		return httputil.Status(400, "Invalid playlist description")
	}
	if !access.For(r.Context(), user).Library(libraryID) {
		return httputil.Status(403, "")
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
			return httputil.Status(400, "Invalid playlist item")
		}
		li, _ := item["libraryItemId"].(string)
		ep, _ := item["episodeId"].(string)
		if jsonvalue.Truthy(item["episodeId"]) && ep == "" {
			return httputil.Status(400, "Invalid playlist item episodeId")
		}
		if li == "" {
			return httputil.Status(400, "Invalid playlist item")
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
			return httputil.Status(400, "Invalid playlist data. Invalid items")
		}
		if isPodcast && ref.episodeID == "" {
			return httputil.Status(400, "Invalid playlist item episodeId")
		}
		if !isPodcast && ref.episodeID != "" {
			return httputil.Status(400, "Invalid playlist item episodeId")
		}
		ref.mediaID, ref.mediaType = mediaID, "book"
		if isPodcast {
			ref.mediaID, ref.mediaType = ref.episodeID, "podcastEpisode"
			var exists int
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, ref.episodeID, mediaID).Scan(&exists); err != nil || exists == 0 {
				return httputil.Status(400, "Invalid playlist data. Invalid podcast episodes")
			}
		}
	}
	id, err := ids.New()
	if err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err := q.InsertPlaylist(r.Context(), sqlc.InsertPlaylistParams{ID: id, Name: name, Description: store.TextOrNull(description), LibraryID: libraryID, UserID: user.ID, Createdat: now, Updatedat: now}); err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	for i, ref := range refs {
		entryID, err := ids.New()
		if err != nil {
			return httputil.Internal("Failed to create playlist", err)
		}
		if err := q.InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: ref.mediaID, MediaType: ref.mediaType, Position: int64(i + 1), PlaylistID: id, Createdat: now}); err != nil {
			return httputil.Internal("Failed to create playlist", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	item, _, err := playlistview.Load(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToUser(user.ID, "playlist_added", item)
	return httputil.JSON(w, r, item)
}

func (h *Handler) updatePlaylist(w http.ResponseWriter, r *http.Request) error {
	user, id := store.RequestUser(r), r.PathValue("id")
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		return playlistError(code)
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid request")
	}
	if jsonvalue.Truthy(body["libraryId"]) || jsonvalue.Truthy(body["userId"]) {
		return httputil.Status(400, "Invalid playlist data. Cannot update libraryId or userId")
	}
	if jsonvalue.Truthy(body["name"]) {
		name := body["name"]
		if _, ok := name.(string); !ok {
			return httputil.Status(400, "Invalid playlist name")
		}
	}
	if jsonvalue.Truthy(body["description"]) {
		desc := body["description"]
		if _, ok := desc.(string); !ok {
			return httputil.Status(400, "Invalid playlist description")
		}
	}
	var requestItems []any
	if raw, exists := body["items"]; exists && raw != nil {
		var ok bool
		requestItems, ok = raw.([]any)
		if !ok {
			return httputil.Status(400, "Invalid playlist items")
		}
		for _, rawItem := range requestItems {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return httputil.Status(400, "Invalid playlist items")
			}
			li, _ := item["libraryItemId"].(string)
			if li == "" {
				return httputil.Status(400, "Invalid playlist items")
			}
			if jsonvalue.Truthy(item["episodeId"]) {
				ep := item["episodeId"]
				if _, ok := ep.(string); !ok {
					return httputil.Status(400, "Invalid playlist items")
				}
			}
		}
	}
	old, err := sqlc.New(h.db).GetPlaylist(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
		if err := sqlc.New(h.db).UpdatePlaylist(r.Context(), sqlc.UpdatePlaylistParams{Name: newName, Description: newDescription, Updatedat: dbtime.Now(), ID: id}); err != nil {
			return httputil.Internal("database write failed", err)
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
				return httputil.Status(400, "Invalid playlist items. Items not found")
			}
			if episode := jsonvalue.String(item["episodeId"]); episode != "" {
				mediaID = episode
			}
			refs = append(refs, mediaID)
		}
		entries, err := sqlc.New(h.db).ListPlaylistMediaItems(r.Context(), id)
		if err != nil {
			return httputil.Internal("database read failed", err)
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
			return httputil.Status(400, "Invalid playlist items. Length mismatch")
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
				if err := sqlc.New(h.db).UpdatePlaylistMediaItemOrder(r.Context(), sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(want), ID: entry.id}); err != nil {
					return httputil.Internal("database write failed", err)
				}
				itemsChanged = true
			}
		}
	}
	item, _, err := playlistview.Load(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if changed || itemsChanged {
		h.emit.ToUser(user.ID, "playlist_updated", item)
	}
	return httputil.JSON(w, r, item)
}

func (h *Handler) deletePlaylist(w http.ResponseWriter, r *http.Request) error {
	user, id := store.RequestUser(r), r.PathValue("id")
	item, _, code, err := h.ownedPlaylist(r, id, user)
	if code != 0 {
		return playlistError(code)
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err := q.DeletePlaylistMediaItems(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := q.DeletePlaylist(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	h.emit.ToUser(user.ID, "playlist_removed", item)
	return httputil.OK(w, r)
}

func (h *Handler) addPlaylistItem(w http.ResponseWriter, r *http.Request, id string, user store.User, batch bool) error {
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		return playlistError(code)
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid request body items")
	}
	type ref struct{ libraryItemID, episodeID string }
	refs := make([]ref, 0)
	if batch {
		items, ok := body["items"].([]any)
		if !ok || len(items) == 0 {
			return httputil.Status(400, "Invalid request body items")
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok || jsonvalue.String(item["libraryItemId"]) == "" {
				return httputil.Status(400, "Invalid request body items")
			}
			if jsonvalue.Truthy(item["episodeId"]) && jsonvalue.String(item["episodeId"]) == "" {
				return httputil.Status(400, "Invalid request body items")
			}
			refs = append(refs, ref{jsonvalue.String(item["libraryItemId"]), jsonvalue.String(item["episodeId"])})
		}
	} else {
		if jsonvalue.String(body["libraryItemId"]) == "" {
			return httputil.Status(400, "Request body has no libraryItemId")
		}
		if jsonvalue.Truthy(body["episodeId"]) && jsonvalue.String(body["episodeId"]) == "" {
			return httputil.Status(400, "Invalid request body items")
		}
		refs = append(refs, ref{jsonvalue.String(body["libraryItemId"]), jsonvalue.String(body["episodeId"])})
	}
	playlist, err := sqlc.New(h.db).GetPlaylist(r.Context(), id)
	if err != nil {
		return httputil.Status(http.StatusNotFound, "Playlist not found")
	}
	libraryID := playlist.Libraryid.String
	entryCount, err := sqlc.New(h.db).CountPlaylistMediaItems(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	count := int(entryCount)
	changed := false
	for _, ref := range refs {
		var mediaID, typ, lib string
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, ref.libraryItemID).Scan(&mediaID, &typ, &lib); err != nil {
			if batch {
				return httputil.Status(400, "Invalid request body items")
			}
			return httputil.Status(400, "Library item not found")
		}
		if lib != libraryID {
			return httputil.Status(400, "Library item in different library")
		}
		mediaType := "book"
		if typ == "podcast" && ref.episodeID != "" {
			var exists int
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, ref.episodeID, mediaID).Scan(&exists); err != nil || exists == 0 {
				return httputil.Status(400, "Episode not found in library item")
			}
			mediaID, mediaType = ref.episodeID, "podcastEpisode"
		} else if typ == "podcast" || ref.episodeID != "" {
			return httputil.Status(400, "Invalid item to add for this library type")
		}
		exists, err := sqlc.New(h.db).CountPlaylistMediaItem(r.Context(), sqlc.CountPlaylistMediaItemParams{PlaylistID: id, MediaID: mediaID})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if exists > 0 {
			if !batch {
				return httputil.Status(400, "Item already in playlist")
			}
			continue
		}
		entryID, err := ids.New()
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		if err := sqlc.New(h.db).InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: mediaID, MediaType: mediaType, Position: int64(count + 1), PlaylistID: id, Createdat: dbtime.Now()}); err != nil {
			return httputil.Internal("database write failed", err)
		}
		count++
		changed = true
	}
	item, _, err := playlistview.Load(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !batch && changed && refs[0].episodeID != "" {
		entries, _ := item["items"].([]any)
		if last := len(entries) - 1; last >= 0 {
			if e, ok := entries[last].(playlistview.Entry); ok && e.EpisodeID == refs[0].episodeID {
				e.LibraryItem = h.withEpisodeTotals(r.Context(), e)
				entries[last] = e
			}
		}
	}
	if changed {
		h.emit.ToUser(user.ID, "playlist_updated", item)
	}
	return httputil.JSON(w, r, item)
}

func (h *Handler) removePlaylistItems(w http.ResponseWriter, r *http.Request) error {
	user, id := store.RequestUser(r), r.PathValue("id")
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		return playlistError(code)
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	body, err := httputil.DecodeObjectRequest(r)
	items, ok := body["items"].([]any)
	if err != nil || !ok || len(items) == 0 {
		return httputil.Status(400, "Invalid request body items")
	}
	remove := make(map[string]bool)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || jsonvalue.String(item["libraryItemId"]) == "" {
			return httputil.Status(400, "Invalid request body items")
		}
		if jsonvalue.Truthy(item["episodeId"]) && jsonvalue.String(item["episodeId"]) == "" {
			return httputil.Status(400, "Invalid request body items")
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
	return h.deletePlaylistMediaIDs(w, r, id, user, remove)
}

func (h *Handler) removePlaylistItem(w http.ResponseWriter, r *http.Request) error {
	id, user := r.PathValue("id"), store.RequestUser(r)
	parts := strings.Split(r.PathValue("itemPath"), "/")
	libraryItemID, episodeID := parts[0], ""
	if len(parts) > 1 {
		episodeID = parts[1]
	}
	if _, _, code, err := h.ownedPlaylist(r, id, user); code != 0 {
		return playlistError(code)
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	mediaID := episodeID
	if mediaID == "" {
		if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, libraryItemID).Scan(&mediaID); err != nil {
			return httputil.Status(404, "Media item not found in playlist")
		}
	}
	if exists, err := sqlc.New(h.db).CountPlaylistMediaItem(r.Context(), sqlc.CountPlaylistMediaItemParams{PlaylistID: id, MediaID: mediaID}); err != nil || exists == 0 {
		return httputil.Status(404, "Media item not found in playlist")
	}
	return h.deletePlaylistMediaIDs(w, r, id, user, map[string]bool{mediaID: true})
}

func (h *Handler) deletePlaylistMediaIDs(w http.ResponseWriter, r *http.Request, id string, user store.User, remove map[string]bool) error {
	finalDTO, _, preErr := playlistview.Load(r.Context(), h.db, id)
	if preErr != nil {
		return httputil.Internal("database read failed", preErr)
	}
	if currentItems, ok := finalDTO["items"].([]any); ok {
		remaining := make([]any, 0, len(currentItems))
		for _, raw := range currentItems {
			entry, _ := raw.(playlistview.Entry)
			if !remove[entry.MediaID] {
				remaining = append(remaining, raw)
			}
		}
		finalDTO["items"] = remaining
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	rows, err := q.ListPlaylistMediaItems(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
				return httputil.Internal("database write failed", err)
			}
			changed = true
			continue
		}
		if err := q.UpdatePlaylistMediaItemOrder(r.Context(), sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(order), ID: e.id}); err != nil {
			return httputil.Internal("database write failed", err)
		}
		order++
	}
	if changed && order == 1 {
		if err := q.DeletePlaylist(r.Context(), id); err != nil {
			return httputil.Internal("database write failed", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if changed {
		event := "playlist_updated"
		if order == 1 {
			event = "playlist_removed"
		}
		h.emit.ToUser(user.ID, event, finalDTO)
	}
	return httputil.JSON(w, r, finalDTO)
}

func (h *Handler) createPlaylistFromCollection(w http.ResponseWriter, r *http.Request, user store.User, collectionID string) error {
	dto, libraryID, err := h.collection(r.Context(), collectionID, user)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !access.For(r.Context(), user).Library(libraryID)) {
		return httputil.Status(http.StatusNotFound, "Collection not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	bookItems, _ := dto["books"].([]any)
	if len(bookItems) == 0 {
		return httputil.Status(400, "Collection has no books")
	}
	name, _ := dto["name"].(string)
	desc, _ := dto["description"].(string)
	id, err := ids.New()
	if err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err = q.InsertPlaylist(r.Context(), sqlc.InsertPlaylistParams{ID: id, Name: name, Description: store.TextOrNull(desc), LibraryID: libraryID, UserID: user.ID, Createdat: now, Updatedat: now}); err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	for i, raw := range bookItems {
		book, _ := raw.(items.Expanded).Media.(books.BookExpanded)
		mediaID := book.ID
		if mediaID == "" {
			continue
		}
		entryID, genErr := ids.New()
		if genErr != nil {
			return httputil.Internal("Failed to create playlist", genErr)
		}
		if err = q.InsertPlaylistMediaItem(r.Context(), sqlc.InsertPlaylistMediaItemParams{ID: entryID, MediaID: mediaID, MediaType: "book", Position: int64(i + 1), PlaylistID: id, Createdat: now}); err != nil {
			return httputil.Internal("Failed to create playlist", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return httputil.Internal("Failed to create playlist", err)
	}
	item, _, err := playlistview.Load(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToUser(user.ID, "playlist_added", item)
	return httputil.JSON(w, r, item)
}

// withEpisodeTotals is the entry's minified podcast with numEpisodes and size
// of its loaded episodes: PlaylistController.addItem appends the added episode
// with the library item it loaded WITH episodes (PlaylistController.js:321-328),
// unlike a reloaded playlist whose podcasts have none.
func (h *Handler) withEpisodeTotals(ctx context.Context, e playlistview.Entry) any {
	minified, ok := e.LibraryItem.(items.Minified)
	podcast, isPodcast := minified.Media.(libitem.PodcastMedia)
	if !ok || !isPodcast {
		return e.LibraryItem
	}
	full, err := items.LoadOne(ctx, h.db, e.LibraryItemID, items.Options{Episodes: true})
	if err != nil {
		return e.LibraryItem
	}
	loaded, _ := full.Expanded(nil).Media.(libitem.PodcastMedia)
	podcast.NumEpisodes, podcast.Size = loaded.NumEpisodes, loaded.Size
	minified.Media = podcast
	return minified
}
