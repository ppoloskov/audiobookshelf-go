package user_me

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// playedSession records one closed playback session of the novel as a user
// (setup through the playback routes, owned by another packet): it listens
// for the given seconds and returns the session id and the UTC dates before
// and after the playback (they differ only when the run crosses midnight).
func playedSession(t *testing.T, e *harness.Env, token string, seconds float64) (id string, before, after time.Time) {
	t.Helper()
	resp := e.Post(t, token, "/api/items/"+e.IDs["novel"]+"/play", map[string]any{
		"deviceInfo":  map[string]any{"clientName": "me-packet", "deviceId": "me-packet-device"},
		"mediaPlayer": "me-packet", "forceDirectPlay": true, "supportedMimeTypes": []string{},
	})
	wantStatus(t, resp, http.StatusOK, "start playback")
	var started struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &started)
	if started.ID == "" {
		t.Fatalf("playback session has no id: %s", resp.Body)
	}
	before = time.Now().UTC()
	wantStatus(t, e.Post(t, token, "/api/session/"+started.ID+"/close", map[string]any{"currentTime": 3, "timeListened": seconds}), http.StatusOK, "close playback")
	return started.ID, before, time.Now().UTC()
}

func playedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r105", Invariant: "INV-R105-CHANGED-001", Case: "edge", State: "stateless", Assertion: "listening-sessions-show-new-session",
			Sources: []string{listeningSource, "server/managers/PlaybackSessionManager.js:421-442"},
			Claim:   "A playback session closed after listening appears first in GET /api/me/listening-sessions and in the item's list with the listened seconds, today's date and weekday, the book ids and the display title.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "finisher")
				id, before, after := playedSession(t, e, token, 5)
				page := getListening(t, e, token, "/api/me/listening-sessions")
				if page.Total != 1 || page.Sessions[0].ID != id {
					t.Fatalf("the new session must be the only one: %+v", page)
				}
				session := page.Sessions[0]
				if session.UserID != finisherID || session.LibraryID != e.IDs["library"] || session.LibraryItemID == nil || *session.LibraryItemID != e.IDs["novel"] ||
					session.BookID == nil || *session.BookID != "85ab0f32-b17f-4c9a-883e-a61e260f1e46" || session.EpisodeID != nil || session.MediaType != "book" ||
					session.DisplayTitle != "Single File Novel" || session.TimeListening == nil || *session.TimeListening != 5 || session.CurrentTime != 3 ||
					session.Date == nil || session.DayOfWeek == nil || !dayMatches(session, before, after) ||
					session.MediaPlayer == nil || *session.MediaPlayer != "me-packet" || session.DeviceInfo["deviceId"] != "me-packet-device" || session.UpdatedAt < session.StartedAt {
					t.Fatalf("new session fields wrong: %+v", session)
				}
				item := getListening(t, e, token, "/api/me/item/listening-sessions/"+e.IDs["novel"])
				if item.Total != 1 || item.Sessions[0].ID != id {
					t.Fatalf("the item's list must hold the new session: %+v", item)
				}
			},
		},
		{
			Requirement: "route-r107", Invariant: "INV-R107-CHANGED-001", Case: "edge", State: "stateless", Assertion: "listening-stats-count-today",
			Sources: []string{statsSource, "server/managers/PlaybackSessionManager.js:421-442"},
			Claim:   "After a session with 7 seconds of listening today, GET /api/me/listening-stats adds 7 to totalTime, today, the item's timeListening and today's entry of days and dayOfWeek, and lists the session in recentSessions first.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "finisher")
				var before, after listeningStats
				getJSON(t, e, token, "/api/me/listening-stats", &before)
				started := time.Now().UTC()
				id, _, ended := playedSession(t, e, token, 7)
				getJSON(t, e, token, "/api/me/listening-stats", &after)
				if len(after.RecentSessions) == 0 || after.RecentSessions[0].ID != id || after.RecentSessions[0].Date == nil || after.RecentSessions[0].DayOfWeek == nil {
					t.Fatalf("the new session must head recentSessions: %+v", after.RecentSessions)
				}
				date, weekday := *after.RecentSessions[0].Date, *after.RecentSessions[0].DayOfWeek
				if !near(after.TotalTime-before.TotalTime, 7) || !near(after.Items[e.IDs["novel"]].TimeListening-before.Items[e.IDs["novel"]].TimeListening, 7) ||
					!near(after.Days[date]-before.Days[date], 7) || !near(after.DayOfWeek[weekday]-before.DayOfWeek[weekday], 7) {
					t.Fatalf("stats did not grow by the listened time:\n before %+v\n after %+v", before, after)
				}
				// today moves by the session only when the whole run stayed on the session's day
				if day := started.Format("2006-01-02"); day == date && ended.Format("2006-01-02") == date && !near(after.Today-before.Today, 7) {
					t.Fatalf("today must grow by 7: before %v after %v", before.Today, after.Today)
				}
			},
		},
	}
}

// dayMatches reports whether the session's date and weekday are those of a UTC
// day the playback ran on.
func dayMatches(session listeningSession, days ...time.Time) bool {
	for _, day := range days {
		if *session.Date == day.Format("2006-01-02") && *session.DayOfWeek == day.Weekday().String() {
			return true
		}
	}
	return false
}
