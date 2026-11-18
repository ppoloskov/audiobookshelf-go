package user_admin

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	sessionsSource = "server/controllers/UserController.js:434-461"
	statsSource    = "server/controllers/UserController.js:471-474"
	helperSource   = "server/routers/ApiRouter.js:525-578"
)

// fixtureSessionOrder is the seeded listener's playback sessions, newest update first.
var fixtureSessionOrder = []string{
	"193bf2e2-70c3-4b91-99e2-40bc7398812a",
	"f166755e-ad8c-4334-adb9-eea51bc07c18",
	"57d12836-1090-4347-b017-0c48eb482e11",
	"ab27584f-2573-49d1-99d9-5baf7ce82c80",
	"legacy-null-0003",
	"legacy-null-0002",
	"legacy-null-0001",
}

// sessionsPage is the GET /api/users/:id/listening-sessions response.
type sessionsPage struct {
	Total        int `json:"total"`
	NumPages     int `json:"numPages"`
	Page         int `json:"page"`
	ItemsPerPage int `json:"itemsPerPage"`
	Sessions     []struct {
		ID            string         `json:"id"`
		UserID        string         `json:"userId"`
		DisplayTitle  string         `json:"displayTitle"`
		MediaType     string         `json:"mediaType"`
		LibraryItemID *string        `json:"libraryItemId"`
		Date          *string        `json:"date"`
		DisplayAuthor string         `json:"displayAuthor"`
		PlayMethod    *int           `json:"playMethod"`
		MediaPlayer   *string        `json:"mediaPlayer"`
		DeviceInfo    map[string]any `json:"deviceInfo"`
		TimeListening *float64       `json:"timeListening"`
		CurrentTime   float64        `json:"currentTime"`
		StartedAt     int64          `json:"startedAt"`
		UpdatedAt     int64          `json:"updatedAt"`
		User          struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	} `json:"sessions"`
}

func (p sessionsPage) ids() []string {
	out := []string{}
	for _, session := range p.Sessions {
		out = append(out, session.ID)
	}
	return out
}

// getSessions reads GET /api/users/:id/listening-sessions.
func getSessions(t *testing.T, e *harness.Env, token, id string, query url.Values) sessionsPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/users/" + id + "/listening-sessions", Token: token, Query: query})
	wantStatus(t, resp, http.StatusOK, "GET listening-sessions of "+id)
	var page sessionsPage
	resp.JSON(t, &page)
	if page.Sessions == nil {
		t.Fatalf("sessions must be an array: %s", resp.Body)
	}
	return page
}

// stats is the GET /api/users/:id/listening-stats response.
type stats struct {
	TotalTime float64 `json:"totalTime"`
	Items     map[string]struct {
		ID            string         `json:"id"`
		TimeListening float64        `json:"timeListening"`
		MediaMetadata map[string]any `json:"mediaMetadata"`
	} `json:"items"`
	Days           map[string]float64 `json:"days"`
	DayOfWeek      map[string]float64 `json:"dayOfWeek"`
	Today          float64            `json:"today"`
	RecentSessions []struct {
		ID string `json:"id"`
	} `json:"recentSessions"`
}

// getStats reads GET /api/users/:id/listening-stats.
func getStats(t *testing.T, e *harness.Env, token, id string) stats {
	t.Helper()
	resp := e.Get(t, token, "/api/users/"+id+"/listening-stats")
	wantStatus(t, resp, http.StatusOK, "GET listening-stats of "+id)
	var out stats
	resp.JSON(t, &out)
	if out.Items == nil || out.Days == nil || out.DayOfWeek == nil || out.RecentSessions == nil {
		t.Fatalf("stats objects and arrays must be present: %s", resp.Body)
	}
	return out
}

// fixtureSession is the literal content of a seeded listener session.
type fixtureSession struct {
	id, title, author, player, device, deviceName, client string
	playMethod                                            int
	listened, current                                     float64
	started, updated                                      int64
}

// fixtureSessions are the first four (newest) seeded sessions of the listener.
var fixtureSessions = []fixtureSession{
	{"193bf2e2-70c3-4b91-99e2-40bc7398812a", "Pilot Episode", "Fixture Host", "exo-player", "trial-android-0001", "Google Pixel 8", "Abs Android", 0, 10.5, 10.5, 1793709988011, 1793709988126},
	{"f166755e-ad8c-4334-adb9-eea51bc07c18", "The Beginning", "Terry Fixture", "exo-player", "trial-android-0001", "Google Pixel 8", "Abs Android", 0, 27.5, 27.5, 1793709986924, 1793709986971},
	{"57d12836-1090-4347-b017-0c48eb482e11", "Third Time", "Fixture Host", "AVPlayer", "trial-ios-0001", "Apple iPhone15,2", "Abs iOS", 3, 6, 6, 1793666787321, 1793666793321},
	{"ab27584f-2573-49d1-99d9-5baf7ce82c80", "The Middle", "Terry Fixture", "AVPlayer", "trial-ios-0001", "Apple iPhone15,2", "Abs iOS", 3, 9, 9, 1793623587321, 1793623596321},
}

func near(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }

func listeningScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r078", Invariant: "INV-R078-SAMPLED", Case: "normal", State: "stateless", Assertion: "user-sessions-listener",
			Sources: []string{"server/routers/ApiRouter.js:140", sessionsSource, helperSource},
			Claim:   "GET /api/users/:id/listening-sessions as root returns the user's seven playback sessions newest update first, each with the target user's id and username, and the paging envelope.",
			Run: func(t *testing.T, e *harness.Env) {
				page := getSessions(t, e, e.Token(t, "root"), e.IDs["listener"], nil)
				if page.Total != 7 || page.NumPages != 1 || page.Page != 0 || page.ItemsPerPage != 10 || !slices.Equal(page.ids(), fixtureSessionOrder) {
					t.Fatalf("envelope or order wrong: %+v", page)
				}
				for _, session := range page.Sessions {
					if session.User.ID != e.IDs["listener"] || session.User.Username != "trial-listener" || session.UserID != e.IDs["listener"] {
						t.Fatalf("every session carries the target user: %+v", session)
					}
				}
				first := page.Sessions[0]
				if first.MediaType != "podcast" || first.LibraryItemID == nil || *first.LibraryItemID != e.IDs["podcast"] || first.Date == nil || *first.Date != "2026-11-03" {
					t.Fatalf("session fields wrong: %+v", first)
				}
				for i, want := range fixtureSessions {
					got := page.Sessions[i]
					if got.ID != want.id || got.DisplayTitle != want.title || got.DisplayAuthor != want.author || got.PlayMethod == nil || *got.PlayMethod != want.playMethod ||
						got.MediaPlayer == nil || *got.MediaPlayer != want.player || got.TimeListening == nil || *got.TimeListening != want.listened || got.CurrentTime != want.current ||
						got.StartedAt != want.started || got.UpdatedAt != want.updated || got.DeviceInfo["deviceId"] != want.device || got.DeviceInfo["deviceName"] != want.deviceName || got.DeviceInfo["clientName"] != want.client {
						t.Fatalf("session %d wrong: want %+v got %+v", i, want, got)
					}
				}
				legacy := page.Sessions[4]
				if legacy.PlayMethod != nil || legacy.TimeListening != nil || len(legacy.DeviceInfo) != 0 || legacy.CurrentTime != 33.75 {
					t.Fatalf("a legacy session keeps its nulls: %+v", legacy)
				}
			},
		},
		{
			Requirement: "route-r078", Invariant: "INV-R078-PAGING-001", Case: "edge", State: "stateless", Assertion: "user-sessions-paging-and-empty",
			Sources: []string{sessionsSource},
			Claim:   "itemsPerPage and page slice the sessions and numPages rounds up; a page beyond the end is an empty array; a user without sessions has total 0 and numPages 0.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				for page, want := range map[int][]string{0: fixtureSessionOrder[0:3], 1: fixtureSessionOrder[3:6], 2: fixtureSessionOrder[6:7], 3: {}} {
					got := getSessions(t, e, root, e.IDs["listener"], url.Values{"itemsPerPage": {"3"}, "page": {strconv.Itoa(page)}})
					if got.Total != 7 || got.NumPages != 3 || got.Page != page || got.ItemsPerPage != 3 || !slices.Equal(got.ids(), want) {
						t.Fatalf("page %d of 3 wrong: %+v", page, got)
					}
				}
				empty := getSessions(t, e, root, rootID, nil)
				if empty.Total != 0 || empty.NumPages != 0 || empty.Page != 0 || empty.ItemsPerPage != 10 || len(empty.Sessions) != 0 {
					t.Fatalf("a user without sessions: %+v", empty)
				}
			},
		},
		{
			Requirement: "route-r078", Invariant: "INV-R078-AUTH-001", Case: "error", State: "stateless", Assertion: "user-sessions-access-rules",
			Sources: []string{"server/Server.js:354", sessionsSource, middlewareJS},
			Claim:   "GET /api/users/:id/listening-sessions answers 401 without a valid token, 404 for an unknown id to an admin, and to a non-admin 403 for any other user's id but 200 for its own id.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/users/"+e.IDs["listener"]+"/listening-sessions")
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/users/"+unknownID+"/listening-sessions"), http.StatusNotFound, "unknown id")
				listener := e.Token(t, "listener")
				for _, id := range []string{rootID, finisherID, unknownID} {
					wantStatus(t, e.Get(t, listener, "/api/users/"+id+"/listening-sessions"), http.StatusForbidden, "listener reads "+id)
				}
				own := getSessions(t, e, listener, e.IDs["listener"], nil)
				if own.Total != 7 || !slices.Equal(own.ids(), fixtureSessionOrder) {
					t.Fatalf("a user may read its own sessions through the admin route: %+v", own)
				}
			},
		},
		{
			Requirement: "route-r079", Invariant: "INV-R079-SAMPLED", Case: "normal", State: "stateless", Assertion: "user-stats-listener",
			Sources: []string{"server/routers/ApiRouter.js:141", statsSource, helperSource},
			Claim:   "GET /api/users/:id/listening-stats as root sums the listener's sessions: totalTime, per-item time and metadata, per-date and per-weekday time, today 0 and the ten most recent sessions.",
			Run: func(t *testing.T, e *harness.Env) {
				got := getStats(t, e, e.Token(t, "root"), e.IDs["listener"])
				if !near(got.TotalTime, 65.4) || got.Today != 0 {
					t.Fatalf("totals wrong: %+v", got)
				}
				for item, want := range map[string]float64{e.IDs["podcast"]: 16.5, e.IDs["saga1"]: 39.9, e.IDs["saga2"]: 9} {
					entry, ok := got.Items[item]
					if !ok || entry.ID != item || !near(entry.TimeListening, want) {
						t.Fatalf("item %s wrong: %+v", item, entry)
					}
				}
				for item, title := range map[string]string{e.IDs["podcast"]: "Fixture Cast", e.IDs["saga1"]: "The Beginning", e.IDs["saga2"]: "The Middle"} {
					if got.Items[item].MediaMetadata["title"] != title {
						t.Fatalf("item %s metadata title must be %q: %+v", item, title, got.Items[item].MediaMetadata)
					}
				}
				for day, want := range map[string]float64{"2026-11-03": 44, "2026-11-02": 9, "2026-02-02": 12.4} {
					if !near(got.Days[day], want) {
						t.Fatalf("day %s must be %v: %v", day, want, got.Days)
					}
				}
				for weekday, want := range map[string]float64{"Tuesday": 38, "Sunday": 15, "Monday": 12.4} {
					if !near(got.DayOfWeek[weekday], want) {
						t.Fatalf("weekday %s must be %v: %v", weekday, want, got.DayOfWeek)
					}
				}
				ids := []string{}
				for _, session := range got.RecentSessions {
					ids = append(ids, session.ID)
				}
				if len(got.Days) != 3 || !slices.Equal(ids, fixtureSessionOrder) {
					t.Fatalf("days or recent sessions wrong: %v %v", got.Days, ids)
				}
			},
		},
		{
			Requirement: "route-r079", Invariant: "INV-R079-CHANGED-001", Case: "edge", State: "stateless", Assertion: "user-stats-empty-and-new-session",
			Sources: []string{statsSource, helperSource},
			Claim:   "A user without sessions has zero stats and empty objects; after the user listens for five seconds today's time, the date, the weekday and the item time each show five.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				empty := getStats(t, e, root, rootID)
				if empty.TotalTime != 0 || empty.Today != 0 || len(empty.Items) != 0 || len(empty.Days) != 0 || len(empty.DayOfWeek) != 0 || len(empty.RecentSessions) != 0 {
					t.Fatalf("empty stats wrong: %+v", empty)
				}
				user := createUser(t, e, map[string]any{"username": "stats-player", "type": "user"})
				token := loginToken(t, e, user.Username)
				now := time.Now().UTC()
				play := e.Post(t, token, "/api/items/"+e.IDs["novel"]+"/play", map[string]any{
					"deviceInfo": map[string]any{"clientName": "admin-packet", "deviceId": "admin-packet-stats"}, "mediaPlayer": "admin-packet", "forceDirectPlay": true, "supportedMimeTypes": []string{},
				})
				wantStatus(t, play, http.StatusOK, "start playback")
				var started struct {
					ID string `json:"id"`
				}
				play.JSON(t, &started)
				wantStatus(t, e.Post(t, token, "/api/session/"+started.ID+"/close", map[string]any{"currentTime": 3, "timeListened": 5}), http.StatusOK, "close playback")
				got := getStats(t, e, root, user.ID)
				date := now.Format("2006-01-02")
				entry := got.Items[e.IDs["novel"]]
				if got.TotalTime != 5 || got.Today != 5 || got.Days[date] != 5 || got.DayOfWeek[now.Weekday().String()] != 5 || entry.ID != e.IDs["novel"] || entry.TimeListening != 5 ||
					entry.MediaMetadata["title"] != "Single File Novel" || len(got.RecentSessions) != 1 || got.RecentSessions[0].ID != started.ID {
					t.Fatalf("stats after listening wrong: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r079", Invariant: "INV-R079-AUTH-001", Case: "error", State: "stateless", Assertion: "user-stats-access-rules",
			Sources: []string{"server/Server.js:354", statsSource, middlewareJS},
			Claim:   "GET /api/users/:id/listening-stats answers 401 without a valid token, 404 for an unknown id to an admin, and to a non-admin 403 for any other user's id but 200 for its own id.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/users/"+e.IDs["listener"]+"/listening-stats")
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/users/"+unknownID+"/listening-stats"), http.StatusNotFound, "unknown id")
				listener := e.Token(t, "listener")
				for _, id := range []string{rootID, finisherID, unknownID} {
					wantStatus(t, e.Get(t, listener, "/api/users/"+id+"/listening-stats"), http.StatusForbidden, "listener reads "+id)
				}
				if own := getStats(t, e, listener, e.IDs["listener"]); !near(own.TotalTime, 65.4) {
					t.Fatalf("a user may read its own stats through the admin route: %+v", own)
				}
			},
		},
	}
}
