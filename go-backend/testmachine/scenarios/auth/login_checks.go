package auth

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// defaultRefreshCookieSeconds is the refresh cookie lifetime without REFRESH_TOKEN_EXPIRY
// (server/auth/TokenManager.js:15-23).
const defaultRefreshCookieSeconds = 30 * 24 * 60 * 60

// checkLoginPayload checks the fields of a login answer that clients consume.
func checkLoginPayload(t *testing.T, e *harness.Env) {
	resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": listenerName, "password": listenerPassword}})
	requireStatus(t, "login", resp, http.StatusOK)
	var payload loginPayload
	resp.JSON(t, &payload)
	requireListenerUser(t, e, resp.Body)
	user := payload.User
	if user.ID != e.IDs["listener"] || user.Username != listenerName || user.Type != "user" || !user.IsActive {
		t.Fatalf("login user = %+v", user)
	}
	if user.Token == "" || user.AccessToken == "" || user.Token == user.AccessToken {
		t.Fatalf("login user must carry the legacy token and a distinct access token: %s", resp.Body)
	}
	if payload.UserDefaultLibraryID == nil || *payload.UserDefaultLibraryID != e.IDs["library"] {
		t.Fatalf("userDefaultLibraryId = %v, want %s", payload.UserDefaultLibraryID, e.IDs["library"])
	}
	if payload.EreaderDevices == nil || payload.Source != "docker" {
		t.Fatalf("ereaderDevices %v and Source %q: want an array and docker", payload.EreaderDevices, payload.Source)
	}
	if payload.ServerSettings["version"] != "2.37.2" {
		t.Fatalf("serverSettings.version = %v", payload.ServerSettings["version"])
	}
	for _, secret := range []string{"tokenSecret", "authOpenIDClientSecret"} {
		if _, present := payload.ServerSettings[secret]; present {
			t.Fatalf("serverSettings must not carry %s", secret)
		}
	}
	for _, cookie := range resp.Cookies {
		if cookie.Name == "refresh_token" && cookie.MaxAge != defaultRefreshCookieSeconds {
			t.Fatalf("refresh cookie MaxAge = %d, want %d", cookie.MaxAge, defaultRefreshCookieSeconds)
		}
	}
	if cookieValue(resp, "refresh_token") == "" {
		t.Fatal("browser login sets no refresh cookie")
	}
}

// checkTokenModeLogin checks the X-Return-Tokens: true answer and its session row.
func checkTokenModeLogin(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	if sessionCount(t, e, acc.ID) != 0 {
		t.Fatal("new account must start without sessions")
	}
	resp := loginAs(t, e, acc, map[string]string{"X-Return-Tokens": "true"})
	requireStatus(t, "token login", resp, http.StatusOK)
	var payload loginPayload
	resp.JSON(t, &payload)
	if payload.User.RefreshToken == nil || strings.Count(*payload.User.RefreshToken, ".") != 2 {
		t.Fatalf("user.refreshToken must be a JWT string: %s", resp.Body)
	}
	if cookieValue(resp, "refresh_token") != "" {
		t.Fatal("token mode must not set a refresh cookie")
	}
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("want one sessions row, got %d", len(rows))
	}
	requireString(t, "sessions.refreshToken", rows[0]["refreshToken"], *payload.User.RefreshToken)
	if rows[0]["lastRefreshToken"] != nil {
		t.Fatalf("a new session has no lastRefreshToken: %v", rows[0]["lastRefreshToken"])
	}
	if ip, _ := rows[0]["ipAddress"].(string); ip == "" {
		t.Fatalf("session must record the client address: %v", rows[0])
	}
	if status := authorizeStatus(t, e, payload.User.AccessToken); status != http.StatusOK {
		t.Fatalf("the access token must authorize, got %d", status)
	}
}

// checkLocalAuthDisabled disables local authentication through the admin
// settings route on a private server and enables it again.
func checkLocalAuthDisabled(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	root := e.Token(t, "root")
	setMethods := func(methods []string) {
		resp := e.Patch(t, root, "/api/auth-settings", map[string]any{"authActiveAuthMethods": methods})
		requireStatus(t, "auth-settings", resp, http.StatusOK)
		var answer struct {
			Updated bool `json:"updated"`
		}
		resp.JSON(t, &answer)
		if !answer.Updated {
			t.Fatalf("auth-settings %v reported no update: %s", methods, resp.Body)
		}
	}
	setMethods([]string{"openid"})
	resp := e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": listenerPassword})
	if resp.Status < 400 || hasTokenMaterial(resp.Body) || cookieValue(resp, "refresh_token") != "" {
		t.Fatalf("login with local auth disabled: status %d: %s", resp.Status, resp.Body)
	}
	setMethods([]string{"local"})
	requireStatus(t, "login after re-enabling", e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": listenerPassword}), http.StatusOK)
}

// checkLoginRateLimit starts a server that allows three authentication requests.
func checkLoginRateLimit(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "3"}})
	before := e.Count(t, "SELECT COUNT(*) FROM sessions")
	for range 3 {
		requireDenied(t, "attempt inside the limit", e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": "wrong"}))
	}
	resp := e.Post(t, "", "/login", map[string]string{"username": listenerName, "password": listenerPassword})
	var answer struct {
		Error string `json:"error"`
	}
	resp.JSON(t, &answer)
	if resp.Status != http.StatusTooManyRequests || answer.Error == "" || hasTokenMaterial(resp.Body) {
		t.Fatalf("fourth login: status %d: %s", resp.Status, resp.Body)
	}
	if after := e.Count(t, "SELECT COUNT(*) FROM sessions"); after != before {
		t.Fatalf("limited login changed sessions from %d to %d", before, after)
	}
}

// checkPasswordlessRoot initialises a server with a root without password.
func checkPasswordlessRoot(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Fresh: true, Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	requireStatus(t, "init", e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "rootless"}}), http.StatusOK)
	requireDenied(t, "root with a password it does not have", e.Post(t, "", "/login", map[string]string{"username": "rootless", "password": "something"}))
	resp := e.Post(t, "", "/login", map[string]string{"username": "rootless", "password": ""})
	requireStatus(t, "root with empty password", resp, http.StatusOK)
	var payload loginPayload
	resp.JSON(t, &payload)
	if payload.User.Type != "root" || payload.User.AccessToken == "" {
		t.Fatalf("passwordless root login = %s", resp.Body)
	}
}

// checkPasswordlessUser empties a user's password hash directly: no API creates a
// non-root user without a password (OpenID auto-registration does).
func checkPasswordlessUser(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
	acc := newAccount(t, e, nil)
	e.Stop(t)
	if n := e.Exec(t, "UPDATE users SET pash = '' WHERE id = ?", acc.ID); n != 1 {
		t.Fatalf("updated %d users, want 1", n)
	}
	e.Restart(t)
	for _, password := range []string{acc.Password, ""} {
		resp := e.Post(t, "", "/login", map[string]string{"username": acc.Name, "password": password})
		requireDenied(t, "passwordless user", resp)
	}
	if sessionCount(t, e, acc.ID) != 0 {
		t.Fatal("a denied login must not create a session")
	}
}
