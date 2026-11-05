// Package lists reads playlist and collection data shared by list handlers.
package lists

import (
	"context"
	"database/sql"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"
)

// Playlist loads the existing playlist response map and its library ID.
// Media-specific response data is supplied by the item reader.
func Playlist(ctx context.Context, db *sql.DB, itemReader items.Reader, id string) (map[string]any, string, error) {
	queries := sqlc.New(db)
	playlist, err := queries.GetPlaylist(ctx, id)
	if err != nil {
		return nil, "", err
	}
	entries, err := queries.ListPlaylistMediaItems(ctx, id)
	if err != nil {
		return nil, "", err
	}
	playlistItems := make([]any, 0, len(entries))
	for _, entry := range entries {
		mediaID := entry.Mediaitemid.String
		mediaType := entry.Mediaitemtype.String
		var libraryItemID string
		if mediaType == "podcastEpisode" {
			err = db.QueryRowContext(ctx, `SELECT li.id FROM podcastEpisodes pe JOIN libraryItems li ON li.mediaId=pe.podcastId AND li.mediaType='podcast' WHERE pe.id=?`, mediaID).Scan(&libraryItemID)
		} else {
			err = db.QueryRowContext(ctx, `SELECT id FROM libraryItems WHERE mediaId=? AND mediaType='book'`, mediaID).Scan(&libraryItemID)
		}
		if err != nil {
			continue // upstream reconciliation removes orphaned playlist records
		}
		if mediaType == "podcastEpisode" {
			// Playlist entries embed an expanded episode and a minified parent item.
			libraryItem, err := itemReader.ItemJSON(ctx, libraryItemID, true)
			if err != nil {
				return nil, "", err
			}
			media, _ := libraryItem["media"].(map[string]any)
			episodes, _ := media["episodes"].([]map[string]any)
			var episode map[string]any
			for _, candidate := range episodes {
				if candidate["id"] == mediaID {
					episode = candidate
					break
				}
			}
			if episode == nil {
				continue
			}
			minified, err := itemReader.ItemMinifiedJSON(ctx, libraryItemID)
			if err != nil {
				return nil, "", err
			}
			playlistItems = append(playlistItems, map[string]any{"episodeId": mediaID, "episode": episode, "libraryItemId": libraryItemID, "libraryItem": minified})
		} else {
			libraryItem, err := itemReader.ItemJSON(ctx, libraryItemID, true)
			if err != nil {
				return nil, "", err
			}
			playlistItems = append(playlistItems, map[string]any{"libraryItemId": libraryItemID, "libraryItem": libraryItem})
		}
	}
	return map[string]any{
		"id": id, "name": playlist.Name.String, "libraryId": playlist.Libraryid.String, "userId": playlist.Userid.String,
		"description": store.NullString(playlist.Description), "lastUpdate": dbtime.Millis(playlist.Updatedat),
		"createdAt": dbtime.Millis(playlist.Createdat), "items": playlistItems,
	}, playlist.Libraryid.String, nil
}

// Collection loads the existing collection response map and its library ID.
// canView is evaluated in collection order for each member item.
func Collection(ctx context.Context, db *sql.DB, itemReader items.Reader, id string, canView func(context.Context, string) (bool, error)) (map[string]any, string, error) {
	queries := sqlc.New(db)
	collection, err := queries.GetCollection(ctx, id)
	if err != nil {
		return nil, "", err
	}
	itemIDs, err := queries.ListCollectionBookItemIDs(ctx, id)
	if err != nil {
		return nil, "", err
	}
	books := make([]any, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		allowed, err := canView(ctx, itemID)
		if err != nil {
			return nil, "", err
		}
		if !allowed {
			continue
		}
		item, err := itemReader.ItemJSON(ctx, itemID, true)
		if err != nil {
			return nil, "", err
		}
		books = append(books, item)
	}
	return map[string]any{
		"id": id, "libraryId": collection.Libraryid.String, "name": collection.Name.String,
		"description": store.NullString(collection.Description), "books": books,
		"lastUpdate": dbtime.Millis(collection.Updatedat), "createdAt": dbtime.Millis(collection.Createdat),
	}, collection.Libraryid.String, nil
}
