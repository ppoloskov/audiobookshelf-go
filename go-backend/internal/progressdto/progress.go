// Package progressdto reads legacy media progress browser DTOs independently
// of catalog handlers so authentication can be tested and used without them.
package progressdto

import (
	"context"
	"database/sql"
	"encoding/json"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// Read returns the user's progress in the order upstream first loads it.
// Upstream joins mediaProgresses onto the user (server/models/User.js:356,
// include: mediaProgress) and SQLite walks the
// media_progresses_user_item_finished_time index (userId, mediaItemId,
// isFinished, currentTime), so a freshly loaded user lists progress by
// mediaItemId. Upstream then appends rows created later in the process
// lifetime (User.js createUpdateMediaProgressFromPayload push); that cache
// artifact is not reproduced here, a restart reloads index order in both.
func Read(ctx context.Context, db *sql.DB, userID string) ([]any, error) {
	rows, err := sqlc.New(db).ListMediaProgressByUser(ctx, store.Text(userID))
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, m := range rows {
		var extra map[string]any
		_ = json.Unmarshal([]byte(m.Extradata.String), &extra)
		var libID, progress any
		if extra != nil {
			libID = extra["libraryItemId"]
			progress = extra["progress"]
		}
		if progress == nil {
			progress = 0
		}
		var episode any
		if m.Mediaitemtype.String == "podcastEpisode" {
			episode = m.Mediaitemid.String
		}
		out = append(out, map[string]any{"id": m.ID, "userId": m.Userid.String, "libraryItemId": libID, "episodeId": episode, "mediaItemId": m.Mediaitemid.String, "mediaItemType": m.Mediaitemtype.String, "duration": number(m.Duration), "progress": progress, "currentTime": number(m.Currenttime), "isFinished": m.Isfinished.Int64 != 0, "hideFromContinueListening": m.Hidefromcontinuelistening.Int64 != 0, "ebookLocation": store.NullString(m.Ebooklocation), "ebookProgress": number(m.Ebookprogress), "lastUpdate": dbtime.Millis(m.Updatedat), "startedAt": dbtime.Millis(m.Createdat), "finishedAt": dbtime.Millis(m.Finishedat)})
	}
	return out, nil
}

// ReadOne mirrors upstream User.getOldMediaProgress(libraryItemId, episodeId)
// (server/models/User.js:716): the first progress whose extraData
// libraryItemId matches, restricted to episodeId when given; nil when absent.
func ReadOne(ctx context.Context, db *sql.DB, userID, libraryItemID, episodeID string) (map[string]any, error) {
	all, err := Read(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	for _, entry := range all {
		p := entry.(map[string]any)
		if episodeID != "" && p["mediaItemId"] != episodeID {
			continue
		}
		if p["libraryItemId"] == libraryItemID {
			return p, nil
		}
	}
	return nil, nil
}

func number(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
