package user_misc

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// adminToken returns a non-root admin token, creating the user once.
func adminToken(t *testing.T, e *harness.Env) string {
	t.Helper()
	if rows := e.Rows(t, "SELECT id FROM users WHERE username = ?", adminName); len(rows) == 0 {
		newUser(t, e, map[string]any{"username": adminName, "type": "admin"}, adminPassword)
	}
	token, _ := e.Login(t, adminName, adminPassword)
	return token
}

// createKey creates a key as root and returns the decoded response.
func createKey(t *testing.T, e *harness.Env, body map[string]any) apiKey {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/api-keys", body)
	wantStatus(t, resp, http.StatusOK, "create api key")
	var wrapper struct {
		APIKey apiKey `json:"apiKey"`
	}
	resp.JSON(t, &wrapper)
	return wrapper.APIKey
}

// listKeys returns the admin key list.
func listKeys(t *testing.T, e *harness.Env, token string) []apiKey {
	t.Helper()
	var body struct {
		APIKeys []apiKey `json:"apiKeys"`
	}
	getJSON(t, e, token, "/api/api-keys", &body)
	if body.APIKeys == nil {
		t.Fatal("apiKeys must be a JSON array")
	}
	return body.APIKeys
}

// findKey returns the key with the id.
func findKey(t *testing.T, keys []apiKey, id string) apiKey {
	t.Helper()
	for _, key := range keys {
		if key.ID == id {
			return key
		}
	}
	t.Fatalf("key %s is not in the list", id)
	return apiKey{}
}

// keyIDOf returns the id of the single key with a name.
func keyIDOf(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM apiKeys WHERE name = ?", name)
	if len(rows) != 1 {
		t.Fatalf("want one key named %q, got %d", name, len(rows))
	}
	return fmtString(rows[0]["id"])
}

// apiKeyCount counts all stored keys.
func apiKeyCount(t *testing.T, e *harness.Env) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM apiKeys")
}

// requireKeyToken requires that a key token authenticates as the user.
func requireKeyToken(t *testing.T, e *harness.Env, token, userID string) {
	t.Helper()
	resp := e.Get(t, token, "/api/me")
	wantStatus(t, resp, http.StatusOK, "api key authentication")
	var body struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &body)
	if body.ID != userID {
		t.Fatalf("api key authenticated as %q, want %q", body.ID, userID)
	}
}

// requireKeyRejected requires that a key token is unauthorized.
func requireKeyRejected(t *testing.T, e *harness.Env, token, what string) {
	t.Helper()
	wantStatus(t, e.Get(t, token, "/api/me"), http.StatusUnauthorized, what)
}

// requireKeyFields compares the deterministic create response fields.
func requireKeyFields(t *testing.T, key apiKey, name, userID string, active bool) {
	t.Helper()
	if key.ID == "" || key.Name != name || key.UserID != userID || key.IsActive != active || key.CreatedByUserID != rootID {
		t.Fatalf("create response fields wrong: %+v", key)
	}
	if key.User == nil || key.User.ID != userID || key.User.Type == "" || key.User.Username == "" {
		t.Fatalf("create response user projection wrong: %+v", key.User)
	}
	if len(strings.Split(key.APIKey, ".")) != 3 {
		t.Fatalf("create response must include the signed key: %q", key.APIKey)
	}
	if key.Description != nil || key.LastUsedAt != nil {
		t.Fatalf("new key must have null description and lastUsedAt: %+v", key)
	}
}
