package list

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/textsort"
)

// playlistView is a playlist with its items expanded.
type playlistView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	LibraryID   string          `json:"libraryId"`
	UserID      string          `json:"userId"`
	Description *string         `json:"description"`
	LastUpdate  db.Time         `json:"lastUpdate"`
	CreatedAt   db.Time         `json:"createdAt"`
	Items       []playlistEntry `json:"items"`
}

// playlistEntry is a book as its expanded item, or a podcast episode with its
// minified podcast item.
type playlistEntry struct {
	EpisodeID     string                `json:"episodeId,omitempty"`
	Episode       *item.EpisodeExpanded `json:"episode,omitempty"`
	LibraryItemID string                `json:"libraryItemId"`
	LibraryItem   any                   `json:"libraryItem"` // item.Expanded or item.Minified
	mediaID       string
}

// itemRequest names a playlist item: a library item, plus the episode for a podcast.
type itemRequest struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId"`
}

// playlistRequest is the body of every playlist route that has one; the
// embedded item is the one to add.
type playlistRequest struct {
	itemRequest
	Name        *string       `json:"name"`
	Description *string       `json:"description"`
	LibraryID   string        `json:"libraryId"` // create only; refused on update
	UserID      string        `json:"userId"`    // refused on update
	Items       []itemRequest `json:"items"`
}

// newEntry is a playlist item resolved to its media row.
type newEntry struct{ mediaID, mediaType string }

// playlistViewOf builds the playlist with its items; entries whose media or
// library item is gone are left out (upstream reconciles them away).
func (h *Handler) playlistViewOf(ctx context.Context, row db.Playlist) (playlistView, error) {
	var rows []struct {
		MediaID       string  `db:"mediaId"`
		MediaType     *string `db:"mediaType"`
		LibraryItemID string  `db:"libraryItemId"`
	}
	err := h.db.Select(ctx, &rows, `SELECT COALESCE(pmi.mediaItemId,'') AS mediaId, pmi.mediaItemType AS mediaType, li.id AS libraryItemId
FROM playlistMediaItems pmi
LEFT JOIN podcastEpisodes pe ON pmi.mediaItemType='podcastEpisode' AND pe.id=pmi.mediaItemId
JOIN libraryItems li ON (pmi.mediaItemType='podcastEpisode' AND li.mediaId=pe.podcastId AND li.mediaType='podcast')
	OR (pmi.mediaItemType<>'podcastEpisode' AND li.mediaId=pmi.mediaItemId AND li.mediaType='book')
WHERE pmi.playlistId=? ORDER BY pmi."order" ASC`, row.ID)
	if err != nil {
		return playlistView{}, err
	}
	itemIDs, episodeIDs := make([]string, len(rows)), []string(nil)
	for i, e := range rows {
		itemIDs[i] = e.LibraryItemID
		if db.Val(e.MediaType) == "podcastEpisode" {
			episodeIDs = append(episodeIDs, e.MediaID)
		}
	}
	options := item.Options{}
	if len(episodeIDs) > 0 {
		options = item.Options{Episodes: true, EpisodeIDs: episodeIDs}
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, options)
	if err != nil {
		return playlistView{}, err
	}
	byID := make(map[string]item.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	prefixes, entries := h.settings.Server().SortingPrefixes, make([]playlistEntry, 0, len(rows))
	for _, e := range rows {
		it, ok := byID[e.LibraryItemID]
		if !ok {
			continue
		}
		entry := playlistEntry{LibraryItemID: it.ID, mediaID: e.MediaID}
		if db.Val(e.MediaType) != "podcastEpisode" {
			entry.LibraryItem = it.Expanded(prefixes)
			entries = append(entries, entry)
		} else if it.Podcast != nil {
			for _, episode := range it.Podcast.Episodes {
				if episode.ID == e.MediaID {
					expanded := episode.Expanded(it.ID)
					entry.EpisodeID, entry.Episode, entry.LibraryItem = e.MediaID, &expanded, it.Minified(prefixes)
					entries = append(entries, entry)
				}
			}
		}
	}
	return playlistView{ID: row.ID, Name: db.Val(row.Name), LibraryID: db.Val(row.LibraryID), UserID: db.Val(row.UserID),
		Description: row.Description, LastUpdate: row.UpdatedAt, CreatedAt: row.CreatedAt, Items: entries}, nil
}

// owned loads the user's playlist: 404 when missing, 403 when it is someone
// else's, 404 when its library is closed to the user.
func (h *Handler) owned(ctx context.Context, user *auth.Principal, id string) (db.Playlist, error) {
	var row db.Playlist
	err := h.db.Get(ctx, &row, `SELECT * FROM playlists WHERE id=?`, id)
	switch {
	case errors.Is(err, sql.ErrNoRows), err == nil && db.Val(row.UserID) == user.ID && !user.CanAccessLibrary(db.Val(row.LibraryID)):
		return row, httpx.Error(http.StatusNotFound, "Playlist not found")
	case err == nil && db.Val(row.UserID) != user.ID:
		return row, httpx.Forbidden()
	}
	return row, err
}

// ownedWithBody is owned plus the decoded body of the request.
func (h *Handler) ownedWithBody(w http.ResponseWriter, r *http.Request, user *auth.Principal) (db.Playlist, playlistRequest, error) {
	row, err := h.owned(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return row, playlistRequest{}, err
	}
	req, err := httpx.Decode[playlistRequest](w, r)
	return row, req, err
}

// finishPlaylist answers with the playlist as it is now and tells its owner
// (nothing when event is empty).
func (h *Handler) finishPlaylist(w http.ResponseWriter, r *http.Request, id, event string) error {
	return h.finishAdjusted(w, r, id, event, nil)
}

// finishAdjusted is finishPlaylist with adjust applied to the view first.
func (h *Handler) finishAdjusted(w http.ResponseWriter, r *http.Request, id, event string, adjust func(context.Context, *playlistView)) error {
	var row db.Playlist
	if err := h.db.Get(r.Context(), &row, `SELECT * FROM playlists WHERE id=?`, id); err != nil {
		return err
	}
	view, err := h.playlistViewOf(r.Context(), row)
	if err != nil {
		return err
	}
	if adjust != nil {
		adjust(r.Context(), &view)
	}
	if event != "" {
		h.emit.ToUser(view.UserID, event, view)
	}
	return httpx.JSON(w, r, view)
}

// episodeTotals gives the last entry's minified podcast the numEpisodes and
// size of its episodes: PlaylistController.addItem answers with the library
// item it loaded WITH episodes, unlike a reloaded playlist (client-visible).
func (h *Handler) episodeTotals(ctx context.Context, view *playlistView) {
	if len(view.Items) == 0 {
		return
	}
	last := &view.Items[len(view.Items)-1]
	minified, ok := last.LibraryItem.(item.Minified)
	podcast, isPodcast := minified.Media.(item.PodcastMinified)
	full, err := item.Get(ctx, h.db, last.LibraryItemID, item.Options{Episodes: true})
	if !ok || !isPodcast || err != nil {
		return
	}
	expanded, _ := full.Expanded(nil).Media.(item.PodcastExpanded)
	podcast.NumEpisodes, podcast.Size = expanded.NumEpisodes, expanded.Size
	minified.Media = podcast
	last.LibraryItem = minified
}

// userPlaylists lists the user's playlists of libraryID ("" for all) sorted by
// name like upstream (localeCompare).
func (h *Handler) userPlaylists(ctx context.Context, user *auth.Principal, libraryID string) ([]playlistView, error) {
	var rows []db.Playlist
	if err := h.db.Select(ctx, &rows, `SELECT * FROM playlists WHERE userId=? AND (?='' OR libraryId=?)`, user.ID, libraryID, libraryID); err != nil {
		return nil, err
	}
	views := make([]playlistView, 0, len(rows))
	for _, row := range rows {
		if user.CanAccessLibrary(db.Val(row.LibraryID)) {
			view, err := h.playlistViewOf(ctx, row)
			if err != nil {
				return nil, err
			}
			views = append(views, view)
		}
	}
	slices.SortStableFunc(views, func(a, b playlistView) int { return textsort.LocaleCompare(a.Name, b.Name) })
	return views, nil
}

func (h *Handler) listPlaylists(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	views, err := h.userPlaylists(r.Context(), user, "")
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, struct {
		Playlists []playlistView `json:"playlists"`
	}{views})
}

// libraryPlaylists serves GET /api/libraries/{libraryID}/playlists.
func (h *Handler) libraryPlaylists(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	libraryID, limit, page, err := h.libraryPage(r, user)
	if err != nil {
		return err
	}
	views, err := h.userPlaylists(r.Context(), user, libraryID)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, struct {
		Results []playlistView `json:"results"`
		Total   int            `json:"total"`
		Limit   int            `json:"limit"`
		Page    int            `json:"page"`
	}{pageOf(views, limit, page), len(views), limit, page})
}

func (h *Handler) getPlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.owned(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	return h.finishPlaylist(w, r, row.ID, "")
}

// itemRefsOf reads the library items that items name.
func (h *Handler) itemRefsOf(ctx context.Context, items []itemRequest) (map[string]itemRef, error) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.LibraryItemID
	}
	return h.itemRefs(ctx, ids...)
}

// episodeIn reports whether the podcast has the episode.
func (h *Handler) episodeIn(ctx context.Context, podcastID, episodeID string) (bool, error) {
	var n int
	err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, episodeID, podcastID)
	return n > 0, err
}

// createEntry resolves an item of a new playlist: all items are books, or all
// are episodes of podcasts (podcast says which), in the playlist's library.
func (h *Handler) createEntry(ctx context.Context, libraryID string, podcast bool, it itemRequest, refs map[string]itemRef) (newEntry, error) {
	ref, ok := refs[it.LibraryItemID]
	wantType := map[bool]string{false: "book", true: "podcast"}[podcast]
	switch {
	case !ok || ref.LibraryID != libraryID || ref.MediaType != wantType:
		return newEntry{}, httpx.BadRequest("Invalid playlist data. Invalid items")
	case podcast != (it.EpisodeID != ""):
		return newEntry{}, httpx.BadRequest("Invalid playlist item episodeId")
	case !podcast:
		return newEntry{ref.MediaID, "book"}, nil
	}
	found, err := h.episodeIn(ctx, ref.MediaID, it.EpisodeID)
	if err == nil && !found {
		err = httpx.BadRequest("Invalid playlist data. Invalid podcast episodes")
	}
	return newEntry{it.EpisodeID, "podcastEpisode"}, err
}

// addEntry resolves an item to add to a playlist of libraryID; batch picks the
// message of an unknown item.
func (h *Handler) addEntry(ctx context.Context, libraryID string, batch bool, it itemRequest, refs map[string]itemRef) (newEntry, error) {
	ref, ok := refs[it.LibraryItemID]
	switch {
	case !ok && batch:
		return newEntry{}, httpx.BadRequest("Invalid request body items")
	case !ok:
		return newEntry{}, httpx.BadRequest("Library item not found")
	case ref.LibraryID != libraryID:
		return newEntry{}, httpx.BadRequest("Library item in different library")
	case ref.MediaType == "podcast" && it.EpisodeID != "":
		found, err := h.episodeIn(ctx, ref.MediaID, it.EpisodeID)
		if err == nil && !found {
			err = httpx.BadRequest("Episode not found in library item")
		}
		return newEntry{it.EpisodeID, "podcastEpisode"}, err
	case ref.MediaType == "podcast" || it.EpisodeID != "":
		return newEntry{}, httpx.BadRequest("Invalid item to add for this library type")
	}
	return newEntry{ref.MediaID, "book"}, nil
}

// insertEntries appends entries to a playlist that already has have of them.
func (h *Handler) insertEntries(ctx context.Context, playlistID string, have int, entries []newEntry) error {
	for i, e := range entries {
		id, err := ids.New()
		if err != nil {
			return err
		}
		order := int64(have + i + 1)
		row := db.PlaylistMediaItem{ID: id, MediaItemID: &e.mediaID, MediaItemType: &e.mediaType, Order: &order, CreatedAt: db.Now(), PlaylistID: &playlistID}
		if err := h.db.Insert(ctx, "playlistMediaItems", &row); err != nil {
			return err
		}
	}
	return nil
}

// insertPlaylist writes a playlist with its entries in one transaction and
// returns its id.
func (h *Handler) insertPlaylist(ctx context.Context, userID, libraryID, name string, description *string, entries []newEntry) (id string, err error) {
	now := db.Now()
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var err error
		if id, err = ids.New(); err != nil {
			return err
		}
		row := db.Playlist{ID: id, Name: &name, Description: emptyAsNull(description), LibraryID: &libraryID, UserID: &userID, CreatedAt: now, UpdatedAt: now}
		if err = h.db.Insert(ctx, "playlists", &row); err != nil {
			return err
		}
		return h.insertEntries(ctx, id, 0, entries)
	})
	return id, err
}

// createPlaylist serves POST /api/playlists.
func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	req, err := httpx.Decode[playlistRequest](w, r)
	if err != nil {
		return err
	}
	name := cleanName(db.Val(req.Name))
	if name == "" || req.LibraryID == "" {
		return httpx.BadRequest("Invalid playlist data")
	}
	if !user.CanAccessLibrary(req.LibraryID) {
		return httpx.Forbidden()
	}
	refs, err := h.itemRefsOf(r.Context(), req.Items)
	if err != nil {
		return err
	}
	podcast, entries := false, make([]newEntry, len(req.Items))
	for _, it := range req.Items {
		if it.LibraryItemID == "" {
			return httpx.BadRequest("Invalid playlist item")
		}
		podcast = podcast || it.EpisodeID != ""
	}
	for i, it := range req.Items {
		if entries[i], err = h.createEntry(r.Context(), req.LibraryID, podcast, it, refs); err != nil {
			return err
		}
	}
	id, err := h.insertPlaylist(r.Context(), user.ID, req.LibraryID, name, req.Description, entries)
	if err != nil {
		return err
	}
	return h.finishPlaylist(w, r, id, "playlist_added")
}

// playlistAction serves POST /api/playlists/{id}/{action}: "collection/{id}"
// makes a playlist from a collection, "{id}/item" adds one item.
func (h *Handler) playlistAction(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	id, action := r.PathValue("id"), r.PathValue("action")
	switch {
	case id == "collection":
		return h.createFromCollection(w, r, user, action)
	case action == "item":
		row, req, err := h.ownedWithBody(w, r, user)
		if err != nil {
			return err
		}
		if req.LibraryItemID == "" {
			return httpx.BadRequest("Request body has no libraryItemId")
		}
		return h.addItems(w, r, row, []itemRequest{req.itemRequest}, false)
	}
	return httpx.NotFound()
}

// createFromCollection copies the books the user can read from a collection.
func (h *Handler) createFromCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal, collectionID string) error {
	collection, err := h.open(r.Context(), user, collectionID, true)
	if err != nil {
		return err
	}
	view, err := h.viewOf(r.Context(), user, collection)
	if err != nil {
		return err
	}
	if len(view.Books) == 0 {
		return httpx.BadRequest("Collection has no books")
	}
	entries := make([]newEntry, len(view.Books))
	for i, book := range view.Books {
		entries[i] = newEntry{book.MediaID(), "book"}
	}
	id, err := h.insertPlaylist(r.Context(), user.ID, view.LibraryID, view.Name, view.Description, entries)
	if err != nil {
		return err
	}
	return h.finishPlaylist(w, r, id, "playlist_added")
}

// mediaIDs maps item requests to media ids (an episode id replaces the item's
// media id); an unknown item is "" and, when strict, a 400.
func (h *Handler) mediaIDs(ctx context.Context, items []itemRequest, strict bool) ([]string, error) {
	refs, err := h.itemRefsOf(ctx, items)
	media := make([]string, len(items))
	for i, it := range items {
		if _, known := refs[it.LibraryItemID]; strict && !known && err == nil {
			err = httpx.BadRequest("Invalid playlist items. Items not found")
		}
		if media[i] = it.EpisodeID; media[i] == "" {
			media[i] = refs[it.LibraryItemID].MediaID
		}
	}
	return media, err
}

// updatePlaylist serves PATCH /api/playlists/{id}: name, description and item
// order. An empty name is ignored.
func (h *Handler) updatePlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := h.ownedWithBody(w, r, user)
	if err != nil {
		return err
	}
	if req.LibraryID != "" || req.UserID != "" {
		return httpx.BadRequest("Invalid playlist data. Cannot update libraryId or userId")
	}
	for _, it := range req.Items {
		if it.LibraryItemID == "" {
			return httpx.BadRequest("Invalid playlist items")
		}
	}
	wanted, err := h.mediaIDs(r.Context(), req.Items, true)
	if err != nil {
		return err
	}
	changed, moved := false, false
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		if name := cleanName(db.Val(req.Name)); name != "" && name != db.Val(row.Name) {
			row.Name, changed = &name, true
		}
		if req.Description != nil && *req.Description != db.Val(row.Description) {
			row.Description, changed = emptyAsNull(req.Description), true
		}
		if changed {
			row.UpdatedAt = db.Now()
			if err := h.db.Update(ctx, "playlists", &row, "name", "description", "updatedAt"); err != nil {
				return err
			}
		}
		if len(wanted) == 0 {
			return nil
		}
		links, err := playlistLinks.list(ctx, h.db, row.ID)
		if err != nil {
			return err
		}
		if len(links) != len(wanted) {
			return httpx.BadRequest("Invalid playlist items. Length mismatch")
		}
		moved, err = playlistLinks.reorder(ctx, h.db, row.ID, wanted, len(wanted))
		return err
	})
	if err != nil {
		return err
	}
	return h.finishPlaylist(w, r, row.ID, eventIf(changed || moved, "playlist_updated"))
}

// deletePlaylist serves DELETE /api/playlists/{id}.
func (h *Handler) deletePlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.owned(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	view, err := h.playlistViewOf(r.Context(), row)
	if err != nil {
		return err
	}
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		if _, err := h.db.Exec(ctx, `DELETE FROM playlistMediaItems WHERE playlistId=?`, row.ID); err != nil {
			return err
		}
		_, err := h.db.Exec(ctx, `DELETE FROM playlists WHERE id=?`, row.ID)
		return err
	})
	if err != nil {
		return err
	}
	h.emit.ToUser(user.ID, "playlist_removed", view)
	return httpx.Status(w, http.StatusOK)
}

// validItems checks the items of a batch request: some, each naming a library item.
func validItems(items []itemRequest) error {
	if len(items) == 0 || slices.ContainsFunc(items, func(it itemRequest) bool { return it.LibraryItemID == "" }) {
		return httpx.BadRequest("Invalid request body items")
	}
	return nil
}

// addPlaylistItems serves POST /api/playlists/{id}/batch/add; members are skipped.
func (h *Handler) addPlaylistItems(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := h.ownedWithBody(w, r, user)
	if err != nil {
		return err
	}
	if err := validItems(req.Items); err != nil {
		return err
	}
	return h.addItems(w, r, row, req.Items, true)
}

// addItems appends items in one transaction; a member is skipped in batch mode
// and refused otherwise.
func (h *Handler) addItems(w http.ResponseWriter, r *http.Request, row db.Playlist, items []itemRequest, batch bool) error {
	refs, err := h.itemRefsOf(r.Context(), items)
	if err != nil {
		return err
	}
	added := 0
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		links, err := playlistLinks.list(ctx, h.db, row.ID)
		if err != nil {
			return err
		}
		for _, it := range items {
			e, err := h.addEntry(ctx, db.Val(row.LibraryID), batch, it, refs)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(links, func(l link) bool { return l.MediaID == e.mediaID }) {
				if !batch {
					return httpx.BadRequest("Item already in playlist")
				}
				continue
			}
			if err := h.insertEntries(ctx, row.ID, len(links), []newEntry{e}); err != nil {
				return err
			}
			links, added = append(links, link{MediaID: e.mediaID}), added+1
		}
		return nil
	})
	if err != nil {
		return err
	}
	var adjust func(context.Context, *playlistView)
	if !batch && added > 0 && items[0].EpisodeID != "" {
		adjust = h.episodeTotals
	}
	return h.finishAdjusted(w, r, row.ID, eventIf(added > 0, "playlist_updated"), adjust)
}

// removePlaylistItems serves POST /api/playlists/{id}/batch/remove; unknown
// items are ignored.
func (h *Handler) removePlaylistItems(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := h.ownedWithBody(w, r, user)
	if err != nil {
		return err
	}
	if err := validItems(req.Items); err != nil {
		return err
	}
	media, err := h.mediaIDs(r.Context(), req.Items, false)
	if err != nil {
		return err
	}
	return h.removeEntries(w, r, user, row, media)
}

// removePlaylistItem serves DELETE /api/playlists/{id}/item/{libraryItemId}[/{episodeId}].
func (h *Handler) removePlaylistItem(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.owned(r.Context(), user, r.PathValue("id"))
	if err != nil {
		return err
	}
	itemID, episodeID, _ := strings.Cut(r.PathValue("itemPath"), "/")
	media, err := h.mediaIDs(r.Context(), []itemRequest{{itemID, episodeID}}, false)
	if err != nil {
		return err
	}
	links, err := playlistLinks.list(r.Context(), h.db, row.ID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(links, func(l link) bool { return l.MediaID == media[0] }) {
		return httpx.Error(http.StatusNotFound, "Media item not found in playlist")
	}
	return h.removeEntries(w, r, user, row, media)
}

// removeEntries deletes entries by media id, renumbers the rest and deletes the
// playlist when it is left empty. It answers with the playlist as it is then.
func (h *Handler) removeEntries(w http.ResponseWriter, r *http.Request, user *auth.Principal, row db.Playlist, media []string) error {
	view, err := h.playlistViewOf(r.Context(), row)
	if err != nil {
		return err
	}
	view.Items = slices.DeleteFunc(view.Items, func(e playlistEntry) bool { return slices.Contains(media, e.mediaID) })
	var removed, remaining int
	err = h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		if removed, remaining, err = playlistLinks.remove(ctx, h.db, row.ID, media); err == nil && removed > 0 && remaining == 0 {
			_, err = h.db.Exec(ctx, `DELETE FROM playlists WHERE id=?`, row.ID)
		}
		return err
	})
	if err != nil {
		return err
	}
	if removed > 0 {
		event := "playlist_updated"
		if remaining == 0 {
			event = "playlist_removed"
		}
		h.emit.ToUser(user.ID, event, view)
	}
	return httpx.JSON(w, r, view)
}
