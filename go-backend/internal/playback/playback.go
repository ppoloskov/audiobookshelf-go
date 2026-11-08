// Package playback implements the pinned server's playback session, direct
// media, byte-range, and HLS routes. It reads and writes only the existing
// Audiobookshelf tables; it never creates or migrates schema.
package playback

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/version"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	emit     events.Emitter
	mu       sync.RWMutex
	sessions map[string]*session
	localIDs map[string]string
}

// session is a playback session as TS PlaybackSession.toJSON writes it (GET
// /api/sessions/open lists these, the client's sessions page reads them).
// sessionJSON adds the fields that are null or computed.
type session struct {
	ID            string           `json:"id"`
	UserID        string           `json:"userId"`
	LibraryID     string           `json:"libraryId"`
	LibraryItemID string           `json:"libraryItemId"`
	BookID        string           `json:"-"`
	EpisodeID     string           `json:"-"`
	MediaType     string           `json:"mediaType"`
	MediaMetadata any              `json:"mediaMetadata"` // libitem.BookMetadataFull or PodcastMetadata
	Chapters      []map[string]any `json:"chapters"`
	DisplayTitle  string           `json:"displayTitle"`
	DisplayAuthor string           `json:"displayAuthor"`
	CoverPath     *string          `json:"coverPath"`
	Duration      float64          `json:"duration"`
	PlayMethod    int              `json:"playMethod"`
	MediaPlayer   string           `json:"mediaPlayer"`
	Device        deviceInfo       `json:"deviceInfo"`
	Date          string           `json:"date"`
	DayOfWeek     string           `json:"dayOfWeek"`
	TimeListening float64          `json:"timeListening"`
	StartTime     float64          `json:"startTime"`
	CurrentTime   float64          `json:"currentTime"`
	StartedAt     int64            `json:"startedAt"`
	UpdatedAt     int64            `json:"updatedAt"`

	Item   libitem.Expanded `json:"-"`
	Files  []audioFile      `json:"-"`
	stream *transcode
}

// nullID marshals "" as null (bookId and episodeId of the other media type).
type nullID string

func (n nullID) MarshalJSON() ([]byte, error) {
	if n == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(n))
}

// sessionJSON is a session as a client reads it. Its own fields shadow the
// session's; the open-sessions listing leaves the tracks and item out.
type sessionJSON struct {
	*session
	BookID        nullID           `json:"bookId"`
	EpisodeID     nullID           `json:"episodeId"`
	ServerVersion string           `json:"serverVersion"`
	AudioTracks   []map[string]any `json:"audioTracks,omitempty"`
	LibraryItem   any              `json:"libraryItem,omitempty"`
	User          *struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user,omitempty"`
}

func (s *session) view() sessionJSON {
	return sessionJSON{session: s, BookID: nullID(s.BookID), EpisodeID: nullID(s.EpisodeID), ServerVersion: version.Server}
}

func (s *session) client() sessionJSON {
	v := s.view()
	v.AudioTracks, v.LibraryItem = s.tracks(), s.Item
	return v
}

type itemData struct {
	libitem.Item
	Title, Author string
	CoverPath     *string
	Duration      float64
	Metadata      any // libitem.BookMetadataFull or PodcastMetadata
	Chapters      []map[string]any
	Files         []audioFile
}

// audioFile is the part of a scanner-written audio file the playback code
// reads; Fields is the whole object, passed through to the client's tracks.
type audioFile struct {
	Index    *int           `json:"index"`
	Ino      string         `json:"ino"`
	Metadata map[string]any `json:"metadata"`
	Duration float64        `json:"duration"`
	Codec    string         `json:"codec"`
	Exclude  bool           `json:"exclude"`
	MimeType string         `json:"-"`
	Fields   map[string]any `json:"-"`
}

// New uses the existing database and configuration. It doesn't initialize a
// playbackSessions table or any other schema.
func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any)) *Handler {
	if emit == nil {
		emit = func(string, string, any) {}
	}
	return &Handler{db: db, cfg: cfg, emit: emit, sessions: map[string]*session{}, localIDs: map[string]string{}}
}

// Close stops every in-memory session and waits briefly for FFmpeg children.
func (h *Handler) Close() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = map[string]*session{}
	h.mu.Unlock()
	for _, s := range sessions {
		if s.stream != nil {
			_ = s.stream.close()
		}
	}
}

// Routes registers the playback routes. Public audio and HLS resources are
// bearer-free upstream capability URLs and run with no user; API routes carry
// the authenticated user in the request context. The public routes accept any
// method so a wrong one is answered 405 as before.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("/public/session/{id}/track/{index}", httputil.Handler(h.handleTrack))
	mux.Handle("/hls/{id}/{name}", httputil.Handler(h.handleHLS))
	mux.Handle("POST /api/items/{id}/play", httputil.Handler(h.handleStart))
	mux.Handle("POST /api/items/{id}/play/{episodeId}", httputil.Handler(h.handleStart))
	mux.Handle("GET /api/sessions", httputil.Handler(h.handleHistory))
	mux.Handle("GET /api/sessions/open", httputil.Handler(h.handleOpenSessions))
	mux.Handle("POST /api/sessions/batch/delete", httputil.Handler(h.handleBatchDelete))
	mux.Handle("DELETE /api/sessions/{id}", httputil.Handler(h.handleDeleteHistory))
	mux.Handle("POST /api/session/local", httputil.Handler(h.handleLocalSync))
	mux.Handle("POST /api/session/local-all", httputil.Handler(h.handleLocalSync))
	mux.Handle("GET /api/session/{id}", httputil.Handler(h.handleGetSession))
	mux.Handle("POST /api/session/{id}/sync", httputil.Handler(h.handleSync))
	mux.Handle("POST /api/session/{id}/close", httputil.Handler(h.handleSync))
}

func (h *Handler) handleStart(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	itemID, episodeID := r.PathValue("id"), r.PathValue("episodeId")
	item, err := h.loadItem(r.Context(), itemID, episodeID)
	if errors.Is(err, sql.ErrNoRows) {
		// LibraryItemController.middleware / startEpisodePlaybackSession
		// (LibraryItemController.js:460-470): res.sendStatus(404).
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	if !access.For(r.Context(), user).Item(item.Access()) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if len(item.Files) == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	var body struct {
		DeviceInfo         clientDevice `json:"deviceInfo"`
		MediaPlayer        string       `json:"mediaPlayer"`
		SupportedMimeTypes []string     `json:"supportedMimeTypes"`
		ForceDirectPlay    bool         `json:"forceDirectPlay"`
		ForceTranscode     bool         `json:"forceTranscode"`
	}
	if err := readBody(r, &body); err != nil {
		return err
	}
	device, err := h.upsertDevice(r.Context(), user, r, body.DeviceInfo)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	if body.MediaPlayer == "" {
		body.MediaPlayer = "unknown"
	}
	progress, err := h.readProgress(r.Context(), user.ID, item, episodeID)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	start := 0.0
	if progress != nil && !progress.finished {
		start = math.Max(0, progress.currentTime)
	}
	playMethod := 2
	if body.ForceDirectPlay || (!body.ForceTranscode && allSupported(item.Files, body.SupportedMimeTypes)) {
		playMethod = 0
	}
	// PlaybackSession.toJSONForClient embeds libraryItem.toOldJSONExpanded()
	// (server/objects/PlaybackSession.js:86-116).
	itemJSON := item.Item.Expanded(serversettings.Load(r.Context(), h.db).SortingPrefixes())
	sessionID, err := ids.New()
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	now := time.Now()
	s := &session{ID: sessionID, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, EpisodeID: episodeID, MediaType: item.MediaType, DisplayTitle: item.Title, DisplayAuthor: item.Author, CoverPath: item.CoverPath, PlayMethod: playMethod, MediaPlayer: body.MediaPlayer, Duration: item.Duration, StartTime: start, CurrentTime: start, StartedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), Date: now.Format("2006-01-02"), DayOfWeek: now.Weekday().String(), MediaMetadata: item.Metadata, Chapters: item.Chapters, Item: itemJSON, Device: device, Files: item.Files}
	if item.MediaType == "book" {
		s.BookID = item.MediaID
	}
	if playMethod == 2 {
		if _, err := exec.LookPath(ffmpegExecutable()); err != nil {
			httputil.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "FFmpeg is required for transcoded playback"})
			return nil
		}
		t, err := h.startTranscode(s, item, start)
		if err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
		s.stream = t
	}
	if playMethod == 0 {
		logging.Debugf("[PlaybackSessionManager] %q starting direct play session for item %q with id %s (Device: %s)", user.Username, item.ID, s.ID, device.description()) // PlaybackSessionManager.js:340
	} else {
		logging.Debugf("[PlaybackSessionManager] %q starting stream session for item %q (Device: %s)", user.Username, item.ID, device.description()) // PlaybackSessionManager.js:344
	}
	h.mu.Lock()
	h.sessions[s.ID] = s
	h.mu.Unlock()
	h.emit("@admins", "user_stream_update", h.publicUser(user, s))
	httputil.WriteJSON(w, http.StatusOK, s.client())
	return nil
}

func (h *Handler) loadItem(ctx context.Context, id, episodeID string) (*itemData, error) {
	it, err := libitem.LoadOne(ctx, h.db, id, libitem.Options{Episodes: true})
	if err != nil {
		return nil, err
	}
	if it.IsMissing || it.IsInvalid {
		return nil, sql.ErrNoRows
	}
	out := &itemData{Item: it, Files: []audioFile{}, Chapters: []map[string]any{}}
	media := it.Full().Media
	if b := it.Book; b != nil {
		out.Title, out.Author, out.CoverPath, out.Chapters, out.Metadata = deref(b.Title), b.AuthorName(), b.CoverPath, b.Chapters, media.(libitem.BookFull).Metadata
		if b.Duration != nil {
			out.Duration = *b.Duration
		}
		for _, raw := range b.AudioFiles {
			f, err := parseAudioFile(raw)
			if err != nil {
				return nil, err
			}
			if !f.Exclude {
				if f.Index == nil {
					n := len(out.Files)
					f.Index = &n
				}
				out.Files = append(out.Files, f)
			}
		}
	} else {
		p := it.Podcast
		out.Title, out.Author, out.CoverPath, out.Metadata = deref(p.Title), deref(p.Author), p.CoverPath, media.(libitem.PodcastMedia).Metadata
	}
	if episodeID != "" {
		if it.Podcast == nil {
			return nil, sql.ErrNoRows
		}
		i := slices.IndexFunc(it.Podcast.Episodes, func(e libitem.Episode) bool { return e.ID == episodeID })
		if i < 0 {
			return nil, sql.ErrNoRows
		}
		ep := it.Podcast.Episodes[i]
		f, err := parseAudioFile(ep.AudioFile)
		if err != nil {
			return nil, sql.ErrNoRows
		}
		one := 1
		f.Index = &one
		out.Files, out.Duration, out.Chapters, out.Title = []audioFile{f}, f.Duration, ep.Chapters, deref(ep.Title)
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type progress struct {
	currentTime float64
	finished    bool
}

func (h *Handler) readProgress(ctx context.Context, userID string, item *itemData, episodeID string) (*progress, error) {
	mediaID, kind := item.MediaID, "book"
	if episodeID != "" {
		mediaID, kind = episodeID, "podcastEpisode"
	}
	pos, err := sqlc.New(h.db).GetMediaProgressPosition(ctx, sqlc.GetMediaProgressPositionParams{UserID: store.Text(userID), MediaItemID: store.Text(mediaID), MediaItemType: store.Text(kind)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &progress{currentTime: pos.Currenttime.Float64, finished: pos.Isfinished.Int64 != 0}, nil
}

func (h *Handler) handleTrack(w http.ResponseWriter, r *http.Request) error {
	id, indexText := r.PathValue("id"), r.PathValue("index")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return httputil.Status(http.StatusMethodNotAllowed, "")
	}
	index, err := strconv.Atoi(indexText)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, "")
	}
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	isPodcastFirst := s.MediaType == "podcast" && index == 0 // episodes were once addressed as track 0
	if s.PlayMethod == 2 {
		// The only track of a transcoded session is the HLS playlist.
		if index != 1 && !isPodcastFirst {
			return httputil.Status(http.StatusNotFound, "")
		}
		http.Redirect(w, r, hlsPlaylist(s.ID), http.StatusFound)
		return nil
	}
	i := slices.IndexFunc(s.Files, func(f audioFile) bool { return f.Index != nil && *f.Index == index })
	if i < 0 && isPodcastFirst && len(s.Files) > 0 {
		i = 0
	}
	if i < 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	f := &s.Files[i]
	path, _ := f.Metadata["path"].(string)
	if path == "" {
		logging.RequestDebug(r, "responding 500: audio file has no path")
		return httputil.Status(http.StatusInternalServerError, "")
	}
	return serveMedia(w, r, path, f.MimeType)
}

func (h *Handler) handleGetSession(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	id := r.PathValue("id")
	s, err := h.openSession(user, id)
	if err != nil {
		return err
	}
	httputil.WriteJSON(w, 200, s.client())
	return nil
}

func (h *Handler) openSession(user store.User, id string) (*session, error) {
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil {
		return nil, httputil.Status(http.StatusNotFound, "")
	}
	if s.UserID != user.ID && !access.AdminUser(user) {
		return nil, httputil.Status(http.StatusForbidden, "")
	}
	return s, nil
}

func (h *Handler) handleSync(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	id := r.PathValue("id")
	closeSession := strings.HasSuffix(r.URL.Path, "/close")
	s, err := h.openSession(user, id)
	if err != nil {
		return err
	}
	var body struct {
		CurrentTime  *float64 `json:"currentTime"`
		TimeListened *float64 `json:"timeListened"`
		Duration     *float64 `json:"duration"`
	}
	if err := readBody(r, &body); err != nil {
		return err
	}
	synced := body.CurrentTime != nil || body.TimeListened != nil || body.Duration != nil
	if synced {
		if body.CurrentTime == nil || *body.CurrentTime < 0 {
			return httputil.Status(400, "Invalid currentTime")
		}
		if body.TimeListened == nil || *body.TimeListened < 0 {
			return httputil.Status(400, "Invalid timeListened")
		}
		s.CurrentTime = *body.CurrentTime
		s.TimeListening += *body.TimeListened
		logging.Debugf("[PlaybackSessionManager] syncSession %q (Device: %s) | Total Time Listened: %v", s.ID, s.Device.description(), s.TimeListening) // PlaybackSessionManager.js:389
		s.UpdatedAt = time.Now().UnixMilli()
		s.Date = time.UnixMilli(s.UpdatedAt).Format("2006-01-02")
		s.DayOfWeek = time.UnixMilli(s.UpdatedAt).Weekday().String()
		if err := h.saveSession(r.Context(), s); err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
		var duration float64
		if body.Duration != nil {
			duration = *body.Duration
		}
		if _, err := h.syncProgress(r.Context(), user.ID, s, duration, nil); err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
	}
	if closeSession {
		if !synced {
			if err := h.saveSession(r.Context(), s); err != nil {
				return httputil.Internal("Internal Server Error", err)
			}
		}
		logging.Debugf("[PlaybackSessionManager] closeSession %q", s.ID) // PlaybackSessionManager.js:427
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		if s.stream != nil {
			if closeErr := s.stream.close(); closeErr != nil {
				logging.Errorf("[PlaybackSessionManager] Failed to close stream for session %q: %v", s.ID, closeErr)
			}
		}
		h.emit(user.ID, "user_session_closed", s.ID)
	}
	httputil.OK(w, r)
	return nil
}

func (h *Handler) saveSession(ctx context.Context, s *session) error {
	if s.TimeListening <= 0 {
		return nil
	}
	return h.saveSessionRecord(ctx, s)
}

func (h *Handler) saveSessionRecord(ctx context.Context, s *session) error {
	mediaID, kind := s.BookID, "book"
	if s.EpisodeID != "" {
		mediaID, kind = s.EpisodeID, "podcastEpisode"
	}
	// Book.oldMetadataToJSON / Podcast.oldMetadataToJSON: TS reads these keys
	// back with JSON.parse, so a missing value is an explicit null.
	metadata, err := json.Marshal(s.MediaMetadata)
	if err != nil {
		return err
	}
	extra, _ := json.Marshal(map[string]any{"libraryItemId": s.LibraryItemID})
	now := dbtime.Format(time.UnixMilli(s.UpdatedAt))
	created := dbtime.Format(time.UnixMilli(s.StartedAt))
	err = sqlc.New(h.db).UpsertPlaybackSession(ctx, sqlc.UpsertPlaybackSessionParams{
		ID: s.ID, MediaItemID: store.Text(mediaID), MediaItemType: store.Text(kind),
		DisplayTitle: store.Text(s.DisplayTitle), DisplayAuthor: store.Text(s.DisplayAuthor),
		Duration: store.Float(s.Duration), PlayMethod: store.Float(float64(s.PlayMethod)), MediaPlayer: store.Text(s.MediaPlayer),
		StartTime: store.Float(s.StartTime), CurrentPosition: store.Float(s.CurrentTime), CoverPath: store.TextPtr(s.CoverPath),
		TimeListening: store.Float(s.TimeListening), MediaMetadata: store.Text(string(metadata)),
		Date: store.Text(s.Date), DayOfWeek: store.Text(s.DayOfWeek), ExtraData: store.Text(string(extra)),
		UserID: store.Text(s.UserID), DeviceID: store.TextOrNull(s.Device.ID), LibraryID: store.Text(s.LibraryID),
		CreatedAt: created, UpdatedAt: now,
	})
	return err
}

// sessionProgress is PlaybackSession.progress (server/objects/PlaybackSession.js:176).
func sessionProgress(s *session) float64 {
	if s.Duration == 0 {
		return 0
	}
	return math.Max(0, math.Min(s.CurrentTime/s.Duration, 1))
}

// libraryFinishSettings reads the library's markAsFinished* settings, passed
// on to the progress update (PlaybackSessionManager.js:398).
func (h *Handler) libraryFinishSettings(ctx context.Context, libraryID string) (percent, remaining progressdto.Optional) {
	var raw []byte
	_ = h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, libraryID).Scan(&raw)
	var settings struct {
		Percent   progressdto.Optional `json:"markAsFinishedPercentComplete"`
		Remaining progressdto.Optional `json:"markAsFinishedTimeRemaining"`
	}
	_ = json.Unmarshal(raw, &settings)
	return settings.Percent, settings.Remaining
}

// syncProgress writes the session's position to the user's media progress and
// announces it (PlaybackSessionManager.syncSession, :373, and the progress
// half of syncLocalSession, :232-285). duration is the sync body's duration,
// 0 when absent; lastUpdate is set for offline sessions, whose progress is
// skipped when the stored row is newer than the session.
func (h *Handler) syncProgress(ctx context.Context, userID string, s *session, duration float64, lastUpdate *int64) (bool, error) {
	mediaID := s.BookID
	if s.EpisodeID != "" {
		mediaID = s.EpisodeID
	}
	if lastUpdate != nil {
		prog, err := sqlc.New(h.db).GetMediaProgressByUserItem(ctx, sqlc.GetMediaProgressByUserItemParams{UserID: store.Text(userID), MediaItemID: store.Text(mediaID)})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err == nil {
			if stored, ok := dbtime.Millis(prog.Updatedat).(int64); ok && stored > *lastUpdate {
				return false, nil
			}
		}
	}
	payload := progressdto.Payload{
		LibraryItemID: s.LibraryItemID, EpisodeID: s.EpisodeID,
		Duration:    progressdto.Value(s.Duration),
		CurrentTime: progressdto.Value(s.CurrentTime),
		Progress:    progressdto.Value(sessionProgress(s)),
	}
	if duration != 0 {
		// syncData.duration || session.duration
		payload.Duration = progressdto.Value(duration)
	}
	if lastUpdate != nil {
		payload.LastUpdate = progressdto.Value(*lastUpdate)
	}
	payload.MarkAsFinishedPercentComplete, payload.MarkAsFinishedTimeRemaining = h.libraryFinishSettings(ctx, s.LibraryID)
	progress, err := progressdto.Apply(ctx, h.db, userID, payload)
	if err != nil {
		return false, err
	}
	h.emit(userID, "user_item_progress_updated", ProgressUpdatedEvent{ID: progress.ID, SessionID: s.ID, DeviceDescription: s.Device.description(), Data: progress})
	return true, nil
}

func (h *Handler) handleOpenSessions(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	if !access.AdminUser(user) {
		return httputil.Status(http.StatusNotFound, "")
	}
	httputil.WriteJSON(w, 200, map[string]any{"sessions": h.openSessions(), "shareSessions": []any{}})
	return nil
}

func (h *Handler) openSessions() []sessionJSON {
	h.mu.RLock()
	out := make([]sessionJSON, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s.view())
	}
	h.mu.RUnlock()
	for i := range out {
		u, _ := sqlc.New(h.db).GetUserByID(context.Background(), out[i].UserID)
		out[i].User = &struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		}{out[i].UserID, u.Username.String}
	}
	return out
}

// OpenSessions returns the current in-memory playback sessions in the same
// shape used by GET /api/sessions/open. Runtime APIs use it to include active
// sessions in their online-users response.
func (h *Handler) OpenSessions() []map[string]any {
	raw, _ := json.Marshal(h.openSessions())
	out := []map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (h *Handler) removeOpenSession(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil && s.stream != nil {
		_ = s.stream.close()
	}
}

func (h *Handler) publicUser(user store.User, s *session) map[string]any {
	client := s.client()
	client.LibraryItem = json.RawMessage("null")
	return map[string]any{"id": user.ID, "username": user.Username, "type": user.Type, "session": client, "lastSeen": user.LastSeen, "createdAt": user.CreatedAt}
}

func validUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// tracks are the session's audio tracks as the client reads them: each file's
// scanner JSON with its position, title and URL. A transcoded session has the
// one HLS playlist.
func (s *session) tracks() []map[string]any {
	if s.PlayMethod == 2 {
		return []map[string]any{{"index": 1, "startOffset": 0, "duration": s.Duration, "title": s.DisplayTitle, "contentUrl": hlsPlaylist(s.ID), "mimeType": "application/vnd.apple.mpegurl", "codec": nil, "metadata": nil}}
	}
	out := make([]map[string]any, 0, len(s.Files))
	offset := 0.0
	for _, f := range s.Files {
		track := maps.Clone(f.Fields)
		track["index"], track["ino"], track["metadata"], track["duration"], track["mimeType"] = f.Index, f.Ino, f.Metadata, f.Duration, f.MimeType
		track["startOffset"], track["title"], track["contentUrl"] = offset, f.Metadata["filename"], "/api/items/"+s.LibraryItemID+"/file/"+f.Ino
		out = append(out, track)
		offset += f.Duration
	}
	return out
}

// unmarshalLoose decodes JSON into v, ignoring values of the wrong type
// (they leave the field zero) but not malformed JSON or a non-object.
func unmarshalLoose(data []byte, v any) error {
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal(data, v); err != nil && !(errors.As(err, &typeErr) && typeErr.Field != "") {
		return err
	}
	return nil
}

// readBody decodes a request body of at most 10 MiB; an empty body leaves v as is.
func readBody(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 10<<20+1))
	if err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	if len(data) > 10<<20 {
		return httputil.Status(http.StatusRequestEntityTooLarge, "Request body too large")
	}
	if len(bytes.TrimSpace(data)) > 0 && unmarshalLoose(data, v) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	return nil
}

func parseAudioFile(raw map[string]any) (audioFile, error) {
	b, _ := json.Marshal(raw)
	var f audioFile
	if err := unmarshalLoose(b, &f); err != nil {
		return f, err
	}
	f.Fields = raw
	if f.Metadata == nil {
		return f, errors.New("audio file has no metadata")
	}
	ext, _ := f.Metadata["ext"].(string)
	format := strings.TrimPrefix(strings.ToLower(ext), ".")
	if format == "" {
		format, _ = f.Metadata["format"].(string)
	}
	f.MimeType = filekind.AudioMime("." + format)
	f.Duration = max(f.Duration, 0)
	return f, nil
}

func serveMedia(w http.ResponseWriter, r *http.Request, path, mime string) error {
	if mime == "" {
		mime = filekind.AudioMime(filepath.Ext(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return httputil.Status(http.StatusNotFound, "")
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
	return nil
}

func allSupported(files []audioFile, supported []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !slices.Contains(supported, f.MimeType) {
			return false
		}
	}
	return true
}
