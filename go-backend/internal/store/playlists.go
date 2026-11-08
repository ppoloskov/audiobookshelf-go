package store

import (
	"context"

	"audiobookshelf-compat/internal/db/sqlc"
)

// PlaylistChange is one playlist RemoveFromPlaylists touched: Removed when it
// was left empty and destroyed, otherwise renumbered. Playlist is the row as
// it was before the change.
type PlaylistChange struct {
	Playlist sqlc.Playlist
	Removed  bool
}

// RemoveFromPlaylists is Playlist.removeMediaItemsFromPlaylists
// (server/models/Playlist.js:172-208), shared by every path that deletes
// media: it drops the entries for mediaIDs, destroys the playlists left empty
// and renumbers the entries of the others. It returns what changed so the
// caller can emit playlist_removed / playlist_updated after commit.
func RemoveFromPlaylists(ctx context.Context, tx sqlc.DBTX, mediaIDs []string) ([]PlaylistChange, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(mediaIDs))
	for i, id := range mediaIDs {
		args[i] = id
	}
	in := Placeholders(len(args))
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT playlistId FROM playlistMediaItems WHERE mediaItemId IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	var playlistIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		playlistIDs = append(playlistIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId IN (`+in+`)`, args...); err != nil {
		return nil, err
	}
	q := sqlc.New(tx)
	changes := make([]PlaylistChange, 0, len(playlistIDs))
	for _, playlistID := range playlistIDs {
		row, err := q.GetPlaylist(ctx, playlistID)
		if err != nil {
			return nil, err
		}
		entryIDs, err := q.ListPlaylistMediaItemIDs(ctx, playlistID)
		if err != nil {
			return nil, err
		}
		changes = append(changes, PlaylistChange{Playlist: row, Removed: len(entryIDs) == 0})
		if len(entryIDs) == 0 {
			if err = q.DeletePlaylist(ctx, playlistID); err != nil {
				return nil, err
			}
			continue
		}
		for order, id := range entryIDs {
			if err = q.UpdatePlaylistMediaItemOrder(ctx, sqlc.UpdatePlaylistMediaItemOrderParams{Position: int64(order + 1), ID: id}); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}
