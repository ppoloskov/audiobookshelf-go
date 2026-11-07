// Package playback implements the pinned server's playback session, direct
// media, byte-range, and HLS routes. It reads and writes only the existing
// Audiobookshelf tables; it never creates or migrates schema.
package playback

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type session struct {
	ID, UserID, LibraryID, LibraryItemID, BookID, EpisodeID string
	MediaType, DisplayTitle, DisplayAuthor                  string
	CoverPath                                               any
	DeviceID                                                string
	PlayMethod                                              int
	MediaPlayer                                             string
	Duration, StartTime, CurrentTime                        float64
	TimeListening                                           float64
	StartedAt, UpdatedAt                                    int64
	Date, DayOfWeek                                         string
	MediaMetadata                                           map[string]any
	Chapters                                                []map[string]any
	AudioTracks                                             []map[string]any
	Item                                                    any
	Device                                                  map[string]any
	Files                                                   []audioFile
	stream                                                  *transcode
}

type itemData struct {
	libitem.Item
	Title, Author string
	CoverPath     any
	Duration      float64
	Metadata      map[string]any
	Chapters      []map[string]any
	Files         []audioFile
	Episodes      []episodeData
}

type audioFile struct {
	Index    any            `json:"index"`
	Ino      string         `json:"ino"`
	Metadata map[string]any `json:"metadata"`
	Duration float64        `json:"duration"`
	BitRate  any            `json:"bitRate"`
	Channels any            `json:"channels"`
	Codec    any            `json:"codec"`
	Format   any            `json:"format"`
	MimeType string         `json:"mimeType"`
	Exclude  bool           `json:"exclude"`
	Fields   map[string]any `json:"-"`
}

type episodeData struct {
	ID, Title string
	Audio     audioFile
	Duration  float64
	Chapters  []map[string]any
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
	if !canAccess(user, item) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if len(item.Files) == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	var body map[string]any
	if r.Body != nil {
		data, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
		if readErr != nil {
			return httputil.Status(400, "Invalid request body")
		}
		if len(data) > 1<<20 {
			return httputil.Status(http.StatusRequestEntityTooLarge, "Request body too large")
		}
		if len(strings.TrimSpace(string(data))) > 0 && json.Unmarshal(data, &body) != nil {
			return httputil.Status(400, "Invalid request body")
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	device, _ := body["deviceInfo"].(map[string]any)
	if device == nil {
		device = map[string]any{}
	}
	deviceID, device, err := h.upsertDevice(r.Context(), user, r, cleanDevice(device))
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	mediaPlayer, _ := body["mediaPlayer"].(string)
	if mediaPlayer == "" {
		mediaPlayer = "unknown"
	}
	supported := stringSlice(body["supportedMimeTypes"])
	forceDirect, _ := body["forceDirectPlay"].(bool)
	forceTranscode, _ := body["forceTranscode"].(bool)
	progress, err := h.readProgress(r.Context(), user.ID, item, episodeID)
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	start := 0.0
	if progress != nil && !progress.finished {
		start = progress.currentTime
	}
	start = math.Max(0, start)
	playMethod := 0
	if forceDirect || (!forceTranscode && len(supported) > 0 && allSupported(item.Files, supported)) {
		playMethod = 0
	} else {
		playMethod = 2
	}
	// PlaybackSession.toJSONForClient embeds libraryItem.toOldJSONExpanded()
	// (server/objects/PlaybackSession.js:86-116).
	itemJSON := item.Item.Expanded(libitem.LoadSortingPrefixes(r.Context(), h.db))
	sessionID, err := ids.New()
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	s := &session{ID: sessionID, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, MediaType: item.MediaType, DisplayTitle: item.Title, DisplayAuthor: item.Author, CoverPath: item.CoverPath, DeviceID: deviceID, PlayMethod: playMethod, MediaPlayer: mediaPlayer, Duration: item.Duration, StartTime: start, CurrentTime: start, TimeListening: 0, StartedAt: nowMS(), UpdatedAt: nowMS(), Date: time.Now().Format("2006-01-02"), DayOfWeek: time.Now().Weekday().String(), MediaMetadata: item.Metadata, Chapters: item.Chapters, Item: itemJSON, Device: device, Files: item.Files}
	if episodeID != "" {
		s.EpisodeID = episodeID
		for _, ep := range item.Episodes {
			if ep.ID == episodeID {
				s.Duration = ep.Duration
				s.Chapters = ep.Chapters
				break
			}
		}
	}
	if item.MediaType == "book" {
		s.BookID = item.MediaID
	}
	if item.MediaType == "podcast" && s.EpisodeID != "" {
		s.BookID = ""
	}
	if playMethod == 0 {
		s.AudioTracks = makeDirectTracks(s, item)
	} else {
		if _, err := exec.LookPath(ffmpegExecutable()); err != nil {
			httputil.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "FFmpeg is required for transcoded playback"})
			return nil
		}
		t, err := h.startTranscode(s, item, start)
		if err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
		s.stream = t
		s.AudioTracks = []map[string]any{{"index": 1, "startOffset": 0, "duration": s.Duration, "title": s.DisplayTitle, "contentUrl": "/hls/" + s.ID + "/output.m3u8", "mimeType": "application/vnd.apple.mpegurl", "codec": nil, "metadata": nil}}
	}
	if playMethod == 0 {
		logging.Debugf("[PlaybackSessionManager] %q starting direct play session for item %q with id %s (Device: %s)", user.Username, item.ID, s.ID, deviceDescription(device)) // PlaybackSessionManager.js:340
	} else {
		logging.Debugf("[PlaybackSessionManager] %q starting stream session for item %q (Device: %s)", user.Username, item.ID, deviceDescription(device)) // PlaybackSessionManager.js:344
	}
	h.mu.Lock()
	h.sessions[s.ID] = s
	h.mu.Unlock()
	h.emit("@admins", "user_stream_update", h.publicUser(user, s))
	httputil.WriteJSON(w, http.StatusOK, s.clientJSON())
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
	var metadata any
	if b := it.Book; b != nil {
		out.Title, out.Author, out.CoverPath, out.Chapters = deref(b.Title), b.AuthorName(), coverAny(b.CoverPath), b.Chapters
		if b.Duration != nil {
			out.Duration = *b.Duration
		}
		for _, raw := range b.AudioFiles {
			f, err := parseAudioFile(raw)
			if err != nil {
				return nil, err
			}
			if !f.Exclude {
				f.Index = numberOr(f.Index, len(out.Files))
				out.Files = append(out.Files, f)
			}
		}
		metadata = it.Full().Media.(libitem.BookFull).Metadata
	} else {
		p := it.Podcast
		out.Title, out.Author, out.CoverPath = deref(p.Title), deref(p.Author), coverAny(p.CoverPath)
		for _, e := range p.Episodes {
			if f, err := parseAudioFile(e.AudioFile); err == nil {
				out.Episodes = append(out.Episodes, episodeData{ID: e.ID, Title: deref(e.Title), Audio: f, Duration: f.Duration, Chapters: e.Chapters})
			}
		}
		metadata = it.Full().Media.(libitem.PodcastMedia).Metadata
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(raw, &out.Metadata)
	if episodeID != "" {
		i := slices.IndexFunc(out.Episodes, func(e episodeData) bool { return e.ID == episodeID })
		if i < 0 {
			return nil, sql.ErrNoRows
		}
		ep := out.Episodes[i]
		out.Files, out.Duration, out.Chapters, out.Title = []audioFile{ep.Audio}, ep.Duration, ep.Chapters, ep.Title
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// coverAny is a nullable cover path as the session's dynamic JSON value.
func coverAny(s *string) any {
	if s == nil {
		return nil
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
	var chosen map[string]any
	for _, track := range s.AudioTracks {
		idx := int(number(track["index"], 1))
		if idx == index {
			chosen = track
			break
		}
	}
	if chosen == nil && s.MediaType == "podcast" && index == 0 && len(s.AudioTracks) > 0 {
		chosen = s.AudioTracks[0]
	}
	if chosen == nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if s.PlayMethod == 2 {
		if content, ok := chosen["contentUrl"].(string); ok && content != "" {
			http.Redirect(w, r, content, http.StatusFound)
			return nil
		}
	}
	ino, _ := chosen["ino"].(string)
	if ino == "" {
		if meta, ok := chosen["metadata"].(map[string]any); ok {
			_ = meta
		}
	}
	var f *audioFile
	for i := range s.Files {
		if s.Files[i].Ino == ino {
			f = &s.Files[i]
			break
		}
	}
	if f == nil {
		logging.RequestDebug(r, "responding 500: audio track "+indexText+" of the item was not found")
		return httputil.Status(http.StatusInternalServerError, "")
	}
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
	httputil.WriteJSON(w, 200, s.clientJSON())
	return nil
}

func (h *Handler) openSession(user store.User, id string) (*session, error) {
	h.mu.RLock()
	s := h.sessions[id]
	h.mu.RUnlock()
	if s == nil {
		return nil, httputil.Status(http.StatusNotFound, "")
	}
	if s.UserID != user.ID && !isAdmin(user) {
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
	var body map[string]any
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		return httputil.Status(400, "Invalid request body")
	}
	if len(body) > 0 {
		ct, ok := asFloat(body["currentTime"])
		if !ok || ct < 0 {
			return httputil.Status(400, "Invalid currentTime")
		}
		listened, ok := asFloat(body["timeListened"])
		if !ok || listened < 0 {
			return httputil.Status(400, "Invalid timeListened")
		}
		s.CurrentTime = ct
		s.TimeListening += listened
		logging.Debugf("[PlaybackSessionManager] syncSession %q (Device: %s) | Total Time Listened: %v", s.ID, deviceDescription(s.Device), s.TimeListening) // PlaybackSessionManager.js:389
		s.UpdatedAt = nowMS()
		s.Date = time.UnixMilli(s.UpdatedAt).Format("2006-01-02")
		s.DayOfWeek = time.UnixMilli(s.UpdatedAt).Weekday().String()
		if err := h.saveSession(r.Context(), s); err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
		syncDuration, _ := asFloat(body["duration"])
		if _, err := h.syncProgress(r.Context(), user.ID, s, syncDuration, nil); err != nil {
			return httputil.Internal("Internal Server Error", err)
		}
	}
	if closeSession {
		if len(body) == 0 {
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
	metadata, err := persistedMetadataJSON(s.MediaMetadata, s.MediaType)
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
		StartTime: store.Float(s.StartTime), CurrentPosition: store.Float(s.CurrentTime), CoverPath: store.TextAny(s.CoverPath),
		TimeListening: store.Float(s.TimeListening), MediaMetadata: store.Text(string(metadata)),
		Date: store.Text(s.Date), DayOfWeek: store.Text(s.DayOfWeek), ExtraData: store.Text(string(extra)),
		UserID: store.Text(s.UserID), DeviceID: store.TextOrNull(s.DeviceID), LibraryID: store.Text(s.LibraryID),
		CreatedAt: created, UpdatedAt: now,
	})
	return err
}

// Persisted playbackSessions.mediaMetadata keys: Book.oldMetadataToJSON and
// Podcast.oldMetadataToJSON. TS reads these back with JSON.parse, so every
// key is kept and a missing value is an explicit null.
var (
	persistedBookMetadataKeys = []string{"title", "subtitle", "authors", "narrators", "series", "genres", "publishedYear",
		"publishedDate", "publisher", "description", "isbn", "asin", "language", "explicit", "abridged"}
	persistedPodcastMetadataKeys = []string{"title", "author", "description", "releaseDate", "genres", "feedUrl", "imageUrl",
		"itunesPageUrl", "itunesId", "itunesArtistId", "explicit", "language", "type"}
)

func persistedMetadataJSON(metadata map[string]any, mediaType string) ([]byte, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	norm := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &norm); err != nil {
		return nil, err
	}
	keys := persistedBookMetadataKeys
	if mediaType == "podcast" {
		keys = persistedPodcastMetadataKeys
	}
	out := make(map[string]json.RawMessage, len(keys))
	for _, k := range keys {
		out[k] = norm[k] // nil marshals as null
	}
	return json.Marshal(out)
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
	h.emit(userID, "user_item_progress_updated", wire.ProgressUpdatedEvent{ID: progress.ID, SessionID: s.ID, DeviceDescription: deviceDescription(s.Device), Data: progress})
	return true, nil
}

func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	if !isAdmin(user) {
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
	if !isAdmin(user) {
		return httputil.Status(http.StatusNotFound, "")
	}
	httputil.WriteJSON(w, 200, map[string]any{"sessions": h.OpenSessions(), "shareSessions": []any{}})
	return nil
}

// OpenSessions returns the current in-memory playback sessions in the same
// shape used by GET /api/sessions/open. Runtime APIs use it to include active
// sessions in their online-users response.
func (h *Handler) OpenSessions() []map[string]any {
	h.mu.RLock()
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.RUnlock()
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		entry := s.openJSON()
		u, _ := sqlc.New(h.db).GetUserByID(context.Background(), s.UserID)
		entry["user"] = map[string]any{"id": s.UserID, "username": u.Username.String}
		out = append(out, entry)
	}
	return out
}

func (h *Handler) handleDeleteHistory(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	id := r.PathValue("id")
	if !canDelete(user) {
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
	if !isAdmin(user) {
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

func (h *Handler) handleLocalSync(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	all := strings.HasSuffix(r.URL.Path, "-all")
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return httputil.Status(400, "Invalid request body")
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	device, _ := body["deviceInfo"].(map[string]any)
	if device == nil {
		device = map[string]any{}
	}
	deviceDBID, device, err := h.upsertDevice(r.Context(), user, r, cleanDevice(device))
	if err != nil {
		return httputil.Internal("Internal Server Error", err)
	}
	if !all {
		_, errMsg := h.syncOneLocal(r.Context(), user, body, deviceDBID, device)
		if errMsg != "" {
			logging.RequestDebug(r, "responding 500: "+errMsg)
			return httputil.Status(500, errMsg)
		}
		sendOK(w)
		return nil
	}
	sessions, _ := body["sessions"].([]any)
	results := make([]map[string]any, 0, len(sessions))
	for _, raw := range sessions {
		obj, ok := raw.(map[string]any)
		if !ok {
			results = append(results, map[string]any{"id": nil, "success": false, "error": "Media item not found"})
			continue
		}
		result, msg := h.syncOneLocal(r.Context(), user, obj, deviceDBID, device)
		if msg != "" {
			results = append(results, map[string]any{"id": obj["id"], "success": false, "error": msg})
		} else {
			results = append(results, result)
		}
	}
	httputil.WriteJSON(w, 200, map[string]any{"results": results})
	return nil
}

func (h *Handler) syncOneLocal(ctx context.Context, user store.User, obj map[string]any, deviceDBID string, device map[string]any) (map[string]any, string) {
	itemID, _ := obj["libraryItemId"].(string)
	episodeID, _ := obj["episodeId"].(string)
	displayTitle, _ := obj["displayTitle"].(string)
	localID, _ := obj["id"].(string)
	logging.Infof("[PlaybackSessionManager] Syncing local session %q (%s) (updatedAt: %s)", displayTitle, localID, jsTemplateValue(obj["updatedAt"])) // PlaybackSessionManager.js:110
	item, err := h.loadItem(ctx, itemID, episodeID)
	if err != nil {
		logging.Errorf("[PlaybackSessionManager] syncLocalSession: Media item not found for session %q (%s): %v", displayTitle, localID, err) // PlaybackSessionManager.js:132
		return nil, "Media item not found"
	}
	var libraryExists int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries WHERE id=?`, item.LibraryID).Scan(&libraryExists); err != nil || libraryExists == 0 {
		logging.Errorf("[PlaybackSessionManager] syncLocalSession: Library not found for session %q (%s)", displayTitle, localID) // PlaybackSessionManager.js:142
		return nil, "Library not found"
	}
	oldID, _ := obj["id"].(string)
	id := oldID
	if strings.HasPrefix(id, "play_local_") {
		h.mu.Lock()
		if h.localIDs[id] == "" {
			generated, genErr := ids.New()
			if genErr != nil {
				h.mu.Unlock()
				logging.Errorf("[PlaybackSessionManager] syncLocalSession: could not create a session id for %q (%s): %v", displayTitle, localID, genErr)
				return nil, "Failed to save session"
			}
			h.localIDs[id] = generated
		}
		id = h.localIDs[oldID]
		h.mu.Unlock()
	}
	if id == "" {
		generated, genErr := ids.New()
		if genErr != nil {
			logging.Errorf("[PlaybackSessionManager] syncLocalSession: could not create a session id for %q (%s): %v", displayTitle, localID, genErr)
			return nil, "Failed to save session"
		}
		id = generated
	}
	updated := timeFromPayload(obj["updatedAt"])
	started := timeFromPayload(obj["startedAt"])
	if updated == 0 {
		updated = nowMS()
	}
	if started == 0 {
		started = updated
	}
	current := number(obj["currentTime"], 0)
	listened := number(obj["timeListening"], 0)
	// PlaybackSessionManager.syncLocalSession (server/managers/PlaybackSessionManager.js:203)
	// always replaces the client duration with the item's playback duration.
	duration := item.Duration
	day, weekday := time.UnixMilli(updated).Format("2006-01-02"), time.UnixMilli(updated).Weekday().String()
	existingSession, _ := sqlc.New(h.db).CountPlaybackSessionByID(ctx, id)
	if existingSession == 0 {
		// A new local session is constructed from the client JSON
		// (PlaybackSessionManager.js:182), keeping its date and dayOfWeek;
		// only updates recompute them from updatedAt (:212-216).
		if v, ok := obj["date"].(string); ok && v != "" {
			day = v
		}
		if v, ok := obj["dayOfWeek"].(string); ok && v != "" {
			weekday = v
		}
	}
	// coverPath also comes from the client JSON, not the library item.
	var coverPath any
	if v, ok := obj["coverPath"].(string); ok {
		coverPath = v
	}
	mediaMeta, _ := obj["mediaMetadata"].(map[string]any)
	if mediaMeta == nil {
		mediaMeta = map[string]any{}
	}
	for k, v := range item.Metadata {
		if _, ok := mediaMeta[k]; !ok {
			mediaMeta[k] = v
		}
	}
	title, _ := obj["displayTitle"].(string)
	if title == "" {
		title = item.Title
	}
	author, _ := obj["displayAuthor"].(string)
	if author == "" {
		author = item.Author
	}
	playMethod := int(number(obj["playMethod"], 3))
	mediaType := item.MediaType
	bookID := item.MediaID
	if episodeID != "" {
		bookID = ""
	}
	s := &session{ID: id, UserID: user.ID, LibraryID: item.LibraryID, LibraryItemID: item.ID, BookID: bookID, EpisodeID: episodeID, MediaType: mediaType, DisplayTitle: title, DisplayAuthor: author, CoverPath: coverPath, DeviceID: deviceDBID, PlayMethod: playMethod, MediaPlayer: stringOr(obj["mediaPlayer"], "unknown"), Duration: duration, StartTime: number(obj["startTime"], 0), CurrentTime: current, TimeListening: listened, StartedAt: started, UpdatedAt: updated, Date: day, DayOfWeek: weekday, MediaMetadata: mediaMeta, Chapters: item.Chapters, Device: device, Files: item.Files}
	if err = h.saveSessionRecord(ctx, s); err != nil {
		return nil, "Failed to save session"
	}
	synced, err := h.syncProgress(ctx, user.ID, s, 0, &updated)
	if err != nil {
		return nil, "Failed to sync progress"
	}
	return map[string]any{"id": id, "success": true, "progressSynced": synced}, ""
}

func (h *Handler) publicUser(user store.User, s *session) map[string]any {
	client := s.clientJSON()
	client["libraryItem"] = nil
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
func canDelete(u store.User) bool {
	if !u.IsActive {
		return false
	}
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	allow, _ := p["delete"].(bool)
	return allow
}
func timeFromPayload(v any) int64 {
	if ms, ok := asFloat(v); ok {
		return int64(ms)
	}
	if s, ok := v.(string); ok {
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			return ms
		}
		if d, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return d.UnixMilli()
		}
	}
	return 0
}
func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
func deviceDescription(device map[string]any) string {
	model := stringOr(device["model"], "")
	if model != "" {
		sdk := stringOr(device["sdkVersion"], "")
		version := stringOr(device["clientVersion"], "")
		if sdk != "" {
			return model + " SDK " + sdk + " / v" + version
		}
		return model + " / v" + version
	}
	return stringOr(device["osName"], "") + " " + stringOr(device["osVersion"], "") + " / " + stringOr(device["browserName"], "")
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
	codec := strings.ToLower(fmt.Sprint(file.Codec))
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

func makeDirectTracks(s *session, item *itemData) []map[string]any {
	out := make([]map[string]any, 0, len(item.Files))
	offset := 0.0
	for _, f := range item.Files {
		track := audioJSON(f)
		track["startOffset"] = offset
		track["title"] = f.Metadata["filename"]
		track["contentUrl"] = "/api/items/" + item.ID + "/file/" + f.Ino
		out = append(out, track)
		offset += f.Duration
	}
	if s.EpisodeID != "" {
		for _, ep := range item.Episodes {
			if ep.ID == s.EpisodeID {
				track := audioJSON(ep.Audio)
				track["index"] = 1
				track["startOffset"] = 0
				track["title"] = ep.Audio.Metadata["filename"]
				track["contentUrl"] = "/api/items/" + item.ID + "/file/" + ep.Audio.Ino
				return []map[string]any{track}
			}
		}
	}
	return out
}

// openJSON is a playback session as TS PlaybackSession.toJSON writes it (GET
// /api/sessions/open lists these, the client's sessions page reads them). The
// client's session response adds the tracks and the item to it.
func (s *session) openJSON() map[string]any {
	return map[string]any{"id": s.ID, "userId": s.UserID, "libraryId": s.LibraryID, "libraryItemId": s.LibraryItemID, "bookId": store.NullIfEmpty(s.BookID), "episodeId": store.NullIfEmpty(s.EpisodeID), "mediaType": s.MediaType, "mediaMetadata": s.MediaMetadata, "chapters": s.Chapters, "displayTitle": s.DisplayTitle, "displayAuthor": s.DisplayAuthor, "coverPath": s.CoverPath, "duration": s.Duration, "playMethod": s.PlayMethod, "mediaPlayer": s.MediaPlayer, "deviceInfo": s.Device, "serverVersion": "2.37.2", "date": s.Date, "dayOfWeek": s.DayOfWeek, "timeListening": s.TimeListening, "startTime": s.StartTime, "currentTime": s.CurrentTime, "startedAt": s.StartedAt, "updatedAt": s.UpdatedAt}
}
func (s *session) clientJSON() map[string]any {
	out := s.openJSON()
	out["audioTracks"] = s.AudioTracks
	out["libraryItem"] = s.Item
	return out
}

func parseAudioFile(raw map[string]any) (audioFile, error) {
	b, _ := json.Marshal(raw)
	var f audioFile
	if err := json.Unmarshal(b, &f); err != nil {
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
	if f.Duration < 0 {
		f.Duration = 0
	}
	return f, nil
}

func canAccess(user store.User, item *itemData) bool {
	var p map[string]any
	if json.Unmarshal(user.Permissions, &p) != nil {
		return false
	}
	all, _ := p["accessAllLibraries"].(bool)
	if !all && !slices.Contains(stringSlice(p["librariesAccessible"]), item.LibraryID) {
		return false
	}
	access := item.Access()
	if access.Explicit {
		allowed, _ := p["accessExplicitContent"].(bool)
		if !allowed {
			return false
		}
	}
	allTags, _ := p["accessAllTags"].(bool)
	if allTags {
		return true
	}
	selected := stringSlice(p["itemTagsSelected"])
	selectedNot, _ := p["selectedTagsNotAccessible"].(bool)
	if len(access.Tags) == 0 {
		return selectedNot
	}
	if selectedNot {
		for _, t := range access.Tags {
			if slices.Contains(selected, t) {
				return false
			}
		}
		return true
	}
	for _, t := range access.Tags {
		if slices.Contains(selected, t) {
			return true
		}
	}
	return false
}
func isAdmin(u store.User) bool { return u.Type == "root" || u.Type == "admin" }
func cleanDevice(in map[string]any) map[string]any {
	out := map[string]any{}
	allowed := []string{"id", "userId", "deviceId", "ipAddress", "browserName", "browserVersion", "osName", "osVersion", "deviceType", "clientVersion", "manufacturer", "model", "sdkVersion", "clientName", "deviceName"}
	for _, k := range allowed {
		if v, ok := in[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

func (h *Handler) upsertDevice(ctx context.Context, user store.User, r *http.Request, client map[string]any) (string, map[string]any, error) {
	get := func(key string) string { v, _ := client[key].(string); return v }
	ua := r.UserAgent()
	browser, browserVersion, osName, osVersion := deviceinfo.ParseUserAgent(ua)
	clientVersion := get("clientVersion")
	if clientVersion == "" {
		clientVersion = "2.37.2"
	}
	clientName := get("clientName")
	manufacturer, model, sdk := get("manufacturer"), get("model"), get("sdkVersion")
	// DeviceInfo.setData (server/objects/DeviceInfo.js:100): the Android app
	// sends sdkVersion as a JSON number, stored as its decimal string.
	if n, ok := client["sdkVersion"].(float64); ok {
		sdk = strconv.FormatFloat(n, 'f', -1, 64)
	}
	if sdk != "" {
		if clientName == "" {
			clientName = "Abs Android"
		}
	} else if model != "" {
		if clientName == "" {
			clientName = "Abs iOS"
		}
	} else if browser != "" && osName != "" {
		if clientName == "" {
			clientName = "Abs Web"
		}
	} else if clientName == "" {
		clientName = "Unknown"
	}
	deviceName := ""
	// DeviceInfo.js:109/112: `${manufacturer || 'Unknown'} ${model || ''}`, untrimmed.
	switch {
	case sdk != "", model != "":
		deviceName = or(manufacturer, "Unknown") + " " + model
	case browser != "":
		deviceName = osName + " " + or(osVersion, "N/A") + " " + browser
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	dbID, err := ids.New()
	if err != nil {
		return "", nil, err
	}
	deviceKey := get("deviceId")
	if deviceKey == "" {
		deviceKey = dbID
	}
	var existing string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? AND userId=? LIMIT 1`, deviceKey, user.ID).Scan(&existing)
	if err == nil {
		dbID = existing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	extra := map[string]any{}
	for key, value := range map[string]string{"manufacturer": manufacturer, "model": model, "osName": osName, "osVersion": osVersion, "browserName": browser} {
		if value != "" {
			extra[key] = value
		}
	}
	extraRaw, _ := json.Marshal(extra)
	version := browserVersion
	if sdk != "" {
		version = sdk
	}
	now := dbtime.Now()
	if existing == "" {
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, dbID, deviceKey, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), user.ID, now, now)
	} else {
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), now, dbID)
	}
	if err != nil {
		return "", nil, err
	}
	info := map[string]any{"id": dbID, "userId": user.ID, "deviceId": deviceKey, "ipAddress": ip, "clientVersion": clientVersion, "clientName": clientName, "deviceName": store.NullIfEmpty(deviceName), "browserName": store.NullIfEmpty(browser), "browserVersion": store.NullIfEmpty(browserVersion), "osName": store.NullIfEmpty(osName), "osVersion": store.NullIfEmpty(osVersion), "deviceType": nil, "manufacturer": store.NullIfEmpty(manufacturer), "model": store.NullIfEmpty(model), "sdkVersion": store.NullIfEmpty(sdk)}
	for k, v := range info {
		if v == nil {
			delete(info, k)
		}
	}
	// The observed v2.37.2 playback-session device DTO omits deviceName,
	// manufacturer, model and sdkVersion entirely when no value exists
	// (PlaybackSession.js:86-104 / DeviceInfo.js:43-67 fixture behavior).
	return dbID, info, nil
}

func or(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
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

func audioJSON(f audioFile) map[string]any {
	out := map[string]any{}
	for k, v := range f.Fields {
		out[k] = v
	}
	out["index"] = f.Index
	out["ino"] = f.Ino
	out["metadata"] = f.Metadata
	out["duration"] = f.Duration
	out["mimeType"] = f.MimeType
	if f.Codec != nil {
		out["codec"] = f.Codec
	}
	return out
}

func number(v any, fallback float64) float64 {
	if n, ok := asFloat(v); ok {
		return n
	}
	return fallback
}
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}
func numberOr(v any, fallback int) any {
	if v == nil {
		return fallback
	}
	return v
}
func stringSlice(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		for _, a := range x {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
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

// jsTemplateValue renders a decoded JSON value as a JS template literal would
// for the log lines that interpolate request fields (numbers without
// exponent notation, null/undefined spelled out).
func jsTemplateValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}
