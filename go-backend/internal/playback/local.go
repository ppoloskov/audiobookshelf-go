package playback

import (
	"cmp"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
)

// localBodyLimit bounds a batch of offline sessions.
const localBodyLimit = 10 << 20

// epochMS is a time a client sends as epoch milliseconds, as a number or as a
// string of digits (the mobile app has sent both); anything else is 0.
type epochMS int64

// UnmarshalJSON reads a number or a numeric string; other values read as 0.
func (t *epochMS) UnmarshalJSON(data []byte) error {
	var n float64
	if json.Unmarshal(data, &n) != nil {
		var text string
		if json.Unmarshal(data, &text) != nil {
			return nil
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil
		}
		n = float64(parsed)
	}
	if !math.IsNaN(n) && !math.IsInf(n, 0) {
		*t = epochMS(n)
	}
	return nil
}

// localSession is an offline session a client uploads (PlaybackSession.toJSON as
// the app stored it).
type localSession struct {
	ID            string          `json:"id"`
	LibraryItemID string          `json:"libraryItemId"`
	EpisodeID     string          `json:"episodeId"`
	DisplayTitle  string          `json:"displayTitle"`
	DisplayAuthor string          `json:"displayAuthor"`
	CoverPath     *string         `json:"coverPath"`
	MediaPlayer   string          `json:"mediaPlayer"`
	PlayMethod    *int            `json:"playMethod"`
	Date          string          `json:"date"`
	DayOfWeek     string          `json:"dayOfWeek"`
	StartTime     float64         `json:"startTime"`
	CurrentTime   float64         `json:"currentTime"`
	TimeListening float64         `json:"timeListening"`
	StartedAt     epochMS         `json:"startedAt"`
	UpdatedAt     epochMS         `json:"updatedAt"`
	MediaMetadata json.RawMessage `json:"mediaMetadata"`
}

// localBody is the body of /api/session/local (the session's own fields, read
// separately into a localSession) and local-all.
type localBody struct {
	DeviceInfo clientDevice      `json:"deviceInfo"`
	Sessions   []json.RawMessage `json:"sessions"`
}

// localResult is one entry of the local-all response.
type localResult struct {
	ID             *string `json:"id"`
	Success        bool    `json:"success"`
	ProgressSynced *bool   `json:"progressSynced,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// looseUnmarshal decodes JSON into v, leaving values of the wrong type zero
// (offline uploads come from apps we cannot see) but rejecting malformed JSON.
func looseUnmarshal(data []byte, v any) error {
	var mistyped *json.UnmarshalTypeError
	if err := json.Unmarshal(data, v); err != nil && !errors.As(err, &mistyped) {
		return err
	}
	return nil
}

// readLocal reads the body of a local sync: empty is allowed, malformed is 400.
func readLocal(w http.ResponseWriter, r *http.Request) (data []byte, body localBody, err error) {
	if data, err = readBody(w, r, localBodyLimit); err != nil {
		return nil, body, err
	}
	if len(strings.TrimSpace(string(data))) > 0 && looseUnmarshal(data, &body) != nil {
		return nil, body, httpx.BadRequest("Invalid request body")
	}
	return data, body, nil
}

// localSync is POST /api/session/local: any authenticated user, for themselves.
func (h *Handler) localSync(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	data, body, err := readLocal(w, r)
	if err != nil {
		return err
	}
	var upload localSession
	_ = looseUnmarshal(data, &upload) // readLocal parsed the same bytes
	device, err := h.upsertDevice(r.Context(), r, user, body.DeviceInfo)
	if err != nil {
		return err
	}
	if res := h.syncLocal(r, user, upload, device); res.Error != "" {
		return httpx.Error(http.StatusInternalServerError, res.Error)
	}
	return ok(w)
}

// localSyncAll is POST /api/session/local-all: every session answered in order,
// a failed one does not stop the rest.
func (h *Handler) localSyncAll(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	_, body, err := readLocal(w, r)
	if err != nil {
		return err
	}
	device, err := h.upsertDevice(r.Context(), r, user, body.DeviceInfo)
	if err != nil {
		return err
	}
	results := make([]localResult, len(body.Sessions))
	for i, raw := range body.Sessions {
		var upload localSession
		_ = looseUnmarshal(raw, &upload) // a malformed entry is already rejected with the body; others read as empty
		results[i] = h.syncLocal(r, user, upload, device)
	}
	return httpx.JSON(w, r, struct {
		Results []localResult `json:"results"`
	}{results})
}

// localSessionID maps a client's play_local_* id to the server id it was first
// stored under; any other client id is kept and a missing one is new.
func (h *Handler) localSessionID(id string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !strings.HasPrefix(id, "play_local_") {
		if id != "" {
			return id, nil
		}
		return ids.New()
	}
	if h.localIDs[id] == "" {
		generated, err := ids.New()
		if err != nil {
			return "", err
		}
		h.localIDs[id] = generated
	}
	return h.localIDs[id], nil
}

// syncLocal stores an uploaded session and applies its progress. The result
// carries a fixed error message; details are logged.
func (h *Handler) syncLocal(r *http.Request, user *auth.Principal, upload localSession, device deviceInfo) localResult {
	ctx := r.Context()
	fail := func(message string, err error) localResult {
		slog.Error("local session sync failed", "session", upload.ID, "item", upload.LibraryItemID, "reason", message, "error", err)
		return localResult{ID: nilIfEmpty(upload.ID), Error: message}
	}
	m, err := h.loadMedia(ctx, upload.LibraryItemID, upload.EpisodeID)
	if err != nil {
		return fail("Media item not found", err)
	}
	var library db.Library
	if err = h.db.Get(ctx, &library, `SELECT * FROM libraries WHERE id = ?`, db.Val(m.item.LibraryID)); err != nil {
		return fail("Library not found", err)
	}
	id, err := h.localSessionID(upload.ID)
	if err != nil {
		return fail("Failed to save session", err)
	}
	updated, started := int64(upload.UpdatedAt), int64(upload.StartedAt)
	if updated == 0 {
		updated = time.Now().UnixMilli()
	}
	if started == 0 {
		started = updated
	}
	s := m.newSession(id, user.ID, upload.EpisodeID)
	s.touch(time.UnixMilli(updated))
	stored, err := h.sessionStored(ctx, id)
	if err != nil {
		return fail("Failed to save session", err)
	}
	if !stored {
		// A new local session keeps the client's date and weekday; updates
		// recompute them from updatedAt (PlaybackSessionManager.js:182, :212).
		s.Date, s.DayOfWeek = cmp.Or(upload.Date, s.Date), cmp.Or(upload.DayOfWeek, s.DayOfWeek)
	}
	s.DisplayTitle, s.DisplayAuthor = cmp.Or(upload.DisplayTitle, m.title), cmp.Or(upload.DisplayAuthor, m.author)
	s.CoverPath = upload.CoverPath // the client's, not the item's
	s.MediaPlayer, s.PlayMethod = cmp.Or(upload.MediaPlayer, "unknown"), playLocal
	if upload.PlayMethod != nil {
		s.PlayMethod = *upload.PlayMethod
	}
	s.StartTime, s.CurrentTime, s.TimeListening, s.StartedAt = upload.StartTime, upload.CurrentTime, upload.TimeListening, started
	s.MediaMetadata = overlayMetadata(m.metadata, upload.MediaMetadata)
	s.DeviceInfo = device // s.Duration stays the item's: the client's is always replaced (PlaybackSessionManager.js:203)
	if err = h.save(ctx, s); err != nil {
		return fail("Failed to save session", err)
	}
	synced, err := h.syncProgress(ctx, s, 0, &updated)
	if err != nil {
		return fail("Failed to sync progress", err)
	}
	return localResult{ID: &id, Success: true, ProgressSynced: &synced}
}

// overlayMetadata applies the keys of the client's mediaMetadata over the item's.
func overlayMetadata(base any, client json.RawMessage) any {
	switch m := base.(type) {
	case item.BookMetadataFull:
		_ = looseUnmarshal(client, &m) // unreadable client metadata leaves the item's
		return m
	case item.PodcastMetadataFull:
		_ = looseUnmarshal(client, &m)
		return m
	}
	return base
}
