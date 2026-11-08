package progressdto

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// StatusError is a progress update refused with the HTTP status and message
// upstream reports (User.createUpdateMediaProgressFromPayload).
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// Optional is one JSON property of a progress payload. Upstream payloads are
// loose JavaScript objects, so a key that is absent (undefined), null or a
// value behave differently; Set records that the key was present.
type Optional struct {
	Set bool
	Raw json.RawMessage
}

// UnmarshalJSON records the key as present, including when it is null.
func (o *Optional) UnmarshalJSON(b []byte) error {
	o.Set = true
	o.Raw = append(o.Raw[:0], b...)
	return nil
}

// Value builds a present Optional from a Go value, for callers that assemble a
// payload themselves.
func Value(v any) Optional {
	raw, err := json.Marshal(v)
	if err != nil {
		return Optional{}
	}
	return Optional{Set: true, Raw: raw}
}

func (o Optional) isNull() bool { return o.Set && string(o.Raw) == "null" }

// number is JavaScript Number(value) for a present value; ok is false when the
// result is NaN. An absent key is NaN (Number(undefined)), null is 0.
func (o Optional) number() (float64, bool) {
	if !o.Set {
		return 0, false
	}
	var v any
	if json.Unmarshal(o.Raw, &v) != nil {
		return 0, false
	}
	switch x := v.(type) {
	case nil:
		return 0, true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, true
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(n) {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// numberOrZero is Number(value) with NaN read as 0, for the
// isNullOrNaN(x) ? 0 : Number(x) idiom.
func (o Optional) numberOrZero() float64 {
	if o.isNull() {
		return 0
	}
	n, ok := o.number()
	if !ok {
		return 0
	}
	return n
}

// nullOrNaN is upstream's isNullOrNaN (server/utils/index.js).
func (o Optional) nullOrNaN() bool {
	if !o.Set || o.isNull() {
		return true
	}
	_, ok := o.number()
	return !ok
}

// truthy is JavaScript truthiness of a present value.
func (o Optional) truthy() bool {
	if !o.Set {
		return false
	}
	var v any
	return json.Unmarshal(o.Raw, &v) == nil && jsonvalue.Truthy(v)
}

// date is new Date(value) for a number of milliseconds or a date string.
func (o Optional) date() (time.Time, bool) {
	var v any
	if !o.Set || json.Unmarshal(o.Raw, &v) != nil {
		return time.Time{}, false
	}
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return time.Time{}, false
		}
		return time.UnixMilli(int64(x)), true
	case string:
		if t, ok := dbtime.Parse(x); ok {
			return t, true
		}
		for _, layout := range []string{"2006-01-02", "Mon, 02 Jan 2006 15:04:05 GMT"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// Payload is the object User.createUpdateMediaProgressFromPayload receives:
// the request body with the library item and episode taken from the URL, or a
// payload assembled by playback sync.
type Payload struct {
	LibraryItemID                 string   `json:"libraryItemId"`
	EpisodeID                     string   `json:"episodeId"`
	Duration                      Optional `json:"duration"`
	CurrentTime                   Optional `json:"currentTime"`
	Progress                      Optional `json:"progress"`
	IsFinished                    Optional `json:"isFinished"`
	HideFromContinueListening     Optional `json:"hideFromContinueListening"`
	EbookLocation                 Optional `json:"ebookLocation"`
	EbookProgress                 Optional `json:"ebookProgress"`
	FinishedAt                    Optional `json:"finishedAt"`
	CreatedAt                     Optional `json:"createdAt"`
	LastUpdate                    Optional `json:"lastUpdate"`
	MarkAsFinishedPercentComplete Optional `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining   Optional `json:"markAsFinishedTimeRemaining"`
}

type target struct {
	mediaItemID, mediaItemType string
	podcastID                  any
}

// resolveTarget finds the book or episode a payload addresses. allow, when
// not nil, is asked about the library of the found item before anything else
// is judged about it.
func resolveTarget(ctx context.Context, db *sql.DB, p Payload, allow func(libraryID string) bool) (target, error) {
	forbidden := &StatusError{http.StatusForbidden, "Forbidden"}
	if p.EpisodeID != "" {
		var podcastID string
		var libraryID sql.NullString
		err := db.QueryRowContext(ctx, `SELECT pe.podcastId, li.libraryId FROM podcastEpisodes pe LEFT JOIN libraryItems li ON li.mediaId = pe.podcastId WHERE pe.id=?`, p.EpisodeID).Scan(&podcastID, &libraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return target{}, &StatusError{404, "Episode not found"}
		}
		if err != nil {
			return target{}, err
		}
		if allow != nil && libraryID.String != "" && !allow(libraryID.String) {
			return target{}, forbidden
		}
		return target{mediaItemID: p.EpisodeID, mediaItemType: "podcastEpisode", podcastID: podcastID}, nil
	}
	var mediaID, mediaType, libraryID string
	err := db.QueryRowContext(ctx, `SELECT mediaId, mediaType, libraryId FROM libraryItems WHERE id=?`, p.LibraryItemID).Scan(&mediaID, &mediaType, &libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		return target{}, &StatusError{404, "Library item not found"}
	}
	if err != nil {
		return target{}, err
	}
	if allow != nil && libraryID != "" && !allow(libraryID) {
		return target{}, forbidden
	}
	if mediaType != "book" {
		return target{}, &StatusError{400, "Library item is not a book"}
	}
	return target{mediaItemID: mediaID, mediaItemType: "book"}, nil
}

type row struct {
	id            string
	userID        string
	mediaItemID   string
	mediaItemType string
	duration      *float64
	currentTime   float64
	isFinished    bool
	hide          bool
	ebookLocation *string
	ebookProgress *float64
	finishedAt    *time.Time
	extra         map[string]json.RawMessage
	createdAt     any
	updatedAt     any
}

// progress is the MediaProgress.progress getter (MediaProgress.js:176).
func (r row) progress() float64 {
	if r.duration == nil || *r.duration == 0 {
		return 0
	}
	return math.Max(0, math.Min(r.currentTime/(*r.duration), 1))
}

func (r row) extraProgress() float64 {
	var v float64
	if raw, ok := r.extra["progress"]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func (r *row) setExtraProgress(v float64) {
	if r.extra == nil {
		r.extra = map[string]json.RawMessage{}
	}
	raw, _ := json.Marshal(v)
	r.extra["progress"] = raw
}

func loadRow(ctx context.Context, db *sql.DB, userID, mediaItemID string) (row, bool, error) {
	m, err := sqlc.New(db).GetMediaProgressByUserItem(ctx, sqlc.GetMediaProgressByUserItemParams{UserID: store.Text(userID), MediaItemID: store.Text(mediaItemID)})
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, err
	}
	return rowFromModel(m), true, nil
}

// rowFromModel is the one decoder of a stored progress row.
func rowFromModel(m sqlc.Mediaprogress) row {
	r := row{id: m.ID, userID: m.Userid.String, mediaItemID: m.Mediaitemid.String, mediaItemType: m.Mediaitemtype.String,
		createdAt: m.Createdat, updatedAt: m.Updatedat, currentTime: m.Currenttime.Float64,
		isFinished: m.Isfinished.Int64 != 0, hide: m.Hidefromcontinuelistening.Int64 != 0}
	if m.Duration.Valid {
		r.duration = &m.Duration.Float64
	}
	if m.Ebooklocation.Valid {
		r.ebookLocation = &m.Ebooklocation.String
	}
	if m.Ebookprogress.Valid {
		r.ebookProgress = &m.Ebookprogress.Float64
	}
	if t := dbtime.Time(m.Finishedat); !t.IsZero() {
		r.finishedAt = &t
	}
	r.extra = map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(m.Extradata.String), &r.extra)
	return r
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UnixMilli() == b.UnixMilli()
}

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Apply is User.createUpdateMediaProgressFromPayload
// (server/models/User.js:728): it creates the user's progress row for the
// book or episode, or applies the payload to the existing row exactly as
// MediaProgress.applyProgressUpdate does (MediaProgress.js:182), and returns
// the stored progress. Refusals are *StatusError.
func Apply(ctx context.Context, db *sql.DB, userID string, p Payload) (wire.MediaProgress, error) {
	return ApplyChecked(ctx, db, userID, p, nil)
}

// ApplyChecked is Apply for callers that act for a user with limited library
// access: allow is asked about the library of the addressed item and a
// refusal is a 403 *StatusError. Upstream has no such check; nil skips it.
func ApplyChecked(ctx context.Context, db *sql.DB, userID string, p Payload, allow func(libraryID string) bool) (wire.MediaProgress, error) {
	t, err := resolveTarget(ctx, db, p, allow)
	if err != nil {
		return wire.MediaProgress{}, err
	}
	existing, found, err := loadRow(ctx, db, userID, t.mediaItemID)
	if err != nil {
		return wire.MediaProgress{}, err
	}
	var saved row
	if !found {
		saved, err = createRow(ctx, db, userID, t, p)
	} else {
		saved, err = updateRow(ctx, db, existing, p)
	}
	if err != nil {
		return wire.MediaProgress{}, err
	}
	return toWire(saved), nil
}

// LastUpdate is when the user's progress for a book or episode last changed,
// in Unix milliseconds; ok is false when there is no such progress.
func LastUpdate(ctx context.Context, db *sql.DB, userID, mediaItemID string) (ms int64, ok bool, err error) {
	r, found, err := loadRow(ctx, db, userID, mediaItemID)
	if err != nil || !found {
		return 0, false, err
	}
	ms, ok = dbtime.Millis(r.updatedAt).(int64)
	return ms, ok, nil
}

func createRow(ctx context.Context, db *sql.DB, userID string, t target, p Payload) (row, error) {
	now := time.Now()
	id, err := ids.New()
	if err != nil {
		return row{}, err
	}
	r := row{id: id, userID: userID, mediaItemID: t.mediaItemID, mediaItemType: t.mediaItemType, extra: map[string]json.RawMessage{}}
	duration := p.Duration.numberOrZero()
	r.duration = &duration
	r.currentTime = p.CurrentTime.numberOrZero()
	r.isFinished = p.IsFinished.truthy()
	r.hide = p.HideFromContinueListening.truthy()
	if p.EbookLocation.truthy() {
		var s string
		if json.Unmarshal(p.EbookLocation.Raw, &s) == nil {
			r.ebookLocation = &s
		}
	}
	ebook := p.EbookProgress.numberOrZero()
	r.ebookProgress = &ebook
	libraryItemID, _ := json.Marshal(p.LibraryItemID)
	r.extra["libraryItemId"] = libraryItemID
	r.setExtraProgress(p.Progress.numberOrZero())
	createdAt := now
	if p.CreatedAt.truthy() {
		if c, ok := p.CreatedAt.date(); ok {
			createdAt = c
		}
	}
	if r.isFinished {
		finished := now
		if p.FinishedAt.truthy() {
			if f, ok := p.FinishedAt.date(); ok {
				finished = f
			}
		}
		r.finishedAt = &finished
		r.setExtraProgress(1)
	}
	r.createdAt = dbtime.Format(createdAt)
	r.updatedAt = dbtime.Format(now)
	extra, _ := json.Marshal(r.extra)
	err = sqlc.New(db).InsertMediaProgress(ctx, sqlc.InsertMediaProgressParams{
		ID: r.id, MediaItemID: store.Text(t.mediaItemID), MediaItemType: store.Text(t.mediaItemType),
		Duration: store.Float(duration), CurrentPosition: store.Float(r.currentTime), IsFinished: store.Flag(r.isFinished),
		HideFromContinueListening: store.Flag(r.hide), EbookLocation: store.TextPtr(r.ebookLocation), EbookProgress: store.Float(ebook),
		FinishedAt: formatTime(r.finishedAt), ExtraData: store.Text(string(extra)), PodcastID: store.TextAny(t.podcastID),
		UserID: store.Text(userID), CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
	})
	return r, err
}

func formatTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbtime.Format(*t)
}

func updateRow(ctx context.Context, db *sql.DB, old row, p Payload) (row, error) {
	r := old
	r.extra = make(map[string]json.RawMessage, len(old.extra))
	for k, v := range old.extra {
		r.extra[k] = v
	}
	extraChanged := false
	now := time.Now()
	finishedAtPayload := p.FinishedAt
	currentTimePayload := p.CurrentTime

	if p.IsFinished.Set {
		wantFinished := p.IsFinished.truthy()
		if wantFinished && !old.isFinished {
			finished := now
			if p.FinishedAt.truthy() {
				if f, ok := p.FinishedAt.date(); ok {
					finished = f
				}
			}
			r.finishedAt = &finished
			r.setExtraProgress(1)
			extraChanged = true
			finishedAtPayload = Optional{}
		} else if !wantFinished && old.isFinished {
			r.finishedAt = nil
			r.setExtraProgress(0)
			r.currentTime = 0
			extraChanged = true
			finishedAtPayload = Optional{}
			currentTimePayload = Optional{}
		}
	} else if p.Progress.Set {
		if n, ok := p.Progress.number(); ok && n != old.progress() {
			r.setExtraProgress(math.Min(1, math.Max(0, n)))
			extraChanged = true
		}
	}

	// this.set(progressPayload): every model attribute present in the payload.
	if p.Duration.Set {
		if p.Duration.isNull() {
			r.duration = nil
		} else if n, ok := p.Duration.number(); ok {
			r.duration = &n
		}
	}
	if currentTimePayload.Set {
		r.currentTime = currentTimePayload.numberOrZero()
	}
	if p.IsFinished.Set {
		r.isFinished = p.IsFinished.truthy()
	}
	if p.HideFromContinueListening.Set {
		r.hide = p.HideFromContinueListening.truthy()
	}
	if p.EbookLocation.Set {
		var s string
		if json.Unmarshal(p.EbookLocation.Raw, &s) == nil {
			r.ebookLocation = &s
		} else {
			r.ebookLocation = nil
		}
	}
	if p.EbookProgress.Set {
		if p.EbookProgress.isNull() {
			r.ebookProgress = nil
		} else if n, ok := p.EbookProgress.number(); ok {
			r.ebookProgress = &n
		}
	}
	if finishedAtPayload.Set {
		if finishedAtPayload.isNull() {
			r.finishedAt = nil
		} else if f, ok := finishedAtPayload.date(); ok {
			r.finishedAt = &f
		}
	}

	currentTimeChanged := r.currentTime != old.currentTime
	if currentTimeChanged && !p.HideFromContinueListening.truthy() {
		r.hide = false
	}

	var duration float64
	if r.duration != nil {
		duration = *r.duration
	}
	timeRemaining := duration - r.currentTime
	shouldMarkAsFinished := false
	if duration != 0 {
		if !p.MarkAsFinishedPercentComplete.nullOrNaN() && p.MarkAsFinishedPercentComplete.numberOrZero() > 0 {
			percent := p.MarkAsFinishedPercentComplete.numberOrZero() / 100
			shouldMarkAsFinished = percent < r.progress()
		} else {
			remaining := 10.0
			if !p.MarkAsFinishedTimeRemaining.nullOrNaN() {
				remaining = p.MarkAsFinishedTimeRemaining.numberOrZero()
			}
			shouldMarkAsFinished = timeRemaining < remaining
		}
	}
	if !r.isFinished && shouldMarkAsFinished {
		r.isFinished = true
		if r.finishedAt == nil {
			r.finishedAt = &now
		}
		r.setExtraProgress(1)
		extraChanged = true
	} else if r.isFinished && currentTimeChanged && !shouldMarkAsFinished {
		r.isFinished = false
		r.finishedAt = nil
	}

	changed := extraChanged || !samePtr(r.duration, old.duration) || r.currentTime != old.currentTime ||
		r.isFinished != old.isFinished || r.hide != old.hide || !samePtr(r.ebookLocation, old.ebookLocation) ||
		!samePtr(r.ebookProgress, old.ebookProgress) || !sameTime(r.finishedAt, old.finishedAt)
	if changed {
		r.updatedAt = dbtime.Format(now)
		extra, _ := json.Marshal(r.extra)
		if err := sqlc.New(db).UpdateMediaProgressState(ctx, sqlc.UpdateMediaProgressStateParams{
			Duration: store.FloatPtr(r.duration), CurrentPosition: store.Float(r.currentTime), IsFinished: store.Flag(r.isFinished),
			HideFromContinueListening: store.Flag(r.hide), EbookLocation: store.TextPtr(r.ebookLocation), EbookProgress: store.FloatPtr(r.ebookProgress),
			FinishedAt: formatTime(r.finishedAt), ExtraData: store.Text(string(extra)), UpdatedAt: r.updatedAt, ID: r.id,
		}); err != nil {
			return row{}, err
		}
	}
	// For local sync: lastUpdate overrides updatedAt after the save.
	if p.LastUpdate.truthy() {
		if when, ok := p.LastUpdate.date(); ok {
			r.updatedAt = dbtime.Format(when)
			if err := sqlc.New(db).UpdateMediaProgressUpdatedAt(ctx, sqlc.UpdateMediaProgressUpdatedAtParams{Updatedat: r.updatedAt, ID: r.id}); err != nil {
				return row{}, err
			}
		}
	}
	return r, nil
}

// toWire is the progress as clients receive it.
func toWire(r row) wire.MediaProgress {
	out := wire.MediaProgress{
		ID: r.id, UserID: r.userID, MediaItemID: r.mediaItemID, MediaItemType: r.mediaItemType,
		Duration: r.duration, Progress: r.extraProgress(), CurrentTime: &r.currentTime,
		IsFinished: r.isFinished, HideFromContinueListening: r.hide,
		EbookLocation: r.ebookLocation, EbookProgress: r.ebookProgress,
	}
	var libraryItemID string
	if raw, ok := r.extra["libraryItemId"]; ok && json.Unmarshal(raw, &libraryItemID) == nil && libraryItemID != "" {
		out.LibraryItemID = &libraryItemID
	}
	if r.mediaItemType == "podcastEpisode" {
		id := r.mediaItemID
		out.EpisodeID = &id
	}
	if ms, ok := dbtime.Millis(r.updatedAt).(int64); ok {
		out.LastUpdate = &ms
	}
	if ms, ok := dbtime.Millis(r.createdAt).(int64); ok {
		out.StartedAt = &ms
	}
	if r.finishedAt != nil {
		if ms := r.finishedAt.UnixMilli(); ms != 0 {
			out.FinishedAt = &ms
		}
	}
	return out
}
