package user

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playback"
)

// sessionsPage is a page of sessions with the paging the clients show.
type sessionsPage[T any] struct {
	Total        int `json:"total"`
	NumPages     int `json:"numPages"`
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	Sessions     []T `json:"sessions"`
}

func (h *Handler) userSessions(ctx context.Context, userID, mediaItemID string) ([]db.PlaybackSession, error) {
	var rows []db.PlaybackSession
	err := h.db.Select(ctx, &rows, `SELECT * FROM playbackSessions WHERE userId = ? AND (? = '' OR mediaItemId = ?) ORDER BY updatedAt DESC`, userID, mediaItemID, mediaItemID)
	return rows, err
}

// listeningSessions answers a page of the user's sessions, listed as the admin history lists them.
func (h *Handler) listeningSessions(w http.ResponseWriter, r *http.Request, userID string) error {
	return h.sessionsOf(w, r, userID, "")
}

func (h *Handler) sessionsOf(w http.ResponseWriter, r *http.Request, userID, mediaItemID string) error {
	rows, err := h.userSessions(r.Context(), userID, mediaItemID)
	if err != nil {
		return err
	}
	page, perPage := pageOf(r)
	if perPage <= 0 {
		perPage = 10
	}
	start := len(rows) // a page past the end is empty; page*perPage could overflow
	if page <= len(rows)/perPage {
		start = min(page*perPage, len(rows))
	}
	sessions, err := h.playback.SessionsOf(r.Context(), rows[start:start+min(perPage, len(rows)-start)])
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, sessionsPage[playback.HistorySession]{len(rows), int(math.Ceil(float64(len(rows)) / float64(perPage))), page, perPage, sessions})
}

type userScoped func(w http.ResponseWriter, r *http.Request, userID string) error

func own(next userScoped) userHandler {
	return func(w http.ResponseWriter, r *http.Request, me *auth.Principal) error { return next(w, r, me.ID) }
}

// ofUser runs next for the user in the URL: the user themselves or any user for an admin (403 to others, 404 when missing).
func (h *Handler) ofUser(next userScoped) userHandler {
	return func(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
		id := r.PathValue("id")
		if !me.IsAdmin() && me.ID != id {
			return httpx.Forbidden()
		}
		if _, err := h.userRow(r.Context(), id); err != nil {
			return err
		}
		return next(w, r, id)
	}
}

// itemListeningSessions is GET /api/me/item/listening-sessions/{id}[/{episodeId}]
// (MeController.js:185): the episode is found by its id alone, a podcast
// without one is a 404 before the access check, and an episode id replaces the
// media item id.
func (h *Handler) itemListeningSessions(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	itemID, episodeID := r.PathValue("id"), r.PathValue("episodeId")
	_, exists, allowed, err := me.ReadItem(ctx, h.db, itemID)
	if err != nil {
		return err
	}
	var item struct {
		MediaID   string `db:"mediaId"`
		MediaType string `db:"mediaType"`
	}
	if !exists || h.db.Get(ctx, &item, `SELECT mediaId, mediaType FROM libraryItems WHERE id = ?`, itemID) != nil {
		return httpx.NotFound()
	}
	episodeFound := false
	if episodeID != "" {
		var n int
		if err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM podcastEpisodes WHERE id = ?`, episodeID); err != nil {
			return err
		}
		episodeFound = n > 0
	}
	if item.MediaType == "podcast" && !episodeFound {
		return httpx.NotFound()
	}
	if !allowed {
		return httpx.Forbidden()
	}
	if episodeFound {
		item.MediaID = episodeID
	}
	return h.sessionsOf(w, r, me.ID, item.MediaID)
}

type itemTime struct {
	ID            *string         `json:"id"`
	TimeListening *float64        `json:"timeListening"` // null until some time was listened
	MediaMetadata json.RawMessage `json:"mediaMetadata"`
}

type listeningStats struct {
	TotalTime      float64                   `json:"totalTime"`
	Items          map[string]*itemTime      `json:"items"`
	Days           totals[string]            `json:"days"`
	DayOfWeek      totals[string]            `json:"dayOfWeek"`
	Today          float64                   `json:"today"`
	RecentSessions []playback.HistorySession `json:"recentSessions"`
}

// listeningStats answers the user's listening totals over all their sessions
// (ApiRouter.getUserListeningStatsHelpers, ApiRouter.js:535-575). Items are keyed
// by the library item id, "null" for sessions without one.
func (h *Handler) listeningStats(w http.ResponseWriter, r *http.Request, userID string) error {
	rows, err := h.userSessions(r.Context(), userID, "")
	if err != nil {
		return err
	}
	stats := listeningStats{Items: map[string]*itemTime{}, Days: totals[string]{}, DayOfWeek: totals[string]{}}
	today := time.Now().Format("2006-01-02")
	for _, row := range rows {
		s := playback.StoredSession(row)
		seconds := db.Val(s.TimeListening)
		stats.TotalTime += seconds
		if day := db.Val(s.DayOfWeek); day != "" {
			stats.DayOfWeek.add(day, seconds)
		}
		if date := db.Val(s.Date); date != "" && seconds > 0 {
			stats.Days.add(date, seconds)
			if date == today {
				stats.Today += seconds
			}
		}
		key := db.Val(s.LibraryItemID)
		if key == "" {
			key = "null"
		}
		if current := stats.Items[key]; current != nil {
			current.TimeListening = db.Ptr(db.Val(current.TimeListening) + seconds)
		} else {
			stats.Items[key] = &itemTime{ID: s.LibraryItemID, TimeListening: s.TimeListening, MediaMetadata: s.MediaMetadata}
		}
	}
	recent, err := h.playback.SessionsOf(r.Context(), rows[:min(len(rows), 10)])
	if err != nil {
		return err
	}
	stats.RecentSessions = recent
	return httpx.JSON(w, r, stats)
}
