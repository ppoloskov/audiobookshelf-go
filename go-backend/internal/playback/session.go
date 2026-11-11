package playback

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/version"
)

// Session is a playback session as clients read it (PlaybackSession.toJSON).
// The open-sessions listing leaves AudioTracks and LibraryItem out and adds User.
type Session struct {
	ID            string       `json:"id"`
	UserID        string       `json:"userId"`
	LibraryID     string       `json:"libraryId"`
	LibraryItemID string       `json:"libraryItemId"`
	BookID        *string      `json:"bookId"`
	EpisodeID     *string      `json:"episodeId"`
	MediaType     string       `json:"mediaType"`
	MediaMetadata any          `json:"mediaMetadata"` // item.BookMetadataFull or item.PodcastMetadataFull, by media type
	Chapters      []db.Chapter `json:"chapters"`
	DisplayTitle  string       `json:"displayTitle"`
	DisplayAuthor string       `json:"displayAuthor"`
	CoverPath     *string      `json:"coverPath"`
	Duration      float64      `json:"duration"`
	PlayMethod    int          `json:"playMethod"`
	MediaPlayer   string       `json:"mediaPlayer"`
	DeviceInfo    deviceInfo   `json:"deviceInfo"`
	ServerVersion string       `json:"serverVersion"`
	Date          string       `json:"date"`
	DayOfWeek     string       `json:"dayOfWeek"`
	TimeListening float64      `json:"timeListening"`
	StartTime     float64      `json:"startTime"`
	CurrentTime   float64      `json:"currentTime"`
	StartedAt     int64        `json:"startedAt"`
	UpdatedAt     int64        `json:"updatedAt"`

	AudioTracks any       `json:"audioTracks,omitempty"` // []item.AudioTrack, or []hlsTrack when transcoding
	LibraryItem any       `json:"libraryItem,omitempty"` // item.Expanded, or JSON null in the admin event
	User        *userStub `json:"user,omitempty"`
}

// hlsTrack is the one track of a transcoded session: its HLS playlist.
type hlsTrack struct {
	Index       int     `json:"index"`
	StartOffset float64 `json:"startOffset"`
	Duration    float64 `json:"duration"`
	Title       string  `json:"title"`
	ContentURL  string  `json:"contentUrl"`
	MimeType    string  `json:"mimeType"`
	Codec       *string `json:"codec"`    // always null
	Metadata    *string `json:"metadata"` // always null
}

// userStub names a session's user in listings.
type userStub struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// session is an open session: its client view plus what the server keeps. The
// handler's mutex guards the view's mutable fields (currentTime, timeListening,
// updatedAt, date, dayOfWeek); everything else is fixed at creation. Code that
// needs a stable view works on a copy of the struct.
type session struct {
	Session
	mediaID string // the books or podcasts row id
	files   []db.AudioFile
	stream  *stream       // nil unless transcoding
	item    item.Expanded // the library item as it was at start, for the client view
}

// mediaRef is the progress row the session reads and writes: the episode of a
// podcast session, otherwise the book (PlaybackSession.js:214).
func (s *session) mediaRef() (id, kind string) {
	if s.EpisodeID != nil {
		return *s.EpisodeID, "podcastEpisode"
	}
	return s.mediaID, "book"
}

// touch records activity at now.
func (s *Session) touch(now time.Time) {
	s.UpdatedAt = now.UnixMilli()
	s.Date, s.DayOfWeek = now.Format("2006-01-02"), now.Weekday().String()
}

// ratio is PlaybackSession.progress.
func (s *Session) ratio() float64 {
	if s.Duration == 0 {
		return 0
	}
	return math.Max(0, math.Min(s.CurrentTime/s.Duration, 1))
}

// forClient is the view with the tracks and the expanded item, on a copy.
func (s session) forClient() Session {
	s.AudioTracks, s.LibraryItem = s.tracks(), s.item
	return s.Session
}

// tracks are the audio tracks as the client reads them; a transcoded session has
// the one HLS playlist.
func (s session) tracks() any {
	if s.PlayMethod == 2 {
		return []hlsTrack{{Index: 1, Duration: s.Duration, Title: s.DisplayTitle, ContentURL: hlsPlaylist(s.ID), MimeType: "application/vnd.apple.mpegurl"}}
	}
	tracks := make([]item.AudioTrack, len(s.files))
	offset := 0.0
	for i, file := range s.files {
		tracks[i] = item.AudioTrack{AudioFile: file, Index: file.Index, Title: file.Metadata.Filename, StartOffset: offset,
			ContentURL: "/api/items/" + s.LibraryItemID + "/file/" + file.Ino}
		offset += db.Val(file.Duration)
	}
	return tracks
}

// row is the playbackSessions row of the session. Only the progress fields are
// updated when the row exists (see upsert).
func (s *session) row() (db.PlaybackSession, error) {
	metadata, err := json.Marshal(s.MediaMetadata)
	if err != nil {
		return db.PlaybackSession{}, fmt.Errorf("encode session metadata: %w", err)
	}
	mediaItemID, kind := s.mediaRef()
	var deviceID *string
	if s.DeviceInfo.ID != "" {
		deviceID = &s.DeviceInfo.ID
	}
	return db.PlaybackSession{
		ID: s.ID, MediaItemID: &mediaItemID, MediaItemType: &kind, DisplayTitle: &s.DisplayTitle, DisplayAuthor: &s.DisplayAuthor,
		Duration: &s.Duration, PlayMethod: db.Ptr(float64(s.PlayMethod)), MediaPlayer: &s.MediaPlayer, StartTime: &s.StartTime,
		CurrentTime: &s.CurrentTime, ServerVersion: db.Ptr(version.Server), CoverPath: s.CoverPath, TimeListening: &s.TimeListening,
		MediaMetadata: db.JSON[json.RawMessage]{V: metadata}, Date: &s.Date, DayOfWeek: &s.DayOfWeek,
		ExtraData: db.JSON[*db.SessionExtra]{V: &db.SessionExtra{LibraryItemID: &s.LibraryItemID}},
		CreatedAt: db.FromMillis(s.StartedAt), UpdatedAt: db.FromMillis(s.UpdatedAt),
		UserID: &s.UserID, DeviceID: deviceID, LibraryID: &s.LibraryID,
	}, nil
}

// upsert inserts the row, or, when the session was stored before, updates only
// currentTime, timeListening, updatedAt, date and dayOfWeek: the device and
// metadata stay as first written (PlaybackSessionManager.js:207-220).
func (h *Handler) upsert(ctx context.Context, row db.PlaybackSession) error {
	return h.db.Tx(ctx, func(ctx context.Context) error {
		exists, err := h.sessionStored(ctx, row.ID)
		if err != nil {
			return err
		}
		if exists {
			return h.db.Update(ctx, "playbackSessions", &row, "currentTime", "timeListening", "updatedAt", "date", "dayOfWeek")
		}
		return h.db.Insert(ctx, "playbackSessions", &row)
	})
}

func (h *Handler) sessionStored(ctx context.Context, id string) (bool, error) {
	var n int
	if err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM playbackSessions WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("find session %s: %w", id, err)
	}
	return n > 0, nil
}

// The open sessions live in memory; all access goes through these methods, which
// hold h.mu.

func (h *Handler) add(s *session) {
	h.mu.Lock()
	h.sessions[s.ID] = s
	h.mu.Unlock()
}

// get returns a copy of the open session id.
func (h *Handler) get(id string) (session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[id]; ok {
		return *s, true
	}
	return session{}, false
}

// update applies fn to the open session id and returns a copy of the result.
func (h *Handler) update(id string, fn func(*Session)) (session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	if !ok {
		return session{}, false
	}
	fn(&s.Session)
	return *s, true
}

// remove forgets the open session id and stops its transcode.
func (h *Handler) remove(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil {
		s.stream.stop(s.ID)
	}
}

// snapshot lists copies of the open sessions, oldest first.
func (h *Handler) snapshot() []session {
	h.mu.Lock()
	out := make([]session, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, *s)
	}
	h.mu.Unlock()
	slices.SortFunc(out, func(a, b session) int {
		return cmp.Or(cmp.Compare(a.StartedAt, b.StartedAt), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Close forgets every open session and stops their transcodes (server shutdown,
// backup restore).
func (h *Handler) Close() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = map[string]*session{}
	h.mu.Unlock()
	for _, s := range sessions {
		s.stream.stop(s.ID)
	}
}

// OpenSessions lists the open sessions with their users, for the online-users
// response. It returns any because userapi cannot import this package.
func (h *Handler) OpenSessions() any { return h.listing(h.snapshot()) }

// SessionOf is one open session of the user as the listing shows it, or nil (an
// untyped nil, so the online-user view omits it).
func (h *Handler) SessionOf(userID string) any {
	for _, s := range h.snapshot() {
		if s.UserID == userID {
			return h.listing([]session{s})[0]
		}
	}
	return nil
}

// listing is the open-sessions view of sessions, each with its user stub.
func (h *Handler) listing(sessions []session) []Session {
	names := map[string]string{}
	out := make([]Session, len(sessions))
	for i, s := range sessions {
		name, ok := names[s.UserID]
		if !ok {
			// No request context here (userapi.Deps.OpenSessions takes none); a missing user is an empty name.
			var user struct {
				Username *string `db:"username"`
			}
			_ = h.db.Get(context.Background(), &user, `SELECT username FROM users WHERE id = ?`, s.UserID)
			name = db.Val(user.Username)
			names[s.UserID] = name
		}
		out[i] = s.Session
		out[i].User = &userStub{ID: s.UserID, Username: name}
	}
	return out
}
