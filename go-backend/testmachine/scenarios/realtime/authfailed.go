package realtime

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// authFailedCases are the eight deliberate rejection branches of
	// authenticateSocket, each with its own message.
	authFailedInvalidToken    = "server/SocketAuthority.js:274-278"
	authFailedInactiveAPIKey  = "server/SocketAuthority.js:284-289"
	authFailedExpiredAPIKey   = "server/SocketAuthority.js:291-296"
	authFailedAPIKeyNoUser    = "server/SocketAuthority.js:298-302"
	authFailedBearerCheck     = "server/SocketAuthority.js:305-308"
	authFailedExpiredToken    = "server/SocketAuthority.js:310-313"
	authFailedDeletedUser     = "server/SocketAuthority.js:315-321"
	authFailedInactiveAccount = "server/SocketAuthority.js:324-327"
	// authFailedClient is where the web client reads the message.
	authFailedClient = "client/layouts/default.vue:143-146"
	// refreshPayload documents why a refresh token cannot authenticate a socket.
	refreshTokenSource = "server/auth/TokenManager.js:81-85"
)

// authFailedScenarios covers socket-outgoing-auth-failed: the event's payload,
// its socket-local audience and the fact that only an expired API key changes
// persisted state.
func authFailedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-auth-failed", Invariant: "INV-SOCKET-OUTGOING-AUTH-FAILED", Case: "normal", State: "initial", Assertion: "auth-failed-messages-and-branches",
			Sources: []string{authFailedInvalidToken, authFailedInactiveAPIKey, authFailedExpiredAPIKey, authFailedAPIKeyNoUser, authFailedBearerCheck, authFailedDeletedUser, authFailedInactiveAccount, authFailedClient},
			Claim:   "Each rejected credential yields one auth_failed whose only field is message: Invalid token for a malformed token, a refresh token or a deleted user, Invalid API key for an inactive, deleted or userless key, API key expired (deactivating the key row) and Invalid user for an inactive account.",
			Run: func(t *testing.T, e *harness.Env) {
				keyUser, _ := createUser(t, e, "rt-af-keyuser")
				deadUser, _ := createUser(t, e, "rt-af-deaduser")
				deletedTokenUser, deletedToken := createUser(t, e, "rt-af-deletedtoken")
				inactiveUser, inactiveToken := createUser(t, e, "rt-af-inactive")
				setUserActive(t, e, inactiveUser, false)
				deletedKey := createAPIKey(t, e, "rt-af-deleted-key", keyUser, true, 0)
				deleteAPIKey(t, e, deletedKey.ID)
				orphanKey := createAPIKey(t, e, "rt-af-orphan-key", deadUser, true, 0)
				deleteUser(t, e, deadUser)
				expiringKey := createAPIKey(t, e, "rt-af-expiring-key", keyUser, true, 1)
				expiry := jwtExpiry(t, expiringKey.Key)
				harness.Eventually(t, 10*time.Second, "the API key to expire", func() bool {
					return time.Now().After(expiry.Add(200 * time.Millisecond))
				})
				deleteUser(t, e, deletedTokenUser)

				cases := []struct {
					name, token, message string
				}{
					{"malformed token", "not-a-token", "Invalid token"},
					{"refresh token", refreshToken(t, e, "trial-listener", "trial-listener-password"), "Invalid token"},
					{"deleted API key", deletedKey.Key, "Invalid API key"},
					{"API key of a deleted user", orphanKey.Key, "Invalid API key"},
					{"API key of an inactive key row", createAPIKey(t, e, "rt-af-inactive-key", keyUser, false, 0).Key, "Invalid API key"},
					{"token of a deleted user", deletedToken, "Invalid token"},
					{"token of an inactive user", inactiveToken, "Invalid user"},
					{"expired API key", expiringKey.Key, "API key expired"},
				}
				for _, c := range cases {
					socket := connectAnon(t, e)
					event := rejectAuth(t, socket, c.token)
					requireExactKeys(t, c.name, argObject(t, event), "message")
					if message := argObject(t, event)["message"]; message != c.message {
						t.Fatalf("%s: want %q, got %v", c.name, c.message, message)
					}
				}
				if row := apiKeyRow(t, e, expiringKey.ID); row == nil || row["isActive"] != int64(0) {
					t.Fatalf("an expired API key must be deactivated, got %+v", row)
				}
				if entry := onlineEntry(t, e, keyUser); entry != nil {
					t.Fatalf("a rejected credential must not authenticate anyone: %+v", entry)
				}
			},
		},
		{
			Requirement: "socket-outgoing-auth-failed", Invariant: "INV-SOCKET-OUTGOING-AUTH-FAILED", Case: "error", State: "changed", Assertion: "auth-failed-is-socket-local",
			Sources: []string{authFailedInvalidToken, authFailedClient, association},
			Claim:   "auth_failed reaches only the socket that sent the credential, never an authenticated admin or user socket, and that socket authenticates successfully afterwards.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-af-local")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				user, _ := connectAuth(t, e, token)
				adminMark, userMark := admin.Mark(), user.Mark()
				rejected := connectAnon(t, e)
				event := rejectAuth(t, rejected, "garbage")
				if message := argObject(t, event)["message"]; message != "Invalid token" {
					t.Fatalf("want Invalid token, got %v", message)
				}
				flush(t, rejected)
				requireQuiet(t, admin, adminMark, "auth_failed")
				requireQuiet(t, user, userMark, "auth_failed")

				if payload := authenticate(t, rejected, token); payload.UserID != id {
					t.Fatalf("the same socket must authenticate afterwards: %+v", payload)
				}
			},
		},
		{
			Requirement: "socket-outgoing-auth-failed", Invariant: "INV-SOCKET-OUTGOING-AUTH-FAILED", Case: "edge", State: "repeat", Assertion: "auth-failed-missing-token",
			Sources: []string{authFailedInvalidToken, authFailedClient},
			Claim:   "An auth event without a token argument and with an empty-string token both answer Invalid token, once per event, and never an init.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				for i := 0; i < 2; i++ {
					if event := rejectAuth(t, socket); argObject(t, event)["message"] != "Invalid token" {
						t.Fatalf("no token, attempt %d: want Invalid token, got %v", i, argObject(t, event))
					}
					if event := rejectAuth(t, socket, ""); argObject(t, event)["message"] != "Invalid token" {
						t.Fatalf("empty token, attempt %d: want Invalid token, got %v", i, argObject(t, event))
					}
				}
				flush(t, socket)
				if got := countEvents(socket, mark, "auth_failed"); got != 4 {
					t.Fatalf("want four auth_failed events, got %d", got)
				}
				requireQuiet(t, socket, mark, "init")
			},
		},
		{
			Requirement: "socket-outgoing-auth-failed", Invariant: "INV-SOCKET-OUTGOING-AUTH-FAILED", Case: "normal", State: "changed", Assertion: "auth-failed-expired-token",
			Sources: []string{authFailedExpiredToken, refreshTokenSource, persistLastSeen},
			Claim:   "An access token authenticates before it expires and answers Token expired on a new socket after it expires, without changing the users row.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Env: map[string]string{"ACCESS_TOKEN_EXPIRY": "3"}})
				token := own.Token(t, "listener")
				socket, _ := connectAuth(t, own, token)
				socket.Close()
				before := userRow(t, own, own.IDs["listener"])
				expiry := jwtExpiry(t, token)
				harness.Eventually(t, 10*time.Second, "the access token to expire", func() bool {
					return time.Now().After(expiry.Add(200 * time.Millisecond))
				})
				late := own.Socket(t, "")
				if event := rejectAuth(t, late, token); argObject(t, event)["message"] != "Token expired" {
					t.Fatalf("want Token expired, got %v", argObject(t, event))
				}
				after := userRow(t, own, own.IDs["listener"])
				if after["isActive"] != before["isActive"] || after["lastSeen"] != before["lastSeen"] {
					t.Fatalf("an expired token must not change the users row: before %v after %v", before, after)
				}
			},
		},
		{
			Requirement: "socket-outgoing-auth-failed", Invariant: "INV-SOCKET-OUTGOING-AUTH-FAILED", Case: "error", State: "failure", Assertion: "auth-failed-no-side-effects",
			Sources: []string{authFailedInvalidToken, authFailedBearerCheck, authFailedInactiveAPIKey, "server/SocketAuthority.js:347-352"},
			Claim:   "A rejected JWT or a rejected inactive API key leaves the users row and the API key row unchanged, emits no user_online and associates no user.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-af-sideeffects")
				key := createAPIKey(t, e, "rt-af-sideeffects-key", id, false, 0)
				userBefore, keyBefore := userRow(t, e, id), apiKeyRow(t, e, key.ID)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket := connectAnon(t, e)
				for _, bad := range []string{token + "x", key.Key} {
					rejectAuth(t, socket, bad)
				}
				flush(t, socket)
				requireRowUnchanged(t, e, id, userBefore)
				if after := apiKeyRow(t, e, key.ID); after["isActive"] != keyBefore["isActive"] {
					t.Fatalf("a rejected inactive key must stay unchanged: before %v after %v", keyBefore, after)
				}
				requireQuiet(t, admin, adminMark, "user_online")
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("a rejected credential must not associate the user: %+v", entry)
				}
			},
		},
	}
}

// apiKeyRow reads the apiKeys row of an id, or nil.
func apiKeyRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM apiKeys WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// deleteAPIKey deletes an API key through the admin API.
func deleteAPIKey(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/api-keys/"+id), http.StatusOK, "DELETE api key")
}

// deleteUser deletes a user through the admin API.
func deleteUser(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/users/"+id), http.StatusOK, "DELETE user")
}

// refreshToken logs in asking for the refresh token in the body, mirroring the
// mobile client's x-return-tokens login (fixture password from fixture.json).
func refreshToken(t *testing.T, e *harness.Env, username, password string) string {
	t.Helper()
	resp := e.Do(t, harness.Request{
		Method: http.MethodPost, Path: "/login",
		Header: map[string]string{"x-return-tokens": "true"},
		JSON:   map[string]string{"username": username, "password": password},
	})
	requireStatus(t, resp, http.StatusOK, "login with tokens")
	token, _ := harness.Dig(resp.Object(t), "user", "refreshToken").(string)
	if token == "" {
		t.Fatalf("login returned no refresh token: %.300s", resp.Body)
	}
	return token
}

// jwtExpiry reads the exp claim of a JWT without verifying it, so a scenario
// waits for the real expiry the server signed.
func jwtExpiry(t *testing.T, token string) time.Time {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a JWT: %.40s", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode token claims: %v", err)
	}
	if payload.Exp == 0 {
		t.Fatalf("token has no exp claim: %s", raw)
	}
	return time.Unix(payload.Exp, 0)
}
