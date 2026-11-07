package userapi

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

func (h *Handler) writeListeningSessions(w http.ResponseWriter, r *http.Request, userID, mediaID string, includeUser bool) error {
	sessions, err := h.listeningSessions(r, userID, mediaID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	page, perPage := queryInt(r, "page", 0), queryInt(r, "itemsPerPage", 10)
	if perPage <= 0 {
		perPage = 10
	}
	if page < 0 {
		page = 0
	}
	start := page * perPage
	if start > len(sessions) {
		start = len(sessions)
	}
	end := start + perPage
	if end > len(sessions) {
		end = len(sessions)
	}
	results := make([]map[string]any, 0, end-start)
	for _, session := range sessions[start:end] {
		row, err := h.listeningSessionJSON(r, session)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if includeUser {
			if u, err := sqlc.New(h.db).GetUserByID(r.Context(), session.UserID.String); err == nil && u.Username.Valid {
				row["user"] = map[string]any{"id": session.UserID.String, "username": u.Username.String}
				wire.AddPlaybackSessionSpreadFields(row)
			}
		}
		results = append(results, row)
	}
	return httputil.JSON(w, r, map[string]any{"total": len(sessions), "numPages": int(math.Ceil(float64(len(sessions)) / float64(perPage))), "page": page, "itemsPerPage": perPage, "sessions": results})
}

func (h *Handler) writeListeningStats(w http.ResponseWriter, r *http.Request, userID string) error {
	sessions, err := h.listeningSessions(r, userID, "")
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	stats := map[string]any{"totalTime": float64(0), "items": map[string]any{}, "days": map[string]any{}, "dayOfWeek": map[string]any{}, "today": float64(0), "recentSessions": []any{}}
	items := stats["items"].(map[string]any)
	days := stats["days"].(map[string]any)
	dayOfWeek := stats["dayOfWeek"].(map[string]any)
	recent := make([]any, 0, minInt(len(sessions), 10))
	for index, session := range sessions {
		// Only the ten most recent sessions are serialized; building the JSON row
		// loads the session's device info, so doing it for every session was one
		// query per session.
		if index < 10 {
			row, err := h.listeningSessionJSON(r, session)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			recent = append(recent, row)
		}
		// ApiRouter.getUserListeningStatsHelpers (server/routers/ApiRouter.js:535-575)
		// works on the constructed legacy session: derived date/dayOfWeek,
		// `timeListening || null`, prefix-stripped libraryItemId and
		// mediaMetadata as stored. JS arithmetic treats null as 0, `null > 0`
		// is false, and an object key of null is the string "null".
		legacy := session.LegacyJSON(nil)
		var seconds float64
		listened := legacy["timeListening"]
		if v, ok := listened.(float64); ok {
			seconds = v
		}
		stats["totalTime"] = stats["totalTime"].(float64) + seconds
		if day, _ := legacy["dayOfWeek"].(string); day != "" {
			dayOfWeek[day] = numberValue(dayOfWeek[day]) + seconds
		}
		if date, _ := legacy["date"].(string); date != "" && seconds > 0 {
			days[date] = numberValue(days[date]) + seconds
			if date == time.Now().Format("2006-01-02") {
				stats["today"] = stats["today"].(float64) + seconds
			}
		}
		itemID := "null"
		if id, ok := legacy["libraryItemId"].(string); ok {
			itemID = id
		}
		if current, ok := items[itemID].(map[string]any); ok {
			current["timeListening"] = numberValue(current["timeListening"]) + seconds
		} else {
			// Upstream also assigns lastUpdate: s.lastUpdate, a property the legacy
			// session object never has, so the key is absent (server/routers/ApiRouter.js:569).
			var id any = itemID
			if itemID == "null" {
				id = nil
			}
			items[itemID] = map[string]any{"id": id, "timeListening": listened, "mediaMetadata": legacy["mediaMetadata"]}
		}
	}
	stats["recentSessions"] = recent
	return httputil.JSON(w, r, stats)
}

func (h *Handler) listeningSessions(r *http.Request, userID, mediaID string) ([]store.PlaybackSessionRow, error) {
	rows, err := sqlc.New(h.db).ListPlaybackSessionsForUser(r.Context(), sqlc.ListPlaybackSessionsForUserParams{UserID: store.Text(userID), MediaItemID: store.TextOrNull(mediaID)})
	if err != nil {
		return nil, err
	}
	result := make([]store.PlaybackSessionRow, 0, len(rows))
	for _, s := range rows {
		result = append(result, store.PlaybackSessionFromRow(s))
	}
	return result, nil
}

func (h *Handler) listeningSessionJSON(r *http.Request, s store.PlaybackSessionRow) (map[string]any, error) {
	device, err := store.LoadDeviceInfo(r.Context(), h.db, s.DeviceID.String)
	if errors.Is(err, sql.ErrNoRows) {
		device, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.LegacyJSON(device), nil
}

func queryInt(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return n
}
func numberValue(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
