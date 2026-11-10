package userapi

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
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
	var total, today float64
	items := map[string]any{}
	days, dayOfWeek := map[string]float64{}, map[string]float64{}
	recent := make([]any, 0, min(len(sessions), 10))
	for index, session := range sessions {
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
		total += seconds
		if day, _ := legacy["dayOfWeek"].(string); day != "" {
			dayOfWeek[day] += seconds
		}
		if date, _ := legacy["date"].(string); date != "" && seconds > 0 {
			days[date] += seconds
			if date == time.Now().Format("2006-01-02") {
				today += seconds
			}
		}
		itemID := "null"
		if id, ok := legacy["libraryItemId"].(string); ok {
			itemID = id
		}
		if current, ok := items[itemID].(map[string]any); ok {
			current["timeListening"] = jsonvalue.Float(current["timeListening"]) + seconds
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
	return httputil.JSON(w, r, map[string]any{"totalTime": total, "items": items, "days": days, "dayOfWeek": dayOfWeek, "today": today, "recentSessions": recent})
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

func (h *Handler) userListening(w http.ResponseWriter, r *http.Request, current store.User) error {
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if kind != "listening-sessions" && kind != "listening-stats" {
		return httputil.Status(404, "")
	}
	if !access.AdminUser(current) && current.ID != id {
		return httputil.Status(403, "")
	}
	if _, err := h.targetUser(r.Context(), id); err != nil {
		return err
	}
	if kind == "listening-stats" {
		return h.writeListeningStats(w, r, id)
	}
	return h.writeListeningSessions(w, r, id, "", true)
}

func (h *Handler) ownListeningSessions(w http.ResponseWriter, r *http.Request, user store.User) error {
	return h.writeListeningSessions(w, r, user.ID, "", false)
}

func (h *Handler) ownListeningStats(w http.ResponseWriter, r *http.Request, user store.User) error {
	return h.writeListeningStats(w, r, user.ID)
}

func (h *Handler) getItemListeningSessions(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID, episodeID := r.PathValue("id"), r.PathValue("episodeId")
	exists, allowed, err := access.ItemAccess(r.Context(), h.db, user, itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !exists {
		return httputil.Status(404, "")
	}
	var mediaID, mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID, &mediaType); err != nil {
		return httputil.Status(404, "")
	}
	// MeController.getItemListeningSessions (MeController.js:185): the episode
	// is looked up by its id alone, a podcast without one is a 404 before the
	// access check, and an existing episode id replaces the media item id.
	episodeFound := false
	if episodeID != "" {
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE id=?`, episodeID).Scan(&exists); err != nil {
			return httputil.Internal("database read failed", err)
		}
		episodeFound = exists > 0
	}
	if mediaType == "podcast" && !episodeFound {
		return httputil.Status(404, "")
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	if episodeFound {
		mediaID = episodeID
	}
	return h.writeListeningSessions(w, r, user.ID, mediaID, false)
}
