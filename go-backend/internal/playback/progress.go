package playback

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// Field is a JSON property that is absent, null or a value. Only the properties
// whose clients tell absent (leave alone) from null (clear) use it.
type Field[T any] struct {
	Present bool // the key was in the JSON
	Null    bool // ... and its value was null
	Value   T
}

// Set is a present Field holding v.
func Set[T any](v T) Field[T] { return Field[T]{Present: true, Value: v} }

// UnmarshalJSON records the key as present and a null value as Null.
func (f *Field[T]) UnmarshalJSON(data []byte) error {
	f.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Null = true
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

// Update is one change to a user's media progress, and the body of
// PATCH /api/me/progress/{id} once the library item and episode are taken from
// the URL. A nil pointer means "leave alone" (or "start from zero" for a new
// row). Times are epoch milliseconds.
type Update struct {
	LibraryItemID             string         `json:"libraryItemId"`
	EpisodeID                 string         `json:"episodeId"`
	Duration                  Field[float64] `json:"duration"`
	CurrentTime               *float64       `json:"currentTime"`
	Progress                  *float64       `json:"progress"`
	IsFinished                *bool          `json:"isFinished"`
	HideFromContinueListening *bool          `json:"hideFromContinueListening"`
	EbookLocation             *string        `json:"ebookLocation"`
	EbookProgress             Field[float64] `json:"ebookProgress"`
	FinishedAt                Field[int64]   `json:"finishedAt"`
	CreatedAt                 *int64         `json:"createdAt"`
	// LastUpdate, when non-zero, becomes the row's updatedAt after the save
	// (offline sync keeps the time the client last listened).
	LastUpdate *int64 `json:"lastUpdate"`
	// The library's auto-finish settings; percent (0-100) wins over seconds.
	MarkAsFinishedPercentComplete *float64 `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining   *float64 `json:"markAsFinishedTimeRemaining"`
}

// ProgressView is the media progress as clients read it (MediaProgress.toJSON).
type ProgressView struct {
	ID                        string   `json:"id"`
	UserID                    string   `json:"userId"`
	LibraryItemID             *string  `json:"libraryItemId"`
	EpisodeID                 *string  `json:"episodeId"`
	MediaItemID               string   `json:"mediaItemId"`
	MediaItemType             string   `json:"mediaItemType"`
	Duration                  *float64 `json:"duration"`
	Progress                  float64  `json:"progress"`
	CurrentTime               float64  `json:"currentTime"`
	IsFinished                bool     `json:"isFinished"`
	HideFromContinueListening bool     `json:"hideFromContinueListening"`
	EbookLocation             *string  `json:"ebookLocation"`
	EbookProgress             *float64 `json:"ebookProgress"`
	LastUpdate                *int64   `json:"lastUpdate"`
	StartedAt                 *int64   `json:"startedAt"`
	FinishedAt                *int64   `json:"finishedAt"`
}

// Progress reads and writes the mediaProgresses rows. It is the one place the
// create and merge rules live (User.createUpdateMediaProgressFromPayload and
// MediaProgress.applyProgressUpdate).
type Progress struct{ db *db.DB }

// NewProgress returns the progress store of d.
func NewProgress(d *db.DB) *Progress { return &Progress{db: d} }

// target is the book or episode an Update addresses.
type target struct {
	mediaItemID, mediaItemType string
	podcastID                  *string
	libraryID                  string
	notBook                    bool // a podcast addressed without an episode
}

func (p *Progress) resolve(ctx context.Context, u Update) (target, error) {
	if u.EpisodeID != "" {
		var row struct {
			PodcastID string  `db:"podcastId"`
			LibraryID *string `db:"libraryId"`
		}
		err := p.db.Get(ctx, &row, `SELECT pe.podcastId, li.libraryId FROM podcastEpisodes pe LEFT JOIN libraryItems li ON li.mediaId = pe.podcastId WHERE pe.id = ?`, u.EpisodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return target{}, httpx.Error(http.StatusNotFound, "Episode not found")
		}
		if err != nil {
			return target{}, fmt.Errorf("find episode %s: %w", u.EpisodeID, err)
		}
		return target{mediaItemID: u.EpisodeID, mediaItemType: "podcastEpisode", podcastID: &row.PodcastID, libraryID: db.Val(row.LibraryID)}, nil
	}
	var row struct {
		MediaID   string  `db:"mediaId"`
		MediaType string  `db:"mediaType"`
		LibraryID *string `db:"libraryId"`
	}
	err := p.db.Get(ctx, &row, `SELECT mediaId, mediaType, libraryId FROM libraryItems WHERE id = ?`, u.LibraryItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return target{}, httpx.Error(http.StatusNotFound, "Library item not found")
	}
	if err != nil {
		return target{}, fmt.Errorf("find library item %s: %w", u.LibraryItemID, err)
	}
	return target{mediaItemID: row.MediaID, mediaItemType: "book", libraryID: db.Val(row.LibraryID), notBook: row.MediaType != "book"}, nil
}

// Apply creates the user's progress row for the book or episode u addresses, or
// merges u into the existing row, and returns the stored progress. Refusals are
// httpx errors: 404 for an unknown item or episode, 403 when the user may not
// access the item's library, 400 for a podcast without an episode.
func (p *Progress) Apply(ctx context.Context, user *auth.Principal, u Update) (ProgressView, error) {
	t, err := p.resolve(ctx, u)
	if err != nil {
		return ProgressView{}, err
	}
	if t.libraryID != "" && !user.CanAccessLibrary(t.libraryID) {
		return ProgressView{}, httpx.Error(http.StatusForbidden, "Forbidden")
	}
	return p.write(ctx, user.ID, t, u)
}

// applyTrusted is Apply for a session that was authorised when it started.
func (p *Progress) applyTrusted(ctx context.Context, userID string, u Update) (ProgressView, error) {
	t, err := p.resolve(ctx, u)
	if err != nil {
		return ProgressView{}, err
	}
	return p.write(ctx, userID, t, u)
}

func (p *Progress) write(ctx context.Context, userID string, t target, u Update) (ProgressView, error) {
	if t.notBook {
		return ProgressView{}, httpx.Error(http.StatusBadRequest, "Library item is not a book")
	}
	var saved db.MediaProgress
	err := p.db.Tx(ctx, func(ctx context.Context) error {
		old, found, err := p.load(ctx, userID, t.mediaItemID)
		if err != nil {
			return err
		}
		if !found {
			if saved, err = newProgress(userID, t, u); err != nil {
				return err
			}
			return p.db.Insert(ctx, "mediaProgresses", &saved)
		}
		var changed bool
		saved, changed = merge(old, u, db.Now())
		if !changed {
			return nil
		}
		return p.db.Update(ctx, "mediaProgresses", &saved, "duration", "currentTime", "isFinished", "hideFromContinueListening",
			"ebookLocation", "ebookProgress", "finishedAt", "extraData", "updatedAt")
	})
	if err != nil {
		return ProgressView{}, fmt.Errorf("apply progress: %w", err)
	}
	return view(saved), nil
}

func (p *Progress) load(ctx context.Context, userID, mediaItemID string) (row db.MediaProgress, found bool, err error) {
	err = p.db.Get(ctx, &row, `SELECT * FROM mediaProgresses WHERE userId = ? AND mediaItemId = ? LIMIT 1`, userID, mediaItemID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

// updatedAt is when the user's progress for the book or episode last changed, in
// epoch milliseconds; ok is false when there is none.
func (p *Progress) updatedAt(ctx context.Context, userID, mediaItemID string) (ms int64, ok bool, err error) {
	row, found, err := p.load(ctx, userID, mediaItemID)
	if err != nil || !found || row.UpdatedAt.IsZero() {
		return 0, false, err
	}
	return row.UpdatedAt.Millis(), true, nil
}

// Read lists the user's progress in the order upstream loads it (the
// user/item/finished/time index).
func (p *Progress) Read(ctx context.Context, userID string) ([]ProgressView, error) {
	var rows []db.MediaProgress
	if err := p.db.Select(ctx, &rows, `SELECT * FROM mediaProgresses WHERE userId = ? ORDER BY mediaItemId, isFinished, currentTime`, userID); err != nil {
		return nil, fmt.Errorf("read progress: %w", err)
	}
	out := make([]ProgressView, len(rows))
	for i, row := range rows {
		out[i] = view(row)
	}
	return out, nil
}

// ReadOne is User.getOldMediaProgress: the first progress of libraryItemID, of
// episodeID when given; nil when there is none.
func (p *Progress) ReadOne(ctx context.Context, userID, libraryItemID, episodeID string) (*ProgressView, error) {
	all, err := p.Read(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i, v := range all {
		if (episodeID == "" || v.MediaItemID == episodeID) && v.LibraryItemID != nil && *v.LibraryItemID == libraryItemID {
			return &all[i], nil
		}
	}
	return nil, nil
}

// newProgress is a row created by its first update. Creation never marks the row
// finished by position and ignores lastUpdate (the row is stamped now).
func newProgress(userID string, t target, u Update) (db.MediaProgress, error) {
	id, err := ids.New()
	if err != nil {
		return db.MediaProgress{}, fmt.Errorf("new progress id: %w", err)
	}
	now := db.Now()
	extra := &db.ProgressExtra{LibraryItemID: &u.LibraryItemID, Progress: db.Ptr(db.Val(u.Progress))}
	r := db.MediaProgress{
		ID: id, MediaItemID: &t.mediaItemID, MediaItemType: &t.mediaItemType, PodcastID: t.podcastID, UserID: &userID,
		Duration: db.Ptr(u.Duration.Value), CurrentTime: db.Ptr(db.Val(u.CurrentTime)),
		IsFinished: db.Bool(db.Val(u.IsFinished)), HideFromContinueListening: db.Bool(db.Val(u.HideFromContinueListening)),
		EbookProgress: db.Ptr(u.EbookProgress.Value), ExtraData: db.JSON[*db.ProgressExtra]{V: extra},
		CreatedAt: now, UpdatedAt: now,
	}
	if loc := db.Val(u.EbookLocation); loc != "" {
		r.EbookLocation = &loc
	}
	if u.CreatedAt != nil && *u.CreatedAt != 0 {
		r.CreatedAt = db.FromMillis(*u.CreatedAt)
	}
	if r.IsFinished {
		r.FinishedAt = finishedAt(u.FinishedAt, now)
		extra.Progress = db.Ptr(1.0)
	}
	return r, nil
}

// finishedAt is the payload's finish time, else now.
func finishedAt(f Field[int64], now db.Time) db.Time {
	if f.Present && !f.Null && f.Value != 0 {
		return db.FromMillis(f.Value)
	}
	return now
}

// ratio is MediaProgress.progress: currentTime over duration, clamped to 0-1.
func ratio(r db.MediaProgress) float64 {
	if duration := db.Val(r.Duration); duration != 0 {
		return math.Max(0, math.Min(db.Val(r.CurrentTime)/duration, 1))
	}
	return 0
}

// merge applies u to the stored row (MediaProgress.applyProgressUpdate) and
// reports whether anything changed. updatedAt is only bumped on a change, or set
// from LastUpdate.
func merge(old db.MediaProgress, u Update, now db.Time) (r db.MediaProgress, changed bool) {
	r = old
	oldExtra := db.ProgressExtra{}
	if old.ExtraData.V != nil {
		oldExtra = *old.ExtraData.V
	}
	extra := oldExtra
	old.ExtraData.V = &oldExtra // compare against a private copy, so a NULL extraData is not a change
	r.ExtraData.V = &extra
	currentTime, finished := u.CurrentTime, u.FinishedAt
	switch {
	case u.IsFinished != nil && *u.IsFinished && !bool(old.IsFinished):
		r.FinishedAt = finishedAt(u.FinishedAt, now)
		extra.Progress, finished = db.Ptr(1.0), Field[int64]{}
	case u.IsFinished != nil && !*u.IsFinished && bool(old.IsFinished):
		r.FinishedAt, r.CurrentTime = db.Time{}, db.Ptr(0.0)
		extra.Progress, finished, currentTime = db.Ptr(0.0), Field[int64]{}, nil
	case u.IsFinished == nil && u.Progress != nil && *u.Progress != ratio(old):
		extra.Progress = db.Ptr(math.Min(1, math.Max(0, *u.Progress)))
	}
	if u.Duration.Present {
		r.Duration = db.Ptr(u.Duration.Value)
		if u.Duration.Null {
			r.Duration = nil
		}
	}
	if currentTime != nil {
		r.CurrentTime = currentTime
	}
	if u.IsFinished != nil {
		r.IsFinished = db.Bool(*u.IsFinished)
	}
	if u.HideFromContinueListening != nil {
		r.HideFromContinueListening = db.Bool(*u.HideFromContinueListening)
	}
	if u.EbookLocation != nil {
		r.EbookLocation = u.EbookLocation
	}
	if u.EbookProgress.Present {
		r.EbookProgress = db.Ptr(u.EbookProgress.Value)
		if u.EbookProgress.Null {
			r.EbookProgress = nil
		}
	}
	if finished.Present {
		r.FinishedAt = db.Time{}
		if !finished.Null {
			r.FinishedAt = db.FromMillis(finished.Value)
		}
	}

	moved := db.Val(r.CurrentTime) != db.Val(old.CurrentTime)
	if moved && !db.Val(u.HideFromContinueListening) {
		r.HideFromContinueListening = false
	}
	// Auto-finish: the library's percent wins over the seconds remaining.
	nearEnd := false
	if duration := db.Val(r.Duration); duration != 0 {
		if percent := u.MarkAsFinishedPercentComplete; percent != nil && *percent > 0 {
			nearEnd = *percent/100 < ratio(r)
		} else {
			remaining := 10.0
			if seconds := u.MarkAsFinishedTimeRemaining; seconds != nil {
				remaining = *seconds
			}
			nearEnd = duration-db.Val(r.CurrentTime) < remaining
		}
	}
	if !bool(r.IsFinished) && nearEnd {
		r.IsFinished = true
		if r.FinishedAt.IsZero() {
			r.FinishedAt = now
		}
		extra.Progress = db.Ptr(1.0)
	} else if bool(r.IsFinished) && moved && !nearEnd {
		r.IsFinished, r.FinishedAt = false, db.Time{}
	}

	changed = !reflect.DeepEqual(r, old)
	if changed {
		r.UpdatedAt = now
	}
	if u.LastUpdate != nil && *u.LastUpdate != 0 {
		r.UpdatedAt, changed = db.FromMillis(*u.LastUpdate), true
	}
	return r, changed
}

func view(r db.MediaProgress) ProgressView {
	v := ProgressView{
		ID: r.ID, UserID: db.Val(r.UserID), MediaItemID: db.Val(r.MediaItemID), MediaItemType: db.Val(r.MediaItemType),
		Duration: r.Duration, CurrentTime: db.Val(r.CurrentTime), IsFinished: bool(r.IsFinished),
		HideFromContinueListening: bool(r.HideFromContinueListening), EbookLocation: r.EbookLocation, EbookProgress: r.EbookProgress,
	}
	if extra := r.ExtraData.V; extra != nil {
		v.Progress = db.Val(extra.Progress)
		if id := db.Val(extra.LibraryItemID); id != "" {
			v.LibraryItemID = &id
		}
	}
	if v.MediaItemType == "podcastEpisode" {
		v.EpisodeID = &v.MediaItemID
	}
	if !r.UpdatedAt.IsZero() {
		v.LastUpdate = db.Ptr(r.UpdatedAt.Millis())
	}
	if !r.CreatedAt.IsZero() {
		v.StartedAt = db.Ptr(r.CreatedAt.Millis())
	}
	if !r.FinishedAt.IsZero() && r.FinishedAt.Millis() != 0 {
		v.FinishedAt = db.Ptr(r.FinishedAt.Millis())
	}
	return v
}
