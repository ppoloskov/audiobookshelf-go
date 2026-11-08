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
	in, args := inList(mediaIDs)
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

// DeleteEpisodes removes podcast episodes with what hangs off them: their
// playlist entries (RemoveFromPlaylists, whose changes it returns for the
// caller to announce after commit), their progress rows and the episode rows.
func DeleteEpisodes(ctx context.Context, tx sqlc.DBTX, episodeIDs []string) ([]PlaylistChange, error) {
	if len(episodeIDs) == 0 {
		return nil, nil
	}
	changes, err := RemoveFromPlaylists(ctx, tx, episodeIDs)
	if err != nil {
		return nil, err
	}
	in, args := inList(episodeIDs)
	for _, del := range [...]string{`DELETE FROM mediaProgresses WHERE mediaItemId IN (`, `DELETE FROM podcastEpisodes WHERE id IN (`} {
		if _, err = tx.ExecContext(ctx, del+in+`)`, args...); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

func inList(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return Placeholders(len(ids)), args
}
