package userapi

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"
)

type listeningSession struct {
	id, userID, libraryID, mediaItemID, mediaItemType       string
	displayTitle, displayAuthor, mediaPlayer, serverVersion string
	coverPath, date, dayOfWeek                              string
	timeListening                                           int64
	duration, playMethod, startTime, currentTime            float64
	mediaMetadata, extraData                                []byte
	deviceID                                                sql.NullString
	createdAt, updatedAt                                    any
}

func (h *Handler) writeListeningSessions(w http.ResponseWriter, r *http.Request, userID, mediaID string, includeUser bool) {
	sessions, err := h.listeningSessions(r, userID, mediaID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
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
			http.Error(w, "database read failed", 500)
			return
		}
		if includeUser {
			var username string
			if err := h.db.QueryRowContext(r.Context(), `SELECT username FROM users WHERE id=?`, session.userID).Scan(&username); err == nil {
				row["user"] = map[string]any{"id": session.userID, "username": username}
			}
		}
		results = append(results, row)
	}
	writeJSON(w, 200, map[string]any{"total": len(sessions), "numPages": int(math.Ceil(float64(len(sessions)) / float64(perPage))), "page": page, "itemsPerPage": perPage, "sessions": results})
}

func (h *Handler) writeListeningStats(w http.ResponseWriter, r *http.Request, userID string) {
	sessions, err := h.listeningSessions(r, userID, "")
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	stats := map[string]any{"totalTime": int64(0), "items": map[string]any{}, "days": map[string]any{}, "dayOfWeek": map[string]any{}, "today": int64(0), "recentSessions": []any{}}
	items := stats["items"].(map[string]any)
	days := stats["days"].(map[string]any)
	dayOfWeek := stats["dayOfWeek"].(map[string]any)
	recent := make([]any, 0, minInt(len(sessions), 10))
	for index, session := range sessions {
		row, err := h.listeningSessionJSON(r, session)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if index < 10 {
			recent = append(recent, row)
		}
		seconds := session.timeListening
		stats["totalTime"] = stats["totalTime"].(int64) + seconds
		if session.dayOfWeek != "" {
			dayOfWeek[session.dayOfWeek] = numberValue(dayOfWeek[session.dayOfWeek]) + float64(seconds)
		}
		if session.date != "" && seconds > 0 {
			days[session.date] = numberValue(days[session.date]) + float64(seconds)
			if session.date == time.Now().Format("2006-01-02") {
				stats["today"] = stats["today"].(int64) + seconds
			}
		}
		var extra map[string]any
		_ = json.Unmarshal(session.extraData, &extra)
		itemID, _ := extra["libraryItemId"].(string)
		if current, ok := items[itemID].(map[string]any); ok {
			current["timeListening"] = numberValue(current["timeListening"]) + float64(seconds)
		} else {
			var metadata map[string]any
			_ = json.Unmarshal(session.mediaMetadata, &metadata)
			items[itemID] = map[string]any{"id": itemID, "timeListening": seconds, "mediaMetadata": metadataOrEmpty(metadata), "lastUpdate": millis(session.updatedAt)}
		}
	}
	stats["recentSessions"] = recent
	writeJSON(w, 200, stats)
}

func (h *Handler) listeningSessions(r *http.Request, userID, mediaID string) ([]listeningSession, error) {
	query := `SELECT id,userId,libraryId,mediaItemId,mediaItemType,displayTitle,displayAuthor,duration,playMethod,mediaPlayer,startTime,currentTime,serverVersion,coverPath,date,dayOfWeek,timeListening,mediaMetadata,extraData,deviceId,createdAt,updatedAt FROM playbackSessions WHERE userId=?`
	args := []any{userID}
	if mediaID != "" {
		query += ` AND mediaItemId=?`
		args = append(args, mediaID)
	}
	query += ` ORDER BY updatedAt DESC`
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]listeningSession, 0)
	for rows.Next() {
		var s listeningSession
		var player, version, cover, date, weekday sql.NullString
		var metadata, extra sql.NullString
		if err := rows.Scan(&s.id, &s.userID, &s.libraryID, &s.mediaItemID, &s.mediaItemType, &s.displayTitle, &s.displayAuthor, &s.duration, &s.playMethod, &player, &s.startTime, &s.currentTime, &version, &cover, &date, &weekday, &s.timeListening, &metadata, &extra, &s.deviceID, &s.createdAt, &s.updatedAt); err != nil {
			return nil, err
		}
		s.mediaPlayer, s.serverVersion, s.coverPath, s.date, s.dayOfWeek = player.String, version.String, cover.String, date.String, weekday.String
		s.mediaMetadata, s.extraData = []byte(metadata.String), []byte(extra.String)
		result = append(result, s)
	}
	return result, rows.Err()
}

func (h *Handler) listeningSessionJSON(r *http.Request, s listeningSession) (map[string]any, error) {
	var extra, metadata map[string]any
	_ = json.Unmarshal(s.extraData, &extra)
	_ = json.Unmarshal(s.mediaMetadata, &metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	var device any
	if s.deviceID.Valid && s.deviceID.String != "" {
		var id, userID, clientName, clientVersion, ip, deviceName, deviceVersion string
		var raw sql.NullString
		err := h.db.QueryRowContext(r.Context(), `SELECT deviceId,userId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData FROM devices WHERE id=?`, s.deviceID.String).Scan(&id, &userID, &clientName, &clientVersion, &ip, &deviceName, &deviceVersion, &raw)
		if err != nil {
			return nil, err
		}
		var fields map[string]any
		_ = json.Unmarshal([]byte(raw.String), &fields)
		browserVersion, sdkVersion := any(nil), any(nil)
		if clientName == "Abs Android" {
			sdkVersion = nullableStringValue(deviceVersion)
		} else {
			browserVersion = nullableStringValue(deviceVersion)
		}
		device = map[string]any{"id": id, "deviceId": id, "userId": userID, "ipAddress": ip, "browserName": fields["browserName"], "browserVersion": browserVersion, "osName": fields["osName"], "osVersion": fields["osVersion"], "clientVersion": clientVersion, "manufacturer": fields["manufacturer"], "model": fields["model"], "sdkVersion": sdkVersion, "deviceName": deviceName, "clientName": clientName}
	}
	podcast := s.mediaItemType == "podcastEpisode"
	var bookID, episodeID any = s.mediaItemID, nil
	mediaType := "book"
	if podcast {
		bookID, episodeID, mediaType = nil, s.mediaItemID, "podcast"
	}
	return map[string]any{"id": s.id, "userId": s.userID, "libraryId": s.libraryID, "libraryItemId": extra["libraryItemId"], "bookId": bookID, "episodeId": episodeID, "mediaType": mediaType, "mediaMetadata": metadata, "chapters": []any{}, "displayTitle": s.displayTitle, "displayAuthor": s.displayAuthor, "coverPath": nullableStringValue(s.coverPath), "duration": s.duration, "playMethod": s.playMethod, "mediaPlayer": nullableStringValue(s.mediaPlayer), "deviceInfo": device, "serverVersion": nullableStringValue(s.serverVersion), "date": nullableStringValue(s.date), "dayOfWeek": nullableStringValue(s.dayOfWeek), "timeListening": s.timeListening, "startTime": s.startTime, "currentTime": s.currentTime, "startedAt": millis(s.createdAt), "updatedAt": millis(s.updatedAt)}, nil
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
func metadataOrEmpty(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}
func nullableStringValue(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
