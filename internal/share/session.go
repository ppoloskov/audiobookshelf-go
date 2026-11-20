package share

import (
	"sync"
	"time"

	"monstrobook/internal/db"
)

const (
	shareSessionTTL  = 24 * time.Hour
	maxShareSessions = 1024
)

// session is one open share playback session and, as JSON, the playbackSession
// of GET /public/share/{slug}. It lives in memory, keyed by shareSessionID
// (the share_session_id cookie), while ID is its own random v4 UUID.
type session struct {
	ID               string       `json:"id"`
	UserID           *string      `json:"userId"`
	LibraryID        string       `json:"libraryId"`
	LibraryItemID    string       `json:"libraryItemId"`
	BookID           string       `json:"bookId"`
	EpisodeID        *string      `json:"episodeId"`
	MediaType        string       `json:"mediaType"`
	MediaMetadata    any          `json:"mediaMetadata"` // the item view's book metadata
	Chapters         []db.Chapter `json:"chapters"`
	DisplayTitle     string       `json:"displayTitle"`
	DisplayAuthor    string       `json:"displayAuthor"`
	CoverPath        *string      `json:"coverPath"`
	CoverAspectRatio float64      `json:"coverAspectRatio,omitempty"`
	Duration         float64      `json:"duration"`
	PlayMethod       int          `json:"playMethod"`
	MediaPlayer      string       `json:"mediaPlayer"`
	DeviceInfo       deviceInfo   `json:"deviceInfo"`
	ServerVersion    string       `json:"serverVersion"`
	Date             string       `json:"date"`
	DayOfWeek        string       `json:"dayOfWeek"`
	TimeListening    int          `json:"timeListening"`
	StartTime        float64      `json:"startTime"`
	CurrentTime      float64      `json:"currentTime"`
	StartedAt        int64        `json:"startedAt"`
	UpdatedAt        int64        `json:"updatedAt"`
	AudioTracks      []track      `json:"audioTracks"`
	LibraryItem      *struct{}    `json:"libraryItem"`
	shareID          string
	shareSessionID   string
	lastSeen         time.Time
}

// track is one entry of audioTracks: an audio file with its running offset and public URL.
type track struct {
	Index       int64           `json:"index"`
	StartOffset float64         `json:"startOffset"`
	Duration    float64         `json:"duration"`
	Title       string          `json:"title"`
	ContentURL  string          `json:"contentUrl"`
	MimeType    *string         `json:"mimeType"`
	Codec       *string         `json:"codec"`
	Metadata    db.FileMetadata `json:"metadata"`
}

// sessions is the set of open sessions; handed out as copies, guarded by mu.
// Entries expire after ttl (default 24h) and capacity is bounded at max (default 1024).
type sessions struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	now  func() time.Time
	byID map[string]*session
}

func (s *sessions) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *sessions) sessionTTL() time.Duration {
	if s.ttl > 0 {
		return s.ttl
	}
	return shareSessionTTL
}

func (s *sessions) maxCap() int {
	if s.max > 0 {
		return s.max
	}
	return maxShareSessions
}

func (s *sessions) isExpired(sess *session, now time.Time) bool {
	return now.Sub(sess.lastSeen) > s.sessionTTL()
}

func (s *sessions) get(id string) (session, bool) {
	return s.update(id, func(*session) {})
}

func (s *sessions) put(sess session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.currentTime()
	if sess.lastSeen.IsZero() {
		sess.lastSeen = now
	}
	if _, exists := s.byID[sess.shareSessionID]; !exists && len(s.byID) >= s.maxCap() {
		for id, existing := range s.byID {
			if s.isExpired(existing, now) {
				delete(s.byID, id)
			}
		}
		for len(s.byID) >= s.maxCap() {
			var oldestID string
			var oldestTime time.Time
			for id, existing := range s.byID {
				if oldestID == "" || existing.lastSeen.Before(oldestTime) {
					oldestID = id
					oldestTime = existing.lastSeen
				}
			}
			if oldestID == "" {
				break
			}
			delete(s.byID, oldestID)
		}
	}
	s.byID[sess.shareSessionID] = &sess
}

func (s *sessions) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// dropShare closes every session that plays the share.
func (s *sessions) dropShare(shareID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.byID {
		if sess.shareID == shareID {
			delete(s.byID, id)
		}
	}
}

// update applies change to session id and returns the resulting copy.
func (s *sessions) update(id string, change func(*session)) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.byID[id]
	if !ok {
		return session{}, false
	}
	now := s.currentTime()
	if s.isExpired(found, now) {
		delete(s.byID, id)
		return session{}, false
	}
	found.lastSeen = now
	change(found)
	return *found, true
}
