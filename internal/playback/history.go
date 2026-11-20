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

	"monstrobook/internal/auth"
	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/ids"
)

// sortColumns are the playbackSessions columns the history can be sorted by.
var sortColumns = []string{"displayTitle", "duration", "playMethod", "startTime", "currentTime", "timeListening", "updatedAt", "createdAt"}

// HistorySession is a stored playback session as clients read it
// (PlaybackSession.toJSON: legacy ids dropped, nullable columns null).
type HistorySession struct {
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
}

// adminHistorySession is a stored session as GET /api/sessions lists it
// (SessionController.getAllWithUserData spreads the PlaybackSession instance
// and attaches user when the user still exists).
// AdminHistorySession is a stored session with the extra admin/user fields
// included by GET /api/sessions and GET /api/users/{id}/listening-sessions.
type AdminHistorySession struct {
	HistorySession
	*withUser
}

// withUser is what GET /api/sessions adds to a session whose user still exists.
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
	Total        int                   `json:"total"`
	NumPages     int                   `json:"numPages"`
	Page         int                   `json:"page"`
	ItemsPerPage int                   `json:"itemsPerPage"`
	Sessions     []AdminHistorySession `json:"sessions"`
	UserID       string                `json:"userId,omitempty"`
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
	sessions, err := h.AdminSessionsOf(ctx, rows)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, historyPage{Total: total, NumPages: int(math.Ceil(float64(total) / float64(perPage))), Page: page, ItemsPerPage: perPage, Sessions: sessions, UserID: userID})
}

// SessionsOf lists stored session rows with their devices, without user/live fields.
func (h *Handler) SessionsOf(ctx context.Context, rows []db.PlaybackSession) ([]HistorySession, error) {
	if len(rows) == 0 {
		return []HistorySession{}, nil
	}
	deviceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		deviceIDs = append(deviceIDs, db.Val(row.DeviceID))
	}
	var devices []db.Device
	if err := h.db.Select(ctx, &devices, `SELECT * FROM devices WHERE id IN (?)`, deviceIDs); err != nil {
		return nil, fmt.Errorf("list session devices: %w", err)
	}
	deviceByID := make(map[string]storedDevice, len(devices))
	for _, d := range devices {
		deviceByID[d.ID] = storedDeviceOf(d)
	}
	out := make([]HistorySession, len(rows))
	for i, row := range rows {
		out[i] = StoredSession(row)
		out[i].DeviceInfo = struct{}{}
		if d, found := deviceByID[db.Val(row.DeviceID)]; found {
			out[i].DeviceInfo = d
		}
	}
	return out, nil
}

// StoredSession is a stored session row without its device and user.
func StoredSession(row db.PlaybackSession) HistorySession {
	s := HistorySession{
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

// AdminSessionsOf builds the admin history listing with device and user info.
func (h *Handler) AdminSessionsOf(ctx context.Context, rows []db.PlaybackSession) ([]AdminHistorySession, error) {
	base, err := h.SessionsOf(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []AdminHistorySession{}, nil
	}
	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, db.Val(row.UserID))
	}
	var users []struct {
		ID       string  `db:"id"`
		Username *string `db:"username"`
	}
	if err := h.db.Select(ctx, &users, `SELECT id, username FROM users WHERE id IN (?)`, userIDs); err != nil {
		return nil, fmt.Errorf("list session users: %w", err)
	}
	nameByID := make(map[string]string, len(users))
	for _, u := range users {
		if u.Username != nil {
			nameByID[u.ID] = *u.Username
		}
	}
	out := make([]AdminHistorySession, len(base))
	for i, row := range rows {
		out[i] = AdminHistorySession{HistorySession: base[i]}
		if name, found := nameByID[db.Val(row.UserID)]; found {
			out[i].withUser = &withUser{User: userStub{ID: db.Val(row.UserID), Username: name}, AudioTracks: []struct{}{}}
		}
	}
	return out, nil
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
