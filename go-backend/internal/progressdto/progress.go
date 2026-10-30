// Package progressdto reads legacy media progress browser DTOs independently
// of catalog handlers so authentication can be tested and used without them.
package progressdto

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

func Read(ctx context.Context, db *sql.DB, userID string) ([]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookLocation,ebookProgress,finishedAt,extraData,createdAt,updatedAt FROM mediaProgresses WHERE userId = ?`, userID)
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
		out = append(out, map[string]any{"id": id, "userId": uid, "libraryItemId": libID, "episodeId": episode, "mediaItemId": itemID, "mediaItemType": typ, "duration": number(duration), "progress": progress, "currentTime": number(current), "isFinished": finished != 0, "hideFromContinueListening": hide != 0, "ebookLocation": nullable(location), "ebookProgress": number(ebook), "lastUpdate": millis(updatedAt), "startedAt": millis(createdAt), "finishedAt": millis(finishedAt)})
	}
	return out, rows.Err()
}
func number(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
func nullable(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}
func millis(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case time.Time:
		return x.UnixMilli()
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		return parseDate(string(x))
	case string:
		return parseDate(x)
	}
	return nil
}
func parseDate(v string) any {
	if n, e := strconv.ParseInt(v, 10, 64); e == nil {
		return n
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999 -07:00", "2006-01-02 15:04:05.000 -07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
		if t, e := time.Parse(layout, v); e == nil {
			return t.UnixMilli()
		}
	}
	return nil
}
