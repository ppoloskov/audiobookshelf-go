package user_me

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	saga2BookID    = "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af" // book row of saga2, tagged "x" by accessSetup
	disposableBook = "54374ca2-e3c2-4121-93eb-4b0d806b241f" // book row of the disposable item, made explicit by accessSetup
	seriesID       = "e987a322-9111-4f79-8cb9-c25ee714e0d6" // Long Saga
)

// accessSetup creates users with tag rules, tags saga2 with "x" and marks the
// disposable item explicit (state no fixture row provides), and returns the
// tokens: tagIn may only access items tagged x, tagOut may access everything
// except items tagged x. suffix keeps user names unique per scenario.
func accessSetup(t *testing.T, e *harness.Env, suffix string) (tagIn, tagOut string) {
	t.Helper()
	tagIn = newUser(t, e, map[string]any{
		"username": "me-tag-in-" + suffix, "type": "user", "itemTagsSelected": []string{"x"},
		"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": false},
	})
	tagOut = newUser(t, e, map[string]any{
		"username": "me-tag-out-" + suffix, "type": "user", "itemTagsSelected": []string{"x"},
		"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true},
	})
	editDatabase(t, e,
		[]any{"UPDATE books SET tags = '[\"x\"]' WHERE id = ?", saga2BookID},
		[]any{"UPDATE books SET explicit = 1 WHERE id = ?", disposableBook})
	return tagIn, tagOut
}

// requireItemAccess checks the tag and explicit-content rules against a route
// taking a library item id.
func requireItemAccess(t *testing.T, e *harness.Env, prefix string, tagIn, tagOut string) {
	t.Helper()
	cases := []struct {
		who    string
		token  string
		item   string
		status int
	}{
		{"tag-restricted user, item tagged x", tagIn, "saga2", http.StatusOK},
		{"tag-restricted user, untagged item", tagIn, "saga1", http.StatusForbidden},
		{"tag-excluded user, item tagged x", tagOut, "saga2", http.StatusForbidden},
		{"tag-excluded user, untagged item", tagOut, "saga1", http.StatusOK},
		{"finisher (no explicit content), explicit item", e.Token(t, "finisher"), "disposable", http.StatusForbidden},
		{"finisher, non-explicit item", e.Token(t, "finisher"), "saga1", http.StatusOK},
		{"listener (explicit allowed), explicit item", e.Token(t, "listener"), "disposable", http.StatusOK},
	}
	for _, c := range cases {
		wantStatus(t, e.Get(t, c.token, prefix+e.IDs[c.item]), c.status, c.who)
	}
}

func userStateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r099", Invariant: "INV-R099-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "me-lists-flags-and-links-filled",
			Sources: []string{"server/models/User.js:603-638", "server/controllers/MeController.js:532-543", "server/controllers/UserController.js:134-168"},
			Claim:   "GET /api/me fills librariesAccessible, itemTagsSelected and seriesHideFromContinueListening and moves the first two out of permissions, and reports isLocked and hasOpenIDLink true when the user is locked and linked to an OpenID subject.",
			Run: func(t *testing.T, e *harness.Env) {
				token := newUser(t, e, map[string]any{
					"username": "me-rich-view", "type": "user",
					"librariesAccessible": []string{e.IDs["podcastLibrary"]}, "itemTagsSelected": []string{"x", "y"},
					"permissions": map[string]any{"accessAllLibraries": false, "accessAllTags": false},
				})
				var hidden userJSON
				getJSON(t, e, token, "/api/me/series/"+seriesID+"/remove-from-continue-listening", &hidden)
				editDatabase(t, e,
					[]any{"UPDATE users SET isLocked = 1, extraData = json_set(extraData, '$.authOpenIDSub', 'me-packet-sub') WHERE username = ?", "me-rich-view"})
				resp := e.Get(t, token, "/api/me")
				wantStatus(t, resp, http.StatusOK, "GET /api/me")
				var raw map[string]any
				resp.JSON(t, &raw)
				var user userJSON
				resp.JSON(t, &user)
				equalJSON(t, "librariesAccessible", user.LibrariesAccessible, []string{e.IDs["podcastLibrary"]})
				equalJSON(t, "itemTagsSelected", user.ItemTagsSelected, []string{"x", "y"})
				equalJSON(t, "seriesHideFromContinueListening", user.SeriesHideFromContinueListening, []string{seriesID})
				permissions, _ := raw["permissions"].(map[string]any)
				_, hasLibraries := permissions["librariesAccessible"]
				_, hasTags := permissions["itemTagsSelected"]
				if hasLibraries || hasTags || user.Permissions["accessAllLibraries"] || user.Permissions["accessAllTags"] {
					t.Fatalf("lists must be moved out of permissions: %v", permissions)
				}
				if !user.IsLocked || !user.HasOpenIDLink || !user.IsActive {
					t.Fatalf("isLocked and hasOpenIDLink must be true: %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r104", Invariant: "INV-R104-SAMPLED", Case: "error", State: "stateless", Assertion: "item-bookmarks-tag-and-explicit-rules",
			Sources: []string{"server/controllers/MeController.js:140-143", "server/models/User.js:658-683"},
			Claim:   "GET /api/me/bookmarks/:libraryItemId answers 403 for an item whose tags the user may not access (selected tags only, or selected tags excluded) and for an explicit item when the user may not see explicit content, and 200 otherwise.",
			Run: func(t *testing.T, e *harness.Env) {
				tagIn, tagOut := accessSetup(t, e, "bookmarks")
				requireItemAccess(t, e, "/api/me/bookmarks/", tagIn, tagOut)
			},
		},
		{
			Requirement: "route-r106", Invariant: "INV-R106-CONTRACT-001", Case: "error", State: "stateless", Assertion: "item-listening-sessions-tag-and-explicit-rules",
			Sources: []string{"server/controllers/MeController.js:194-198", "server/models/User.js:658-683"},
			Claim:   "GET /api/me/item/listening-sessions/:libraryItemId answers 403 for an item whose tags the user may not access and for an explicit item when the user may not see explicit content, and 200 otherwise.",
			Run: func(t *testing.T, e *harness.Env) {
				tagIn, tagOut := accessSetup(t, e, "listening")
				requireItemAccess(t, e, "/api/me/item/listening-sessions/", tagIn, tagOut)
			},
		},
	}
}
