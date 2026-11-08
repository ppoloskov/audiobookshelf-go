package playback

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

// epochMS is a time a client sends as epoch milliseconds, as a string of them
// or as an RFC 3339 string; anything else is 0.
type epochMS int64

func (t *epochMS) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*t = epochMS(n)
		return nil
	}
	var text string
	if json.Unmarshal(b, &text) != nil {
		return nil
	}
	if ms, err := strconv.ParseInt(text, 10, 64); err == nil {
		*t = epochMS(ms)
	} else if d, err := time.Parse(time.RFC3339Nano, text); err == nil {
		*t = epochMS(d.UnixMilli())
	}
	return nil
}

// localSession is an offline session a client uploads (PlaybackSession.toJSON
// as the app stored it).
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

// localResult is one entry of the local-all response.
type localResult struct {
	ID             nullID `json:"id"`
	Success        bool   `json:"success"`
	ProgressSynced *bool  `json:"progressSynced,omitempty"`
	Error          string `json:"error,omitempty"`
}

func (h *Handler) handleLocalSync(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	var body struct {
		localSession
		DeviceInfo clientDevice   `json:"deviceInfo"`
		Sessions   []localSession `json:"sessions"`
	}
	if err := readBody(r, &body); err != nil {
		return err
	}
	device, err := h.upsertDevice(r.Context(), user, r, body.DeviceInfo)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	if !strings.HasSuffix(r.URL.Path, "-all") {
		if res := h.syncOneLocal(r.Context(), user, body.localSession, device); res.Error != "" {
			logging.RequestDebug(r, "responding 500: "+res.Error)
			return httputil.Status(500, res.Error)
		}
		httputil.OK(w, r)
		return nil
	}
	results := make([]localResult, len(body.Sessions))
	for i, ls := range body.Sessions {
		results[i] = h.syncOneLocal(r.Context(), user, ls, device)
	}
	httputil.WriteJSON(w, 200, map[string]any{"results": results})
	return nil
}

// localSessionID maps a client's play_local_* id to the server id it was
// first stored under; any other client id is kept and a missing one is new.
func (h *Handler) localSessionID(id string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.HasPrefix(id, "play_local_") {
		if h.localIDs[id] == "" {
			generated, err := ids.New()
			if err != nil {
				return "", err
			}
			h.localIDs[id] = generated
		}
		return h.localIDs[id], nil
	}
	if id != "" {
		return id, nil
	}
	return ids.New()
}

// overlayMetadata applies the keys of the client's mediaMetadata over the
// item's, as the persisted legacy subset.
func overlayMetadata(base any, client json.RawMessage) any {
	switch m := base.(type) {
	case libitem.BookMetadataFull:
		_ = unmarshalLoose(client, &m)
		return m
	case libitem.PodcastMetadata:
		_ = unmarshalLoose(client, &m)
		m.TitleIgnorePrefix = nil
		return m
	}
	return base
}

func (h *Handler) syncOneLocal(ctx context.Context, user store.User, ls localSession, device deviceInfo) localResult {
	logging.Infof("[PlaybackSessionManager] Syncing local session %q (%s) (updatedAt: %d)", ls.DisplayTitle, ls.ID, ls.UpdatedAt) // PlaybackSessionManager.js:110
	fail := func(msg string) localResult { return localResult{ID: nullID(ls.ID), Error: msg} }
	item, err := h.loadItem(ctx, ls.LibraryItemID, ls.EpisodeID)
	if err != nil {
		logging.Errorf("[PlaybackSessionManager] syncLocalSession: Media item not found for session %q (%s): %v", ls.DisplayTitle, ls.ID, err) // PlaybackSessionManager.js:132
		return fail("Media item not found")
	}
	var libraryExists int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries WHERE id=?`, item.LibraryID).Scan(&libraryExists); err != nil || libraryExists == 0 {
		logging.Errorf("[PlaybackSessionManager] syncLocalSession: Library not found for session %q (%s)", ls.DisplayTitle, ls.ID) // PlaybackSessionManager.js:142
		return fail("Library not found")
	}
	id, err := h.localSessionID(ls.ID)
	if err != nil {
		logging.Errorf("[PlaybackSessionManager] syncLocalSession: could not create a session id for %q (%s): %v", ls.DisplayTitle, ls.ID, err)
		return fail("Failed to save session")
	}
	updated, started := int64(ls.UpdatedAt), int64(ls.StartedAt)
	if updated == 0 {
		updated = time.Now().UnixMilli()
	}
	if started == 0 {
		started = updated
	}
	day, weekday := time.UnixMilli(updated).Format("2006-01-02"), time.UnixMilli(updated).Weekday().String()
	if existing, _ := sqlc.New(h.db).CountPlaybackSessionByID(ctx, id); existing == 0 {
		// A new local session is constructed from the client JSON
		// (PlaybackSessionManager.js:182), keeping its date and dayOfWeek;
		// only updates recompute them from updatedAt (:212-216).
		if ls.Date != "" {
			day = ls.Date
		}
		if ls.DayOfWeek != "" {
			weekday = ls.DayOfWeek
		}
	}
	if ls.DisplayTitle == "" {
		ls.DisplayTitle = item.Title
	}
	if ls.DisplayAuthor == "" {
		ls.DisplayAuthor = item.Author
	}
	if ls.MediaPlayer == "" {
		ls.MediaPlayer = "unknown"
	}
	playMethod := 3
	if ls.PlayMethod != nil {
		playMethod = *ls.PlayMethod
	}
	s := &session{ID: id, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, MediaID: item.MediaID, EpisodeID: ls.EpisodeID, MediaType: item.MediaType,
		DisplayTitle: ls.DisplayTitle, DisplayAuthor: ls.DisplayAuthor, CoverPath: ls.CoverPath, // coverPath also comes from the client JSON, not the library item
		PlayMethod: playMethod, MediaPlayer: ls.MediaPlayer,
		// PlaybackSessionManager.syncLocalSession (server/managers/PlaybackSessionManager.js:203)
		// always replaces the client duration with the item's playback duration.
		Duration: item.Duration, StartTime: ls.StartTime, CurrentTime: ls.CurrentTime, TimeListening: ls.TimeListening, StartedAt: started, UpdatedAt: updated,
		Date: day, DayOfWeek: weekday, MediaMetadata: overlayMetadata(item.Metadata, ls.MediaMetadata), Chapters: item.Chapters, Device: device, Files: item.Files}
	if err = h.saveSessionRecord(ctx, s); err != nil {
		return fail("Failed to save session")
	}
	synced, err := h.syncProgress(ctx, user.ID, s, 0, &updated)
	if err != nil {
		return fail("Failed to sync progress")
	}
	return localResult{ID: nullID(id), Success: true, ProgressSynced: &synced}
}
