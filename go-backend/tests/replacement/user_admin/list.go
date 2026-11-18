package user_admin

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	listSource   = "server/controllers/UserController.js:29-49"
	middlewareJS = "server/controllers/UserController.js:501-516"
	helperAdmin  = "adm-helper"
)

// adminToken returns the access token of a non-root admin, creating the
// account on first use (setup through POST /api/users).
func adminToken(t *testing.T, e *harness.Env) string {
	t.Helper()
	if e.Count(t, "SELECT COUNT(*) FROM users WHERE username = ?", helperAdmin) == 0 {
		createUser(t, e, map[string]any{"username": helperAdmin, "type": "admin"})
	}
	return loginToken(t, e, helperAdmin)
}

// listUsers reads GET /api/users as raw objects (to see which keys exist).
func listUsers(t *testing.T, e *harness.Env, token string, query url.Values) []map[string]any {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/users", Token: token, Query: query})
	wantStatus(t, resp, http.StatusOK, "GET /api/users")
	var body struct {
		Users []map[string]any `json:"users"`
	}
	resp.JSON(t, &body)
	if body.Users == nil {
		t.Fatalf("GET /api/users must return a users array: %s", resp.Body)
	}
	return body.Users
}

// findUser returns the object with the id, or nil.
func findUser(users []map[string]any, id string) map[string]any {
	for _, user := range users {
		if user["id"] == id {
			return user
		}
	}
	return nil
}

// requireUnauthenticated requires 401 for no token and for an unknown bearer.
func requireUnauthenticated(t *testing.T, e *harness.Env, method, path string) {
	t.Helper()
	for _, token := range []string{"", "not-a-token"} {
		resp := e.Do(t, harness.Request{Method: method, Path: path, Token: token})
		wantStatus(t, resp, http.StatusUnauthorized, method+" "+path+" with token "+token)
	}
}

func listScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r072", Invariant: "INV-R072-SAMPLED", Case: "normal", State: "stateless", Assertion: "list-users-fields",
			Sources: []string{"server/routers/ApiRouter.js:134", listSource, "server/models/User.js:338-376"},
			Claim:   "GET /api/users as root returns every user in the minimal browser view: identity, type, token, flags, permissions and selection lists, without mediaProgress and bookmarks.",
			Run: func(t *testing.T, e *harness.Env) {
				users := listUsers(t, e, e.Token(t, "root"), nil)
				if len(users) != e.Count(t, "SELECT COUNT(*) FROM users") || len(users) < 3 {
					t.Fatalf("every user must be listed: %d", len(users))
				}
				for _, user := range users {
					if _, ok := user["mediaProgress"]; ok {
						t.Fatalf("the list must not include mediaProgress: %v", user)
					}
					if _, ok := user["bookmarks"]; ok {
						t.Fatalf("the list must not include bookmarks: %v", user)
					}
					if _, ok := user["latestSession"]; ok {
						t.Fatalf("latestSession is only included on request: %v", user)
					}
				}
				listener := findUser(users, e.IDs["listener"])
				if listener == nil {
					t.Fatalf("the listener must be listed")
				}
				row := userRow(t, e, e.IDs["listener"])
				wantPerms := map[string]any{"download": true, "update": false, "delete": false, "upload": false, "createEreader": false, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false}
				for key, want := range map[string]any{
					"username": "trial-listener", "type": "user", "isActive": true, "isLocked": false, "hasOpenIDLink": false,
					"token": row["token"], "email": nil,
				} {
					if listener[key] != want {
						t.Fatalf("listener.%s: got %v want %v", key, listener[key], want)
					}
				}
				if got, _ := listener["createdAt"].(float64); got <= 0 {
					t.Fatalf("createdAt must be a ms timestamp: %v", listener["createdAt"])
				}
				if !equalValue(listener["permissions"], wantPerms) || !equalValue(listener["librariesAccessible"], []any{}) ||
					!equalValue(listener["itemTagsSelected"], []any{}) || !equalValue(listener["seriesHideFromContinueListening"], []any{}) {
					t.Fatalf("listener permissions or lists wrong: %v", listener)
				}
				root := findUser(users, rootID)
				if root == nil || root["type"] != "root" || root["token"] == "" || root["token"] == nil {
					t.Fatalf("root must be listed with its token for a root caller: %v", root)
				}
			},
		},
		{
			Requirement: "route-r072", Invariant: "INV-R072-ROOT-TOKEN-001", Case: "edge", State: "stateless", Assertion: "list-users-hides-root-token-from-admin",
			Sources: []string{listSource, "server/models/User.js:353"},
			Claim:   "GET /api/users as a non-root admin shows the root user's token as an empty string and every other user's token unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				users := listUsers(t, e, adminToken(t, e), nil)
				root := findUser(users, rootID)
				if root == nil || root["token"] != "" {
					t.Fatalf("the root token must be hidden from an admin: %v", root)
				}
				listener := findUser(users, e.IDs["listener"])
				if listener == nil || listener["token"] != userRow(t, e, e.IDs["listener"])["token"] {
					t.Fatalf("other tokens stay visible to an admin: %v", listener)
				}
			},
		},
		{
			Requirement: "route-r072", Invariant: "INV-R072-LATEST-SESSION-001", Case: "edge", State: "stateless", Assertion: "list-users-include-latest-session",
			Sources: []string{listSource},
			Claim:   "GET /api/users?include=latestSession adds each user's most recently updated playback session or null; the include list is comma separated and trimmed; without it the key is absent.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				for _, include := range []string{"latestSession", "other, latestSession"} {
					users := listUsers(t, e, token, url.Values{"include": {include}})
					listener := findUser(users, e.IDs["listener"])
					latest, ok := listener["latestSession"].(map[string]any)
					device, _ := latest["deviceInfo"].(map[string]any)
					if !ok || latest["id"] != "193bf2e2-70c3-4b91-99e2-40bc7398812a" || latest["userId"] != e.IDs["listener"] || latest["displayTitle"] != "Pilot Episode" ||
						latest["updatedAt"] != float64(1793709988126) || latest["timeListening"] != 10.5 || latest["currentTime"] != 10.5 || latest["displayAuthor"] != "Fixture Host" ||
						device["deviceId"] != "trial-android-0001" || device["deviceName"] != "Google Pixel 8" || device["clientName"] != "Abs Android" {
						t.Fatalf("include %q: the listener's latest session must be the newest updated one: %v", include, listener["latestSession"])
					}
					root := findUser(users, rootID)
					if value, present := root["latestSession"]; !present || value != nil {
						t.Fatalf("include %q: a user without sessions gets latestSession null: %v", include, root)
					}
				}
				for _, include := range []string{"", "something-else"} {
					users := listUsers(t, e, token, url.Values{"include": {include}})
					if _, present := findUser(users, e.IDs["listener"])["latestSession"]; present {
						t.Fatalf("include %q must not add latestSession", include)
					}
				}
			},
		},
		{
			Requirement: "route-r072", Invariant: "INV-R072-AUTH-001", Case: "error", State: "stateless", Assertion: "list-users-requires-admin",
			Sources: []string{"server/Server.js:354", listSource, middlewareJS},
			Claim:   "GET /api/users answers 401 without a valid token and 403 to a non-admin user.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/users")
				for _, who := range []string{"listener", "finisher"} {
					wantStatus(t, e.Get(t, e.Token(t, who), "/api/users"), http.StatusForbidden, who+" GET /api/users")
				}
			},
		},
	}
}

// equalValue compares two decoded JSON values by their encoding.
func equalValue(got, want any) bool {
	return jsonString(got) == jsonString(want)
}
