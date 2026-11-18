package auth

import (
	"bytes"
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// loginScenarios are the scenarios of POST /login (route-r000).
func loginScenarios() []harness.Scenario {
	return []harness.Scenario{
		denial("wrong-password-denied", "error", "initial", 1, "server/Auth.js:322", "server/auth/LocalAuthStrategy.js:40-62"),
		denial("wrong-password-repeat-denied", "error", "repeat", 2, "server/Auth.js:322", "server/auth/LocalAuthStrategy.js:40-62"),
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "initial", Assertion: "malformed-login-json-rejected",
			Sources: []string{"server/Server.js:349-352"},
			Claim:   "POST /login with a malformed JSON body is rejected with 400 and no token material or refresh cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				checkRejected(t, e, http.StatusBadRequest, `{"username":`)
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "edge", State: "initial", Assertion: "missing-login-credential-keys-rejected",
			Sources: []string{"server/libs/passportLocal/strategy.js:77-93", "server/Auth.js:322"},
			Claim:   "POST /login with a missing or null username or password key is rejected with 400 and no token material.",
			Run: func(t *testing.T, e *harness.Env) {
				checkRejected(t, e, http.StatusBadRequest,
					`{"password":"trial-listener-password"}`, `{"username":"trial-listener"}`,
					`{"username":null,"password":"trial-listener-password"}`, `{"username":"trial-listener","password":null}`)
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "edge", State: "initial", Assertion: "empty-login-credential-values-denied",
			Sources: []string{"server/libs/passportLocal/strategy.js:92-93", "server/Auth.js:322"},
			Claim:   "POST /login with empty username or password strings reaches authentication and is denied with 401 and no token material.",
			Run: func(t *testing.T, e *harness.Env) {
				checkRejected(t, e, http.StatusUnauthorized, `{"username":"","password":""}`, `{"username":"trial-listener","password":""}`)
			},
		},
		browserLogin("browser-login-user-and-secure-cookie", "initial", "https"),
		browserLogin("browser-login-http-cookie", "repeat", "http"),
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-SUCCESS-001", Case: "normal", State: "initial", Assertion: "login-payload-fields",
			Sources: []string{"server/Auth.js:96-105", "server/Auth.js:296-312", "server/models/User.js:603-641", "server/objects/settings/ServerSettings.js:299-309"},
			Claim:   "A login answer carries the user projection, the default library id, the server settings without secrets, an ereader device list and the server source, and the cookie lifetime is the 30 day default.",
			Run:     checkLoginPayload,
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-SUCCESS-001", Case: "edge", State: "initial", Assertion: "login-username-case-insensitive",
			Sources: []string{"server/auth/LocalAuthStrategy.js:53"},
			Claim:   "POST /login matches the username case-insensitively and the answer carries the stored username.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Post(t, "", "/login", map[string]string{"username": "TRIAL-Listener", "password": listenerPassword})
				requireStatus(t, "mixed-case login", resp, http.StatusOK)
				var payload loginPayload
				resp.JSON(t, &payload)
				if payload.User.ID != e.IDs["listener"] || payload.User.Username != listenerName {
					t.Fatalf("login answered user %q/%q, want %q/%q", payload.User.ID, payload.User.Username, e.IDs["listener"], listenerName)
				}
				bad := e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": "TRIAL-LISTENER-PASSWORD"})
				requireDenied(t, "password in other case", bad)
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-TOKEN-MODE-001", Case: "normal", State: "initial", Assertion: "token-mode-login-returns-refresh-token",
			Sources: []string{"server/Auth.js:322-327", "server/Auth.js:296-312", "server/auth/TokenManager.js:182-205"},
			Claim:   "POST /login with X-Return-Tokens: true returns the refresh token in user.refreshToken, sets no refresh cookie, and stores exactly that token in a new sessions row of the user.",
			Run:     checkTokenModeLogin,
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-TOKEN-MODE-001", Case: "edge", State: "initial", Assertion: "token-mode-needs-exact-true",
			Sources: []string{"server/Auth.js:324"},
			Claim:   "POST /login with X-Return-Tokens other than the text true behaves as a browser login: refreshToken null and a refresh cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				resp := loginAs(t, e, acc, map[string]string{"X-Return-Tokens": "false"})
				requireStatus(t, "login", resp, http.StatusOK)
				var payload loginPayload
				resp.JSON(t, &payload)
				if payload.User.RefreshToken != nil || cookieValue(resp, "refresh_token") == "" {
					t.Fatalf("X-Return-Tokens: false must give refreshToken null and a cookie: %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "initial", Assertion: "unknown-user-denied",
			Sources: []string{"server/auth/LocalAuthStrategy.js:53-62"},
			Claim:   "POST /login with an unknown username is denied with 401 and no token material.",
			Run: func(t *testing.T, e *harness.Env) {
				requireDenied(t, "unknown user", e.Post(t, "", "/login", map[string]string{"username": "nobody-here", "password": listenerPassword}))
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "failure", Assertion: "failed-login-writes-no-session",
			Sources: []string{"server/Auth.js:322-327", "server/auth/TokenManager.js:182-205"},
			Claim:   "A denied or rejected login creates no sessions row and sets no refresh cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				before := e.Count(t, "SELECT COUNT(*) FROM sessions")
				for _, body := range []string{`{"username":"trial-listener","password":"nope"}`, `{"username":"nobody","password":"x"}`, `{"username":"trial-listener"}`} {
					resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: body})
					if resp.Status < 400 || cookieValue(resp, "refresh_token") != "" {
						t.Fatalf("login %s: status %d cookie %q", body, resp.Status, cookieValue(resp, "refresh_token"))
					}
				}
				if after := e.Count(t, "SELECT COUNT(*) FROM sessions"); after != before {
					t.Fatalf("rejected logins changed the sessions count from %d to %d", before, after)
				}
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "changed", Assertion: "inactive-user-login-denied-until-reactivated",
			Sources: []string{"server/auth/LocalAuthStrategy.js:55-62"},
			Claim:   "A user who was deactivated is denied at POST /login with 401 and no new session, and can log in again once reactivated.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				requireStatus(t, "active login", loginAs(t, e, acc, nil), http.StatusOK)
				patchUser(t, e, acc.ID, map[string]any{"isActive": false})
				before := sessionCount(t, e, acc.ID)
				requireDenied(t, "inactive login", loginAs(t, e, acc, nil))
				if after := sessionCount(t, e, acc.ID); after != before {
					t.Fatalf("denied login changed sessions from %d to %d", before, after)
				}
				patchUser(t, e, acc.ID, map[string]any{"isActive": true})
				requireStatus(t, "reactivated login", loginAs(t, e, acc, nil), http.StatusOK)
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "changed", Assertion: "login-follows-password-change",
			Sources: []string{"server/controllers/UserController.js:244-250", "server/auth/LocalAuthStrategy.js:84-96"},
			Claim:   "After an admin changes a user's password, POST /login with the old password is denied with 401 and the new password logs in.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				patchUser(t, e, acc.ID, map[string]any{"password": "replacement-password"})
				requireDenied(t, "old password", loginAs(t, e, acc, nil))
				acc.Password = "replacement-password"
				requireStatus(t, "new password", loginAs(t, e, acc, nil), http.StatusOK)
			},
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "changed", Assertion: "user-without-password-denied",
			Sources: []string{"server/auth/LocalAuthStrategy.js:79-82"},
			Claim:   "A non-root user stored without a password hash (as OpenID-created users are) is denied at POST /login with 401 for any password, including the empty one, and gets no session.",
			Run:     checkPasswordlessUser,
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "error", State: "changed", Assertion: "local-auth-disabled-login-denied",
			Sources: []string{"server/Auth.js:113-115", "server/controllers/MiscController.js:644-740", "server/Auth.js:322"},
			Claim:   "When the active auth methods no longer include local, POST /login is refused with an error status and no token material; enabling local again restores it.",
			Run:     checkLocalAuthDisabled,
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "edge", State: "repeat", Assertion: "login-rate-limit-429",
			Sources: []string{"server/utils/rateLimiterFactory.js:40-75", "server/Auth.js:322"},
			Claim:   "With RATE_LIMIT_AUTH_MAX=3 the fourth POST /login in the window is answered 429 with an error message, even with valid credentials, and no session is created.",
			Run:     checkLoginRateLimit,
		},
		{
			Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: "edge", State: "initial", Assertion: "passwordless-root-login-rules",
			Sources: []string{"server/auth/LocalAuthStrategy.js:65-82", "server/Server.js:485-498"},
			Claim:   "A root created without a password logs in only with an empty password; any non-empty password is denied with 401.",
			Run:     checkPasswordlessRoot,
		},
	}
}

// denial builds a wrong-password scenario that sends the login attempts times.
func denial(assertion, caseName, state string, attempts int, sources ...string) harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r000", Invariant: "INV-R000-LOGIN-DENIAL-001", Case: caseName, State: state, Assertion: assertion,
		Sources: sources,
		Claim:   "POST /login with a wrong password is denied with 401 and no token material, however often it is repeated.",
		Run: func(t *testing.T, e *harness.Env) {
			for range attempts {
				resp := e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": "definitely-not-the-fixture-password"})
				if resp.Status != http.StatusUnauthorized || hasTokenMaterial(resp.Body) {
					t.Fatalf("invalid password must return 401 without token material, got %d %s", resp.Status, resp.Body)
				}
			}
		},
	}
}

// browserLogin builds a successful-login scenario for a request scheme
// (the scheme is conveyed to the server by X-Forwarded-Proto).
func browserLogin(assertion, state, scheme string) harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r000", Invariant: "INV-R000-LOGIN-SUCCESS-001", Case: "normal", State: state, Assertion: assertion,
		Sources: []string{"server/Auth.js:322", "server/auth/TokenManager.js:65-73", "server/utils/requestUtils.js:8-15"},
		Claim:   "A browser login returns the fixture user with an access token, refreshToken:null and a bookmarks array, and sets an HttpOnly refresh cookie that is Secure exactly when the request is HTTPS.",
		Run: func(t *testing.T, e *harness.Env) {
			resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": listenerName, "password": listenerPassword}, Header: map[string]string{"X-Forwarded-Proto": scheme}})
			if err := checkBrowserLogin(resp.Status, resp.Body, resp.Cookies, e.IDs["listener"], scheme); err != nil {
				t.Fatal(err)
			}
		},
	}
}

func hasTokenMaterial(body []byte) bool {
	return bytes.Contains(body, []byte("accessToken")) || bytes.Contains(body, []byte("refreshToken"))
}

// checkRejected posts each raw payload to /login and requires the status, no
// token material and no refresh cookie.
// Server.js:352 parses JSON before Auth.js:322 authenticates. passportLocal/
// strategy.js:92-93 rejects missing/null keys with 400, while supplied empty
// strings reach verifyCredentials and fail authentication with Passport's 401.
func checkRejected(t *testing.T, e *harness.Env, status int, payloads ...string) {
	t.Helper()
	for _, payload := range payloads {
		resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: payload})
		if resp.Status != status || hasTokenMaterial(resp.Body) {
			t.Fatalf("login input %s must return %d without token material; got %d", payload, status, resp.Status)
		}
		for _, cookie := range resp.Cookies {
			if cookie.Name == "refresh_token" && cookie.Value != "" {
				t.Fatal("rejected login must not establish a refresh cookie")
			}
		}
	}
}
