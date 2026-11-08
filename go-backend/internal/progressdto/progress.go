// Package progressdto reads legacy media progress browser DTOs independently
// of catalog handlers so authentication can be tested and used without them.
package progressdto

import (
	"context"
	"database/sql"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// Read returns the user's progress in the order upstream first loads it.
// Upstream joins mediaProgresses onto the user (server/models/User.js:356,
// include: mediaProgress) and SQLite walks the
// media_progresses_user_item_finished_time index (userId, mediaItemId,
// isFinished, currentTime), so a freshly loaded user lists progress by
// mediaItemId. Upstream then appends rows created later in the process
// lifetime (User.js createUpdateMediaProgressFromPayload push); that cache
// artifact is not reproduced here, a restart reloads index order in both.
func Read(ctx context.Context, db *sql.DB, userID string) ([]wire.MediaProgress, error) {
	rows, err := sqlc.New(db).ListMediaProgressByUser(ctx, store.Text(userID))
	if err != nil {
		return nil, err
	}
	out := make([]wire.MediaProgress, 0, len(rows))
	for _, m := range rows {
		out = append(out, toWire(rowFromModel(m)))
	}
	return out, nil
}

// ReadOne mirrors upstream User.getOldMediaProgress(libraryItemId, episodeId)
// (server/models/User.js:716): the first progress whose extraData
// libraryItemId matches, restricted to episodeId when given; nil when absent.
func ReadOne(ctx context.Context, db *sql.DB, userID, libraryItemID, episodeID string) (*wire.MediaProgress, error) {
	all, err := Read(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	for i, p := range all {
		if episodeID != "" && p.MediaItemID != episodeID {
			continue
		}
		if p.LibraryItemID != nil && *p.LibraryItemID == libraryItemID {
			return &all[i], nil
		}
	}
	return nil, nil
}
