package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/wire"
)

// PlaybackSessionColumns is the column list ScanPlaybackSession expects, in
// order. Use it as `SELECT ` + PlaybackSessionColumns + ` FROM playbackSessions`.
const PlaybackSessionColumns = `id,userId,libraryId,mediaItemId,mediaItemType,displayTitle,displayAuthor,duration,playMethod,mediaPlayer,startTime,currentTime,serverVersion,coverPath,date,dayOfWeek,timeListening,mediaMetadata,extraData,deviceId,createdAt,updatedAt`

// PlaybackSessionRow is one playbackSessions row as stored. Every column but
// id, createdAt and updatedAt is nullable in the pinned schema, and real
// installs contain NULLs (sessions from older upstream versions and mobile
// uploads have NULL playMethod, timeListening, displayAuthor, date, ...).
type PlaybackSessionRow struct {
	ID                                            string
	UserID, LibraryID, MediaItemID, MediaItemType sql.NullString
	DisplayTitle, DisplayAuthor                   sql.NullString
	Duration, PlayMethod, StartTime, CurrentTime  sql.NullFloat64
	TimeListening                                 sql.NullFloat64
	MediaPlayer, ServerVersion, CoverPath         sql.NullString
	Date, DayOfWeek                               sql.NullString
	MediaMetadata, ExtraData                      sql.NullString
	DeviceID                                      sql.NullString
	CreatedAt, UpdatedAt                          any
}

// ScanPlaybackSession scans a row selected with PlaybackSessionColumns.
func ScanPlaybackSession(rows *sql.Rows) (PlaybackSessionRow, error) {
	var s PlaybackSessionRow
	err := rows.Scan(&s.ID, &s.UserID, &s.LibraryID, &s.MediaItemID, &s.MediaItemType, &s.DisplayTitle, &s.DisplayAuthor,
		&s.Duration, &s.PlayMethod, &s.MediaPlayer, &s.StartTime, &s.CurrentTime, &s.ServerVersion, &s.CoverPath,
		&s.Date, &s.DayOfWeek, &s.TimeListening, &s.MediaMetadata, &s.ExtraData, &s.DeviceID, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func millisOf(v any) (int64, bool) {
	switch x := v.(type) {
	case time.Time:
		return x.UnixMilli(), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case string:
		if t, ok := dbtime.Parse(x); ok {
			return t.UnixMilli(), true
		}
	case []byte:
		if t, ok := dbtime.Parse(string(x)); ok {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// LegacyDate returns date and dayOfWeek as the legacy session object has them
// after construct (server/objects/PlaybackSession.js:118-180): stored values,
// or, when date is empty, both derived from updatedAt in the server's local
// time zone ("Local playback sessions dont set this date field").
func (s PlaybackSessionRow) LegacyDate() (date, dayOfWeek any) {
	date, dayOfWeek = nullString(s.Date), nullString(s.DayOfWeek)
	if s.Date.String == "" {
		if ms, ok := millisOf(s.UpdatedAt); ok {
			t := time.UnixMilli(ms).In(time.Local)
			return t.Format("2006-01-02"), t.Weekday().String()
		}
	}
	return date, dayOfWeek
}

// LegacyTimeListening is `session.timeListening || null`: NULL and 0 are null.
func (s PlaybackSessionRow) LegacyTimeListening() any {
	if !s.TimeListening.Valid || s.TimeListening.Float64 == 0 {
		return nil
	}
	return s.TimeListening.Float64
}

func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func orEmpty(v sql.NullString) string { return v.String }

func orZero(v sql.NullFloat64) float64 { return v.Float64 }

func withoutLegacyPrefix(v any, prefixes ...string) any {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return nil
		}
	}
	return s
}

// LegacyJSON is the session as PlaybackSession.getOldPlaybackSession builds
// it and objects/PlaybackSession construct + toJSON serialize it
// (server/models/PlaybackSession.js:79-108, objects/PlaybackSession.js:52-180):
// `|| ”` titles, `|| 0` start/current time, `|| null` timeListening, derived
// date/dayOfWeek, mediaMetadata passed through (null stays null), legacy
// lib_/li_/ep_/local_ ids dropped, and a missing device serialized as {}
// (new DeviceInfo(null).toJSON() deletes every null key).
func (s PlaybackSessionRow) LegacyJSON(device *wire.DeviceInfo) map[string]any {
	var extra map[string]any
	_ = json.Unmarshal([]byte(s.ExtraData.String), &extra)
	var metadata any
	if s.MediaMetadata.Valid {
		_ = json.Unmarshal([]byte(s.MediaMetadata.String), &metadata)
	}
	podcast := s.MediaItemType.String == "podcastEpisode"
	var bookID, episodeID any
	mediaType := "book"
	if podcast {
		mediaType = "podcast"
		episodeID = withoutLegacyPrefix(nullString(s.MediaItemID), "ep_", "local_")
	} else {
		bookID = withoutLegacyPrefix(nullString(s.MediaItemID))
	}
	var deviceInfo any = map[string]any{}
	if device != nil {
		deviceInfo = device
	}
	date, dayOfWeek := s.LegacyDate()
	startedAt, _ := millisOf(s.CreatedAt)
	updatedAt, _ := millisOf(s.UpdatedAt)
	return map[string]any{
		"id": s.ID, "userId": nullString(s.UserID),
		"libraryId":     withoutLegacyPrefix(nullString(s.LibraryID), "lib_"),
		"libraryItemId": withoutLegacyPrefix(extra["libraryItemId"], "li_", "local_"),
		"bookId":        bookID, "episodeId": episodeID, "mediaType": mediaType,
		"mediaMetadata": metadata, "chapters": []any{},
		"displayTitle": orEmpty(s.DisplayTitle), "displayAuthor": orEmpty(s.DisplayAuthor),
		"coverPath": nullString(s.CoverPath), "duration": nullFloat(s.Duration), "playMethod": nullFloat(s.PlayMethod),
		"mediaPlayer": withoutLegacyPrefix(nullString(s.MediaPlayer)), "deviceInfo": deviceInfo,
		"serverVersion": nullString(s.ServerVersion), "date": date, "dayOfWeek": dayOfWeek,
		"timeListening": s.LegacyTimeListening(), "startTime": orZero(s.StartTime), "currentTime": orZero(s.CurrentTime),
		"startedAt": startedAt, "updatedAt": updatedAt,
	}
}
