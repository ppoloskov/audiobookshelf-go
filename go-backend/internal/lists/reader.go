// Package lists reads playlist and collection data shared by list handlers.
package lists

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
	"errors"
	"slices"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
)

// playlistEntry is one playlist item: a book's expanded item, or an expanded
// episode with its minified podcast. MediaID (the book or episode id) is not
// serialized.
type playlistEntry struct {
	MediaID       string                   `json:"-"`
	EpisodeID     string                   `json:"episodeId,omitempty"`
	Episode       *libitem.EpisodeExpanded `json:"episode,omitempty"`
	LibraryItemID string                   `json:"libraryItemId"`
	LibraryItem   any                      `json:"libraryItem"`
}

// playlistEntryItems resolves each entry to its library item; orphaned
// entries are dropped (upstream reconciliation removes them).
const playlistEntryItems = `SELECT pmi.mediaItemId, pmi.mediaItemType, li.id
FROM playlistMediaItems pmi
LEFT JOIN podcastEpisodes pe ON pmi.mediaItemType='podcastEpisode' AND pe.id=pmi.mediaItemId
JOIN libraryItems li ON (pmi.mediaItemType='podcastEpisode' AND li.mediaId=pe.podcastId AND li.mediaType='podcast')
	OR (pmi.mediaItemType<>'podcastEpisode' AND li.mediaId=pmi.mediaItemId AND li.mediaType='book')
WHERE pmi.playlistId=? ORDER BY pmi."order" ASC`

// Playlist loads the playlist response map and its library ID. Its items are
// loaded with one libitem.Load.
func Playlist(ctx context.Context, db *sql.DB, id string) (map[string]any, string, error) {
	playlist, err := sqlc.New(db).GetPlaylist(ctx, id)
	if err != nil {
		return nil, "", err
	}
	rows, err := db.QueryContext(ctx, playlistEntryItems, id)
	if err != nil {
		return nil, "", err
	}
	var entries []playlistEntry
	var itemIDs []string
	for rows.Next() {
		var e playlistEntry
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
	loaded, err := libitem.Load(ctx, db, itemIDs, libitem.Options{Episodes: true})
	if err != nil {
		return nil, "", err
	}
	byID := make(map[string]libitem.Item, len(loaded))
	for _, it := range loaded {
		byID[it.ID] = it
	}
	prefixes := serversettings.Load(ctx, db).SortingPrefixes
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
	return map[string]any{
		"id": id, "name": playlist.Name.String, "libraryId": playlist.Libraryid.String, "userId": playlist.Userid.String,
		"description": store.NullString(playlist.Description), "lastUpdate": dbtime.Millis(playlist.Updatedat),
		"createdAt": dbtime.Millis(playlist.Createdat), "items": playlistItems,
	}, playlist.Libraryid.String, nil
}

// Collection loads the collection response map and its library ID. canView is
// evaluated in collection order for each member item; the visible members are
// loaded with one libitem.Load.
func Collection(ctx context.Context, db *sql.DB, id string, canView func(context.Context, string) (bool, error)) (map[string]any, string, error) {
	queries := sqlc.New(db)
	collection, err := queries.GetCollection(ctx, id)
	if err != nil {
		return nil, "", err
	}
	itemIDs, err := queries.ListCollectionBookItemIDs(ctx, id)
	if err != nil {
		return nil, "", err
	}
	visible := itemIDs[:0:0]
	for _, itemID := range itemIDs {
		allowed, err := canView(ctx, itemID)
		if err != nil {
			return nil, "", err
		}
		if allowed {
			visible = append(visible, itemID)
		}
	}
	loaded, err := libitem.Load(ctx, db, visible, libitem.Options{})
	if err != nil {
		return nil, "", err
	}
	prefixes := serversettings.Load(ctx, db).SortingPrefixes
	books := make([]any, len(loaded))
	for i, it := range loaded {
		books[i] = it.Expanded(prefixes)
	}
	return map[string]any{
		"id": id, "libraryId": collection.Libraryid.String, "name": collection.Name.String,
		"description": store.NullString(collection.Description), "books": books,
		"lastUpdate": dbtime.Millis(collection.Updatedat), "createdAt": dbtime.Millis(collection.Createdat),
	}, collection.Libraryid.String, nil
}
