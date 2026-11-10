package library

import (
	"context"
	"database/sql"
)

// NumEpisodesIncomplete is podcasts.numEpisodes minus the user's finished
// progress rows tagged with the podcast
// (libraryItemsPodcastFilters.getFilteredLibraryItems,
// server/utils/queries/libraryItemsPodcastFilters.js:221-229). Callers set the
// key only when it is non-zero, as LibraryItem.getByFilterAndSort does
// (server/models/LibraryItem.js:313).
func NumEpisodesIncomplete(ctx context.Context, db *sql.DB, userID, podcastID string) (int64, error) {
	var numEpisodes, finished int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(numEpisodes,0) FROM podcasts WHERE id=?`, podcastID).Scan(&numEpisodes); err != nil {
		return 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mediaProgresses WHERE userId=? AND podcastId=? AND isFinished=1`, userID, podcastID).Scan(&finished); err != nil {
		return 0, err
	}
	return numEpisodes - finished, nil
}
