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
func (h *Handler) publicShare(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	ctx := r.Context()
	share, err := h.loadShare(ctx, slug)
	if errors.Is(err, errShareGone) {
		httputil.SendText(w, 404, "Media item share not found", httputil.WithoutETag())
		return
	}
	if err != nil {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
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
			return
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
		httputil.SendText(w, 404, "Media item not found", httputil.WithoutETag())
		return
	}
	var itemID, libraryID, mediaID sql.NullString
	if err := h.db.QueryRowContext(ctx, `SELECT id,libraryId,mediaId FROM libraryItems WHERE mediaId=? LIMIT 1`, share.MediaItemID).Scan(&itemID, &libraryID, &mediaID); err != nil {
		httputil.SendText(w, 404, "Media item not found", httputil.WithoutETag())
		return
	}
	var title, cover sql.NullString
	var duration sql.NullFloat64
	var audioRaw, chaptersRaw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT title,coverPath,duration,audioFiles,chapters FROM books WHERE id=?`, share.MediaItemID).Scan(&title, &cover, &duration, &audioRaw, &chaptersRaw); err != nil {
		httputil.SendText(w, 404, "Media item not found", httputil.WithoutETag())
		return
	}
	authors, err := h.bookAuthors(ctx, share.MediaItemID)
	if err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	var files []shareAudioFile
	_ = json.Unmarshal(audioRaw, &files)
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
	chapters := []any{}
	_ = json.Unmarshal(chaptersRaw, &chapters)
	now := time.Now().UnixMilli()
	device, err := h.shareDevice(ctx, r, sid)
	if err != nil {
		logging.RequestError(r, "Internal server error", err)
		httputil.SendText(w, 500, "Internal server error", httputil.WithoutETag())
		return
	}
	s := &shareSession{
		BookID: share.MediaItemID, Device: device,
		ID: sid, ShareID: share.ID, Slug: slug, ItemID: itemID.String, LibraryID: libraryID.String, Title: title.String,
		Author: joinAuthors(authors), CoverPath: cover.String, Duration: duration.Float64, Current: start, Start: start,
		Updated: now, Tracks: tracks, Metadata: h.oldBookMetadata(ctx, share.MediaItemID), Chapters: chapters,
	}
	var librarySettings []byte
	_ = h.db.QueryRowContext(ctx, `SELECT settings FROM libraries WHERE id=?`, libraryID.String).Scan(&librarySettings)
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
func (h *Handler) shareSessionFor(w http.ResponseWriter, r *http.Request, slug string) (shareRow, shareSession, bool) {
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		httputil.SendText(w, 404, "Share session not set", httputil.WithoutETag())
		return shareRow{}, shareSession{}, false
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return shareRow{}, shareSession{}, false
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	var copySession shareSession
	if s != nil {
		copySession = *s
	}
	h.mu.Unlock()
	if s == nil || s.ShareID != share.ID {
		httputil.SendText(w, 404, "Share session not found", httputil.WithoutETag())
		return shareRow{}, shareSession{}, false
	}
	return share, copySession, true
}

// shareProgress is ShareController.updateMediaItemShareProgress.
func (h *Handler) shareProgress(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		httputil.SendText(w, 404, "Share session not set", httputil.WithoutETag())
		return
	}
	var p struct {
		CurrentTime *float64 `json:"currentTime"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.CurrentTime == nil || *p.CurrentTime < 0 {
		httputil.SendText(w, 400, "Invalid current time", httputil.WithoutETag())
		return
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	if s == nil || s.ShareID != share.ID {
		h.mu.Unlock()
		httputil.SendText(w, 404, "Share session not found", httputil.WithoutETag())
		return
	}
	s.Current = math.Min(*p.CurrentTime, s.Duration)
	s.Updated = time.Now().UnixMilli()
	h.mu.Unlock()
	httputil.SendStatus(w, 204, httputil.WithoutETag())
}

// shareCover serves /public/share/:slug/cover.
func (h *Handler) shareCover(w http.ResponseWriter, r *http.Request) {
	_, s, ok := h.shareSessionFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if s.CoverPath == "" {
		httputil.SendText(w, 404, "Cover image not found", httputil.WithoutETag())
		return
	}
	serveMedia(w, r, s.CoverPath)
}

// shareTrack serves /public/share/:slug/track/:index.
func (h *Handler) shareTrack(w http.ResponseWriter, r *http.Request) {
	_, s, ok := h.shareSessionFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	index := -1
	if n := jsParseInt(r.PathValue("index")); !math.IsNaN(n) {
		index = int(n)
	}
	for _, track := range s.Tracks {
		if track.Index == index {
			serveMedia(w, r, track.path)
			return
		}
	}
	httputil.SendText(w, 404, "Track not found", httputil.WithoutETag())
}

// shareDownload serves /public/share/:slug/download.
func (h *Handler) shareDownload(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	cookie, err := r.Cookie("share_session_id")
	if err != nil || cookie.Value == "" {
		httputil.SendText(w, 404, "Share session not set", httputil.WithoutETag())
		return
	}
	share, err := h.findShare(r.Context(), slug)
	if err != nil {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	if !share.IsDownloadable.Bool {
		httputil.SendText(w, 403, "Download is not allowed for this item", httputil.WithoutETag())
		return
	}
	_, s, ok := h.shareSessionFor(w, r, slug)
	if !ok {
		return
	}
	var itemPath, relPath sql.NullString
	var isFile int
	if err := h.db.QueryRowContext(r.Context(), `SELECT path,relPath,isFile FROM libraryItems WHERE id=?`, s.ItemID).Scan(&itemPath, &relPath, &isFile); err != nil {
		httputil.SendText(w, 404, "Library item not found", httputil.WithoutETag())
		return
	}
	if isFile != 0 {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(relPath.String)+`"`)
		serveMedia(w, r, itemPath.String)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(s.Title)+`.zip"`)
	if err := zipDirectory(w, itemPath.String); err != nil {
		logging.RequestError(r, "Failed to download the item", err)
		httputil.SendText(w, 500, "Failed to download the item", httputil.WithoutETag())
	}
}
