package sharing

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// shareTrack is one entry of audioTracks in a share playback session
// (server/controllers/ShareController.js:73-86): the audio file's index, the
// running startOffset and the file's metadata object as stored.
type shareTrack struct {
	Index       int             `json:"index"`
	StartOffset float64         `json:"startOffset"`
	Duration    float64         `json:"duration"`
	Title       string          `json:"title"`
	ContentURL  string          `json:"contentUrl"`
	MimeType    *string         `json:"mimeType"`
	Codec       *string         `json:"codec"`
	Metadata    json.RawMessage `json:"metadata"`
	path        string
}

// publicShareResponse is GET /public/share/:slug: the share (without pash,
// userId and extraData) plus its playbackSession.
type publicShareResponse struct {
	wire.MediaItemShare
	PlaybackSession map[string]any `json:"playbackSession"`
}

type shareAudioFile struct {
	Index    int             `json:"index"`
	Exclude  bool            `json:"exclude"`
	Duration float64         `json:"duration"`
	MimeType *string         `json:"mimeType"`
	Codec    *string         `json:"codec"`
	Metadata json.RawMessage `json:"metadata"`
}

// jsParseInt is parseInt(value) for the ?t= query: the leading integer.
func jsParseInt(s string) float64 {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || end == 0 && (s[end] == '-' || s[end] == '+')) {
		end++
	}
	n, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return math.NaN()
	}
	return math.Trunc(n)
}

func clearShareCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "share_session_id", Value: "", Path: "/", Expires: time.Unix(0, 0)})
}

func validUUIDv4(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
			return false
		}
	}
	return s[14] == '4' && strings.ContainsRune("89abAB", rune(s[19]))
}

// publicShare is ShareController.getMediaItemShareBySlug
// (server/controllers/ShareController.js:31).
func (h *Handler) publicShare(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	ctx := r.Context()
	share, err := h.loadShare(ctx, slug)
	if errors.Is(err, errShareGone) {
		return httputil.Status(404, "Media item share not found")
	}
	if err != nil {
		return httputil.Status(404, "")
	}
	start := 0.0
	if raw := r.URL.Query().Get("t"); raw != "" {
		if _, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			if n := jsParseInt(raw); !math.IsNaN(n) {
				start = math.Max(0, n)
			}
		}
	}
	cookie, _ := r.Cookie("share_session_id")
	sessionID := ""
	if cookie != nil {
		sessionID = cookie.Value
	}
	if sessionID != "" {
		h.mu.Lock()
		old := h.sessions[sessionID]
		if old != nil && old.ShareID == share.ID {
			if start > 0 && start < old.Duration {
				old.Current = start
			}
			payload := publicShareResponse{MediaItemShare: share.wire(), PlaybackSession: old.json()}
			h.mu.Unlock()
			write(w, payload)
			return nil
		}
		if old != nil {
			delete(h.sessions, sessionID)
		}
		h.mu.Unlock()
		if old == nil && !validUUIDv4(sessionID) {
			clearShareCookie(w)
		}
	}
	if share.MediaItemType != "book" {
		return httputil.Status(404, "Media item not found")
	}
	var itemID string
	if err := h.db.QueryRowContext(ctx, `SELECT id FROM libraryItems WHERE mediaId=? LIMIT 1`, share.MediaItemID).Scan(&itemID); err != nil {
		return httputil.Status(404, "Media item not found")
	}
	item, err := libitem.LoadOne(ctx, h.db, itemID, libitem.Options{})
	if err != nil || item.Book == nil {
		return httputil.Status(404, "Media item not found")
	}
	book := item.Book
	var files []shareAudioFile
	remarshal(book.AudioFiles, &files)
	tracks := []shareTrack{}
	total := 0.0
	for _, f := range files {
		if f.Exclude {
			continue
		}
		var md struct {
			Filename string `json:"filename"`
			Path     string `json:"path"`
		}
		_ = json.Unmarshal(f.Metadata, &md)
		tracks = append(tracks, shareTrack{
			Index: f.Index, StartOffset: total, Duration: f.Duration, Title: md.Filename,
			ContentURL: h.cfg.RouterBasePath + "/public/share/" + slug + "/track/" + strconv.Itoa(f.Index),
			MimeType:   f.MimeType, Codec: nonEmpty(f.Codec), Metadata: f.Metadata, path: md.Path,
		})
		total += f.Duration
	}
	if start > total {
		start = 0
	}
	sid := sessionID
	if sid == "" {
		sid, _ = ids.New()
	}
	now := time.Now().UnixMilli()
	device, err := h.shareDevice(ctx, r, sid)
	if err != nil {
		return httputil.Internal("Internal server error", err)
	}
	s := &shareSession{
		BookID: share.MediaItemID, Device: device,
		ID: sid, ShareID: share.ID, Slug: slug, ItemID: item.ID, LibraryID: item.LibraryID, Title: deref(book.Title),
		Author: book.AuthorName(), CoverPath: deref(book.CoverPath), Duration: deref64(book.Duration), Current: start, Start: start,
		Updated: now, Tracks: tracks, Metadata: item.Full().Media.(libitem.BookFull).Metadata, Chapters: book.Chapters,
	}
	var librarySettings []byte
	_ = h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, item.LibraryID).Scan(&librarySettings)
	var ls struct {
		CoverAspectRatio *float64 `json:"coverAspectRatio"`
	}
	_ = json.Unmarshal(librarySettings, &ls)
	s.CoverAspect = ls.CoverAspectRatio
	h.mu.Lock()
	h.sessions[sid] = s
	payload := publicShareResponse{MediaItemShare: share.wire(), PlaybackSession: s.json()}
	h.mu.Unlock()
	logging.Infof("[ShareManager] Adding new open share playback session %q", s.Title) // ShareManager.js:31
	http.SetCookie(w, &http.Cookie{Name: "share_session_id", Value: sid, Path: "/", HttpOnly: true, MaxAge: 30 * 24 * 60 * 60})
	write(w, payload)
	return nil
}

func deref64(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// json is PlaybackSession.toJSONForClient for a web-share session.
func (s *shareSession) json() map[string]any {
	out := map[string]any{"id": s.ID, "userId": nil, "libraryId": s.LibraryID, "libraryItemId": s.ItemID, "bookId": s.BookID, "episodeId": nil, "mediaType": "book", "mediaMetadata": s.Metadata, "chapters": s.Chapters, "displayTitle": s.Title, "displayAuthor": s.Author, "coverPath": store.NullIfEmpty(s.CoverPath), "duration": s.Duration, "playMethod": 0, "mediaPlayer": "web-share", "deviceInfo": s.Device, "serverVersion": "2.37.2", "date": time.UnixMilli(s.Updated).Local().Format("2006-01-02"), "dayOfWeek": time.UnixMilli(s.Updated).Local().Weekday().String(), "timeListening": 0, "startTime": s.Start, "currentTime": s.Current, "startedAt": s.Updated, "updatedAt": s.Updated, "audioTracks": s.Tracks, "libraryItem": nil}
	if s.CoverAspect != nil {
		out["coverAspectRatio"] = *s.CoverAspect
	}
	return out
}

// shareSessionFor runs the checks the cookie-protected share routes share:
// cookie, share by slug, playback session of that share.
func (h *Handler) shareSessionFor(r *http.Request, slug string) (shareRow, shareSession, error) {
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		return shareRow{}, shareSession{}, httputil.Status(404, "Share session not set")
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		return shareRow{}, shareSession{}, httputil.Status(404, "")
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	var copySession shareSession
	if s != nil {
		copySession = *s
	}
	h.mu.Unlock()
	if s == nil || s.ShareID != share.ID {
		return shareRow{}, shareSession{}, httputil.Status(404, "Share session not found")
	}
	return share, copySession, nil
}

// shareProgress is ShareController.updateMediaItemShareProgress.
func (h *Handler) shareProgress(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		return httputil.Status(404, "Share session not set")
	}
	var p struct {
		CurrentTime *float64 `json:"currentTime"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.CurrentTime == nil || *p.CurrentTime < 0 {
		return httputil.Status(400, "Invalid current time")
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		return httputil.Status(404, "")
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	if s == nil || s.ShareID != share.ID {
		h.mu.Unlock()
		return httputil.Status(404, "Share session not found")
	}
	s.Current = math.Min(*p.CurrentTime, s.Duration)
	s.Updated = time.Now().UnixMilli()
	h.mu.Unlock()
	return httputil.Empty(204)
}

// shareCover serves /public/share/:slug/cover.
func (h *Handler) shareCover(w http.ResponseWriter, r *http.Request) error {
	_, s, err := h.shareSessionFor(r, r.PathValue("slug"))
	if err != nil {
		return err
	}
	if s.CoverPath == "" {
		return httputil.Status(404, "Cover image not found")
	}
	serveMedia(w, r, s.CoverPath)
	return nil
}

// shareTrack serves /public/share/:slug/track/:index.
func (h *Handler) shareTrack(w http.ResponseWriter, r *http.Request) error {
	_, s, err := h.shareSessionFor(r, r.PathValue("slug"))
	if err != nil {
		return err
	}
	index := -1
	if n := jsParseInt(r.PathValue("index")); !math.IsNaN(n) {
		index = int(n)
	}
	for _, track := range s.Tracks {
		if track.Index == index {
			serveMedia(w, r, track.path)
			return nil
		}
	}
	return httputil.Status(404, "Track not found")
}

// shareDownload serves /public/share/:slug/download.
func (h *Handler) shareDownload(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		return httputil.Status(404, "Share session not set")
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		return httputil.Status(404, "")
	}
	if !share.IsDownloadable.Bool {
		return httputil.Status(403, "Download is not allowed for this item")
	}
	_, s, err := h.shareSessionFor(r, slug)
	if err != nil {
		return err
	}
	var itemPath, relPath sql.NullString
	var isFile int
	if err := h.db.QueryRowContext(r.Context(), `SELECT path,relPath,isFile FROM libraryItems WHERE id=?`, s.ItemID).Scan(&itemPath, &relPath, &isFile); err != nil {
		return httputil.Status(404, "Library item not found")
	}
	if isFile != 0 {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(relPath.String)+`"`)
		serveMedia(w, r, itemPath.String)
		return nil
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(s.Title)+`.zip"`)
	if err := zipDirectory(w, itemPath.String); err != nil {
		return httputil.Internal("Failed to download the item", err)
	}
	return nil
}
