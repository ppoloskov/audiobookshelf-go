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
		out = append(out, fromRow(m))
	}
	return out, nil
}

func fromRow(m sqlc.Mediaprogress) wire.MediaProgress {
	var extra struct {
		LibraryItemID *string  `json:"libraryItemId"`
		Progress      *float64 `json:"progress"`
	}
	_ = json.Unmarshal([]byte(m.Extradata.String), &extra)
	p := wire.MediaProgress{
		ID: m.ID, UserID: m.Userid.String, LibraryItemID: extra.LibraryItemID, MediaItemID: m.Mediaitemid.String,
		MediaItemType: m.Mediaitemtype.String, Duration: floatPtr(m.Duration), CurrentTime: floatPtr(m.Currenttime),
		IsFinished: m.Isfinished.Int64 != 0, HideFromContinueListening: m.Hidefromcontinuelistening.Int64 != 0,
		EbookLocation: store.NullStringPtr(m.Ebooklocation), EbookProgress: floatPtr(m.Ebookprogress),
		LastUpdate: millisPtr(m.Updatedat), StartedAt: millisPtr(m.Createdat), FinishedAt: millisPtr(m.Finishedat),
	}
	if extra.Progress != nil {
		p.Progress = *extra.Progress
	}
	if p.MediaItemType == "podcastEpisode" {
		p.EpisodeID = &m.Mediaitemid.String
	}
	return p
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

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func millisPtr(v any) *int64 {
	ms, ok := dbtime.Millis(v).(int64)
	if !ok {
		return nil
	}
	return &ms
}
