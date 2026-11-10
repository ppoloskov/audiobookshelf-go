// Package playlistview builds the expanded playlist JSON (Playlist.toOldJSONExpanded)
// shared by the playlist routes and by every delete path that empties or
// shrinks playlists.
package playlistview

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
)

// Entry is one playlist item: a book's expanded item, or an expanded
// episode with its minified podcast. MediaID (the book or episode id) is not
// serialized.
type Entry struct {
	MediaID       string                   `json:"-"`
	EpisodeID     string                   `json:"episodeId,omitempty"`
	Episode       *libitem.EpisodeExpanded `json:"episode,omitempty"`
	LibraryItemID string                   `json:"libraryItemId"`
	LibraryItem   any                      `json:"libraryItem"`
}

// entryItems resolves each entry to its library item; orphaned
// entries are dropped (upstream reconciliation removes them).
const entryItems = `SELECT pmi.mediaItemId, pmi.mediaItemType, li.id
FROM playlistMediaItems pmi
LEFT JOIN podcastEpisodes pe ON pmi.mediaItemType='podcastEpisode' AND pe.id=pmi.mediaItemId
JOIN libraryItems li ON (pmi.mediaItemType='podcastEpisode' AND li.mediaId=pe.podcastId AND li.mediaType='podcast')
	OR (pmi.mediaItemType<>'podcastEpisode' AND li.mediaId=pmi.mediaItemId AND li.mediaType='book')
WHERE pmi.playlistId=? ORDER BY pmi."order" ASC`

// Load loads the playlist response map and its library ID. Its items are
// loaded with one items.Load.
func Load(ctx context.Context, db *sql.DB, id string) (map[string]any, string, error) {
	playlist, err := sqlc.New(db).GetPlaylist(ctx, id)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.QueryContext(ctx, entryItems, id)
	if err != nil {
		return nil, "", err
	}
	var entries []Entry
	var itemIDs []string
	for rows.Next() {
		var e Entry
		var mediaType sql.NullString
		if err = rows.Scan(&e.MediaID, &mediaType, &e.LibraryItemID); err != nil {
			_ = rows.Close()
			return nil, "", err
		}
		if mediaType.String == "podcastEpisode" {
			e.EpisodeID = e.MediaID
		}
		entries = append(entries, e)
		itemIDs = append(itemIDs, e.LibraryItemID)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, "", err
	}
	loaded, err := items.Load(ctx, db, itemIDs, items.Options{Episodes: true})
	if err != nil {
		return nil, "", err
	}
	byID := make(map[string]items.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	prefixes := serversettings.Load(ctx, db).SortingPrefixes()
	playlistItems := make([]any, 0, len(entries))
	for _, e := range entries {
		it, ok := byID[e.LibraryItemID]
		if !ok {
			continue
		}
		if e.EpisodeID == "" {
			e.LibraryItem = it.Expanded(prefixes)
		} else {
			if it.Podcast == nil {
				continue
			}
			i := slices.IndexFunc(it.Podcast.Episodes, func(ep libitem.Episode) bool { return ep.ID == e.EpisodeID })
			if i < 0 {
				continue
			}
			ep := it.Podcast.Episodes[i].Expanded(it.ID)
			e.Episode, e.LibraryItem = &ep, it.Minified(prefixes)
		}
		playlistItems = append(playlistItems, e)
	}
	return view(playlist, playlistItems), playlist.Libraryid.String, nil
}

func view(playlist sqlc.Playlist, items []any) map[string]any {
	return map[string]any{
		"id": playlist.ID, "name": playlist.Name.String, "libraryId": playlist.Libraryid.String, "userId": playlist.Userid.String,
		"description": store.NullString(playlist.Description), "lastUpdate": dbtime.Millis(playlist.Updatedat),
		"createdAt": dbtime.Millis(playlist.Createdat), "items": items,
	}
}

// Announce emits playlist_removed / playlist_updated to each changed
// playlist's owner (Playlist.js:199-206). Call it after the transaction that
// produced the changes has committed. A removed playlist is sent with no
// items, as upstream expands it after its entries are gone and before it is
// destroyed.
func Announce(ctx context.Context, db *sql.DB, emit events.Emitter, changes []store.PlaylistChange) {
	for _, c := range changes {
		if c.Removed {
			emit.ToUser(c.Playlist.Userid.String, "playlist_removed", view(c.Playlist, []any{}))
			continue
		}
		item, _, err := Load(ctx, db, c.Playlist.ID)
		if err != nil {
			logging.Errorf("[playlistview] playlist_updated for %s: %v", c.Playlist.ID, err)
			continue
		}
		emit.ToUser(c.Playlist.Userid.String, "playlist_updated", item)
	}
}
