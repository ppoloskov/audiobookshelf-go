package share

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/version"
)

// sessionCookie ties a browser to its share session (HttpOnly, 30 days).
const sessionCookie = "share_session_id"

func notFound(message string) error { return httpx.Error(http.StatusNotFound, message) }

// lookupError is a 404 for an absent row and a wrapped internal error otherwise.
func lookupError(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("Not found")
	}
	return fmt.Errorf("%s: %w", what, err)
}

// publicShareResponse is GET /public/share/{slug}: the share plus its playbackSession.
type publicShareResponse struct {
	shareView
	PlaybackSession session `json:"playbackSession"`
}

// publicShare is ShareController.getMediaItemShareBySlug. It answers the
// browser's session when the cookie names one of this share and opens a new
// one otherwise. Only books can be played through a share.
func (h *Handler) publicShare(w http.ResponseWriter, r *http.Request) error {
	share, err := h.loadShare(r.Context(), r.PathValue("slug"))
	if err != nil {
		return err
	}
	start, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 {
		start = 0
	}
	start = math.Trunc(start)
	var sessionID string
	if cookie, err := r.Cookie(sessionCookie); err == nil && ids.ValidV4(cookie.Value) {
		sessionID = cookie.Value // a session lost on restart keeps its id
		if existing, ok := h.sessions.get(sessionID); ok {
			if existing.shareID != share.ID {
				h.sessions.remove(sessionID) // the cookie came from another share
			} else {
				if start > 0 && start < existing.Duration {
					existing, _ = h.sessions.update(sessionID, func(s *session) { s.CurrentTime = start })
				}
				return httpx.JSON(w, r, publicShareResponse{viewOf(share), existing})
			}
		}
	}
	if db.Val(share.MediaItemType) != "book" {
		return notFound("Media item not found")
	}
	if sessionID == "" {
		if sessionID, err = ids.New(); err != nil {
			return fmt.Errorf("new share session id: %w", err)
		}
	}
	sess, err := h.openSession(r.Context(), r, share, sessionID, start)
	if err != nil {
		return err
	}
	h.sessions.put(sess)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sessionID, Path: "/", HttpOnly: true, MaxAge: 30 * 24 * 60 * 60})
	return httpx.JSON(w, r, publicShareResponse{viewOf(share), sess})
}

// openSession builds the playback session of the shared book, starting at
// start seconds (0 when beyond the audio).
func (h *Handler) openSession(ctx context.Context, r *http.Request, share db.MediaItemShare, id string, start float64) (session, error) {
	var itemID string
	err := h.db.Get(ctx, &itemID, `SELECT id FROM libraryItems WHERE mediaId = ? LIMIT 1`, db.Val(share.MediaItemID))
	if err != nil {
		return session{}, lookupError(err, "find shared item")
	}
	it, err := item.Get(ctx, h.db, itemID, item.Options{})
	if err != nil {
		return session{}, lookupError(err, "load shared item")
	}
	if it.Book == nil {
		return session{}, notFound("Media item not found")
	}
	book := it.Book
	tracks, total := []track{}, 0.0
	for _, file := range book.AudioFiles.V {
		if file.Exclude {
			continue
		}
		duration, codec := db.Val(file.Duration), file.Codec
		if codec != nil && *codec == "" {
			codec = nil
		}
		tracks = append(tracks, track{
			Index: file.Index, StartOffset: total, Duration: duration, Title: file.Metadata.Filename,
			ContentURL: h.base + "/public/share/" + db.Val(share.Slug) + "/track/" + strconv.FormatInt(file.Index, 10),
			MimeType:   nilIfEmpty(file.MimeType), Codec: codec, Metadata: file.Metadata,
		})
		total += duration
	}
	if start > total {
		start = 0
	}
	device, err := h.recordDevice(ctx, r, id)
	if err != nil {
		return session{}, err
	}
	var library db.Library
	if err := h.db.Get(ctx, &library, `SELECT * FROM libraries WHERE id = ?`, db.Val(it.LibraryID)); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return session{}, fmt.Errorf("load library: %w", err)
	}
	now := time.Now()
	sess := session{
		ID: id, LibraryID: db.Val(it.LibraryID), LibraryItemID: it.ID, BookID: db.Val(share.MediaItemID), MediaType: "book",
		MediaMetadata: book.Full(it.ID).Metadata, ServerVersion: version.Server, MediaPlayer: "web-share",
		DeviceInfo: device,
		Date:       now.Local().Format("2006-01-02"), DayOfWeek: now.Local().Weekday().String(), StartedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
		Chapters: book.Chapters.V, DisplayTitle: db.Val(book.Title), DisplayAuthor: book.AuthorName(),
		CoverPath: book.CoverPath, CoverAspectRatio: library.Settings.V.CoverAspectRatio, Duration: db.Val(book.Duration),
		StartTime: start, CurrentTime: start, AudioTracks: tracks, shareID: share.ID,
	}
	if sess.CoverPath != nil && *sess.CoverPath == "" {
		sess.CoverPath = nil
	}
	return sess, nil
}

// sessionOf is the session named by the request cookie, if it plays share.
func (h *Handler) sessionOf(r *http.Request, share db.MediaItemShare) (session, error) {
	sess, ok := h.sessions.get(cookieValue(r))
	if !ok || sess.shareID != share.ID {
		return session{}, notFound("Share session not found")
	}
	return sess, nil
}

func cookieValue(r *http.Request) string {
	cookie, _ := r.Cookie(sessionCookie)
	if cookie == nil {
		return ""
	}
	return cookie.Value
}

// guarded runs the checks every cookie-guarded route shares, in order: the
// cookie, the live share of the slug and the session of that share. check, if
// set, vets the share between the last two.
func (h *Handler) guarded(r *http.Request, check func(db.MediaItemShare) error) (session, error) {
	if cookieValue(r) == "" {
		return session{}, notFound("Share session not set")
	}
	share, err := h.loadShare(r.Context(), r.PathValue("slug"))
	if err != nil {
		return session{}, err
	}
	if check != nil {
		if err := check(share); err != nil {
			return session{}, err
		}
	}
	return h.sessionOf(r, share)
}

// shareProgress is ShareController.updateMediaItemShareProgress.
func (h *Handler) shareProgress(w http.ResponseWriter, r *http.Request) error {
	if cookieValue(r) == "" {
		return notFound("Share session not set")
	}
	body, err := httpx.Decode[struct {
		CurrentTime *float64 `json:"currentTime"`
	}](w, r)
	if err != nil {
		return err
	}
	if body.CurrentTime == nil || *body.CurrentTime < 0 {
		return httpx.BadRequest("Invalid current time")
	}
	if _, err := h.guarded(r, nil); err != nil {
		return err
	}
	h.sessions.update(cookieValue(r), func(s *session) {
		s.CurrentTime, s.UpdatedAt = min(*body.CurrentTime, s.Duration), time.Now().UnixMilli()
	})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) shareCover(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.guarded(r, nil)
	if err != nil {
		return err
	}
	if sess.CoverPath == nil {
		return notFound("Cover image not found")
	}
	return serveMedia(w, r, *sess.CoverPath, "")
}

func (h *Handler) shareTrack(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.guarded(r, nil)
	if err != nil {
		return err
	}
	index, err := strconv.ParseInt(r.PathValue("index"), 10, 64)
	if err != nil {
		index = -1
	}
	for _, t := range sess.AudioTracks {
		if t.Index == index {
			return serveMedia(w, r, t.Metadata.Path, "")
		}
	}
	return notFound("Track not found")
}

// shareDownload serves the shared item: the file of a single-file item, a zip
// of the folder otherwise. Only downloadable shares allow it.
func (h *Handler) shareDownload(w http.ResponseWriter, r *http.Request) error {
	sess, err := h.guarded(r, func(share db.MediaItemShare) error {
		if !share.IsDownloadable {
			return httpx.Error(http.StatusForbidden, "Download is not allowed for this item")
		}
		return nil
	})
	if err != nil {
		return err
	}
	var row db.LibraryItem
	if err := h.db.Get(r.Context(), &row, `SELECT * FROM libraryItems WHERE id = ?`, sess.LibraryItemID); err != nil {
		return lookupError(err, "load shared library item")
	}
	if row.IsFile {
		return serveMedia(w, r, db.Val(row.Path), filepath.Base(db.Val(row.RelPath)))
	}
	name := filepath.Base(sess.DisplayTitle)
	if name == "." || name == string(filepath.Separator) {
		name = "download"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	zw := zip.NewWriter(w)
	if err := zw.AddFS(os.DirFS(db.Val(row.Path))); err != nil {
		return fmt.Errorf("zip item folder: %w", err)
	}
	return zw.Close()
}
