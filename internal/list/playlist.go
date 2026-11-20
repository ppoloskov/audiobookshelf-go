package list

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"monstrobook/internal/auth"
	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/item"
	"monstrobook/internal/text"
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

// newPlaylistView builds a playlistView from a playlists row and entries.
func newPlaylistView(row db.Playlist, items []playlistEntry) playlistView {
	return playlistView{
		ID: row.ID, Name: db.Val(row.Name), LibraryID: db.Val(row.LibraryID), UserID: db.Val(row.UserID),
		Description: row.Description, LastUpdate: row.UpdatedAt, CreatedAt: row.CreatedAt, Items: items,
	}
}

// AnnouncePlaylists tells the owner of each playlist that a deletion of items
// or episodes changed: playlist_removed for one left empty (and deleted),
// shown with no items, as upstream expands it after its entries are gone and
// before it is destroyed (Playlist.js:199-206), else playlist_updated with the
// playlist as it is now. Call it after the deletion committed; it is the
// item.Deps.AnnouncePlaylists implementation.
func (h *Handler) AnnouncePlaylists(ctx context.Context, changes []item.PlaylistChange) {
	for _, change := range changes {
		owner := db.Val(change.Playlist.UserID)
		if change.Removed {
			h.emit.ToUser(owner, "playlist_removed", newPlaylistView(change.Playlist, []playlistEntry{}))
			continue
		}
		view, err := h.playlistViewOf(ctx, nil, change.Playlist)
		if err != nil {
			slog.Error("playlist_updated after delete", "playlist", change.Playlist.ID, "error", err)
			continue
		}
		h.emit.ToUser(owner, "playlist_updated", view)
	}
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

func (h *Handler) playlistSpec() container[db.Playlist, playlistView] {
	return container[db.Playlist, playlistView]{
		h: h, kind: "playlist", table: "playlists", notFound: "Playlist not found", links: playlistLinks, clearEmptyDesc: true,
		access: func(user *auth.Principal, row db.Playlist, _ listOp) error {
			if db.Val(row.UserID) != user.ID {
				return httpx.Forbidden()
			}
			if !user.CanAccessLibrary(db.Val(row.LibraryID)) {
				return httpx.Error(http.StatusNotFound, "Playlist not found")
			}
			return nil
		},
		libraryID: func(r db.Playlist) string { return db.Val(r.LibraryID) },
		metaPtrs:  func(r *db.Playlist) (**string, **string, *db.Time) { return &r.Name, &r.Description, &r.UpdatedAt },
		viewOf:    h.playlistViewOf,
		emit:      func(v playlistView, ev string) { h.emit.ToUser(v.UserID, ev, v) },
	}
}

// playlistViewOf builds the playlist with its items; entries whose media or
// library item is gone are left out (upstream reconciles them away).
func (h *Handler) playlistViewOf(ctx context.Context, _ *auth.Principal, row db.Playlist) (playlistView, error) {
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
	loaded, prefixes, err := h.loadItems(ctx, itemIDs, options)
	if err != nil {
		return playlistView{}, err
	}
	byID, entries := indexItems(loaded), make([]playlistEntry, 0, len(rows))
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
	return newPlaylistView(row, entries), nil
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
	views, err := h.playlistSpec().list(ctx, user, `WHERE userId=? AND (?='' OR libraryId=?)`, user.ID, libraryID, libraryID)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(views, func(a, b playlistView) int { return text.LocaleCompare(a.Name, b.Name) })
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
	row, err := h.playlistSpec().open(r.Context(), user, r.PathValue("id"), opRead)
	if err != nil {
		return err
	}
	view, err := h.playlistViewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, view)
}

// itemRefsOf reads the library items that items name.
func (h *Handler) itemRefsOf(ctx context.Context, items []itemRequest) (map[string]itemRef, error) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.LibraryItemID
	}
	return h.itemRefs(ctx, ids...)
}

// requireEpisode checks that podcastID owns episodeID, returning badMsg if not.
func (h *Handler) requireEpisode(ctx context.Context, podcastID, episodeID, badMsg string) (newEntry, error) {
	var n int
	err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM podcastEpisodes WHERE id=? AND podcastId=?`, episodeID, podcastID)
	if err == nil && n == 0 {
		err = httpx.BadRequest(badMsg)
	}
	return newEntry{episodeID, "podcastEpisode"}, err
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
	return h.requireEpisode(ctx, ref.MediaID, it.EpisodeID, "Invalid playlist data. Invalid podcast episodes")
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
		return h.requireEpisode(ctx, ref.MediaID, it.EpisodeID, "Episode not found in library item")
	case ref.MediaType == "podcast" || it.EpisodeID != "":
		return newEntry{}, httpx.BadRequest("Invalid item to add for this library type")
	}
	return newEntry{ref.MediaID, "book"}, nil
}

func (h *Handler) createPlaylistWith(w http.ResponseWriter, r *http.Request, user *auth.Principal, libraryID, name string, description *string, entries []newEntry) error {
	return h.playlistSpec().create(w, r, user, func(id string, now db.Time) db.Playlist {
		return db.Playlist{ID: id, Name: &name, Description: emptyAsNull(description), LibraryID: &libraryID, UserID: &user.ID, CreatedAt: now, UpdatedAt: now}
	}, entries)
}

// createPlaylist serves POST /api/playlists.
func (h *Handler) createPlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	req, err := httpx.Decode[playlistRequest](w, r)
	if err != nil {
		return err
	}
	name, err := validateCreate(user, req.Name, req.LibraryID, "Invalid playlist data")
	if err != nil {
		return err
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
	return h.createPlaylistWith(w, r, user, req.LibraryID, name, req.Description, entries)
}

// playlistAction serves POST /api/playlists/{id}/{action}: "collection/{id}"
// makes a playlist from a collection, "{id}/item" adds one item.
func (h *Handler) playlistAction(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	id, action := r.PathValue("id"), r.PathValue("action")
	switch {
	case id == "collection":
		return h.createFromCollection(w, r, user, action)
	case action == "item":
		row, req, err := openBody[playlistRequest](h.playlistSpec(), w, r, user, opUpdate)
		if err != nil {
			return err
		}
		if req.LibraryItemID == "" {
			return httpx.BadRequest("Request body has no libraryItemId")
		}
		return h.addItems(w, r, user, row, []itemRequest{req.itemRequest}, false)
	}
	return httpx.NotFound()
}

// createFromCollection copies the books the user can read from a collection.
func (h *Handler) createFromCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal, collectionID string) error {
	collection, err := h.collectionSpec().open(r.Context(), user, collectionID, opRead)
	if err != nil {
		return err
	}
	view, err := h.collectionViewOf(r.Context(), user, collection)
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
	return h.createPlaylistWith(w, r, user, view.LibraryID, view.Name, view.Description, entries)
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
	c := h.playlistSpec()
	row, req, err := openBody[playlistRequest](c, w, r, user, opUpdate)
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
	return c.update(w, r, user, row, row.ID, req.Name, req.Description, wanted, len(wanted), func(ctx context.Context) error {
		links, err := playlistLinks.list(ctx, h.db, row.ID)
		if err != nil {
			return err
		}
		if len(links) != len(wanted) {
			return httpx.BadRequest("Invalid playlist items. Length mismatch")
		}
		return nil
	})
}

// deletePlaylist serves DELETE /api/playlists/{id}.
func (h *Handler) deletePlaylist(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	return h.playlistSpec().remove(w, r, user, func(ctx context.Context, row db.Playlist) error {
		_, err := h.db.Exec(ctx, `DELETE FROM playlistMediaItems WHERE playlistId=?`, row.ID)
		return err
	}, nil)
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
	row, req, err := openBody[playlistRequest](h.playlistSpec(), w, r, user, opUpdate)
	if err != nil {
		return err
	}
	if err := validItems(req.Items); err != nil {
		return err
	}
	return h.addItems(w, r, user, row, req.Items, true)
}

// addItems appends items in one transaction; a member is skipped in batch mode
// and refused otherwise.
func (h *Handler) addItems(w http.ResponseWriter, r *http.Request, user *auth.Principal, row db.Playlist, items []itemRequest, batch bool) error {
	refs, err := h.itemRefsOf(r.Context(), items)
	if err != nil {
		return err
	}
	entries := make([]newEntry, len(items))
	for i, it := range items {
		if entries[i], err = h.addEntry(r.Context(), db.Val(row.LibraryID), batch, it, refs); err != nil {
			return err
		}
	}
	dupMsg := "Item already in playlist"
	if batch {
		dupMsg = ""
	}
	return h.playlistSpec().addEntries(w, r, user, row.ID, entries, dupMsg, func(added int) func(context.Context, *playlistView) {
		if !batch && added > 0 && items[0].EpisodeID != "" {
			return h.episodeTotals
		}
		return nil
	})
}

// removePlaylistItems serves POST /api/playlists/{id}/batch/remove; unknown
// items are ignored. Like PlaylistController.removeBatch, remaining entries
// keep their existing order values.
func (h *Handler) removePlaylistItems(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := openBody[playlistRequest](h.playlistSpec(), w, r, user, opUpdate)
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
	return h.removeEntries(w, r, user, row, media, false)
}

// removePlaylistItem serves DELETE /api/playlists/{id}/item/{libraryItemId}[/{episodeId}].
func (h *Handler) removePlaylistItem(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.playlistSpec().open(r.Context(), user, r.PathValue("id"), opUpdate)
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
	return h.removeEntries(w, r, user, row, media, true)
}

// removeEntries deletes entries by media id, optionally renumbers the rest,
// and deletes the playlist when it is left empty. It answers with the playlist
// as it is then.
func (h *Handler) removeEntries(w http.ResponseWriter, r *http.Request, user *auth.Principal, row db.Playlist, media []string, recompact bool) error {
	view, err := h.playlistViewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	view.Items = slices.DeleteFunc(view.Items, func(e playlistEntry) bool { return slices.Contains(media, e.mediaID) })
	var removed, remaining int
	err = h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		if removed, remaining, err = playlistLinks.remove(ctx, h.db, row.ID, media, recompact); err == nil && removed > 0 && remaining == 0 {
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
