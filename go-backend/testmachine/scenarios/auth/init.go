package auth

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// initScenarios are the scenarios of POST /init (route-r010).
func initScenarios() []harness.Scenario {
	const id = "INV-R010-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: "route-r010", Invariant: id, Case: "normal", State: "initial", Assertion: "init-creates-root-user",
			Sources: []string{"server/Server.js:377-383", "server/Server.js:485-498", "server/models/User.js:192-211", "server/Database.js:421-438"},
			Claim:   "POST /init on an uninitialised server answers 200 and persists one active root user with the given username, a bcrypt password hash, a token and the full root permission set, and that user can log in.",
			Run:     checkInitCreatesRoot,
		},
		{
			Requirement: "route-r010", Invariant: id, Case: "normal", State: "changed", Assertion: "init-flips-status-and-enables-login",
			Sources: []string{"server/Server.js:384-399"},
			Claim:   "Before POST /init /status reports isInit false with the config and metadata paths and login is denied; afterwards it reports isInit true without paths.",
			Run:     checkInitChangesStatus,
		},
		{
			Requirement: "route-r010", Invariant: id, Case: "edge", State: "initial", Assertion: "init-defaults-username-and-empty-password",
			Sources: []string{"server/Server.js:487-490"},
			Claim:   "POST /init with an empty newRoot creates the root user named root with an empty password hash.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{Fresh: true, Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
				requireStatus(t, "init", e.Post(t, "", "/init", map[string]any{"newRoot": map[string]any{}}), http.StatusOK)
				rows := e.Rows(t, "SELECT username, type, pash, isActive FROM users")
				if len(rows) != 1 {
					t.Fatalf("want one user, got %v", rows)
				}
				requireString(t, "username", rows[0]["username"], "root")
				requireString(t, "type", rows[0]["type"], "root")
				if pash, _ := rows[0]["pash"].(string); pash != "" {
					t.Fatalf("pash = %q, want empty", pash)
				}
				requireStatus(t, "login", e.Post(t, "", "/login", map[string]string{"username": "root", "password": ""}), http.StatusOK)
			},
		},
		{
			Requirement: "route-r010", Invariant: id, Case: "error", State: "repeat", Assertion: "second-init-denied-keeps-root",
			Sources: []string{"server/Server.js:378-381", "server/Database.js:421-438"},
			Claim:   "A second POST /init is denied with 500, creates no user and leaves the first root's credentials working.",
			Run:     checkSecondInit,
		},
		{
			Requirement: "route-r010", Invariant: id, Case: "error", State: "failure", Assertion: "init-on-initialised-server-denied",
			Sources: []string{"server/Server.js:378-381"},
			Claim:   "POST /init on an installation that already has a root user is denied with 500 and neither adds a user nor changes the existing ones.",
			Run: func(t *testing.T, e *harness.Env) {
				before := e.Rows(t, "SELECT id, username, type, pash, token, updatedAt FROM users ORDER BY id")
				resp := e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "second-root", "password": "second-root-password"}})
				if resp.Status != http.StatusInternalServerError {
					t.Fatalf("init on an initialised server: status %d, want 500", resp.Status)
				}
				after := e.Rows(t, "SELECT id, username, type, pash, token, updatedAt FROM users ORDER BY id")
				if len(after) != len(before) {
					t.Fatalf("users changed from %d to %d rows", len(before), len(after))
				}
				for i := range before {
					for key, value := range before[i] {
						if after[i][key] != value {
							t.Fatalf("user row %d column %s changed", i, key)
						}
					}
				}
			},
		},
	}
}

// startFresh starts an uninitialised server without the auth rate limit.
func startFresh(t *testing.T) *harness.Env {
	t.Helper()
	return harness.Start(t, harness.Options{Fresh: true, Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
}

func checkInitCreatesRoot(t *testing.T, _ *harness.Env) {
	e := startFresh(t)
	resp := e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "FirstAdmin", "password": "first-admin-password"}})
	requireStatus(t, "init", resp, http.StatusOK)
	rows := e.Rows(t, "SELECT id, username, type, pash, token, isActive, permissions, email FROM users")
	if len(rows) != 1 {
		t.Fatalf("want exactly one user, got %v", rows)
	}
	row := rows[0]
	requireString(t, "username", row["username"], "FirstAdmin")
	requireString(t, "type", row["type"], "root")
	if pash, _ := row["pash"].(string); !strings.HasPrefix(pash, "$2") || strings.Contains(pash, "first-admin-password") {
		t.Fatalf("pash must be a bcrypt hash, got %q", pash)
	}
	if token, _ := row["token"].(string); strings.Count(token, ".") != 2 {
		t.Fatalf("token must be a JWT, got %q", token)
	}
	if row["isActive"] != int64(1) {
		t.Fatalf("isActive = %v", row["isActive"])
	}
	var permissions map[string]any
	if err := jsonDecode(row["permissions"], &permissions); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"download", "update", "delete", "upload", "createEreader", "accessAllLibraries", "accessAllTags", "accessExplicitContent"} {
		if permissions[key] != true {
			t.Fatalf("root permission %s = %v, want true (%v)", key, permissions[key], permissions)
		}
	}
	if permissions["selectedTagsNotAccessible"] != false {
		t.Fatalf("selectedTagsNotAccessible = %v", permissions["selectedTagsNotAccessible"])
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		if list, ok := permissions[key].([]any); !ok || len(list) != 0 {
			t.Fatalf("%s = %v, want []", key, permissions[key])
		}
	}
	// The username is stored as given; login matches it case-insensitively.
	resp = e.Post(t, "", "/login", map[string]string{"username": "firstadmin", "password": "first-admin-password"})
	requireStatus(t, "login", resp, http.StatusOK)
	var payload loginPayload
	resp.JSON(t, &payload)
	if payload.User.ID != row["id"] || payload.User.Type != "root" || payload.UserDefaultLibraryID != nil {
		t.Fatalf("first login answer = %s", resp.Body)
	}
}

func checkInitChangesStatus(t *testing.T, _ *harness.Env) {
	e := startFresh(t)
	type status struct {
		IsInit       bool    `json:"isInit"`
		ConfigPath   *string `json:"ConfigPath"`
		MetadataPath *string `json:"MetadataPath"`
	}
	var before status
	e.Get(t, "", "/status").JSON(t, &before)
	if before.IsInit || before.ConfigPath == nil || before.MetadataPath == nil {
		t.Fatalf("uninitialised /status = %+v", before)
	}
	requireDenied(t, "login before init", e.Post(t, "", "/login", map[string]string{"username": "admin", "password": "admin-password"}))
	requireStatus(t, "init", e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "admin", "password": "admin-password"}}), http.StatusOK)
	var after status
	e.Get(t, "", "/status").JSON(t, &after)
	if !after.IsInit || after.ConfigPath != nil || after.MetadataPath != nil {
		t.Fatalf("initialised /status = %+v", after)
	}
	requireStatus(t, "login after init", e.Post(t, "", "/login", map[string]string{"username": "admin", "password": "admin-password"}), http.StatusOK)
}

func checkSecondInit(t *testing.T, _ *harness.Env) {
	e := startFresh(t)
	requireStatus(t, "first init", e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "first", "password": "first-password"}}), http.StatusOK)
	before := e.Rows(t, "SELECT id, pash, token FROM users")
	resp := e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "second", "password": "second-password"}})
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("second init: status %d, want 500", resp.Status)
	}
	after := e.Rows(t, "SELECT id, pash, token FROM users")
	if len(after) != 1 || after[0]["id"] != before[0]["id"] || after[0]["pash"] != before[0]["pash"] || after[0]["token"] != before[0]["token"] {
		t.Fatalf("second init changed the users: %v -> %v", before, after)
	}
	requireStatus(t, "original root", e.Post(t, "", "/login", map[string]string{"username": "first", "password": "first-password"}), http.StatusOK)
	requireDenied(t, "second root", e.Post(t, "", "/login", map[string]string{"username": "second", "password": "second-password"}))
}
