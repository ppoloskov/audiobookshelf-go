package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// accountSeq makes the usernames of accounts created by scenarios unique.
var accountSeq atomic.Int64

// account is a user created by a scenario through the admin API.
type account struct {
	ID       string
	Name     string
	Password string
}

// loginPayload is the part of a login/refresh answer that clients read.
type loginPayload struct {
	User struct {
		ID           string   `json:"id"`
		Username     string   `json:"username"`
		Type         string   `json:"type"`
		Token        string   `json:"token"`
		AccessToken  string   `json:"accessToken"`
		RefreshToken *string  `json:"refreshToken"`
		IsActive     bool     `json:"isActive"`
		Libraries    []string `json:"librariesAccessible"`
		Tags         []string `json:"itemTagsSelected"`
	} `json:"user"`
	UserDefaultLibraryID *string        `json:"userDefaultLibraryId"`
	ServerSettings       map[string]any `json:"serverSettings"`
	EreaderDevices       []any          `json:"ereaderDevices"`
	Source               string         `json:"Source"`
}

// newAccount creates an active ordinary user with the given extra request
// fields (type, isActive, permissions, ...) as root.
func newAccount(t *testing.T, e *harness.Env, extra map[string]any) account {
	t.Helper()
	n := accountSeq.Add(1)
	acc := account{Name: fmt.Sprintf("auth-scenario-%d", n), Password: fmt.Sprintf("auth-scenario-password-%d", n)}
	body := map[string]any{"username": acc.Name, "password": acc.Password, "isActive": true}
	for key, value := range extra {
		body[key] = value
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	if resp.Status != http.StatusOK {
		t.Fatalf("create account: status %d: %s", resp.Status, resp.Body)
	}
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	acc.ID = created.User.ID
	if acc.ID == "" {
		t.Fatalf("create account: no user id: %s", resp.Body)
	}
	return acc
}

// patchUser updates a user as root and requires 200.
func patchUser(t *testing.T, e *harness.Env, id string, body map[string]any) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/users/"+id, body)
	if resp.Status != http.StatusOK {
		t.Fatalf("patch user %s: status %d: %s", id, resp.Status, resp.Body)
	}
}

// loginAs posts the account's credentials with extra headers.
func loginAs(t *testing.T, e *harness.Env, acc account, headers map[string]string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": acc.Name, "password": acc.Password}, Header: headers})
}

// session is one client session after a login: the access token plus the
// refresh token as the cookie or the body delivered it.
type session struct {
	Access  string
	Refresh string
	Payload loginPayload
}

// loginBrowser logs in without X-Return-Tokens and returns the session whose
// refresh token comes from the refresh_token cookie.
func loginBrowser(t *testing.T, e *harness.Env, acc account) session {
	t.Helper()
	resp := loginAs(t, e, acc, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("browser login: status %d: %s", resp.Status, resp.Body)
	}
	var out session
	resp.JSON(t, &out.Payload)
	out.Access = out.Payload.User.AccessToken
	out.Refresh = cookieValue(resp, "refresh_token")
	if out.Access == "" || out.Refresh == "" {
		t.Fatalf("browser login: missing access token or refresh cookie: %s", resp.Body)
	}
	return out
}

// loginNative logs in with X-Return-Tokens: true; the refresh token comes from
// the body.
func loginNative(t *testing.T, e *harness.Env, acc account) session {
	t.Helper()
	resp := loginAs(t, e, acc, map[string]string{"X-Return-Tokens": "true"})
	if resp.Status != http.StatusOK {
		t.Fatalf("native login: status %d: %s", resp.Status, resp.Body)
	}
	var out session
	resp.JSON(t, &out.Payload)
	out.Access = out.Payload.User.AccessToken
	if out.Payload.User.RefreshToken != nil {
		out.Refresh = *out.Payload.User.RefreshToken
	}
	if out.Access == "" || out.Refresh == "" {
		t.Fatalf("native login: missing tokens: %s", resp.Body)
	}
	return out
}

// cookieValue returns the value of the named Set-Cookie of resp, "" when absent.
func cookieValue(resp harness.Response, name string) string {
	for _, cookie := range resp.Cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// refreshByCookie posts /auth/refresh with only the refresh_token cookie.
func refreshByCookie(t *testing.T, e *harness.Env, token string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"Cookie": "refresh_token=" + token}})
}

// refreshByHeader posts /auth/refresh with only the x-refresh-token header.
func refreshByHeader(t *testing.T, e *harness.Env, token string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"x-refresh-token": token}})
}

// logoutWith posts /logout with the refresh token as x-refresh-token and an
// optional raw query string.
func logoutWith(t *testing.T, e *harness.Env, token, query string) harness.Response {
	t.Helper()
	req := harness.Request{Method: http.MethodPost, Path: "/logout"}
	if token != "" {
		req.Header = map[string]string{"x-refresh-token": token}
	}
	if query != "" {
		req.Path += "?" + query
	}
	return e.Do(t, req)
}

// authorizeStatus posts /api/authorize with a bearer token and returns the status.
func authorizeStatus(t *testing.T, e *harness.Env, token string) int {
	t.Helper()
	return e.Post(t, token, "/api/authorize", nil).Status
}

// sessionRows returns the sessions rows of a user.
func sessionRows(t *testing.T, e *harness.Env, userID string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, refreshToken, lastRefreshToken, expiresAt, lastRefreshTokenExpiresAt, ipAddress, userAgent FROM sessions WHERE userId = ? ORDER BY createdAt", userID)
}

// sessionCount returns how many sessions a user has.
func sessionCount(t *testing.T, e *harness.Env, userID string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", userID)
}

// requireStatus fails unless resp has the wanted status.
func requireStatus(t *testing.T, what string, resp harness.Response, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d: %s", what, resp.Status, want, resp.Body)
	}
}

// requireDenied fails unless resp is a 401 without token material.
func requireDenied(t *testing.T, what string, resp harness.Response) {
	t.Helper()
	if resp.Status != http.StatusUnauthorized || hasTokenMaterial(resp.Body) {
		t.Fatalf("%s: want 401 without token material, got %d: %s", what, resp.Status, resp.Body)
	}
}

// requireString fails unless v is the string want.
func requireString(t *testing.T, what string, got any, want string) {
	t.Helper()
	if text, ok := got.(string); !ok || text != want {
		t.Fatalf("%s = %#v, want %q", what, got, want)
	}
}

// jsonDecode decodes a JSON column that the harness returned as a string.
func jsonDecode(column any, into any) error {
	text, ok := column.(string)
	if !ok {
		return fmt.Errorf("column is %T, want a JSON string", column)
	}
	return json.Unmarshal([]byte(text), into)
}

// rootID returns the id of the installation's root user.
func rootID(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM users WHERE type = 'root'")
	if len(rows) != 1 {
		t.Fatalf("want one root user, got %d", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// eventFor matches a socket event whose first argument is an object with the given id.
func eventFor(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var object struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &object) == nil && object.ID == id
	}
}

// tokenFor logs an account in and returns its access token.
func tokenFor(t *testing.T, e *harness.Env, acc account) string {
	t.Helper()
	token, _ := e.Login(t, acc.Name, acc.Password)
	return token
}

// requireListenerUser checks every deterministic field of the fixture listener
// as the login, refresh and authorize answers carry it.
func requireListenerUser(t *testing.T, e *harness.Env, body []byte) {
	t.Helper()
	var answer struct {
		User struct {
			ID            string           `json:"id"`
			Username      string           `json:"username"`
			Email         *string          `json:"email"`
			Type          string           `json:"type"`
			IsActive      bool             `json:"isActive"`
			IsLocked      bool             `json:"isLocked"`
			HasOpenIDLink bool             `json:"hasOpenIDLink"`
			CreatedAt     int64            `json:"createdAt"`
			Permissions   map[string]any   `json:"permissions"`
			Libraries     []string         `json:"librariesAccessible"`
			Tags          []string         `json:"itemTagsSelected"`
			SeriesHidden  []string         `json:"seriesHideFromContinueListening"`
			Bookmarks     []map[string]any `json:"bookmarks"`
			MediaProgress []map[string]any `json:"mediaProgress"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	u := answer.User
	if u.ID != e.IDs["listener"] || u.Username != listenerName || u.Email != nil || u.Type != "user" || !u.IsActive || u.IsLocked || u.HasOpenIDLink || u.CreatedAt <= 0 {
		t.Fatalf("listener user fields = %+v", u)
	}
	wantPermissions := map[string]any{"download": true, "update": false, "delete": false, "upload": false, "createEreader": false, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false}
	if len(u.Permissions) != len(wantPermissions) {
		t.Fatalf("permissions = %v", u.Permissions)
	}
	for key, want := range wantPermissions {
		if u.Permissions[key] != want {
			t.Fatalf("permission %s = %v, want %v", key, u.Permissions[key], want)
		}
	}
	if u.Libraries == nil || u.Tags == nil || u.SeriesHidden == nil || u.Bookmarks == nil || u.MediaProgress == nil {
		t.Fatalf("user arrays must be present: %s", body)
	}
	if rows := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", e.IDs["listener"]); rows == 0 || len(u.MediaProgress) != rows {
		t.Fatalf("mediaProgress has %d entries, database %d", len(u.MediaProgress), rows)
	}
}
