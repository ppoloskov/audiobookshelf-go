package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// sortColumns are the playbackSessions columns the history can be sorted by.
var sortColumns = []string{"displayTitle", "duration", "playMethod", "startTime", "currentTime", "timeListening", "updatedAt", "createdAt"}

// historySession is a stored session as the admin history lists it
// (PlaybackSession from the row: legacy ids dropped, nullable columns null).
type historySession struct {
	ID            string          `json:"id"`
	UserID        *string         `json:"userId"`
	LibraryID     *string         `json:"libraryId"`
	LibraryItemID *string         `json:"libraryItemId"`
	BookID        *string         `json:"bookId"`
	EpisodeID     *string         `json:"episodeId"`
	MediaType     string          `json:"mediaType"`
	MediaMetadata json.RawMessage `json:"mediaMetadata"`
	Chapters      []db.Chapter    `json:"chapters"` // always empty
	DisplayTitle  string          `json:"displayTitle"`
	DisplayAuthor string          `json:"displayAuthor"`
	CoverPath     *string         `json:"coverPath"`
	Duration      *float64        `json:"duration"`
	PlayMethod    *float64        `json:"playMethod"`
	MediaPlayer   *string         `json:"mediaPlayer"`
	DeviceInfo    any             `json:"deviceInfo"` // storedDevice, or {} when the device row is gone
	ServerVersion *string         `json:"serverVersion"`
	Date          *string         `json:"date"`
	DayOfWeek     *string         `json:"dayOfWeek"`
	TimeListening *float64        `json:"timeListening"` // null when 0
	StartTime     float64         `json:"startTime"`
	CurrentTime   float64         `json:"currentTime"`
	StartedAt     int64           `json:"startedAt"`
	UpdatedAt     int64           `json:"updatedAt"`
	*withUser
}

// withUser is what the listing adds to a session whose user still exists
// (the fields of an open session that a stored one lacks).
type withUser struct {
	User             userStub   `json:"user"`
	LastSave         int        `json:"lastSave"`
	AudioTracks      []struct{} `json:"audioTracks"`
	Stream           *string    `json:"stream"`
	ShareSessionID   *string    `json:"shareSessionId"`
	MediaItemShareID *string    `json:"mediaItemShareId"`
	CoverAspectRatio *float64   `json:"coverAspectRatio"`
}

type historyPage struct {
	Total        int              `json:"total"`
	NumPages     int              `json:"numPages"`
	Page         int              `json:"page"`
	ItemsPerPage int              `json:"itemsPerPage"`
	Sessions     []historySession `json:"sessions"`
	UserID       string           `json:"userId,omitempty"`
}

// history is GET /api/sessions: admins only, answered 404 to others as upstream
// does. Query: sort, desc=1, itemsPerPage, page, user.
func (h *Handler) history(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.IsAdmin() {
		return httpx.NotFound()
	}
	ctx, query := r.Context(), r.URL.Query()
	sortBy := query.Get("sort")
	if !slices.Contains(sortColumns, sortBy) {
		sortBy = "updatedAt"
	}
	direction := "ASC"
	if query.Get("desc") == "1" {
		direction = "DESC"
	}
	perPage := httpx.QueryInt(r, "itemsPerPage", 10)
	if perPage < 1 {
		perPage = 10
	}
	page := max(httpx.QueryInt(r, "page", 0), 0)
	where, args := "", []any{}
	userID := query.Get("user")
	if ids.Valid(userID) {
		where, args = " WHERE userId = ?", []any{userID}
	} else {
		userID = ""
	}
	var total int
	if err := h.db.Get(ctx, &total, `SELECT COUNT(*) FROM playbackSessions`+where, args...); err != nil {
		return fmt.Errorf("count sessions: %w", err)
	}
	var rows []db.PlaybackSession
	err := h.db.Select(ctx, &rows, `SELECT * FROM playbackSessions`+where+` ORDER BY `+sortBy+` `+direction+` LIMIT ? OFFSET ?`, append(args, perPage, perPage*page)...)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	sessions, err := h.historyOf(ctx, rows)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, historyPage{Total: total, NumPages: int(math.Ceil(float64(total) / float64(perPage))), Page: page, ItemsPerPage: perPage, Sessions: sessions, UserID: userID})
}

// HistorySession is a stored session as the history lists it.
type HistorySession = historySession

// SessionsOf lists stored session rows the way the admin history does.
func (h *Handler) SessionsOf(ctx context.Context, rows []db.PlaybackSession) ([]HistorySession, error) {
	return h.historyOf(ctx, rows)
}

// StoredSession is a stored session row without its device and user.
func StoredSession(row db.PlaybackSession) HistorySession { return historyOf(row) }

// historyOf builds the listing of rows with one query for their devices and one
// for their users.
func (h *Handler) historyOf(ctx context.Context, rows []db.PlaybackSession) ([]historySession, error) {
	var deviceIDs, userIDs []string
	for _, row := range rows {
		deviceIDs = append(deviceIDs, db.Val(row.DeviceID))
		userIDs = append(userIDs, db.Val(row.UserID))
	}
	var devices []db.Device
	var users []struct {
		ID       string  `db:"id"`
		Username *string `db:"username"`
	}
	if err := h.db.Select(ctx, &devices, `SELECT * FROM devices WHERE id IN (?)`, deviceIDs); err != nil {
		return nil, fmt.Errorf("list session devices: %w", err)
	}
	if err := h.db.Select(ctx, &users, `SELECT id, username FROM users WHERE id IN (?)`, userIDs); err != nil {
		return nil, fmt.Errorf("list session users: %w", err)
	}
	deviceByID, nameByID := map[string]storedDevice{}, map[string]string{}
	for _, d := range devices {
		deviceByID[d.ID] = storedDeviceOf(d)
	}
	for _, u := range users {
		if u.Username != nil {
			nameByID[u.ID] = *u.Username
		}
	}
	out := make([]historySession, len(rows))
	for i, row := range rows {
		out[i] = historyOf(row)
		out[i].DeviceInfo = struct{}{}
		if d, found := deviceByID[db.Val(row.DeviceID)]; found {
			out[i].DeviceInfo = d
		}
		if name, found := nameByID[db.Val(row.UserID)]; found {
			out[i].withUser = &withUser{User: userStub{ID: db.Val(row.UserID), Username: name}, AudioTracks: []struct{}{}}
		}
	}
	return out, nil
}

// historyOf maps a row: NULLs stay null, 0 listening time is null, and ids that
// older upstream versions prefixed (lib_, li_, ep_, local_) are dropped.
func historyOf(row db.PlaybackSession) historySession {
	s := historySession{
		ID: row.ID, UserID: row.UserID, LibraryID: legacyID(row.LibraryID, "lib_"), MediaType: "book", Chapters: []db.Chapter{},
		DisplayTitle: db.Val(row.DisplayTitle), DisplayAuthor: db.Val(row.DisplayAuthor), CoverPath: row.CoverPath,
		Duration: row.Duration, PlayMethod: row.PlayMethod, MediaPlayer: legacyID(row.MediaPlayer), ServerVersion: row.ServerVersion,
		Date: row.Date, DayOfWeek: row.DayOfWeek, StartTime: db.Val(row.StartTime), CurrentTime: db.Val(row.CurrentTime),
		MediaMetadata: row.MediaMetadata.V,
	}
	if extra := row.ExtraData.V; extra != nil {
		s.LibraryItemID = legacyID(extra.LibraryItemID, "li_", "local_")
	}
	if db.Val(row.MediaItemType) == "podcastEpisode" {
		s.MediaType, s.EpisodeID = "podcast", legacyID(row.MediaItemID, "ep_", "local_")
	} else {
		s.BookID = legacyID(row.MediaItemID)
	}
	if tl := db.Val(row.TimeListening); tl != 0 {
		s.TimeListening = &tl
	}
	if !row.CreatedAt.IsZero() {
		s.StartedAt = row.CreatedAt.Millis()
	}
	if !row.UpdatedAt.IsZero() {
		s.UpdatedAt = row.UpdatedAt.Millis()
		if db.Val(row.Date) == "" {
			// Local sessions have no date: derive it from updatedAt in the server's time zone.
			at := row.UpdatedAt.In(time.Local)
			s.Date, s.DayOfWeek = db.Ptr(at.Format("2006-01-02")), db.Ptr(at.Weekday().String())
		}
	}
	return s
}

// legacyID is *s, nil when it is empty or starts with one of the prefixes.
func legacyID(s *string, prefixes ...string) *string {
	if db.Val(s) == "" || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(*s, p) }) {
		return nil
	}
	return s
}

// deleteHistory is DELETE /api/sessions/{id}: users with the delete permission.
func (h *Handler) deleteHistory(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.CanDelete() {
		return httpx.Forbidden()
	}
	id := r.PathValue("id")
	stored, err := h.sessionStored(r.Context(), id)
	if err != nil {
		return err
	}
	if !stored {
		return httpx.NotFound()
	}
	h.remove(id)
	if _, err := h.db.Exec(r.Context(), `DELETE FROM playbackSessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return ok(w)
}

// batchDelete is POST /api/sessions/batch/delete: admins only (auth.Admin).
func (h *Handler) batchDelete(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	const invalid = `Invalid request body. "sessions" array of session id strings is required.`
	body, err := httpx.Decode[struct {
		Sessions []string `json:"sessions"`
	}](w, r)
	if err != nil || len(body.Sessions) == 0 || slices.ContainsFunc(body.Sessions, func(id string) bool { return !ids.Valid(id) }) {
		return httpx.BadRequest(invalid)
	}
	for _, id := range body.Sessions {
		h.remove(id)
	}
	result, err := h.db.Exec(r.Context(), `DELETE FROM playbackSessions WHERE id IN (?)`, body.Sessions)
	if err != nil {
		return httpx.Internal("Failed to remove sessions", err)
	}
	removed, _ := result.RowsAffected() // only logged
	slog.Info("playback sessions removed", "count", removed, "by", db.Val(user.Username))
	return ok(w)
}
