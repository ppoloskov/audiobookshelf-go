package playback

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/mediatools"
)

// Play methods (PlaybackSession.js): 0 direct play, 2 transcode; local sessions
// uploaded by a client are 3.
const (
	playDirect    = 0
	playTranscode = 2
	playLocal     = 3
)

type startRequest struct {
	DeviceInfo         clientDevice `json:"deviceInfo"`
	MediaPlayer        string       `json:"mediaPlayer"`
	SupportedMimeTypes []string     `json:"supportedMimeTypes"`
	ForceDirectPlay    bool         `json:"forceDirectPlay"`
	ForceTranscode     bool         `json:"forceTranscode"`
}

// onlineUser is the payload of user_stream_update (OnlineUserDTO).
type onlineUser struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	Type      string  `json:"type"`
	Session   Session `json:"session"`
	LastSeen  *int64  `json:"lastSeen"`
	CreatedAt *int64  `json:"createdAt"`
}

// start is POST /api/items/{id}[/play/{episodeId}]: the user must have access to
// the item (library, explicit content and tags).
func (h *Handler) start(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	episodeID := r.PathValue("episodeId")
	m, err := h.loadMedia(ctx, r.PathValue("id"), episodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return fmt.Errorf("load media: %w", err)
	}
	access := m.item.Access()
	if !user.CanAccessItem(access.LibraryID, access.Explicit, access.Tags) {
		return httpx.Forbidden()
	}
	if len(m.files) == 0 {
		return httpx.NotFound()
	}
	req, err := decodeBody[startRequest](w, r)
	if err != nil {
		return err
	}
	device, err := h.upsertDevice(ctx, r, user, req.DeviceInfo)
	if err != nil {
		return err
	}
	id, err := ids.New()
	if err != nil {
		return fmt.Errorf("new session id: %w", err)
	}
	s := m.newSession(id, user.ID, episodeID)
	s.MediaPlayer, s.DeviceInfo, s.item = cmp.Or(req.MediaPlayer, "unknown"), device, m.item.Expanded(h.settings.Server().SortingPrefixes)
	mediaID, _ := s.mediaRef()
	saved, found, err := h.progress.load(ctx, user.ID, mediaID)
	if err != nil {
		return err
	}
	if found && !bool(saved.IsFinished) {
		s.StartTime = math.Max(0, db.Val(saved.CurrentTime))
	}
	s.CurrentTime = s.StartTime
	s.PlayMethod = playTranscode
	if req.ForceDirectPlay || (!req.ForceTranscode && allSupported(m.files, req.SupportedMimeTypes)) {
		s.PlayMethod = playDirect
	}
	s.touch(time.Now())
	s.StartedAt = s.UpdatedAt
	if s.PlayMethod == playTranscode {
		if !mediatools.FFmpegAvailable() {
			return httpx.JSONStatus(w, r, http.StatusNotImplemented, map[string]string{"error": "FFmpeg is required for transcoded playback"})
		}
		if s.stream, err = h.startStream(s.ID, m.files, s.Duration, s.StartTime); err != nil {
			return err
		}
	}
	slog.Debug("playback session started", "session", s.ID, "user", user.ID, "item", m.item.ID, "playMethod", s.PlayMethod, "device", device.description())
	h.add(s)
	announce := s.forClient()
	announce.LibraryItem = item.JSONNull
	h.emit.ToAdmins("user_stream_update", onlineUserOf(user, announce))
	return httpx.JSON(w, r, s.forClient())
}

func onlineUserOf(user *auth.Principal, session Session) onlineUser {
	out := onlineUser{ID: user.ID, Username: db.Val(user.Username), Type: db.Val(user.Type), Session: session}
	if !user.LastSeen.IsZero() && user.LastSeen.Millis() != 0 {
		out.LastSeen = db.Ptr(user.LastSeen.Millis())
	}
	if !user.CreatedAt.IsZero() {
		out.CreatedAt = db.Ptr(user.CreatedAt.Millis())
	}
	return out
}

// allSupported reports whether the client can play every file as is.
func allSupported(files []db.AudioFile, supported []string) bool {
	return len(files) > 0 && !slices.ContainsFunc(files, func(f db.AudioFile) bool { return !slices.Contains(supported, f.MimeType) })
}

// openSession is the open session id when user may use it: its owner or an admin.
func (h *Handler) openSession(user *auth.Principal, id string) (session, error) {
	s, found := h.get(id)
	switch {
	case !found:
		return session{}, httpx.NotFound()
	case s.UserID != user.ID && !user.IsAdmin():
		return session{}, httpx.Forbidden()
	}
	return s, nil
}

// getSession is GET /api/session/{id}: the owner or an admin.
func (h *Handler) getSession(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	s, err := h.openSession(user, r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, s.forClient())
}

// openSessions is GET /api/sessions/open: admins only, answered 404 to others as
// upstream does.
func (h *Handler) openSessions(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.IsAdmin() {
		return httpx.NotFound()
	}
	return httpx.JSON(w, r, struct {
		Sessions      []Session `json:"sessions"`
		ShareSessions []Session `json:"shareSessions"`
	}{h.listing(h.snapshot()), []Session{}})
}

// track is /public/session/{id}/track/{index}: a capability URL, no user.
func (h *Handler) track(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return httpx.Error(http.StatusMethodNotAllowed, "")
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		return httpx.BadRequest("")
	}
	s, found := h.get(r.PathValue("id"))
	if !found {
		return httpx.NotFound()
	}
	podcastFirst := s.MediaType == "podcast" && index == 0 // episodes were once addressed as track 0
	if s.PlayMethod == playTranscode {
		if index != 1 && !podcastFirst {
			return httpx.NotFound()
		}
		http.Redirect(w, r, hlsPlaylist(s.ID), http.StatusFound)
		return nil
	}
	i := slices.IndexFunc(s.files, func(f db.AudioFile) bool { return int(f.Index) == index })
	if i < 0 && podcastFirst && len(s.files) > 0 {
		i = 0
	}
	if i < 0 {
		return httpx.NotFound()
	}
	file := s.files[i]
	if file.Metadata.Path == "" {
		return httpx.Internal("", errors.New("audio file has no path"))
	}
	if file.MimeType != "" {
		w.Header().Set("Content-Type", file.MimeType)
	}
	return httpx.ServeFile(w, r, file.Metadata.Path)
}

// syncRequest is the body of sync and close.
type syncRequest struct {
	CurrentTime  *float64 `json:"currentTime"`
	TimeListened *float64 `json:"timeListened"`
	Duration     *float64 `json:"duration"`
}

func (q syncRequest) present() bool {
	return q.CurrentTime != nil || q.TimeListened != nil || q.Duration != nil
}

// sync is POST /api/session/{id}/sync: the owner or an admin.
func (h *Handler) sync(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	s, err := h.openSession(user, r.PathValue("id"))
	if err != nil {
		return err
	}
	req, err := decodeBody[syncRequest](w, r)
	if err != nil {
		return err
	}
	if req.present() {
		if _, err = h.applySync(r.Context(), s.ID, req); err != nil {
			return err
		}
	}
	return ok(w)
}

// closeSession is POST /api/session/{id}/close: a last sync when the body has
// one, otherwise a save of what was listened; then the session is forgotten.
func (h *Handler) closeSession(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	s, err := h.openSession(user, r.PathValue("id"))
	if err != nil {
		return err
	}
	req, err := decodeBody[syncRequest](w, r)
	if err != nil {
		return err
	}
	if req.present() {
		s, err = h.applySync(r.Context(), s.ID, req)
	} else {
		err = h.saveListened(r.Context(), &s)
	}
	if err != nil {
		return err
	}
	h.remove(s.ID)
	h.emit.ToUser(s.UserID, "user_session_closed", s.ID)
	return ok(w)
}

// applySync records the position and listened time on the open session, stores
// it and writes the progress. It returns the session as stored.
func (h *Handler) applySync(ctx context.Context, id string, req syncRequest) (session, error) {
	if req.CurrentTime == nil || *req.CurrentTime < 0 {
		return session{}, httpx.BadRequest("Invalid currentTime")
	}
	if req.TimeListened == nil || *req.TimeListened < 0 {
		return session{}, httpx.BadRequest("Invalid timeListened")
	}
	s, found := h.update(id, func(s *Session) {
		s.CurrentTime = *req.CurrentTime
		s.TimeListening += *req.TimeListened
		s.touch(time.Now())
	})
	if !found {
		return session{}, httpx.NotFound() // closed while this request ran
	}
	// Concurrent syncs of one session may store out of order; the next sync
	// writes the newer totals.
	if err := h.saveListened(ctx, &s); err != nil {
		return session{}, err
	}
	if _, err := h.syncProgress(ctx, &s, db.Val(req.Duration), nil); err != nil {
		return session{}, err
	}
	return s, nil
}

// saveListened stores a session that has listening time; others leave no row.
func (h *Handler) saveListened(ctx context.Context, s *session) error {
	if s.TimeListening <= 0 {
		return nil
	}
	return h.save(ctx, s)
}

func (h *Handler) save(ctx context.Context, s *session) error {
	row, err := s.row()
	if err != nil {
		return err
	}
	return h.upsert(ctx, row)
}

// progressEvent is the payload of user_item_progress_updated: the stored
// progress with the session and device that produced it.
type progressEvent struct {
	ID                string       `json:"id"`
	SessionID         string       `json:"sessionId"`
	DeviceDescription string       `json:"deviceDescription"`
	Data              ProgressView `json:"data"`
}

// syncProgress writes the session's position to the user's progress of its book
// or episode and announces it (PlaybackSessionManager.syncSession). duration, when
// non-zero, replaces the session's. A lastUpdate (offline sessions) skips the
// progress when the stored row is strictly newer, and reports false.
func (h *Handler) syncProgress(ctx context.Context, s *session, duration float64, lastUpdate *int64) (bool, error) {
	mediaID, _ := s.mediaRef()
	if lastUpdate != nil {
		stored, found, err := h.progress.updatedAt(ctx, s.UserID, mediaID)
		if err != nil {
			return false, err
		}
		if found && stored > *lastUpdate {
			return false, nil
		}
	}
	var library db.Library
	err := h.db.Get(ctx, &library, `SELECT * FROM libraries WHERE id = ?`, s.LibraryID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read library %s: %w", s.LibraryID, err)
	}
	settings := library.Settings.V
	update := Update{
		LibraryItemID: s.LibraryItemID, EpisodeID: db.Val(s.EpisodeID),
		Duration: Set(s.Duration), CurrentTime: &s.CurrentTime, Progress: db.Ptr(s.ratio()), LastUpdate: lastUpdate,
		MarkAsFinishedPercentComplete: settings.MarkAsFinishedPercentComplete, MarkAsFinishedTimeRemaining: settings.MarkAsFinishedTimeRemaining,
	}
	if duration != 0 {
		update.Duration = Set(duration)
	}
	progress, err := h.progress.applyTrusted(ctx, s.UserID, update)
	if err != nil {
		return false, err
	}
	h.emit.ToUser(s.UserID, "user_item_progress_updated", progressEvent{ID: progress.ID, SessionID: s.ID, DeviceDescription: s.DeviceInfo.description(), Data: progress})
	return true, nil
}
