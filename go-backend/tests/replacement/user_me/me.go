package user_me

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	meRouter  = "server/routers/ApiRouter.js:173"
	meSource  = "server/controllers/MeController.js:27"
	userModel = "server/models/User.js:603"
)

// sortedProgress returns the progress entries ordered by id.
func sortedProgress(list []progressJSON) []progressJSON {
	out := slices.Clone(list)
	slices.SortFunc(out, func(a, b progressJSON) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// wantProgressSet requires the list to hold exactly the wanted entries (any order).
func wantProgressSet(t *testing.T, what string, got []progressJSON, want map[string]progressJSON) {
	t.Helper()
	list := make([]progressJSON, 0, len(want))
	for _, progress := range want {
		list = append(list, progress)
	}
	equalJSON(t, what, sortedProgress(got), sortedProgress(list))
}

func meScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r099", Invariant: "INV-R099-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "me-browser-view-of-listener",
			Sources: []string{meRouter, meSource, userModel},
			Claim:   "GET /api/me returns the listener's browser view: identity, type, stored token, flags, permissions, empty library/tag lists, bookmarks and all seven progress entries, without password hash fields.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "listener"), "/api/me")
				wantStatus(t, resp, http.StatusOK, "GET /api/me")
				var user userJSON
				resp.JSON(t, &user)
				rows := e.Rows(t, "SELECT token FROM users WHERE id = ?", e.IDs["listener"])
				if user.ID != e.IDs["listener"] || user.Username != "trial-listener" || user.Email != nil || user.Type != "user" ||
					len(rows) != 1 || user.Token == "" || user.Token != rows[0]["token"] {
					t.Fatalf("identity fields wrong: %s", resp.Body)
				}
				if !user.IsActive || user.IsLocked || user.HasOpenIDLink || user.CreatedAt <= 0 {
					t.Fatalf("flags wrong: %s", resp.Body)
				}
				equalJSON(t, "permissions", user.Permissions, map[string]bool{
					"download": true, "update": false, "delete": false, "upload": false, "createEreader": false,
					"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false,
				})
				equalJSON(t, "lists", [][]string{user.LibrariesAccessible, user.ItemTagsSelected, user.SeriesHideFromContinueListening}, [][]string{{}, {}, {}})
				equalJSON(t, "bookmarks", user.Bookmarks, listenerBookmarks(e))
				wantProgressSet(t, "mediaProgress", user.MediaProgress, listenerProgress(e))
				for _, forbidden := range []string{"pash", "$2a$", "password"} {
					if strings.Contains(string(resp.Body), forbidden) {
						t.Fatalf("response must not carry %q", forbidden)
					}
				}
			},
		},
		{
			Requirement: "route-r099", Invariant: "INV-R099-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "me-is-per-caller",
			Sources: []string{meSource, userModel},
			Claim:   "GET /api/me shows each caller its own data only: root has empty progress and bookmarks arrays and type root, the finisher has just its own finished progress.",
			Run: func(t *testing.T, e *harness.Env) {
				var root userJSON
				getJSON(t, e, e.Token(t, "root"), "/api/me", &root)
				if root.ID != rootID || root.Type != "root" || root.Username != "trial-root" || root.MediaProgress == nil || len(root.MediaProgress) != 0 || root.Bookmarks == nil || len(root.Bookmarks) != 0 || !root.Permissions["delete"] {
					t.Fatalf("root view wrong: %+v", root)
				}
				var finisher userJSON
				getJSON(t, e, e.Token(t, "finisher"), "/api/me", &finisher)
				if finisher.ID != finisherID || finisher.Username != "trial-finisher" || finisher.Permissions["accessExplicitContent"] || len(finisher.Bookmarks) != 0 {
					t.Fatalf("finisher view wrong: %+v", finisher)
				}
				equalJSON(t, "finisher progress", finisher.MediaProgress, []progressJSON{finisherSaga1Progress(e)})
			},
		},
		{
			Requirement: "route-r099", Invariant: "INV-R099-CONTRACT-001", Case: "error", State: "stateless", Assertion: "me-requires-authentication",
			Sources: []string{"server/Server.js:354", meRouter},
			Claim:   "GET /api/me answers 401 without a token and with an invalid bearer token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me")
			},
		},
		{
			Requirement: "route-r102", Invariant: "INV-R102-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "progress-list-listener",
			Sources: []string{"server/routers/ApiRouter.js:176", "server/controllers/MeController.js:112", "server/models/MediaProgress.js:155"},
			Claim:   "GET /api/me/progress returns {mediaProgress} with all seven listener entries: book, finished, ebook-only and podcast episode ids, durations, progress, times, finish flags, ebook fields and timestamps.",
			Run: func(t *testing.T, e *harness.Env) {
				var body struct {
					MediaProgress []progressJSON `json:"mediaProgress"`
				}
				getJSON(t, e, e.Token(t, "listener"), "/api/me/progress", &body)
				wantProgressSet(t, "mediaProgress", body.MediaProgress, listenerProgress(e))
			},
		},
		{
			Requirement: "route-r102", Invariant: "INV-R102-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "progress-list-empty-and-isolated",
			Sources: []string{"server/controllers/MeController.js:113"},
			Claim:   "GET /api/me/progress returns an empty mediaProgress array for a user without progress and only the caller's own entry for the finisher.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/me/progress")
				wantStatus(t, resp, http.StatusOK, "root progress")
				if strings.ReplaceAll(string(resp.Body), " ", "") != `{"mediaProgress":[]}` {
					t.Fatalf("root must get an empty array, got %s", resp.Body)
				}
				var body struct {
					MediaProgress []progressJSON `json:"mediaProgress"`
				}
				getJSON(t, e, e.Token(t, "finisher"), "/api/me/progress", &body)
				equalJSON(t, "finisher progress", body.MediaProgress, []progressJSON{finisherSaga1Progress(e)})
			},
		},
		{
			Requirement: "route-r102", Invariant: "INV-R102-CONTRACT-001", Case: "error", State: "stateless", Assertion: "progress-list-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:176"},
			Claim:   "GET /api/me/progress answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/progress")
			},
		},
		{
			Requirement: "route-r103", Invariant: "INV-R103-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "bookmarks-list-listener",
			Sources: []string{"server/routers/ApiRouter.js:177", "server/controllers/MeController.js:123"},
			Claim:   "GET /api/me/bookmarks returns {bookmarks} with the listener's two bookmarks (item id, time, title, createdAt) in stored order.",
			Run: func(t *testing.T, e *harness.Env) {
				var body struct {
					Bookmarks []bookmarkJSON `json:"bookmarks"`
				}
				getJSON(t, e, e.Token(t, "listener"), "/api/me/bookmarks", &body)
				equalJSON(t, "bookmarks", body.Bookmarks, listenerBookmarks(e))
			},
		},
		{
			Requirement: "route-r103", Invariant: "INV-R103-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "bookmarks-list-empty",
			Sources: []string{"server/controllers/MeController.js:124"},
			Claim:   "GET /api/me/bookmarks returns an empty bookmarks array (not null) for a user without bookmarks.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/me/bookmarks")
				wantStatus(t, resp, http.StatusOK, "root bookmarks")
				if strings.ReplaceAll(string(resp.Body), " ", "") != `{"bookmarks":[]}` {
					t.Fatalf("root must get an empty array, got %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r103", Invariant: "INV-R103-CONTRACT-001", Case: "error", State: "stateless", Assertion: "bookmarks-list-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:177"},
			Claim:   "GET /api/me/bookmarks answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/bookmarks")
			},
		},
		{
			Requirement: "route-r104", Invariant: "INV-R104-SAMPLED", Case: "normal", State: "stateless", Assertion: "item-bookmarks-listener",
			Sources: []string{"server/routers/ApiRouter.js:178", "server/controllers/MeController.js:134"},
			Claim:   "GET /api/me/bookmarks/:libraryItemId returns only the caller's bookmarks of that item, for a book and for a podcast.",
			Run: func(t *testing.T, e *harness.Env) {
				var body struct {
					Bookmarks []bookmarkJSON `json:"bookmarks"`
				}
				all := listenerBookmarks(e)
				getJSON(t, e, e.Token(t, "listener"), "/api/me/bookmarks/"+e.IDs["saga1"], &body)
				equalJSON(t, "saga1 bookmarks", body.Bookmarks, all[:1])
				getJSON(t, e, e.Token(t, "listener"), "/api/me/bookmarks/"+e.IDs["podcast"], &body)
				equalJSON(t, "podcast bookmarks", body.Bookmarks, all[1:])
			},
		},
		{
			Requirement: "route-r104", Invariant: "INV-R104-SAMPLED", Case: "edge", State: "stateless", Assertion: "item-bookmarks-empty-and-other-user",
			Sources: []string{"server/controllers/MeController.js:148"},
			Claim:   "GET /api/me/bookmarks/:libraryItemId returns an empty bookmarks array for an item without bookmarks and does not show another user's bookmarks of that item.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, c := range []struct{ who, item string }{{"listener", "saga2"}, {"finisher", "saga1"}, {"root", "podcast"}} {
					resp := e.Get(t, e.Token(t, c.who), "/api/me/bookmarks/"+e.IDs[c.item])
					wantStatus(t, resp, http.StatusOK, c.who+" "+c.item)
					if strings.ReplaceAll(string(resp.Body), " ", "") != `{"bookmarks":[]}` {
						t.Fatalf("%s on %s must get an empty array, got %s", c.who, c.item, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r104", Invariant: "INV-R104-SAMPLED", Case: "error", State: "stateless", Assertion: "item-bookmarks-errors",
			Sources: []string{"server/controllers/MeController.js:135-145"},
			Claim:   "GET /api/me/bookmarks/:libraryItemId answers 401 without a token, 404 for an unknown item and 403 for a user without access to the item's library.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/bookmarks/"+e.IDs["saga1"])
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/me/bookmarks/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown item")
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/me/bookmarks/not-an-id"), http.StatusNotFound, "malformed item id")
				restricted := restrictedUser(t, e, "me-restricted-bookmarks")
				wantStatus(t, e.Get(t, restricted, "/api/me/bookmarks/"+e.IDs["saga1"]), http.StatusForbidden, "item of a forbidden library")
				wantStatus(t, e.Get(t, restricted, "/api/me/bookmarks/"+e.IDs["podcast"]), http.StatusOK, "item of the allowed library")
			},
		},
	}
}
