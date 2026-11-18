package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// refreshScenarios are the scenarios of POST /auth/refresh (route-r001).
func refreshScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-LIFECYCLE-001", Case: "normal", State: "initial", Assertion: "refresh-via-cookie-rotates-session",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:65-73", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425"},
			Claim:   "POST /auth/refresh with the refresh cookie answers a new access token and refreshToken null, sets a rotated HttpOnly refresh cookie with the request-security and 30 day lifetime, and moves the old token to lastRefreshToken in the one sessions row.",
			Run:     checkRefreshByCookie,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-SAMPLED", Case: "normal", State: "initial", Assertion: "refresh-via-header-returns-refresh-token",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425"},
			Claim:   "POST /auth/refresh with x-refresh-token answers user.refreshToken equal to the rotated token stored in the sessions row, and the refresh cookie carries the same token.",
			Run:     checkRefreshByHeader,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-SAMPLED", Case: "edge", State: "initial", Assertion: "refresh-header-wins-over-cookie",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425"},
			Claim:   "When both the x-refresh-token header and the refresh cookie are sent, the header token's session is rotated, the cookie's session is untouched and the refresh token is returned in the body.",
			Run:     checkRefreshHeaderWins,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-GRACE-001", Case: "edge", State: "repeat", Assertion: "rotated-token-replays-within-grace",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425", "server/Auth.js:349-358"},
			Claim:   "Replaying a just-rotated refresh token inside the grace period answers 200 with a fresh access token and the already rotated refresh token, without rotating the session again.",
			Run:     checkRefreshGraceReplay,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-GRACE-001", Case: "error", State: "changed", Assertion: "two-generations-old-token-denied",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425", "server/Auth.js:349-358"},
			Claim:   "After two rotations the first refresh token matches neither refreshToken nor lastRefreshToken and is denied with 401, while the previous one still replays inside the grace period.",
			Run:     checkRefreshTwoGenerations,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-GRACE-001", Case: "error", State: "changed", Assertion: "grace-expiry-denies-rotated-token",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:205-268", "server/auth/TokenManager.js:350-425", "server/Auth.js:349-358"},
			Claim:   "With REFRESH_TOKEN_GRACE_PERIOD=2 a rotated token is denied with 401 once the grace period is over, while the current token still refreshes.",
			Run:     checkRefreshGraceExpiry,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "error", State: "initial", Assertion: "missing-refresh-token-denied",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:350-455"},
			Claim:   "POST /auth/refresh with neither cookie nor header is denied with 401 and an error message.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Post(t, "", "/auth/refresh", nil)
				requireDeniedWithError(t, "no refresh token", resp)
			},
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "error", State: "initial", Assertion: "invalid-refresh-tokens-denied",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:350-455"},
			Claim:   "POST /auth/refresh with garbage, a wrongly signed refresh token, or an access token is denied with 401 and an error message.",
			Run:     checkRefreshInvalidTokens,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "error", State: "failure", Assertion: "denied-refresh-leaves-sessions-unchanged",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:350-455"},
			Claim:   "A denied refresh changes no sessions row.",
			Run:     checkRefreshDenialChangesNothing,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "error", State: "changed", Assertion: "inactive-user-refresh-denied",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:350-455"},
			Claim:   "A refresh token of a deactivated user is denied with 401 and leaves the sessions row unchanged; after reactivation the same token refreshes.",
			Run:     checkRefreshInactiveUser,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "edge", State: "changed", Assertion: "session-expired-in-database-denied-and-removed",
			Sources: []string{"server/auth/TokenManager.js:387-396"},
			Claim:   "A refresh token whose sessions row has expired in the database is denied with 401 and the row is removed, although the token itself is still valid.",
			Run:     checkRefreshSessionExpiredInDatabase,
		},
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-DENIAL-001", Case: "edge", State: "changed", Assertion: "expired-refresh-token-denied-and-session-removed",
			Sources: []string{"server/Auth.js:331-358", "server/auth/TokenManager.js:350-455"},
			Claim:   "With REFRESH_TOKEN_EXPIRY=2 a refresh token past its lifetime is denied with 401 and its sessions row is removed.",
			Run:     checkRefreshExpired,
		},
	}
}

// requireDeniedWithError requires 401 and a non-empty JSON error message.
func requireDeniedWithError(t *testing.T, what string, resp harness.Response) {
	t.Helper()
	var answer struct {
		Error string `json:"error"`
	}
	resp.JSON(t, &answer)
	if resp.Status != http.StatusUnauthorized || answer.Error == "" || hasTokenMaterial(resp.Body) {
		t.Fatalf("%s: want 401 with an error message and no tokens, got %d: %s", what, resp.Status, resp.Body)
	}
}

// refreshed decodes a successful refresh answer.
func refreshed(t *testing.T, what string, resp harness.Response) loginPayload {
	t.Helper()
	requireStatus(t, what, resp, http.StatusOK)
	var payload loginPayload
	resp.JSON(t, &payload)
	if payload.User.AccessToken == "" {
		t.Fatalf("%s: no access token: %s", what, resp.Body)
	}
	return payload
}

func checkRefreshByCookie(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginBrowser(t, e, acc)
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"Cookie": "refresh_token=" + s.Refresh, "X-Forwarded-Proto": "https"}})
	payload := refreshed(t, "refresh", resp)
	if payload.User.ID != acc.ID || payload.User.RefreshToken != nil || payload.User.AccessToken == s.Access {
		t.Fatalf("cookie refresh must answer the user, a new access token and refreshToken null: %s", resp.Body)
	}
	if payload.UserDefaultLibraryID == nil || payload.ServerSettings == nil {
		t.Fatalf("refresh answer lacks the login payload fields: %s", resp.Body)
	}
	var rotated *http.Cookie
	for _, cookie := range resp.Cookies {
		if cookie.Name == "refresh_token" {
			rotated = cookie
		}
	}
	if rotated == nil || rotated.Value == "" || rotated.Value == s.Refresh {
		t.Fatalf("refresh must set a rotated cookie: %+v", rotated)
	}
	if !rotated.HttpOnly || !rotated.Secure || rotated.Path != "/" || rotated.SameSite != http.SameSiteLaxMode || rotated.MaxAge != defaultRefreshCookieSeconds {
		t.Fatalf("rotated cookie attributes = %+v", rotated)
	}
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("want one session row, got %d", len(rows))
	}
	requireString(t, "refreshToken", rows[0]["refreshToken"], rotated.Value)
	requireString(t, "lastRefreshToken", rows[0]["lastRefreshToken"], s.Refresh)
	if rows[0]["lastRefreshTokenExpiresAt"] == nil {
		t.Fatal("rotation must set lastRefreshTokenExpiresAt")
	}
	if status := authorizeStatus(t, e, payload.User.AccessToken); status != http.StatusOK {
		t.Fatalf("refreshed access token must authorize, got %d", status)
	}
}

func checkRefreshByHeader(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	resp := refreshByHeader(t, e, s.Refresh)
	payload := refreshed(t, "header refresh", resp)
	if payload.User.RefreshToken == nil || *payload.User.RefreshToken == "" || *payload.User.RefreshToken == s.Refresh {
		t.Fatalf("header refresh must return a new refresh token: %s", resp.Body)
	}
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("want one session row, got %d", len(rows))
	}
	requireString(t, "refreshToken", rows[0]["refreshToken"], *payload.User.RefreshToken)
	requireString(t, "refresh cookie", cookieValue(resp, "refresh_token"), *payload.User.RefreshToken)
}

func checkRefreshHeaderWins(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	viaCookie := loginBrowser(t, e, acc)
	viaHeader := loginNative(t, e, acc)
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"Cookie": "refresh_token=" + viaCookie.Refresh, "x-refresh-token": viaHeader.Refresh}})
	payload := refreshed(t, "refresh", resp)
	if payload.User.RefreshToken == nil {
		t.Fatalf("a header refresh returns the refresh token: %s", resp.Body)
	}
	rows := e.Rows(t, "SELECT refreshToken FROM sessions WHERE userId = ? AND refreshToken = ?", acc.ID, viaCookie.Refresh)
	if len(rows) != 1 {
		t.Fatal("the cookie's session must stay untouched")
	}
	rows = e.Rows(t, "SELECT lastRefreshToken FROM sessions WHERE userId = ? AND refreshToken = ?", acc.ID, *payload.User.RefreshToken)
	if len(rows) != 1 {
		t.Fatal("the header's session must carry the rotated token")
	}
	requireString(t, "lastRefreshToken", rows[0]["lastRefreshToken"], viaHeader.Refresh)
}

func checkRefreshGraceReplay(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	first := refreshed(t, "first refresh", refreshByHeader(t, e, s.Refresh))
	rotated := *first.User.RefreshToken
	resp := refreshByHeader(t, e, s.Refresh)
	replay := refreshed(t, "replay", resp)
	if replay.User.RefreshToken == nil || *replay.User.RefreshToken != rotated {
		t.Fatalf("replay must return the already rotated token %q: %s", rotated, resp.Body)
	}
	if replay.User.AccessToken == first.User.AccessToken {
		t.Fatal("replay must issue a fresh access token")
	}
	requireString(t, "replay cookie", cookieValue(resp, "refresh_token"), rotated)
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("want one session row, got %d", len(rows))
	}
	requireString(t, "refreshToken", rows[0]["refreshToken"], rotated)
	requireString(t, "lastRefreshToken", rows[0]["lastRefreshToken"], s.Refresh)
}

func checkRefreshTwoGenerations(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	t0 := loginNative(t, e, acc).Refresh
	t1 := *refreshed(t, "first", refreshByHeader(t, e, t0)).User.RefreshToken
	t2 := *refreshed(t, "second", refreshByHeader(t, e, t1)).User.RefreshToken
	requireDeniedWithError(t, "two generations old", refreshByHeader(t, e, t0))
	replay := refreshed(t, "previous generation", refreshByHeader(t, e, t1))
	if replay.User.RefreshToken == nil || *replay.User.RefreshToken != t2 {
		t.Fatalf("the previous token must replay the current token %q", t2)
	}
}

func checkRefreshGraceExpiry(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0", "REFRESH_TOKEN_GRACE_PERIOD": "2"}})
	acc := newAccount(t, e, nil)
	old := loginNative(t, e, acc).Refresh
	current := *refreshed(t, "rotation", refreshByHeader(t, e, old)).User.RefreshToken
	harness.Eventually(t, 15*time.Second, "the rotated token to be denied after the grace period", func() bool {
		return refreshByHeader(t, e, old).Status == http.StatusUnauthorized
	})
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("a denied replay must keep the session, got %d rows", len(rows))
	}
	requireString(t, "refreshToken", rows[0]["refreshToken"], current)
	refreshed(t, "current token", refreshByHeader(t, e, current))
}

// forgeJWT signs claims with HS256 and the given secret.
func forgeJWT(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput)) // hash writes never fail
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// foreignToken is a well-formed token of the user signed with the wrong secret.
func foreignToken(t *testing.T, userID, kind string) string {
	return forgeJWT(t, "not-the-server-secret", map[string]any{"userId": userID, "type": kind, "jti": "forged", "exp": time.Now().Add(time.Hour).Unix()})
}

func checkRefreshInvalidTokens(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	cases := map[string]harness.Response{
		"garbage":         refreshByHeader(t, e, "not-a-token"),
		"wrong signature": refreshByHeader(t, e, foreignToken(t, acc.ID, "refresh")),
		"access token":    refreshByHeader(t, e, s.Access),
		"garbage cookie":  refreshByCookie(t, e, "x.y.z"),
	}
	for name, resp := range cases {
		requireDeniedWithError(t, name, resp)
	}
}

// sessionSnapshot reads the whole sessions table in a comparable form.
func sessionSnapshot(t *testing.T, e *harness.Env) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, userId, refreshToken, lastRefreshToken, expiresAt, lastRefreshTokenExpiresAt, updatedAt FROM sessions ORDER BY id")
}

func checkRefreshDenialChangesNothing(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	before := sessionSnapshot(t, e)
	for _, resp := range []harness.Response{
		e.Post(t, "", "/auth/refresh", nil),
		refreshByHeader(t, e, "garbage"),
		refreshByHeader(t, e, s.Access),
		refreshByHeader(t, e, foreignToken(t, acc.ID, "refresh")),
	} {
		if resp.Status != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d: %s", resp.Status, resp.Body)
		}
	}
	if after := sessionSnapshot(t, e); !reflect.DeepEqual(before, after) {
		t.Fatalf("denied refreshes changed the sessions table:\nbefore %v\nafter  %v", before, after)
	}
}

func checkRefreshInactiveUser(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	patchUser(t, e, acc.ID, map[string]any{"isActive": false})
	before := sessionRows(t, e, acc.ID)
	requireDeniedWithError(t, "inactive user", refreshByHeader(t, e, s.Refresh))
	if after := sessionRows(t, e, acc.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("denied refresh changed the session: %v -> %v", before, after)
	}
	patchUser(t, e, acc.ID, map[string]any{"isActive": true})
	refreshed(t, "reactivated user", refreshByHeader(t, e, s.Refresh))
}

func checkRefreshExpired(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0", "REFRESH_TOKEN_EXPIRY": "2", "ACCESS_TOKEN_EXPIRY": "5"}})
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	if len(sessionRows(t, e, acc.ID)) != 1 {
		t.Fatal("login must create the session")
	}
	// The access token outlives the refresh token by three seconds; once it is
	// gone the refresh token has certainly expired without being used.
	harness.Eventually(t, 20*time.Second, "the access token to expire", func() bool {
		return authorizeStatus(t, e, s.Access) == http.StatusUnauthorized
	})
	requireDeniedWithError(t, "expired refresh token", refreshByHeader(t, e, s.Refresh))
	if rows := sessionRows(t, e, acc.ID); len(rows) != 0 {
		t.Fatalf("an expired refresh token must remove its session, %d rows left", len(rows))
	}
}

func checkRefreshSessionExpiredInDatabase(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	e.Stop(t)
	// No API can age a session; the row's date is set directly while the server is stopped.
	if n := e.Exec(t, "UPDATE sessions SET expiresAt = ? WHERE userId = ?", "2020-01-01 00:00:00.000 +00:00", acc.ID); n != 1 {
		t.Fatalf("aged %d sessions, want 1", n)
	}
	e.Restart(t)
	requireDeniedWithError(t, "session expired in database", refreshByHeader(t, e, s.Refresh))
	if sessionCount(t, e, acc.ID) != 0 {
		t.Fatal("the expired session row must be removed")
	}
}
