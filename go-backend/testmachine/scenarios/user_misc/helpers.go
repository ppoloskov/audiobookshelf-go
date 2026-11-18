// Package user_misc holds the replacement scenarios of the "user_misc" packet:
// the remaining current-user routes (password change, items in progress, series
// continue-listening visibility, per-year statistics, ereader devices and admin
// API keys) plus the ereader-devices-updated socket event.
package user_misc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install). Library
// item ids come from Env.IDs; these are seeded row values.
const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386" // trial-root user id
	finisherID = "7c538a8f-9711-47bd-89e4-8d0139473dcf" // trial-finisher user id
	seriesID   = "e987a322-9111-4f79-8cb9-c25ee714e0d6" // "Long Saga" series
	saga1Media = "5e33de11-3e94-4c51-87d8-d06d2928ac38"
	saga2Media = "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af"
	ebookMedia = "83013005-1614-4732-8f4b-07858cfb5de0"
	castMedia  = "a477b353-4058-4b9e-8399-49781256fc24"
	bookFolder = "bc88289a-fc01-4256-a39e-90649091e152"
	castFolder = "707e28b2-414a-4996-be70-fbfcdb687727"
	castItem   = "eeb6224f-1316-40c8-b8bf-00804cc90436"
)

// apiKey is one API key as clients read it (ApiKeysTable, ApiKeyModal).
type apiKey struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	ExpiresAt       *string  `json:"expiresAt"`
	LastUsedAt      *string  `json:"lastUsedAt"`
	IsActive        bool     `json:"isActive"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
	UserID          string   `json:"userId"`
	CreatedByUserID string   `json:"createdByUserId"`
	User            *keyUser `json:"user"`
	CreatedByUser   *keyUser `json:"createdByUser"`
	// APIKey is the signed JWT, present only in the create response.
	APIKey string `json:"apiKey"`
}

// keyUser is the id/username/type projection of a user attached to a key.
type keyUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// ereaderDevice is one email/ereader device entry.
type ereaderDevice struct {
	Name               string   `json:"name"`
	Email              string   `json:"email"`
	AvailabilityOption string   `json:"availabilityOption"`
	Users              []string `json:"users"`
}

// devicesResponse is the body of POST /api/me/ereader-devices.
type devicesResponse struct {
	EreaderDevices []ereaderDevice `json:"ereaderDevices"`
}

// emailSettingsRow is the persisted ereader part of the email-settings row.
type emailSettingsRow struct {
	ID             string          `json:"id"`
	EreaderDevices []ereaderDevice `json:"ereaderDevices"`
}

// getJSON performs a GET as token and decodes a 200 response into out.
func getJSON(t *testing.T, e *harness.Env, token, path string, out any) {
	t.Helper()
	resp := e.Get(t, token, path)
	wantStatus(t, resp, http.StatusOK, "GET "+path)
	if out != nil {
		resp.JSON(t, out)
	}
}

// wantStatus requires the status of a response.
func wantStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %s", what, status, resp.Status, resp.Body)
	}
}

// equalJSON compares two values by their JSON encoding (map keys sorted, so
// this compares by value, never by key order).
func equalJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: encode got: %v", what, err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: encode want: %v", what, err)
	}
	if string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// anyJSON decodes a body for whole-object comparison.
func anyJSON(t *testing.T, resp harness.Response) any {
	t.Helper()
	var value any
	resp.JSON(t, &value)
	return value
}

// requireUnauthenticated requires 401 for no token and for an unknown bearer.
func requireUnauthenticated(t *testing.T, e *harness.Env, method, path string) {
	t.Helper()
	for _, token := range []string{"", "not-a-token"} {
		resp := e.Do(t, harness.Request{Method: method, Path: path, Token: token})
		wantStatus(t, resp, http.StatusUnauthorized, method+" "+path)
	}
}

// newUser creates a user through the admin API and returns its id and a token.
func newUser(t *testing.T, e *harness.Env, body map[string]any, password string) (string, string) {
	t.Helper()
	body["password"] = password
	body["isActive"] = true
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, resp, http.StatusOK, "create user")
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	username, _ := body["username"].(string)
	token, _ := e.Login(t, username, password)
	return created.User.ID, token
}

// mobileLogin logs in with the mobile-style token response and returns the
// access token, the refresh token and the created session id.
func mobileLogin(t *testing.T, e *harness.Env, username, password string) (access, refresh, sessionID string) {
	t.Helper()
	resp := e.Do(t, harness.Request{
		Method: http.MethodPost, Path: "/login",
		JSON:   map[string]string{"username": username, "password": password},
		Header: map[string]string{"X-Return-Tokens": "true"},
	})
	wantStatus(t, resp, http.StatusOK, "login")
	var body struct {
		User struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	resp.JSON(t, &body)
	rows := e.Rows(t, "SELECT id FROM sessions WHERE refreshToken = ?", body.User.RefreshToken)
	if body.User.AccessToken == "" || body.User.RefreshToken == "" || len(rows) != 1 {
		t.Fatalf("login must return both tokens and store one session row: %s", resp.Body)
	}
	return body.User.AccessToken, body.User.RefreshToken, fmt.Sprint(rows[0]["id"])
}

// userIDOf returns the id of a user by username.
func userIDOf(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM users WHERE username = ?", username)
	if len(rows) != 1 {
		t.Fatalf("user %q has %d rows", username, len(rows))
	}
	return fmt.Sprint(rows[0]["id"])
}

// sessionIDs returns the session ids stored for a user, sorted.
func sessionIDs(t *testing.T, e *harness.Env, userID string) []string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM sessions WHERE userId = ? ORDER BY id", userID)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, fmt.Sprint(row["id"]))
	}
	return ids
}

// pashHash returns the stored bcrypt hash of a user.
func pashHash(t *testing.T, e *harness.Env, userID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT pash FROM users WHERE id = ?", userID)
	if len(rows) != 1 {
		t.Fatalf("user %s has no row", userID)
	}
	hash, _ := rows[0]["pash"].(string)
	return hash
}

// extraData returns the stored users.extraData of a user as JSON.
func extraData(t *testing.T, e *harness.Env, userID string) any {
	t.Helper()
	rows := e.Rows(t, "SELECT extraData FROM users WHERE id = ?", userID)
	if len(rows) != 1 {
		t.Fatalf("user %s has no row", userID)
	}
	var value any
	text, _ := rows[0]["extraData"].(string)
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("stored extraData of %s: %v", userID, err)
	}
	return value
}

// hiddenSeries returns users.extraData.seriesHideFromContinueListening.
func hiddenSeries(t *testing.T, e *harness.Env, userID string) []any {
	t.Helper()
	value, _ := extraData(t, e, userID).(map[string]any)
	hidden, _ := value["seriesHideFromContinueListening"].([]any)
	return hidden
}

// emailSettings returns the persisted email-settings row.
func emailSettings(t *testing.T, e *harness.Env) emailSettingsRow {
	t.Helper()
	rows := e.Rows(t, "SELECT value FROM settings WHERE key = 'email-settings'")
	if len(rows) != 1 {
		t.Fatalf("email-settings row missing: %v", rows)
	}
	text, _ := rows[0]["value"].(string)
	var settings emailSettingsRow
	if err := json.Unmarshal([]byte(text), &settings); err != nil {
		t.Fatalf("persisted email-settings: %v", err)
	}
	return settings
}

// keyRows returns the apiKeys rows of a key id.
func keyRows(t *testing.T, e *harness.Env, id string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, name, isActive, userId, createdByUserId FROM apiKeys WHERE id = ?", id)
}

// parseTime parses a stored/served timestamp.
func parseTime(t *testing.T, value, what string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("%s is not a timestamp: %q: %v", what, value, err)
	}
	return parsed
}
