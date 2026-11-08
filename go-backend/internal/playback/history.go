package playback

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/ids"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

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
	if !ids.Valid(userFilter) {
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

func (h *Handler) handleDeleteHistory(w http.ResponseWriter, r *http.Request) error {
	user, _ := store.UserFromContext(r.Context())
	id := r.PathValue("id")
	if !access.For(r.Context(), user).Delete() {
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
	httputil.OK(w, r)
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
		if !ids.Valid(id) {
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
	httputil.OK(w, r)
	return nil
}
