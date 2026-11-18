package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// dbTimeLayout is how the database stores dates ("2026-12-03 12:46:24.959 +00:00").
const dbTimeLayout = "2006-01-02 15:04:05.000 -07:00"

// jwtClaims are the claims of a server-issued token.
type jwtClaims struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Type     string `json:"type"`
	JTI      string `json:"jti"`
	IAT      int64  `json:"iat"`
	EXP      *int64 `json:"exp"`
}

// decodeJWT reads the payload of a JWT without verifying it.
func decodeJWT(t *testing.T, token string) jwtClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %.40q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("JWT payload: %v", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("JWT payload: %v", err)
	}
	return claims
}

// requireNear fails unless got is within tolerance of want.
func requireNear(t *testing.T, what string, got, want time.Time, tolerance time.Duration) {
	t.Helper()
	if diff := got.Sub(want); diff > tolerance || diff < -tolerance {
		t.Fatalf("%s = %s, want %s +-%s", what, got.Format(time.RFC3339), want.Format(time.RFC3339), tolerance)
	}
}

// dbTime parses a date column.
func dbTime(t *testing.T, what string, column any) time.Time {
	t.Helper()
	text, ok := column.(string)
	if !ok {
		t.Fatalf("%s = %v, want a date", what, column)
	}
	parsed, err := time.Parse(dbTimeLayout, text)
	if err != nil {
		t.Fatalf("%s %q: %v", what, text, err)
	}
	return parsed
}

// requireTokenShape checks the claims and lifetime of an access or refresh token.
func requireTokenShape(t *testing.T, acc account, token, kind string, lifetime time.Duration) {
	t.Helper()
	claims := decodeJWT(t, token)
	if claims.UserID != acc.ID || claims.Username != acc.Name || claims.Type != kind || claims.JTI == "" || claims.EXP == nil {
		t.Fatalf("%s token claims = %+v", kind, claims)
	}
	if got := time.Duration(*claims.EXP-claims.IAT) * time.Second; got != lifetime {
		t.Fatalf("%s token lifetime = %s, want %s", kind, got, lifetime)
	}
	requireNear(t, kind+" token iat", time.Unix(claims.IAT, 0), time.Now(), 2*time.Minute)
}

// tokenScenarios prove the default token lifetimes and claims.
func tokenScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r001", Invariant: "INV-R001-REFRESH-LIFECYCLE-001", Case: "normal", State: "changed", Assertion: "default-token-lifetimes-and-claims",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:133-180", "server/auth/TokenManager.js:182-205", "server/auth/TokenManager.js:210-275"},
			Claim:   "With the default configuration the access token is a JWT (userId, username, type access, exp one hour after iat), the refresh token has type refresh and expires after 30 days, sessions.expiresAt is about 30 days away after login and rotation, and rotation sets lastRefreshTokenExpiresAt about 10 minutes ahead.",
			Run:     checkDefaultTokenLifetimes,
		},
		{
			Requirement: "flow-client-auth-token-lifecycle", Invariant: "INV-FLOW-CLIENT-AUTH-TOKEN-LIFECYCLE-002", Case: "edge", State: "changed", Assertion: "default-grace-period-ends-by-timestamp",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:372-388"},
			Claim:   "With the default grace period a rotated token still refreshes right after rotation, and is denied once the stored grace timestamp has passed, while the current token keeps working.",
			Run:     checkDefaultGrace,
		},
	}
}

func checkDefaultTokenLifetimes(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	s := loginNative(t, e, acc)
	requireTokenShape(t, acc, s.Access, "access", time.Hour)
	requireTokenShape(t, acc, s.Refresh, "refresh", 30*24*time.Hour)
	legacy := decodeJWT(t, s.Payload.User.Token)
	if legacy.UserID != acc.ID || legacy.Username != acc.Name || legacy.EXP != nil {
		t.Fatalf("legacy user token claims = %+v, want userId, username and no exp", legacy)
	}
	rows := sessionRows(t, e, acc.ID)
	requireNear(t, "sessions.expiresAt after login", dbTime(t, "expiresAt", rows[0]["expiresAt"]), time.Now().Add(30*24*time.Hour), 5*time.Minute)
	if rows[0]["lastRefreshTokenExpiresAt"] != nil {
		t.Fatalf("a new session has no grace timestamp: %v", rows[0]["lastRefreshTokenExpiresAt"])
	}
	next := refreshed(t, "rotation", refreshByHeader(t, e, s.Refresh))
	requireTokenShape(t, acc, next.User.AccessToken, "access", time.Hour)
	requireTokenShape(t, acc, *next.User.RefreshToken, "refresh", 30*24*time.Hour)
	rows = sessionRows(t, e, acc.ID)
	requireNear(t, "sessions.expiresAt after rotation", dbTime(t, "expiresAt", rows[0]["expiresAt"]), time.Now().Add(30*24*time.Hour), 5*time.Minute)
	requireNear(t, "lastRefreshTokenExpiresAt", dbTime(t, "lastRefreshTokenExpiresAt", rows[0]["lastRefreshTokenExpiresAt"]), time.Now().Add(10*time.Minute), time.Minute)
}

func checkDefaultGrace(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	acc := newAccount(t, e, nil)
	old := loginNative(t, e, acc).Refresh
	current := *refreshed(t, "rotation", refreshByHeader(t, e, old)).User.RefreshToken
	refreshed(t, "replay inside the default grace period", refreshByHeader(t, e, old))
	e.Stop(t)
	// No API can move the stored grace timestamp; end the grace period directly.
	past := time.Now().Add(-time.Minute).UTC().Format(dbTimeLayout)
	if n := e.Exec(t, "UPDATE sessions SET lastRefreshTokenExpiresAt = ? WHERE userId = ?", past, acc.ID); n != 1 {
		t.Fatalf("updated %d sessions, want 1", n)
	}
	e.Restart(t)
	if status := refreshByHeader(t, e, old).Status; status != http.StatusUnauthorized {
		t.Fatalf("rotated token after the grace period: status %d, want 401", status)
	}
	refreshed(t, "current token", refreshByHeader(t, e, current))
}
