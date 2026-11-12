package item

import (
	"context"
	"slices"

	"audiobookshelf-compat/internal/db"
)

// PlaylistChange is a playlist that lost entries to a deletion. Removed means
// it was left empty and deleted; Playlist is its row.
type PlaylistChange struct {
	Playlist db.Playlist
	Removed  bool
}

// RemoveFromPlaylists drops the playlist entries of mediaIDs (books or
// episodes), deletes the playlists left empty and renumbers the others, inside
// the transaction ctx carries (Playlist.removeMediaItemsFromPlaylists,
// Playlist.js:172-208). It returns what changed, for the caller to announce
// through Deps.AnnouncePlaylists once the transaction has committed.
func RemoveFromPlaylists(ctx context.Context, d *db.DB, mediaIDs []string) ([]PlaylistChange, error) {
	var playlistIDs []string
	err := forChunks(mediaIDs, func(chunk []string) error {
		var found []string
		if err := d.Select(ctx, &found, `SELECT DISTINCT playlistId FROM playlistMediaItems WHERE mediaItemId IN (?) AND playlistId IS NOT NULL`, chunk); err != nil {
			return err
		}
		for _, id := range found {
			if !slices.Contains(playlistIDs, id) {
				playlistIDs = append(playlistIDs, id)
			}
		}
		if len(found) == 0 {
			return nil
		}
		_, err := d.Exec(ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId IN (?)`, chunk)
		return err
	})
	if err != nil {
		return nil, err
	}
	var changes []PlaylistChange
	for _, id := range playlistIDs {
		var playlist db.Playlist
		var entryIDs []string
		if err := d.Get(ctx, &playlist, `SELECT * FROM playlists WHERE id = ?`, id); err != nil {
			return nil, err
		}
		if err := d.Select(ctx, &entryIDs, "SELECT id FROM playlistMediaItems WHERE playlistId = ? ORDER BY `order`", id); err != nil {
			return nil, err
		}
		changes = append(changes, PlaylistChange{Playlist: playlist, Removed: len(entryIDs) == 0})
		if len(entryIDs) == 0 {
			if _, err := d.Exec(ctx, `DELETE FROM playlists WHERE id = ?`, id); err != nil {
				return nil, err
			}
			continue
		}
		for order, entryID := range entryIDs {
			if _, err := d.Exec(ctx, "UPDATE playlistMediaItems SET `order` = ? WHERE id = ?", order+1, entryID); err != nil {
				return nil, err
			}
		}
	}
	return changes, nil
}

// announcePlaylists hands the changes to Deps.AnnouncePlaylists, if any.
func (h *Handler) announcePlaylists(ctx context.Context, changes []PlaylistChange) {
	if len(changes) > 0 && h.deps.AnnouncePlaylists != nil {
		h.deps.AnnouncePlaylists(ctx, changes)
	}
}

// DeleteEpisodeRows removes podcast episodes with their progress and playlist
// entries inside the transaction ctx carries, and returns the playlist changes
// for the caller to announce (through AnnouncePlaylists) after the commit.
func DeleteEpisodeRows(ctx context.Context, d *db.DB, episodeIDs []string) ([]PlaylistChange, error) {
	if len(episodeIDs) == 0 {
		return nil, nil
	}
	changes, err := RemoveFromPlaylists(ctx, d, episodeIDs)
	if err != nil {
		return nil, err
	}
	for _, query := range []string{`DELETE FROM mediaProgresses WHERE mediaItemId IN (?)`, `DELETE FROM podcastEpisodes WHERE id IN (?)`} {
		if _, err := d.Exec(ctx, query, episodeIDs); err != nil {
			return nil, err
		}
	}
	return changes, nil
}
