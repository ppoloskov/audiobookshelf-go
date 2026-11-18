package auth

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// authorizeScenarios are the scenarios of POST /api/authorize (route-r207).
func authorizeScenarios() []harness.Scenario {
	const (
		payload = "INV-R207-SAMPLED"
		creds   = "INV-R207-CREDENTIALS-001"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r207", Invariant: payload, Case: "normal", State: "initial", Assertion: "authorize-returns-user-payload",
			Sources: []string{"server/routers/ApiRouter.js:350", "server/controllers/MiscController.js:285-288", "server/Auth.js:96-105"},
			Claim:   "POST /api/authorize with a valid bearer token answers the login payload of that user (user, userDefaultLibraryId, serverSettings without secrets, ereaderDevices, Source) and creates no session or cookie.",
			Run:     checkAuthorizePayload,
		},
		{
			Requirement: "route-r207", Invariant: payload, Case: "normal", State: "repeat", Assertion: "authorize-repeat-is-stable",
			Sources: []string{"server/controllers/MiscController.js:285-288"},
			Claim:   "Repeating POST /api/authorize answers the same user and default library and adds no sessions row.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				before := e.Count(t, "SELECT COUNT(*) FROM sessions")
				var first loginPayload
				for i := range 2 {
					resp := e.Post(t, token, "/api/authorize", nil)
					requireStatus(t, "authorize", resp, http.StatusOK)
					var got loginPayload
					resp.JSON(t, &got)
					if i == 0 {
						first = got
					} else if got.User.ID != first.User.ID || !reflect.DeepEqual(got.UserDefaultLibraryID, first.UserDefaultLibraryID) {
						t.Fatalf("repeat answered %+v, first %+v", got.User, first.User)
					}
				}
				if after := e.Count(t, "SELECT COUNT(*) FROM sessions"); after != before {
					t.Fatalf("authorize changed sessions from %d to %d", before, after)
				}
			},
		},
		{
			Requirement: "route-r207", Invariant: payload, Case: "normal", State: "changed", Assertion: "authorize-reflects-permission-change",
			Sources: []string{"server/Auth.js:96-105", "server/models/User.js:691-694", "server/controllers/UserController.js:254-320"},
			Claim:   "userDefaultLibraryId follows the user's library permissions: the first accessible library, and again the first library once all libraries are allowed.",
			Run:     checkAuthorizeFollowsPermissions,
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "edge", State: "initial", Assertion: "authorize-accepts-token-query-and-legacy-token",
			Sources: []string{"server/Auth.js:120-135", "server/auth/TokenManager.js:306-345"},
			Claim:   "POST /api/authorize also accepts the access token as ?token= query parameter and the legacy non-expiring user.token as bearer, both authorizing the same user.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginBrowser(t, e, acc)
				resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/authorize", Query: url.Values{"token": {s.Access}}})
				requireAuthorizedAs(t, resp, acc.ID)
				if s.Payload.User.Token == "" {
					t.Fatal("login must carry the legacy token")
				}
				requireAuthorizedAs(t, e.Post(t, s.Payload.User.Token, "/api/authorize", nil), acc.ID)
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "edge", State: "initial", Assertion: "authorize-accepts-active-api-key",
			Sources: []string{"server/auth/TokenManager.js:278-304", "server/controllers/ApiKeyController.js:50-110"},
			Claim:   "An active API key used as bearer token authorizes as the key's user.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				key := newAPIKey(t, e, acc.ID, 0, true)
				requireAuthorizedAs(t, e.Post(t, key.Token, "/api/authorize", nil), acc.ID)
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "edge", State: "initial", Assertion: "authorize-honours-jwt-secret-key-env",
			Sources: []string{"server/auth/TokenManager.js:42-62"},
			Claim:   "With JWT_SECRET_KEY set, tokens signed with that secret authorize the user they name, tokens signed with another secret are denied, and the secret is not stored in the server settings.",
			Run: func(t *testing.T, _ *harness.Env) {
				const secret = "scenario-owned-secret-key"
				e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0", "JWT_SECRET_KEY": secret}})
				stored := func() string {
					rows := e.Rows(t, "SELECT value FROM settings WHERE key = 'server-settings'")
					if len(rows) != 1 {
						t.Fatalf("want one server-settings row, got %d", len(rows))
					}
					var settings struct {
						TokenSecret string `json:"tokenSecret"`
					}
					if err := jsonDecode(rows[0]["value"], &settings); err != nil {
						t.Fatal(err)
					}
					return settings.TokenSecret
				}
				storedSecret := stored()
				if storedSecret == "" || storedSecret == secret {
					t.Fatalf("the stored fixture secret must exist and differ from the env secret")
				}
				claims := map[string]any{"userId": e.IDs["listener"], "type": "access", "jti": "scenario", "exp": time.Now().Add(time.Hour).Unix()}
				requireAuthorizedAs(t, e.Post(t, forgeJWT(t, secret, claims), "/api/authorize", nil), e.IDs["listener"])
				if status := authorizeStatus(t, e, forgeJWT(t, storedSecret, claims)); status != http.StatusUnauthorized {
					t.Fatalf("token signed with the stored secret: status %d, want 401 while JWT_SECRET_KEY is set", status)
				}
				// Tokens the server issues are signed with the env secret too.
				requireAuthorizedAs(t, e.Post(t, tokenFor(t, e, account{Name: listenerName, Password: listenerPassword}), "/api/authorize", nil), e.IDs["listener"])
				if stored() != storedSecret || strings.Contains(e.Rows(t, "SELECT value FROM settings WHERE key = 'server-settings'")[0]["value"].(string), secret) {
					t.Fatal("the env secret must not be written to the server settings and the stored secret must stay")
				}
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "error", State: "initial", Assertion: "authorize-rejects-missing-and-invalid-tokens",
			Sources: []string{"server/Auth.js:61-63", "server/auth/TokenManager.js:81-85", "server/auth/TokenManager.js:306-317"},
			Claim:   "POST /api/authorize without a token, with garbage, with a wrongly signed access token or with a refresh token as bearer is denied with 401.",
			Run:     checkAuthorizeRejectsInvalid,
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "error", State: "failure", Assertion: "rejected-authorize-leaves-state-unchanged",
			Sources: []string{"server/Auth.js:61-63", "server/auth/TokenManager.js:306-317"},
			Claim:   "Denied authorize requests change no sessions row and set no cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				before := sessionSnapshot(t, e)
				for _, token := range []string{"", "garbage", s.Refresh, foreignToken(t, acc.ID, "access")} {
					resp := e.Post(t, token, "/api/authorize", nil)
					if resp.Status != http.StatusUnauthorized || len(resp.Cookies) != 0 {
						t.Fatalf("token %.12q: status %d cookies %v", token, resp.Status, resp.Cookies)
					}
				}
				if after := sessionSnapshot(t, e); !reflect.DeepEqual(before, after) {
					t.Fatal("denied authorize changed the sessions table")
				}
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "error", State: "changed", Assertion: "authorize-rejects-inactive-user",
			Sources: []string{"server/auth/TokenManager.js:320-326", "server/auth/TokenManager.js:298-303"},
			Claim:   "Access tokens and API keys of a deactivated user are denied with 401 and work again after reactivation.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginBrowser(t, e, acc)
				key := newAPIKey(t, e, acc.ID, 0, true)
				requireAuthorizedAs(t, e.Post(t, s.Access, "/api/authorize", nil), acc.ID)
				patchUser(t, e, acc.ID, map[string]any{"isActive": false})
				for name, token := range map[string]string{"access token": s.Access, "api key": key.Token} {
					if status := authorizeStatus(t, e, token); status != http.StatusUnauthorized {
						t.Fatalf("%s of an inactive user: status %d", name, status)
					}
				}
				patchUser(t, e, acc.ID, map[string]any{"isActive": true})
				requireAuthorizedAs(t, e.Post(t, s.Access, "/api/authorize", nil), acc.ID)
				requireAuthorizedAs(t, e.Post(t, key.Token, "/api/authorize", nil), acc.ID)
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "error", State: "changed", Assertion: "authorize-rejects-inactive-api-key",
			Sources: []string{"server/auth/TokenManager.js:278-285", "server/controllers/ApiKeyController.js:112-160"},
			Claim:   "An inactive API key is denied with 401; activating it through PATCH /api/api-keys/:id authorizes it and deactivating it denies it again.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				key := newAPIKey(t, e, acc.ID, 0, false)
				if status := authorizeStatus(t, e, key.Token); status != http.StatusUnauthorized {
					t.Fatalf("inactive key: status %d", status)
				}
				setAPIKeyActive(t, e, key.ID, true)
				requireAuthorizedAs(t, e.Post(t, key.Token, "/api/authorize", nil), acc.ID)
				setAPIKeyActive(t, e, key.ID, false)
				if status := authorizeStatus(t, e, key.Token); status != http.StatusUnauthorized {
					t.Fatalf("deactivated key: status %d", status)
				}
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "edge", State: "changed", Assertion: "authorize-rejects-expired-api-key-and-deactivates-it",
			Sources: []string{"server/auth/TokenManager.js:287-296"},
			Claim:   "An API key created with expiresIn 2 authorizes at first, is denied with 401 once expired, and its row is then deactivated.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				key := newAPIKey(t, e, acc.ID, 2, true)
				requireAuthorizedAs(t, e.Post(t, key.Token, "/api/authorize", nil), acc.ID)
				harness.Eventually(t, 15*time.Second, "the API key to expire", func() bool {
					return authorizeStatus(t, e, key.Token) == http.StatusUnauthorized
				})
				harness.Eventually(t, 5*time.Second, "the expired key to be deactivated", func() bool {
					rows := e.Rows(t, "SELECT isActive FROM apiKeys WHERE id = ?", key.ID)
					return len(rows) == 1 && rows[0]["isActive"] == int64(0)
				})
			},
		},
		{
			Requirement: "route-r207", Invariant: creds, Case: "edge", State: "changed", Assertion: "authorize-rejects-expired-access-token",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:312-317"},
			Claim:   "With ACCESS_TOKEN_EXPIRY=2 an access token authorizes at first and is denied with 401 once expired; a refresh yields a working token.",
			Run:     checkAccessTokenExpiry,
		},
	}
}

// requireAuthorizedAs requires an authorize answer for the given user id.
func requireAuthorizedAs(t *testing.T, resp harness.Response, userID string) {
	t.Helper()
	requireStatus(t, "authorize", resp, http.StatusOK)
	var got loginPayload
	resp.JSON(t, &got)
	if got.User.ID != userID {
		t.Fatalf("authorized as %q, want %q", got.User.ID, userID)
	}
}

// apiKey is an API key created through the admin API.
type apiKey struct {
	ID    string
	Token string
}

// newAPIKey creates an API key for a user as root; expiresIn 0 means no expiry.
func newAPIKey(t *testing.T, e *harness.Env, userID string, expiresIn int, active bool) apiKey {
	t.Helper()
	body := map[string]any{"name": "auth-scenario-key", "userId": userID, "isActive": active}
	if expiresIn > 0 {
		body["expiresIn"] = expiresIn
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/api-keys", body)
	requireStatus(t, "create api key", resp, http.StatusOK)
	var created struct {
		APIKey struct {
			ID     string `json:"id"`
			APIKey string `json:"apiKey"`
		} `json:"apiKey"`
	}
	resp.JSON(t, &created)
	if created.APIKey.ID == "" || created.APIKey.APIKey == "" {
		t.Fatalf("api key answer: %s", resp.Body)
	}
	return apiKey{ID: created.APIKey.ID, Token: created.APIKey.APIKey}
}

// setAPIKeyActive toggles an API key as root.
func setAPIKeyActive(t *testing.T, e *harness.Env, id string, active bool) {
	t.Helper()
	requireStatus(t, "patch api key", e.Patch(t, e.Token(t, "root"), "/api/api-keys/"+id, map[string]any{"isActive": active}), http.StatusOK)
}

func checkAuthorizePayload(t *testing.T, e *harness.Env) {
	token := e.Token(t, "listener")
	before := e.Count(t, "SELECT COUNT(*) FROM sessions")
	resp := e.Post(t, token, "/api/authorize", nil)
	requireStatus(t, "authorize", resp, http.StatusOK)
	var got loginPayload
	resp.JSON(t, &got)
	requireListenerUser(t, e, resp.Body)
	if got.User.ID != e.IDs["listener"] || got.User.Username != listenerName || got.User.Type != "user" || !got.User.IsActive || got.User.Token == "" {
		t.Fatalf("authorize user = %+v", got.User)
	}
	if got.UserDefaultLibraryID == nil || *got.UserDefaultLibraryID != e.IDs["library"] {
		t.Fatalf("userDefaultLibraryId = %v", got.UserDefaultLibraryID)
	}
	if got.EreaderDevices == nil || got.Source != "docker" || got.ServerSettings["version"] != "2.37.2" {
		t.Fatalf("authorize answer lacks ereaderDevices/Source/serverSettings: %s", resp.Body)
	}
	if _, leaked := got.ServerSettings["tokenSecret"]; leaked {
		t.Fatal("serverSettings must not carry tokenSecret")
	}
	if len(resp.Cookies) != 0 || e.Count(t, "SELECT COUNT(*) FROM sessions") != before {
		t.Fatal("authorize must neither set cookies nor create sessions")
	}
}

func checkAuthorizeFollowsPermissions(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, map[string]any{
		"permissions":         map[string]any{"accessAllLibraries": false},
		"librariesAccessible": []string{e.IDs["podcastLibrary"]},
	})
	token, _ := e.Login(t, acc.Name, acc.Password)
	defaultLibrary := func() *string {
		resp := e.Post(t, token, "/api/authorize", nil)
		requireStatus(t, "authorize", resp, http.StatusOK)
		var got loginPayload
		resp.JSON(t, &got)
		return got.UserDefaultLibraryID
	}
	if got := defaultLibrary(); got == nil || *got != e.IDs["podcastLibrary"] {
		t.Fatalf("restricted default library = %v, want the podcast library", got)
	}
	patchUser(t, e, acc.ID, map[string]any{"permissions": map[string]any{"accessAllLibraries": true}, "librariesAccessible": []string{}})
	if got := defaultLibrary(); got == nil || *got != e.IDs["library"] {
		t.Fatalf("unrestricted default library = %v, want the first library", got)
	}
}

func checkAuthorizeRejectsInvalid(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	for name, token := range map[string]string{
		"no token":            "",
		"garbage":             "garbage",
		"wrong signature":     foreignToken(t, acc.ID, "access"),
		"refresh as bearer":   s.Refresh,
		"foreign legacy form": forgeJWT(t, "not-the-server-secret", map[string]any{"userId": acc.ID, "username": acc.Name}),
	} {
		if status := authorizeStatus(t, e, token); status != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401", name, status)
		}
	}
}

func checkAccessTokenExpiry(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0", "ACCESS_TOKEN_EXPIRY": "2"}})
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	requireAuthorizedAs(t, e.Post(t, s.Access, "/api/authorize", nil), acc.ID)
	harness.Eventually(t, 15*time.Second, "the access token to expire", func() bool {
		return authorizeStatus(t, e, s.Access) == http.StatusUnauthorized
	})
	fresh := refreshed(t, "refresh after expiry", refreshByHeader(t, e, s.Refresh))
	requireAuthorizedAs(t, e.Post(t, fresh.User.AccessToken, "/api/authorize", nil), acc.ID)
}
