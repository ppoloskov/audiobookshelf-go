package launch

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// deviceScenarios prove the boot-boundary workflow a client needs: read the
// uninitialised status, initialise on a fresh install, then log in and read the
// user identity; a bad password changes nothing.
func deviceScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "client-device-acceptance", Invariant: "INV-CLIENT-DEVICE-ACCEPTANCE", Case: "normal", State: "initial", Assertion: "device-fresh-status-drives-setup",
			Sources: []string{"client/pages/login.vue:266-270", "server/Server.js:384-400"},
			Claim:   "A device booting a fresh install sees isInit false with the actual config and metadata paths in /status and a live /ping.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Fresh: true})
				defer own.Stop(t)
				checkPing(t, own.Get(t, "", "/ping"))
				resp := own.Get(t, "", "/status")
				requireStatus(t, resp, http.StatusOK, "fresh GET /status")
				var payload launchStatus
				resp.JSON(t, &payload)
				if payload.IsInit {
					t.Fatal("fresh /status reported isInit true")
				}
				if payload.ConfigPath == nil || *payload.ConfigPath != own.Config {
					t.Fatalf("fresh /status ConfigPath = %v, want %q", payload.ConfigPath, own.Config)
				}
				if payload.MetadataPath == nil || *payload.MetadataPath != own.Metadata {
					t.Fatalf("fresh /status MetadataPath = %v, want %q", payload.MetadataPath, own.Metadata)
				}
			},
		},
		{
			Requirement: "client-device-acceptance", Invariant: "INV-CLIENT-DEVICE-ACCEPTANCE", Case: "normal", State: "repeat", Assertion: "device-boot-status-stable-on-repeat",
			Sources: []string{"server/Server.js:384-404", "client/pages/login.vue:266-270"},
			Claim:   "On an initialized install repeated /status and /ping reads and repeated device logins succeed with stable boot data.",
			Run: func(t *testing.T, e *harness.Env) {
				first := e.Get(t, "", "/status")
				second := e.Get(t, "", "/status")
				requireStatus(t, first, http.StatusOK, "first GET /status")
				requireStatus(t, second, http.StatusOK, "second GET /status")
				var payload launchStatus
				first.JSON(t, &payload)
				if !payload.IsInit || payload.App != "audiobookshelf" || payload.ServerVersion != "2.37.2" {
					t.Fatalf("initialized /status identity = %+v", payload)
				}
				if string(first.Body) != string(second.Body) {
					t.Fatalf("repeated /status bodies differ: %s vs %s", first.Body, second.Body)
				}
				checkPing(t, e.Get(t, "", "/ping"))
				checkPing(t, e.Get(t, "", "/ping"))
				for range 2 {
					if token, _ := e.Login(t, fixtureUsername(t, e, "root"), fixturePassword(t, e, "root")); token == "" {
						t.Fatal("device login returned no token")
					}
				}
			},
		},
		{
			Requirement: "client-device-acceptance", Invariant: "INV-CLIENT-DEVICE-ACCEPTANCE", Case: "normal", State: "changed", Assertion: "device-init-changes-status-and-login",
			Sources: []string{"client/pages/login.vue:162-165", "server/Server.js:377-383", "server/Server.js:384-400"},
			Claim:   "POST /init changes /status from isInit false with paths to isInit true without them, and the new root credentials authenticate /api/me.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Fresh: true})
				defer own.Stop(t)
				before := own.Get(t, "", "/status")
				requireStatus(t, before, http.StatusOK, "fresh GET /status")
				var initial launchStatus
				before.JSON(t, &initial)
				if initial.IsInit || initial.ConfigPath == nil {
					t.Fatalf("pre-init /status = %+v, want uninitialised with paths", initial)
				}
				const username = "launch-root"
				const password = "launch-root-password"
				requireStatus(t, own.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": username, "password": password}}), http.StatusOK, "POST /init")
				after := own.Get(t, "", "/status")
				requireStatus(t, after, http.StatusOK, "GET /status after init")
				var initialized launchStatus
				after.JSON(t, &initialized)
				if !initialized.IsInit {
					t.Fatal("/status isInit is still false after init")
				}
				if initialized.ConfigPath != nil || initialized.MetadataPath != nil {
					t.Fatalf("initialized /status still leaks paths: %+v", initialized)
				}
				token, _ := own.Login(t, username, password)
				if token == "" {
					t.Fatal("new root credentials did not return a token")
				}
				me := own.Get(t, token, "/api/me")
				requireStatus(t, me, http.StatusOK, "GET /api/me")
				var identity struct {
					Username string `json:"username"`
				}
				me.JSON(t, &identity)
				if identity.Username != username {
					t.Fatalf("device identity username = %q, want %q", identity.Username, username)
				}
			},
		},
		{
			Requirement: "client-device-acceptance", Invariant: "INV-CLIENT-DEVICE-ACCEPTANCE", Case: "edge", State: "initial", Assertion: "device-status-public-with-stale-token",
			Sources: []string{"server/Server.js:384-404", "client/pages/login.vue:266-270"},
			Claim:   "The device boot read of /status stays public even with a stale bearer token, and HEAD /ping answers 200 with an empty body.",
			Run: func(t *testing.T, e *harness.Env) {
				plain := e.Get(t, "", "/status")
				requireStatus(t, plain, http.StatusOK, "public GET /status")
				stale := e.Do(t, harness.Request{Path: "/status", Token: "an-expired-token"})
				requireStatus(t, stale, http.StatusOK, "GET /status with a stale token")
				if string(plain.Body) != string(stale.Body) {
					t.Fatalf("a stale token changed the public status body: %s vs %s", plain.Body, stale.Body)
				}
				head := e.Do(t, harness.Request{Method: http.MethodHead, Path: "/ping"})
				requireStatus(t, head, http.StatusOK, "HEAD /ping")
				if len(head.Body) != 0 {
					t.Fatalf("HEAD /ping body = %q, want empty", head.Body)
				}
			},
		},
		{
			Requirement: "client-device-acceptance", Invariant: "INV-CLIENT-DEVICE-ACCEPTANCE-FAILURE-001", Case: "error", State: "failure", Assertion: "device-wrong-password-no-session",
			Sources: []string{"server/Auth.js:322", "server/auth/LocalAuthStrategy.js:90-96", "client/pages/login.vue:212-218"},
			Claim:   "A device login with a wrong password is rejected with 401 and leaves the user and session rows unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				username := fixtureUsername(t, e, "root")
				usersBefore := e.Count(t, "SELECT COUNT(*) FROM users")
				sessionsBefore := e.Count(t, "SELECT COUNT(*) FROM sessions")
				resp := e.Post(t, "", "/login", map[string]string{"username": username, "password": "definitely-wrong-password"})
				requireStatus(t, resp, http.StatusUnauthorized, "login with wrong password")
				if got := e.Count(t, "SELECT COUNT(*) FROM users"); got != usersBefore {
					t.Fatalf("user count changed after failed login: %d -> %d", usersBefore, got)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM sessions"); got != sessionsBefore {
					t.Fatalf("session count changed after failed login: %d -> %d", sessionsBefore, got)
				}
			},
		},
	}
}

// fixtureUser is one named login in an install's fixture.json.
type fixtureUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// fixtureCredentials reads a named fixture user from the install.
func fixtureCredentials(t *testing.T, e *harness.Env, who string) fixtureUser {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.Install, "fixture.json"))
	if err != nil {
		t.Fatalf("read fixture users: %v", err)
	}
	var meta struct {
		Users map[string]fixtureUser `json:"users"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("decode fixture users: %v", err)
	}
	user, ok := meta.Users[who]
	if !ok {
		t.Fatalf("fixture user %q is missing", who)
	}
	return user
}

func fixtureUsername(t *testing.T, e *harness.Env, who string) string {
	t.Helper()
	return fixtureCredentials(t, e, who).Username
}

func fixturePassword(t *testing.T, e *harness.Env, who string) string {
	t.Helper()
	return fixtureCredentials(t, e, who).Password
}
