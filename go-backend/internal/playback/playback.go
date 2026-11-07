// Package playback implements the pinned server's playback session, direct
// media, byte-range, and HLS routes. It reads and writes only the existing
// Audiobookshelf tables; it never creates or migrates schema.
package playback

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/serversettings"
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	emit     events.Emitter
	mu       sync.RWMutex
	sessions map[string]*session
	localIDs map[string]string
}

// serverVersion is the pinned TS release the session JSON mirrors.
const serverVersion = "2.37.2"

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
	return sessionJSON{session: s, BookID: nullID(s.BookID), EpisodeID: nullID(s.EpisodeID), ServerVersion: serverVersion}
}

func (s *session) client() sessionJSON {
	v := s.view()
	v.AudioTracks, v.LibraryItem = s.tracks(), s.Item
	return v
}

// deviceInfo is the session's device DTO (DeviceInfo.js toJSON): empty values
// are omitted.
type deviceInfo struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	DeviceID       string `json:"deviceId"`
	IPAddress      string `json:"ipAddress"`
	ClientVersion  string `json:"clientVersion"`
	ClientName     string `json:"clientName"`
	DeviceName     string `json:"deviceName,omitempty"`
	BrowserName    string `json:"browserName,omitempty"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	OSName         string `json:"osName,omitempty"`
	OSVersion      string `json:"osVersion,omitempty"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	Model          string `json:"model,omitempty"`
	SDKVersion     string `json:"sdkVersion,omitempty"`
}

// clientDevice is the deviceInfo a client sends.
type clientDevice struct {
	DeviceID      string     `json:"deviceId"`
	ClientVersion string     `json:"clientVersion"`
	ClientName    string     `json:"clientName"`
	Manufacturer  string     `json:"manufacturer"`
	Model         string     `json:"model"`
	SDKVersion    sdkVersion `json:"sdkVersion"`
}

// sdkVersion is a JSON number (the Android app) or string, kept as text
// (DeviceInfo.setData, server/objects/DeviceInfo.js:100).
type sdkVersion string

func (v *sdkVersion) UnmarshalJSON(b []byte) error {
	var n json.Number
	if json.Unmarshal(b, &n) != nil {
		_ = json.Unmarshal(b, (*string)(v))
		return nil
	}
	*v = sdkVersion(n)
	return nil
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

type transcode struct {
	mu            sync.Mutex
	root          string
	audioCodec    string
	cmd           *exec.Cmd
	ctx           context.Context
	cancel        context.CancelFunc
	startTime     float64
	segmentStart  int
	segmentLength int
	complete      bool
	resetting     bool
	done          chan struct{}
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
	if !access.Of(user).Item(item.Access()) {
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
		s.UpdatedAt = nowMS()
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
	sendOK(w)
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
	h.emit(userID, "user_item_progress_updated", wire.ProgressUpdatedEvent{ID: progress.ID, SessionID: s.ID, DeviceDescription: s.Device.description(), Data: progress})
	return true, nil
}

func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	if !access.AdminUser(user) {
		return httputil.Status(http.StatusNotFound, "")
	}
	validSort := map[string]string{"displayTitle": "displayTitle", "duration": "duration", "playMethod": "playMethod", "startTime": "startTime", "currentTime": "currentTime", "timeListening": "timeListening", "updatedAt": "updatedAt", "createdAt": "createdAt"}
	sortKey := r.URL.Query().Get("sort")
	if _, ok := validSort[sortKey]; !ok {
		sortKey = "updatedAt"
	}
	direction := "ASC"
	if r.URL.Query().Get("desc") == "1" {
		direction = "DESC"
	}
	perPage := 10
	if n, e := strconv.Atoi(r.URL.Query().Get("itemsPerPage")); e == nil && n != 0 {
		perPage = n
	}
	if perPage < 1 {
		perPage = 10
	}
	page := 0
	if n, e := strconv.Atoi(r.URL.Query().Get("page")); e == nil {
		page = n
	}
	if page < 0 {
		page = 0
	}
	userFilter := r.URL.Query().Get("user")
	if !validUUID(userFilter) {
		userFilter = ""
	}
	where := ""
	args := []any{}
	if userFilter != "" {
		where = ` WHERE userId=?`
		args = append(args, userFilter)
	}
	total, err := sqlc.New(h.db).CountPlaybackSessionsForUser(r.Context(), store.TextOrNull(userFilter))
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	query := `SELECT ` + store.PlaybackSessionColumns + ` FROM playbackSessions` + where + ` ORDER BY ` + validSort[sortKey] + ` ` + direction + ` LIMIT ? OFFSET ?`
	args = append(args, perPage, perPage*page)
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	sessions := []store.PlaybackSessionRow{}
	for rows.Next() {
		s, err := store.ScanPlaybackSession(rows)
		if err != nil {
			_ = rows.Close()
			return httputil.Internal("Internal Server Error", err)
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return httputil.Internal("Internal Server Error", err)
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	result := []map[string]any{}
	for _, s := range sessions {
		device, err := store.LoadDeviceInfo(r.Context(), h.db, s.DeviceID.String)
		if errors.Is(err, sql.ErrNoRows) {
			device, err = nil, nil
		}
		if err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
		obj := s.LegacyJSON(device)
		if u, err := sqlc.New(h.db).GetUserByID(r.Context(), s.UserID.String); err == nil && u.Username.Valid {
			obj["user"] = map[string]any{"id": s.UserID.String, "username": u.Username.String}
			wire.AddPlaybackSessionSpreadFields(obj)
		}
		result = append(result, obj)
	}
	payload := map[string]any{"total": total, "numPages": int(math.Ceil(float64(total) / float64(perPage))), "page": page, "itemsPerPage": perPage, "sessions": result}
	if userFilter != "" {
		payload["userId"] = userFilter
	}
	httputil.WriteJSON(w, 200, payload)
	return nil
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

func (h *Handler) handleDeleteHistory(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	id := r.PathValue("id")
	if !access.Of(user).Delete() {
		return httputil.Status(403, "")
	}
	exists, err := sqlc.New(h.db).CountPlaybackSessionByID(r.Context(), id)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	if exists == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	h.removeOpenSession(id)
	if err := sqlc.New(h.db).DeletePlaybackSession(r.Context(), id); err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	sendOK(w)
	return nil
}

func (h *Handler) handleBatchDelete(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	if !access.AdminUser(user) {
		return httputil.Status(403, "")
	}
	var body struct {
		Sessions []string `json:"sessions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.Sessions) == 0 {
		return httputil.Status(400, `Invalid request body. "sessions" array of session id strings is required.`)
	}
	for _, id := range body.Sessions {
		if !validUUID(id) {
			return httputil.Status(400, `Invalid request body. "sessions" array of session id strings is required.`)
		}
	}
	for _, id := range body.Sessions {
		h.removeOpenSession(id)
	}
	res, err := sqlc.New(h.db).DeletePlaybackSessionsByIDs(r.Context(), body.Sessions)
	if err != nil {
		return httputil.Internal("Failed to remove sessions", err)
	}
	if removed, rowsErr := res.RowsAffected(); rowsErr == nil {
		logging.Infof("[SessionController] %d playback sessions removed by %q", removed, user.Username) // SessionController.js:238
	}
	sendOK(w)
	return nil
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
		sendOK(w)
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
		updated = nowMS()
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
	s := &session{ID: id, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, EpisodeID: ls.EpisodeID, MediaType: item.MediaType,
		DisplayTitle: ls.DisplayTitle, DisplayAuthor: ls.DisplayAuthor, CoverPath: ls.CoverPath, // coverPath also comes from the client JSON, not the library item
		PlayMethod: playMethod, MediaPlayer: ls.MediaPlayer,
		// PlaybackSessionManager.syncLocalSession (server/managers/PlaybackSessionManager.js:203)
		// always replaces the client duration with the item's playback duration.
		Duration: item.Duration, StartTime: ls.StartTime, CurrentTime: ls.CurrentTime, TimeListening: ls.TimeListening, StartedAt: started, UpdatedAt: updated,
		Date: day, DayOfWeek: weekday, MediaMetadata: overlayMetadata(item.Metadata, ls.MediaMetadata), Chapters: item.Chapters, Device: device, Files: item.Files}
	if ls.EpisodeID == "" {
		s.BookID = item.MediaID
	}
	if err = h.saveSessionRecord(ctx, s); err != nil {
		return fail("Failed to save session")
	}
	synced, err := h.syncProgress(ctx, user.ID, s, 0, &updated)
	if err != nil {
		return fail("Failed to sync progress")
	}
	return localResult{ID: nullID(id), Success: true, ProgressSynced: &synced}
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

func (d deviceInfo) description() string {
	if d.Model != "" {
		if d.SDKVersion != "" {
			return d.Model + " SDK " + d.SDKVersion + " / v" + d.ClientVersion
		}
		return d.Model + " / v" + d.ClientVersion
	}
	return d.OSName + " " + d.OSVersion + " / " + d.BrowserName
}
func (h *Handler) handleHLS(w http.ResponseWriter, r *http.Request) error {
	id, name := r.PathValue("id"), r.PathValue("name")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return httputil.Status(http.StatusMethodNotAllowed, "")
	}
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil || s.stream == nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	t := s.stream
	if filepath.Base(name) != name || name == "." || name == ".." {
		return httputil.Status(400, "")
	}
	ext := filepath.Ext(name)
	if ext != ".ts" && ext != ".m3u8" {
		return httputil.Status(400, "")
	}
	path := filepath.Join(t.root, name)
	rel, err := filepath.Rel(t.root, path)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return httputil.Status(400, "")
	}
	if _, err := os.Stat(path); err != nil {
		if ext == ".ts" {
			if seg, ok := segmentNumber(name); ok {
				if at := t.checkSegment(seg); at >= 0 {
					h.emit("*", "stream_reset", map[string]any{"startTime": at, "streamId": id})
				}
			}
		}
		return httputil.Status(http.StatusNotFound, "")
	}
	contentType := "video/mp2t"
	if ext == ".m3u8" {
		contentType = "application/vnd.apple.mpegurl"
	}
	f, err := os.Open(path)
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, st.ModTime(), f)
	return nil
}

func (h *Handler) startTranscode(s *session, item *itemData, start float64) (*transcode, error) {
	root := filepath.Join(h.cfg.MetadataPath, "streams", s.ID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create HLS stream directory: %w", err)
	}
	concat := filepath.Join(root, "files.txt")
	var b strings.Builder
	for _, f := range item.Files {
		p, _ := f.Metadata["path"].(string)
		if p == "" {
			return nil, errors.New("audio track path is empty")
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("audio track is unavailable: %w", err)
		}
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(p, "'", "'\\''"))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(concat, []byte(b.String()), 0600); err != nil {
		return nil, fmt.Errorf("write FFmpeg concat file: %w", err)
	}
	t := &transcode{root: root, audioCodec: transcodeAudioCodec(item.Files), startTime: start, segmentLength: 6, segmentStart: int(math.Floor(math.Max(start-30, 0) / 6))}
	if err := t.generatePlaylist(s.Duration); err != nil {
		return nil, err
	}
	if err := t.start(concat, s.Duration); err != nil {
		return nil, err
	}
	return t, nil
}

// transcodeAudioCodec mirrors Stream.js: AAC is copied by default and only
// the source's explicit MIME/codec set is re-encoded as AAC.
func transcodeAudioCodec(files []audioFile) string {
	if len(files) == 0 {
		return "copy"
	}
	file := files[0]
	codec := strings.ToLower(file.Codec)
	switch file.MimeType {
	case "audio/flac", "audio/ogg", "audio/x-ms-wma", "audio/x-aiff", "audio/webm", "audio/amr-wb", "audio/x-caf":
		return "aac"
	}
	switch codec {
	case "alac", "ac3", "eac3", "opus":
		return "aac"
	default:
		return "copy"
	}
}

func (t *transcode) generatePlaylist(duration float64) error {
	num := int(math.Floor(duration / 6))
	last := duration - float64(num*6)
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-ALLOW-CACHE:NO\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	for i := 0; i < num; i++ {
		fmt.Fprintf(&b, "#EXTINF:6,\noutput-%d.ts\n", i)
	}
	if last > 0 {
		fmt.Fprintf(&b, "#EXTINF:%v,\noutput-%d.ts\n", last, num)
	}
	b.WriteString("#EXT-X-ENDLIST")
	return os.WriteFile(filepath.Join(t.root, "output.m3u8"), []byte(b.String()), 0600)
}

func (t *transcode) start(concat string, duration float64) error {
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-hide_banner", "-loglevel", "error"}
	adjusted := math.Max(t.startTime-30, 0)
	if adjusted > 0 {
		args = append(args, "-ss", strconv.FormatFloat(math.Round(adjusted*10)/10, 'f', 1, 64)+"s")
	}
	args = append(args, "-f", "concat", "-safe", "0", "-i", concat, "-map", "0:a", "-c:a", t.audioCodec, "-f", "hls", "-copyts", "-avoid_negative_ts", "make_non_negative", "-max_delay", "5000000", "-max_muxing_queue_size", "2048", "-hls_time", "6", "-hls_segment_type", "mpegts", "-start_number", strconv.Itoa(t.segmentStart), "-hls_playlist_type", "vod", "-hls_list_size", "0", "-hls_allow_cache", "0", "-hls_segment_filename", filepath.Join(t.root, "output-%d.ts"), filepath.Join(t.root, "final-output.m3u8"))
	cmd := exec.CommandContext(ctx, ffmpegExecutable(), args...)
	cmd.Stdout = io.Discard
	stderrTail := &tailBuffer{limit: 4096}
	cmd.Stderr = stderrTail
	done := make(chan struct{})
	t.mu.Lock()
	t.ctx = ctx
	t.cancel = cancel
	t.cmd = cmd
	t.done = done
	t.complete = false
	t.resetting = false
	t.mu.Unlock()
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	go func() {
		defer close(done)
		err := cmd.Wait()
		t.mu.Lock()
		t.complete = true
		t.cmd = nil
		t.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			// Stream.js:318-321: a SIGKILL is the intentional stop on
			// session close or seek, logged at INFO, not as a failure.
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
					logging.Infof("[FFMPEG] Transcode Killed")
					return
				}
			}
			// Upstream runs ffmpeg with -loglevel error in production
			// (Stream.js:268) and logs its failure.
			logging.Errorf("[Stream] FFmpeg transcode in %q failed: %v: %s", t.root, err, stderrTail.String())
			return
		}
		if err == nil {
			if promoteErr := t.promoteFinalPlaylist(); promoteErr != nil {
				logging.Errorf("[Stream] Failed to publish the final playlist in %q: %v", t.root, promoteErr)
			}
		}
	}()
	_ = duration
	return nil
}

// tailBuffer keeps the last limit bytes written to it (ffmpeg's stderr).
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

func (t *transcode) promoteFinalPlaylist() error {
	src := filepath.Join(t.root, "final-output.m3u8")
	dst := filepath.Join(t.root, "output.m3u8")
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Keep the upstream public playlist URI stable while letting FFmpeg finalize
	// segment ordering and durations.
	return os.WriteFile(dst, b, 0600)
}

// checkSegment returns a nonnegative reset seek time or -1 when no reset is due.
func (t *transcode) checkSegment(seg int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resetting || t.complete {
		return -1
	}
	start := seg * 6
	reset := t.segmentStart > seg
	if !reset {
		return -1
	}
	seek := math.Max(float64(start-30), 0)
	t.resetting = true
	oldCancel := t.cancel
	root := t.root
	concat := filepath.Join(root, "files.txt")
	if oldCancel != nil {
		oldCancel()
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.Name() != "files.txt" {
				_ = os.Remove(filepath.Join(root, e.Name()))
			}
		}
		t.startTime = seek
		t.segmentStart = int(math.Floor(seek / 6))
		_ = t.generatePlaylist(float64(math.Max(float64(seg*6+6), 6)))
		_ = t.start(concat, 0)
	}()
	return float64(start)
}

func (t *transcode) close() error {
	t.mu.Lock()
	cancel := t.cancel
	done := t.done
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return os.RemoveAll(t.root)
}

func hlsPlaylist(sessionID string) string { return "/hls/" + sessionID + "/output.m3u8" }

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
	f.MimeType = mimeForFormat(format)
	f.Duration = max(f.Duration, 0)
	return f, nil
}

func (h *Handler) upsertDevice(ctx context.Context, user store.User, r *http.Request, client clientDevice) (deviceInfo, error) {
	browser, browserVersion, osName, osVersion := deviceinfo.ParseUserAgent(r.UserAgent())
	clientVersion := cmp.Or(client.ClientVersion, serverVersion)
	manufacturer, model, sdk := client.Manufacturer, client.Model, string(client.SDKVersion)
	clientName := client.ClientName
	if clientName == "" {
		switch {
		case sdk != "":
			clientName = "Abs Android"
		case model != "":
			clientName = "Abs iOS"
		case browser != "" && osName != "":
			clientName = "Abs Web"
		default:
			clientName = "Unknown"
		}
	}
	deviceName := ""
	// DeviceInfo.js:109/112: `${manufacturer || 'Unknown'} ${model || ''}`, untrimmed.
	switch {
	case sdk != "", model != "":
		deviceName = cmp.Or(manufacturer, "Unknown") + " " + model
	case browser != "":
		deviceName = osName + " " + cmp.Or(osVersion, "N/A") + " " + browser
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	dbID, err := ids.New()
	if err != nil {
		return deviceInfo{}, err
	}
	deviceKey := cmp.Or(client.DeviceID, dbID)
	var existing string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? AND userId=? LIMIT 1`, deviceKey, user.ID).Scan(&existing)
	if err == nil {
		dbID = existing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return deviceInfo{}, err
	}
	extraRaw, _ := json.Marshal(struct {
		Manufacturer string `json:"manufacturer,omitempty"`
		Model        string `json:"model,omitempty"`
		OSName       string `json:"osName,omitempty"`
		OSVersion    string `json:"osVersion,omitempty"`
		BrowserName  string `json:"browserName,omitempty"`
	}{manufacturer, model, osName, osVersion, browser})
	version := cmp.Or(sdk, browserVersion)
	now := dbtime.Now()
	if existing == "" {
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, dbID, deviceKey, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), user.ID, now, now)
	} else {
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), now, dbID)
	}
	if err != nil {
		return deviceInfo{}, err
	}
	// The observed v2.37.2 playback-session device DTO omits deviceName,
	// manufacturer, model and sdkVersion entirely when no value exists
	// (PlaybackSession.js:86-104 / DeviceInfo.js:43-67 fixture behavior).
	return deviceInfo{ID: dbID, UserID: user.ID, DeviceID: deviceKey, IPAddress: ip, ClientVersion: clientVersion, ClientName: clientName, DeviceName: deviceName,
		BrowserName: browser, BrowserVersion: browserVersion, OSName: osName, OSVersion: osVersion, Manufacturer: manufacturer, Model: model, SDKVersion: sdk}, nil
}

func serveMedia(w http.ResponseWriter, r *http.Request, path, mime string) error {
	if mime == "" {
		mime = mimeForFormat(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."))
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

func mimeForFormat(ext string) string {
	switch strings.ToLower(ext) {
	case "mp3", "mpeg", "mpg":
		return "audio/mpeg"
	case "m4b", "m4a", "mp4":
		return "audio/mp4"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wma":
		return "audio/x-ms-wma"
	case "aiff", "aif":
		return "audio/x-aiff"
	case "webm", "webma":
		return "audio/webm"
	case "mka":
		return "audio/x-matroska"
	case "awb":
		return "audio/amr-wb"
	case "caf":
		return "audio/x-caf"
	}
	return "audio/mpeg"
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
func nowMS() int64 { return time.Now().UnixMilli() }
func segmentNumber(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "-")
	if len(parts) != 2 {
		return 0, false
	}
	n, e := strconv.Atoi(parts[1])
	return n, e == nil && n >= 0
}
func sendOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", "2")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}
func ffmpegExecutable() string {
	if p := strings.TrimSpace(os.Getenv("FFMPEG_PATH")); p != "" {
		return p
	}
	return "ffmpeg"
}
