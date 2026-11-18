package playback_sessions

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	routeHistory = "server/routers/ApiRouter.js:234"
	historySrc   = "server/controllers/SessionController.js:29-118"
)

// legacySessions are the seeded stored sessions with null columns
// (cap-route-r140: historical null/device fields).
var legacySessions = []historyDoc{
	{
		ID: "legacy-null-0001", UserID: "f6af59cf-3988-45fe-b0ad-81420a953f9d", LibraryID: fixtureLibraryID,
		LibraryItemID: ptrOf("b3c61dfc-dd4b-47ee-8998-0340c1228f52"), BookID: ptrOf(saga1BookID),
		MediaType: "book", Chapters: []chapterDoc{}, DisplayTitle: "", DisplayAuthor: "",
		DeviceInfo: map[string]any{},
		// A stored row without a date derives it from updatedAt
		// (server/objects/PlaybackSession.js:186-190).
		Date: ptrOf("2026-01-15"), DayOfWeek: ptrOf("Thursday"),
		StartedAt: utcMillis(2026, 1, 15, 10, 0, 0),
		UpdatedAt: utcMillis(2026, 1, 15, 10, 5, 0),
	},
	{
		ID: "legacy-null-0002", UserID: "f6af59cf-3988-45fe-b0ad-81420a953f9d", LibraryID: fixtureLibraryID,
		LibraryItemID: ptrOf("b3c61dfc-dd4b-47ee-8998-0340c1228f52"), BookID: ptrOf(saga1BookID),
		MediaType: "book", Chapters: []chapterDoc{}, DisplayTitle: "The Beginning", DisplayAuthor: "",
		MediaMetadata: map[string]any{"title": "The Beginning", "authors": []any{map[string]any{"name": "Terry Fixture"}}, "genres": []any{"Fiction"}},
		Duration:      ptrOf(saga1Duration), PlayMethod: ptrOf(3), DeviceInfo: map[string]any{},
		ServerVersion: ptrOf("2.17.0"), Date: ptrOf("2026-02-02"), DayOfWeek: ptrOf("Monday"),
		TimeListening: ptrOf(12.4), CurrentTime: 12.4,
		StartedAt: utcMillis(2026, 2, 2, 10, 0, 0),
		UpdatedAt: utcMillis(2026, 2, 2, 10, 1, 0),
	},
	{
		ID: "legacy-null-0003", UserID: "f6af59cf-3988-45fe-b0ad-81420a953f9d", LibraryID: fixtureLibraryID,
		BookID: ptrOf(saga1BookID), MediaType: "book", Chapters: []chapterDoc{}, DisplayTitle: "The Beginning", DisplayAuthor: "Terry Fixture",
		MediaMetadata: map[string]any{}, Duration: ptrOf(saga1Duration), MediaPlayer: ptrOf("AVPlayer"), DeviceInfo: map[string]any{},
		Date: ptrOf("2026-03-02"), DayOfWeek: ptrOf("Monday"),
		StartTime: 1.25, CurrentTime: 33.75,
		StartedAt: utcMillis(2026, 3, 2, 10, 0, 0),
		UpdatedAt: utcMillis(2026, 3, 2, 10, 1, 0),
	},
}

// historyScenarios covers GET /api/sessions: the admin history list.
func historyScenarios() []harness.Scenario {
	owner := minifiedUser{ID: "f6af59cf-3988-45fe-b0ad-81420a953f9d", Username: "trial-listener"}
	for i := range legacySessions {
		legacySessions[i].User = &owner
	}
	return []harness.Scenario{
		{
			Requirement: "route-r140", Invariant: "INV-R140-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "sessions-history-lists-user-sessions",
			Sources: []string{routeHistory, historySrc, "server/models/PlaybackSession.js:74-106", "client/pages/config/sessions.vue:477-495"},
			Claim:   "GET /api/sessions as an admin filtered by user answers {total, numPages, page, itemsPerPage, sessions, userId}; every session carries its old playback fields, the user {id, username} and the device, ordered by the validated sort key.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-history", map[string]any{})
				book := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-history-1", 3)
				middle := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-history-2", 4)
				episode := createStoredSession(t, e, user, e.IDs["podcast"], e.IDs["ep1"], "ps-history-3", 5)
				page := history(t, e, root, "?user="+user.ID+"&sort=displayTitle&desc=0&itemsPerPage=10&page=0")
				if page.Total != 3 || page.NumPages != 1 || page.Page != 0 || page.ItemsPerPage != 10 || page.UserID == nil || *page.UserID != user.ID {
					t.Fatalf("history envelope: %+v", page)
				}
				requireHistoryIDs(t, page, episode.ID, book.ID, middle.ID)
				for _, c := range []struct {
					doc       sessionDoc
					listening float64
					current   float64
				}{{book, 3, 23}, {middle, 4, 24}, {episode, 5, 25}} {
					stored := findHistory(page, c.doc.ID)
					if stored == nil {
						t.Fatalf("session %s is missing from the history", c.doc.ID)
					}
					requireStoredSession(t, *stored, c.doc, c.listening, c.current, dbTime(t, sessionRow(t, e, c.doc.ID)["updatedAt"]))
					requireEqual(t, "session owner", stored.User, &minifiedUser{ID: user.ID, Username: "ps-history"})
					requireEqual(t, "session device", stored.DeviceInfo, c.doc.DeviceInfo)
				}
				if got := findHistory(page, episode.ID); got.EpisodeID == nil || *got.EpisodeID != e.IDs["ep1"] || got.BookID != nil || got.MediaType != "podcast" {
					t.Fatalf("episode session: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r140", Invariant: "INV-R140-CONTRACT-001", Case: "error", State: "stateless", Assertion: "sessions-history-refused",
			Sources: []string{routeHistory, historySrc},
			Claim:   "GET /api/sessions answers 404 to a listener without admin rights and 401 without a valid token, exposing no session data and changing no row.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				finisher := fixtureActor(t, e, "finisher")
				rows := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				for _, c := range []struct {
					name, token string
					status      int
				}{
					{"no token", "", http.StatusUnauthorized},
					{"invalid token", "not-a-token", http.StatusUnauthorized},
					{"listener", listener.Token, http.StatusNotFound},
					{"finisher", finisher.Token, http.StatusNotFound},
				} {
					resp := e.Get(t, c.token, "/api/sessions?itemsPerPage=5&page=0")
					requireStatus(t, resp, c.status, "GET /api/sessions: "+c.name)
					if body := string(resp.Body); len(body) > 0 && (body == "[]" || containsToken(body, listener.ID) || containsToken(body, "sessions")) {
						t.Fatalf("%s: the refusal leaked session data: %.200s", c.name, body)
					}
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != rows {
					t.Fatalf("refused history reads changed %d rows", got-rows)
				}
			},
		},
		{
			Requirement: "route-r140", Invariant: "INV-R140-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "sessions-history-paging-and-legacy",
			Sources: []string{routeHistory, historySrc, "server/models/PlaybackSession.js:74-106"},
			Claim:   "Stored sessions with null columns answer null fields, an empty device object and a date derived from updatedAt; pages split the rows with numPages from the total, invalid sort/itemsPerPage/page fall back to their defaults, and desc reverses the order.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-history-page", map[string]any{})
				book := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-page-1", 3)
				middle := createStoredSession(t, e, user, e.IDs["saga2"], "", "ps-page-2", 4)
				episode := createStoredSession(t, e, user, e.IDs["podcast"], e.IDs["ep1"], "ps-page-3", 5)

				// The seeded rows of the fixture carry null columns.
				legacy := history(t, e, root, "?user="+e.IDs["listener"]+"&sort=createdAt&desc=0&itemsPerPage=3&page=0")
				if legacy.Total < 7 || legacy.NumPages < 3 || legacy.ItemsPerPage != 3 || legacy.Page != 0 {
					t.Fatalf("legacy page envelope: %+v", legacy)
				}
				requireHistoryIDs(t, legacy, "legacy-null-0001", "legacy-null-0002", "legacy-null-0003")
				for _, want := range legacySessions {
					got := findHistory(legacy, want.ID)
					if got == nil {
						t.Fatalf("seeded session %s is missing", want.ID)
					}
					requireEqual(t, "seeded session "+want.ID, *got, want)
				}

				// Two pages of the fresh user's sessions.
				first := history(t, e, root, "?user="+user.ID+"&sort=displayTitle&desc=0&itemsPerPage=2&page=0")
				if first.Total != 3 || first.NumPages != 2 || first.Page != 0 {
					t.Fatalf("page 0 envelope: %+v", first)
				}
				requireHistoryIDs(t, first, episode.ID, book.ID)
				second := history(t, e, root, "?user="+user.ID+"&sort=displayTitle&desc=0&itemsPerPage=2&page=1")
				if second.Total != 3 || second.NumPages != 2 || second.Page != 1 {
					t.Fatalf("page 1 envelope: %+v", second)
				}
				requireHistoryIDs(t, second, middle.ID)

				// Invalid parameters fall back: itemsPerPage 10, page 0, sort updatedAt.
				fallback := history(t, e, root, "?user="+user.ID+"&sort=bogus&desc=0&itemsPerPage=0&page=-1")
				if fallback.ItemsPerPage != 10 || fallback.Page != 0 || fallback.Total != 3 {
					t.Fatalf("fallback envelope: %+v", fallback)
				}
				negative := history(t, e, root, "?user="+user.ID+"&itemsPerPage=-3&page=0")
				if negative.ItemsPerPage != 10 || negative.Total != 3 || len(negative.Sessions) != 3 {
					t.Fatalf("a negative itemsPerPage falls back to 10: %+v", negative)
				}
				desc := history(t, e, root, "?user="+user.ID+"&sort=displayTitle&desc=1&itemsPerPage=10&page=0")
				requireHistoryIDs(t, desc, middle.ID, book.ID, episode.ID)

				// An invalid user filter is ignored: the unfiltered list, no userId.
				all := history(t, e, root, "?user=not-a-uuid&itemsPerPage=1&page=0")
				full := history(t, e, root, "?itemsPerPage=1&page=0")
				if all.UserID != nil || all.Total != full.Total || all.Total < 7 || len(all.Sessions) != 1 {
					t.Fatalf("invalid user filter: %+v vs %+v", all, full)
				}
			},
		},
		{
			Requirement: "route-r140", Invariant: "INV-R140-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "sessions-history-deleted-user",
			Sources: []string{routeHistory, historySrc, "server/controllers/UserController.js:375-400", "client/pages/config/sessions.vue:529-535"},
			Claim:   "Deleting a user keeps their stored sessions with userId null and user null, as the admin history renders N/A, and no user filter matches them.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, "ps-history-deleted", map[string]any{})
				doc := createStoredSession(t, e, user, e.IDs["saga1"], "", "ps-history-deleted", 3)
				requireStatus(t, e.Delete(t, root, "/api/users/"+user.ID), http.StatusOK, "delete the user")
				if row := sessionRow(t, e, doc.ID); row == nil || row["userId"] != nil || row["displayTitle"] != "The Beginning" || !near(number(t, row["timeListening"]), 3) {
					t.Fatalf("stored session after the user deletion: %v", row)
				}
				if progressRow(t, e, user.ID, saga1BookID) != nil {
					t.Fatal("deleting a user must remove their progress")
				}
				resp := e.Get(t, root, "/api/sessions?itemsPerPage=100&page=0")
				requireStatus(t, resp, http.StatusOK, "GET /api/sessions")
				var page struct {
					Sessions []map[string]any `json:"sessions"`
				}
				resp.JSON(t, &page)
				var found map[string]any
				for _, s := range page.Sessions {
					if s["id"] == doc.ID {
						found = s
					}
				}
				if found == nil {
					t.Fatal("the session of the deleted user is missing from the history")
				}
				if found["userId"] != nil || found["user"] != nil || found["displayTitle"] != "The Beginning" || !near(number(t, found["timeListening"]), 3) {
					t.Fatalf("session of a deleted user: %v", found)
				}
				if filtered := history(t, e, root, "?user="+user.ID+"&itemsPerPage=10"); filtered.Total != 0 {
					t.Fatalf("a deleted user filter must match nothing: %+v", filtered)
				}
			},
		},
	}
}
