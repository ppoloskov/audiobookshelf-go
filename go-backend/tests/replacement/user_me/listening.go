package user_me

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	listeningSource = "server/controllers/MeController.js:157-181"
	itemSource      = "server/controllers/MeController.js:185-225"
	statsSource     = "server/routers/ApiRouter.js:535-575"
)

// fixtureSessions returns the listener's seeded playback sessions, newest
// update first, as the listening-sessions routes serve them. MediaMetadata and
// DeviceInfo are left nil: callers compare those separately.
func fixtureSessions(e *harness.Env) []listeningSession {
	user := e.IDs["listener"]
	book, podcast := e.IDs["library"], e.IDs["podcastLibrary"]
	bookID := func(s string) *string { return &s }
	return []listeningSession{
		{ID: fixtureSessionOrder[0], UserID: user, LibraryID: podcast, LibraryItemID: strp(e.IDs["podcast"]), EpisodeID: strp(e.IDs["ep1"]), MediaType: "podcast", DisplayTitle: "Pilot Episode", DisplayAuthor: "Fixture Host", Duration: f64p(24.058776), PlayMethod: intp(0), MediaPlayer: strp("exo-player"), Date: strp("2026-11-03"), DayOfWeek: strp("Tuesday"), TimeListening: f64p(10.5), CurrentTime: 10.5, StartedAt: 1793709988011, UpdatedAt: 1793709988126},
		{ID: fixtureSessionOrder[1], UserID: user, LibraryID: book, LibraryItemID: strp(e.IDs["saga1"]), BookID: bookID("5e33de11-3e94-4c51-87d8-d06d2928ac38"), MediaType: "book", DisplayTitle: "The Beginning", DisplayAuthor: "Terry Fixture", Duration: f64p(60.186123), PlayMethod: intp(0), MediaPlayer: strp("exo-player"), Date: strp("2026-11-03"), DayOfWeek: strp("Tuesday"), TimeListening: f64p(27.5), CurrentTime: 27.5, StartedAt: 1793709986924, UpdatedAt: 1793709986971},
		{ID: fixtureSessionOrder[2], UserID: user, LibraryID: podcast, LibraryItemID: strp(e.IDs["podcast"]), EpisodeID: strp(e.IDs["ep3"]), MediaType: "podcast", DisplayTitle: "Third Time", DisplayAuthor: "Fixture Host", Duration: f64p(12.068571), PlayMethod: intp(3), MediaPlayer: strp("AVPlayer"), Date: strp("2026-11-03"), DayOfWeek: strp("Sunday"), TimeListening: f64p(6), CurrentTime: 6, StartedAt: 1793666787321, UpdatedAt: 1793666793321},
		{ID: fixtureSessionOrder[3], UserID: user, LibraryID: book, LibraryItemID: strp(e.IDs["saga2"]), BookID: bookID("5c5badbc-e141-4fbc-8a5b-31c1ba7d33af"), MediaType: "book", DisplayTitle: "The Middle", DisplayAuthor: "Terry Fixture", Duration: f64p(30), PlayMethod: intp(3), MediaPlayer: strp("AVPlayer"), Date: strp("2026-11-02"), DayOfWeek: strp("Sunday"), TimeListening: f64p(9), CurrentTime: 9, StartedAt: 1793623587321, UpdatedAt: 1793623596321},
		{ID: fixtureSessionOrder[4], UserID: user, LibraryID: book, BookID: bookID("5e33de11-3e94-4c51-87d8-d06d2928ac38"), MediaType: "book", DisplayTitle: "The Beginning", DisplayAuthor: "Terry Fixture", Duration: f64p(60.186123), MediaPlayer: strp("AVPlayer"), StartTime: 1.25, CurrentTime: 33.75, StartedAt: 1772445600000, UpdatedAt: 1772445660000, Date: strp("2026-03-02"), DayOfWeek: strp("Monday")},
		{ID: fixtureSessionOrder[5], UserID: user, LibraryID: book, LibraryItemID: strp(e.IDs["saga1"]), BookID: bookID("5e33de11-3e94-4c51-87d8-d06d2928ac38"), MediaType: "book", DisplayTitle: "The Beginning", Duration: f64p(60.186123), PlayMethod: intp(3), Date: strp("2026-02-02"), DayOfWeek: strp("Monday"), TimeListening: f64p(12.4), CurrentTime: 12.4, StartedAt: 1770026400000, UpdatedAt: 1770026460000},
		{ID: fixtureSessionOrder[6], UserID: user, LibraryID: book, LibraryItemID: strp(e.IDs["saga1"]), BookID: bookID("5e33de11-3e94-4c51-87d8-d06d2928ac38"), MediaType: "book", Date: strp("2026-01-15"), DayOfWeek: strp("Thursday"), StartedAt: 1768471200000, UpdatedAt: 1768471500000},
	}
}

func intp(i int) *int { return &i }

// seededDevices are the devices of the seeded sessions as clients read them.
var seededDevices = map[string]map[string]any{
	"trial-android-0001": {"id": "a3f1efdb-c8f2-4feb-9e55-0fe835835fa2", "deviceId": "trial-android-0001", "ipAddress": "127.0.0.1", "clientVersion": "0.9.79-beta", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": "34", "clientName": "Abs Android", "deviceName": "Google Pixel 8"},
	"trial-ios-0001":     {"id": "1675ea96-d556-433c-a991-c69561110b7f", "deviceId": "trial-ios-0001", "ipAddress": "127.0.0.1", "clientVersion": "0.9.79-beta", "manufacturer": "Apple", "model": "iPhone15,2", "browserVersion": "17", "sdkVersion": "", "clientName": "Abs iOS", "deviceName": "Apple iPhone15,2"},
}

// requireStoredFields compares the members of a served seeded session that
// the exact comparison leaves out (coverPath, serverVersion, chapters,
// mediaMetadata, deviceInfo) with the stored row and the seeded device.
func requireStoredFields(t *testing.T, e *harness.Env, session listeningSession) {
	t.Helper()
	rows := e.Rows(t, "SELECT coverPath, serverVersion, mediaMetadata, deviceId FROM playbackSessions WHERE id = ?", session.ID)
	if len(rows) != 1 {
		t.Fatalf("session %s has no stored row", session.ID)
	}
	row := rows[0]
	text := func(column string) *string {
		if value, ok := row[column].(string); ok && value != "" {
			return &value
		}
		return nil
	}
	equalJSON(t, session.ID+" coverPath", session.CoverPath, text("coverPath"))
	equalJSON(t, session.ID+" serverVersion", session.ServerVersion, text("serverVersion"))
	if session.Chapters == nil || len(session.Chapters) != 0 {
		t.Fatalf("session %s chapters must be an empty array: %v", session.ID, session.Chapters)
	}
	var metadata any
	if stored := text("mediaMetadata"); stored != nil {
		if err := json.Unmarshal([]byte(*stored), &metadata); err != nil {
			t.Fatalf("stored metadata of %s: %v", session.ID, err)
		}
	}
	equalJSON(t, session.ID+" mediaMetadata", session.MediaMetadata, metadata)
	if stored := text("deviceId"); stored == nil {
		if len(session.DeviceInfo) != 0 {
			t.Fatalf("session %s has no device, deviceInfo must be empty: %v", session.ID, session.DeviceInfo)
		}
		return
	}
	var want map[string]any
	for _, device := range seededDevices {
		if device["id"] == *text("deviceId") {
			want = map[string]any{"userId": e.IDs["listener"]}
			for key, value := range device {
				want[key] = value
			}
		}
	}
	equalJSON(t, session.ID+" deviceInfo", session.DeviceInfo, want)
}

// withoutOpenFields clears the open-ended members so sessions can be compared exactly.
func withoutOpenFields(sessions []listeningSession) []listeningSession {
	out := slices.Clone(sessions)
	for i := range out {
		out[i].MediaMetadata = nil
		out[i].DeviceInfo = nil
		out[i].Chapters = nil
		out[i].CoverPath = nil
		out[i].ServerVersion = nil
	}
	return out
}

// getListening reads one of the listening-sessions routes.
func getListening(t *testing.T, e *harness.Env, token, path string, query ...string) listeningPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: path, Token: token, Query: pageQuery(query...)})
	wantStatus(t, resp, http.StatusOK, "GET "+path)
	var page listeningPage
	resp.JSON(t, &page)
	return page
}

// listeningStats is the GET /api/me/listening-stats response.
type listeningStats struct {
	TotalTime      float64              `json:"totalTime"`
	Items          map[string]statsItem `json:"items"`
	Days           map[string]float64   `json:"days"`
	DayOfWeek      map[string]float64   `json:"dayOfWeek"`
	Today          float64              `json:"today"`
	RecentSessions []listeningSession   `json:"recentSessions"`
}

type statsItem struct {
	ID            string         `json:"id"`
	TimeListening float64        `json:"timeListening"`
	MediaMetadata map[string]any `json:"mediaMetadata"`
}

func listeningScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r105", Invariant: "INV-R105-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "listening-sessions-listener",
			Sources: []string{"server/routers/ApiRouter.js:179", listeningSource, "server/models/PlaybackSession.js:79-108", "server/objects/PlaybackSession.js:163-167"},
			Claim:   "GET /api/me/listening-sessions returns the listener's seven playback sessions newest update first with the paging envelope and the old-session fields clients read, including device info and the legacy rows with null fields.",
			Run: func(t *testing.T, e *harness.Env) {
				page := getListening(t, e, e.Token(t, "listener"), "/api/me/listening-sessions")
				if page.Total != 7 || page.NumPages != 1 || page.Page != 0 || page.ItemsPerPage != 10 {
					t.Fatalf("envelope wrong: %+v", page)
				}
				equalJSON(t, "sessions", withoutOpenFields(page.Sessions), withoutOpenFields(fixtureSessions(e)))
				devices := map[string]string{fixtureSessionOrder[0]: "trial-android-0001", fixtureSessionOrder[1]: "trial-android-0001", fixtureSessionOrder[2]: "trial-ios-0001", fixtureSessionOrder[3]: "trial-ios-0001"}
				for _, session := range page.Sessions {
					want, hasDevice := devices[session.ID]
					if hasDevice && session.DeviceInfo["deviceId"] != want {
						t.Fatalf("session %s device wrong: %v", session.ID, session.DeviceInfo)
					}
					if !hasDevice && len(session.DeviceInfo) != 0 {
						t.Fatalf("session %s must have an empty deviceInfo: %v", session.ID, session.DeviceInfo)
					}
				}
				for _, session := range page.Sessions {
					requireStoredFields(t, e, session)
				}
				first := page.Sessions[0]
				if first.DeviceInfo["deviceName"] != "Google Pixel 8" || first.DeviceInfo["clientName"] != "Abs Android" || first.MediaMetadata["title"] != "Fixture Cast" {
					t.Fatalf("first session device or metadata wrong: %+v", first)
				}
			},
		},
		{
			Requirement: "route-r105", Invariant: "INV-R105-PAGING-001", Case: "edge", State: "stateless", Assertion: "listening-sessions-paging",
			Sources: []string{"server/controllers/MeController.js:158-172"},
			Claim:   "GET /api/me/listening-sessions slices by itemsPerPage and page, falls back to 10 per page for 0 or a non-number, returns an empty page past the end, and gives other users only their own (none for the finisher).",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				const path = "/api/me/listening-sessions"
				page := getListening(t, e, token, path, "itemsPerPage", "3", "page", "1")
				if page.Total != 7 || page.NumPages != 3 || page.Page != 1 || page.ItemsPerPage != 3 || !slices.Equal(page.sessionIDs(), fixtureSessionOrder[3:6]) {
					t.Fatalf("page 1 of 3 wrong: %+v", page)
				}
				if last := getListening(t, e, token, path, "itemsPerPage", "3", "page", "2"); !slices.Equal(last.sessionIDs(), fixtureSessionOrder[6:]) {
					t.Fatalf("last page wrong: %v", last.sessionIDs())
				}
				for _, value := range []string{"0", "abc"} {
					if fallback := getListening(t, e, token, path, "itemsPerPage", value); fallback.ItemsPerPage != 10 || len(fallback.Sessions) != 7 {
						t.Fatalf("itemsPerPage=%s must fall back to 10: %+v", value, fallback)
					}
				}
				if past := getListening(t, e, token, path, "page", "9"); past.Total != 7 || past.Page != 9 || len(past.Sessions) != 0 {
					t.Fatalf("page past the end wrong: %+v", past)
				}
				if none := getListening(t, e, e.Token(t, "finisher"), path); none.Total != 0 || none.NumPages != 0 || none.Sessions == nil || len(none.Sessions) != 0 {
					t.Fatalf("finisher has no sessions: %+v", none)
				}
			},
		},
		{
			Requirement: "route-r105", Invariant: "INV-R105-CONTRACT-001", Case: "error", State: "stateless", Assertion: "listening-sessions-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:179"},
			Claim:   "GET /api/me/listening-sessions answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/listening-sessions")
			},
		},
		{
			Requirement: "route-r106", Invariant: "INV-R106-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "item-listening-sessions-book-and-episode",
			Sources: []string{"server/routers/ApiRouter.js:180", itemSource},
			Claim:   "GET /api/me/item/listening-sessions/:libraryItemId lists the caller's sessions of that book newest first; with an episode id of a podcast it lists only that episode's sessions.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				book := getListening(t, e, token, "/api/me/item/listening-sessions/"+e.IDs["saga1"])
				if book.Total != 4 || book.NumPages != 1 || book.Page != 0 || book.ItemsPerPage != 10 {
					t.Fatalf("book envelope wrong: %+v", book)
				}
				want := []listeningSession{fixtureSessions(e)[1], fixtureSessions(e)[4], fixtureSessions(e)[5], fixtureSessions(e)[6]}
				equalJSON(t, "book sessions", withoutOpenFields(book.Sessions), withoutOpenFields(want))
				for _, session := range book.Sessions {
					requireStoredFields(t, e, session)
				}
				for episode, index := range map[string]int{e.IDs["ep1"]: 0, e.IDs["ep3"]: 2} {
					page := getListening(t, e, token, "/api/me/item/listening-sessions/"+e.IDs["podcast"]+"/"+episode)
					if page.Total != 1 || page.NumPages != 1 || !slices.Equal(page.sessionIDs(), fixtureSessionOrder[index:index+1]) {
						t.Fatalf("episode %s sessions wrong: %+v", episode, page)
					}
				}
				if page := getListening(t, e, token, "/api/me/item/listening-sessions/"+e.IDs["saga2"]); page.Total != 1 || page.Sessions[0].ID != fixtureSessionOrder[3] || page.Sessions[0].BookID == nil {
					t.Fatalf("saga2 sessions wrong: %+v", page)
				}
			},
		},
		{
			Requirement: "route-r106", Invariant: "INV-R106-PAGING-001", Case: "edge", State: "stateless", Assertion: "item-listening-sessions-paging-and-empty",
			Sources: []string{"server/controllers/MeController.js:204-218"},
			Claim:   "GET /api/me/item/listening-sessions/:libraryItemId pages like the full list, returns an empty list for an item the caller never played and does not include another user's sessions.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				path := "/api/me/item/listening-sessions/" + e.IDs["saga1"]
				page := getListening(t, e, listener, path, "itemsPerPage", "2", "page", "1")
				if page.Total != 4 || page.NumPages != 2 || page.Page != 1 || page.ItemsPerPage != 2 || !slices.Equal(page.sessionIDs(), fixtureSessionOrder[5:]) {
					t.Fatalf("page 1 of 2 wrong: %+v", page)
				}
				if fallback := getListening(t, e, listener, path, "itemsPerPage", "0"); fallback.ItemsPerPage != 10 || len(fallback.Sessions) != 4 {
					t.Fatalf("itemsPerPage=0 must fall back to 10: %+v", fallback)
				}
				for _, c := range []struct{ token, item string }{{listener, "novel"}, {e.Token(t, "finisher"), "saga1"}} {
					page := getListening(t, e, c.token, "/api/me/item/listening-sessions/"+e.IDs[c.item])
					if page.Total != 0 || page.NumPages != 0 || page.Sessions == nil || len(page.Sessions) != 0 {
						t.Fatalf("%s must have no sessions: %+v", c.item, page)
					}
				}
			},
		},
		{
			Requirement: "route-r106", Invariant: "INV-R106-CONTRACT-001", Case: "error", State: "stateless", Assertion: "item-listening-sessions-errors",
			Sources: []string{"server/controllers/MeController.js:186-200"},
			Claim:   "GET /api/me/item/listening-sessions/... answers 401 without a token, 404 for an unknown item, for a podcast without an episode id and for an unknown episode, and 403 for an item of a library the user cannot access.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				const base = "/api/me/item/listening-sessions/"
				requireUnauthenticated(t, e, http.MethodGet, base+e.IDs["saga1"])
				wantStatus(t, e.Get(t, token, base+"00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown item")
				wantStatus(t, e.Get(t, token, base+e.IDs["podcast"]), http.StatusNotFound, "podcast without episode")
				wantStatus(t, e.Get(t, token, base+e.IDs["podcast"]+"/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown episode")
				wantStatus(t, e.Get(t, token, base+e.IDs["podcast"]+"/"+e.IDs["saga1"]), http.StatusNotFound, "a book id as the episode")
				restricted := restrictedUser(t, e, "me-restricted-listening")
				wantStatus(t, e.Get(t, restricted, base+e.IDs["saga1"]), http.StatusForbidden, "forbidden library")
				wantStatus(t, e.Get(t, restricted, base+e.IDs["podcast"]+"/"+e.IDs["ep1"]), http.StatusOK, "allowed library")
			},
		},
		{
			Requirement: "route-r107", Invariant: "INV-R107-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "listening-stats-listener",
			Sources: []string{"server/routers/ApiRouter.js:181", "server/controllers/MeController.js:228-231", statsSource},
			Claim:   "GET /api/me/listening-stats sums the listener's sessions: totalTime, per-item time and metadata, per-date and per-weekday time, today (zero unless a seeded date is the run date) and the ten most recent sessions.",
			Run: func(t *testing.T, e *harness.Env) {
				var stats listeningStats
				getJSON(t, e, e.Token(t, "listener"), "/api/me/listening-stats", &stats)
				// today counts the seeded sessions dated today (none, unless the run date equals a fixture date)
				wantToday := map[string]float64{"2026-11-03": 44, "2026-11-02": 9, "2026-02-02": 12.4}[time.Now().UTC().Format("2006-01-02")]
				if !near(stats.TotalTime, 65.4) || !near(stats.Today, wantToday) {
					t.Fatalf("totals wrong: %+v", stats)
				}
				for item, want := range map[string]float64{e.IDs["podcast"]: 16.5, e.IDs["saga1"]: 39.9, e.IDs["saga2"]: 9} {
					got, ok := stats.Items[item]
					if !ok || got.ID != item || !near(got.TimeListening, want) {
						t.Fatalf("item %s wrong: %+v", item, got)
					}
				}
				for item, title := range map[string]string{e.IDs["podcast"]: "Fixture Cast", e.IDs["saga1"]: "The Beginning", e.IDs["saga2"]: "The Middle"} {
					if stats.Items[item].MediaMetadata["title"] != title {
						t.Fatalf("item %s metadata title must be %q: %+v", item, title, stats.Items[item].MediaMetadata)
					}
				}
				for day, want := range map[string]float64{"2026-11-03": 44, "2026-11-02": 9, "2026-02-02": 12.4} {
					if !near(stats.Days[day], want) {
						t.Fatalf("day %s must be %v: %v", day, want, stats.Days)
					}
				}
				if len(stats.Days) != 3 {
					t.Fatalf("only dates with listening time count: %v", stats.Days)
				}
				for weekday, want := range map[string]float64{"Tuesday": 38, "Sunday": 15, "Monday": 12.4} {
					if !near(stats.DayOfWeek[weekday], want) {
						t.Fatalf("weekday %s must be %v: %v", weekday, want, stats.DayOfWeek)
					}
				}
				ids := make([]string, 0, len(stats.RecentSessions))
				for _, session := range stats.RecentSessions {
					ids = append(ids, session.ID)
				}
				if !slices.Equal(ids, fixtureSessionOrder) {
					t.Fatalf("recentSessions must be the newest sessions in order: %v", ids)
				}
				for _, session := range stats.RecentSessions {
					requireStoredFields(t, e, session)
				}
			},
		},
		{
			Requirement: "route-r107", Invariant: "INV-R107-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "listening-stats-without-sessions",
			Sources: []string{statsSource},
			Claim:   "GET /api/me/listening-stats for a user without sessions returns zero totals, empty objects and an empty recentSessions array.",
			Run: func(t *testing.T, e *harness.Env) {
				var stats listeningStats
				getJSON(t, e, e.Token(t, "root"), "/api/me/listening-stats", &stats)
				if stats.TotalTime != 0 || stats.Today != 0 || stats.Items == nil || len(stats.Items) != 0 || stats.Days == nil || len(stats.Days) != 0 ||
					stats.DayOfWeek == nil || len(stats.DayOfWeek) != 0 || stats.RecentSessions == nil || len(stats.RecentSessions) != 0 {
					t.Fatalf("empty stats wrong: %+v", stats)
				}
			},
		},
		{
			Requirement: "route-r107", Invariant: "INV-R107-CONTRACT-001", Case: "error", State: "stateless", Assertion: "listening-stats-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:181"},
			Claim:   "GET /api/me/listening-stats answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/listening-stats")
			},
		},
	}
}
