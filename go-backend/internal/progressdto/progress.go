// Package progressdto reads legacy media progress browser DTOs independently
// of catalog handlers so authentication can be tested and used without them.
package progressdto

import (
	"context"
	"database/sql"
	"encoding/json"

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
	rows, err := db.QueryContext(ctx, `SELECT id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookLocation,ebookProgress,finishedAt,extraData,createdAt,updatedAt FROM mediaProgresses WHERE userId = ? ORDER BY mediaItemId, isFinished, currentTime`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]any, 0)
	for rows.Next() {
		var id, uid, itemID, typ string
		var duration, current, ebook sql.NullFloat64
		var finished, hide int
		var location sql.NullString
		var finishedAt, createdAt, updatedAt any
		var raw []byte
		if err := rows.Scan(&id, &uid, &itemID, &typ, &duration, &current, &finished, &hide, &location, &ebook, &finishedAt, &raw, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var extra map[string]any
		_ = json.Unmarshal(raw, &extra)
		var libID, progress any
		if extra != nil {
			libID = extra["libraryItemId"]
			progress = extra["progress"]
		}
		if progress == nil {
			progress = 0
		}
		var episode any
		if typ == "podcastEpisode" {
			episode = itemID
		}
		out = append(out, map[string]any{"id": id, "userId": uid, "libraryItemId": libID, "episodeId": episode, "mediaItemId": itemID, "mediaItemType": typ, "duration": number(duration), "progress": progress, "currentTime": number(current), "isFinished": finished != 0, "hideFromContinueListening": hide != 0, "ebookLocation": store.NullString(location), "ebookProgress": number(ebook), "lastUpdate": dbtime.Millis(updatedAt), "startedAt": dbtime.Millis(createdAt), "finishedAt": dbtime.Millis(finishedAt)})
	}
	return out, rows.Err()
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
