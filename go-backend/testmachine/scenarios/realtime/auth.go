package realtime

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// authEvent is the client contract: emit auth with the access token after
	// connect, then wait for init or auth_failed.
	authClient = "client/layouts/default.vue:111-115"
	authAnswer = "client/layouts/default.vue:143-155"
	// mobileAudit is the owner-recorded mobile contract for the same handshake.
	mobileAudit = "go-backend/testmachine/contracts/mobile-client-audit.md:46"
	// jwtBranch validates a bearer JWT, rejects refresh payloads and expires.
	jwtBranch = "server/SocketAuthority.js:304-321"
	// apiKeyBranch authenticates an api-type token through its API key row.
	apiKeyBranch = "server/SocketAuthority.js:282-302"
	// inactiveUser is the final activity check.
	inactiveUser = "server/SocketAuthority.js:324-327"
	// association writes client.user, lastSeen and the init payload.
	association = "server/SocketAuthority.js:329-361"
)

// authScenarios covers socket-incoming-auth: which credentials associate a
// user with a socket, what that association writes and who sees it.
func authScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-auth", Invariant: "INV-SOCKET-INCOMING-AUTH", Case: "normal", State: "initial", Assertion: "auth-valid-token-initializes",
			Sources: []string{authClient, jwtBranch, association, persistLastSeen, onlineList, mobileAudit},
			Claim:   "A valid access token authenticates the socket: init carries exactly userId and username of the token's user, the socket is listed online once and the users row lastSeen is written.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				socket := connectAnon(t, e)
				mark := socket.Mark()
				payload := authenticate(t, socket, e.Token(t, "listener"))
				requireExactKeys(t, "non-admin init", argObject(t, waitEvent(t, socket, mark, "init")), "userId", "username")
				if payload.UserID != e.IDs["listener"] || payload.Username != "trial-listener" {
					t.Fatalf("init: want the listener, got %+v", payload)
				}
				if count := countEvents(socket, mark, "init"); count != 1 {
					t.Fatalf("want exactly one init, got %d", count)
				}
				entry := onlineEntry(t, e, e.IDs["listener"])
				if entry == nil || entry.Connections != 1 {
					t.Fatalf("the authenticated socket must be online once: %+v", entry)
				}
				if row := userRow(t, e, e.IDs["listener"]); row == nil || dbMillis(t, row["lastSeen"]) < started {
					t.Fatalf("a successful auth writes lastSeen: %+v", row)
				}
			},
		},
		{
			Requirement: "socket-incoming-auth", Invariant: "INV-SOCKET-INCOMING-AUTH", Case: "error", State: "changed", Assertion: "auth-inactive-user-rejected",
			Sources: []string{jwtBranch, inactiveUser, "server/controllers/UserController.js:320-327"},
			Claim:   "An inactive user's valid token is rejected with Invalid user and no init, and the same socket authenticates once the user is active again.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-auth-inactive")
				setUserActive(t, e, id, false)
				socket := connectAnon(t, e)
				mark := socket.Mark()
				event := rejectAuth(t, socket, token)
				if message := argObject(t, event)["message"]; message != "Invalid user" {
					t.Fatalf("inactive user: want Invalid user, got %v", message)
				}
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("an inactive user must not be online: %+v", entry)
				}

				setUserActive(t, e, id, true)
				if payload := authenticate(t, socket, token); payload.UserID != id {
					t.Fatalf("init after reactivation: want %s, got %+v", id, payload)
				}
				if count := countEvents(socket, mark, "init"); count != 1 {
					t.Fatalf("want one init after reactivation, got %d", count)
				}
				if entry := onlineEntry(t, e, id); entry == nil || entry.Connections != 1 {
					t.Fatalf("the reactivated user must be online once: %+v", entry)
				}
			},
		},
		{
			Requirement: "socket-incoming-auth", Invariant: "INV-SOCKET-INCOMING-AUTH", Case: "edge", State: "changed", Assertion: "auth-api-key-authenticates-key-user",
			Sources: []string{apiKeyBranch, association, "server/controllers/ApiKeyController.js:64-110", onlineList},
			Claim:   "An active API key authenticates the socket as the key's user, and re-authenticating that socket as another user moves the connection: init names the new user, who becomes online while the key's user leaves usersOnline.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := createUser(t, e, "rt-auth-apikey")
				key := createAPIKey(t, e, "rt-auth-apikey-key", id, true, 0)
				socket := connectAnon(t, e)
				payload := authenticate(t, socket, key.Key)
				if payload.UserID != id || payload.Username != "rt-auth-apikey" {
					t.Fatalf("API key init: want user %s, got %+v", id, payload)
				}
				if entry := onlineEntry(t, e, id); entry == nil || entry.Type != "user" || entry.Connections != 1 {
					t.Fatalf("the key's user must be online once: %+v", entry)
				}

				// Re-authenticating a socket as a different user is allowed (upstream
				// logs a warning and replaces client.user).
				if switched := authenticate(t, socket, e.Token(t, "listener")); switched.UserID != e.IDs["listener"] {
					t.Fatalf("the switched init must name the listener: %+v", switched)
				}
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("the previous user must leave usersOnline after the switch: %+v", entry)
				}
				if entry := onlineEntry(t, e, e.IDs["listener"]); entry == nil || entry.Connections != 1 {
					t.Fatalf("the new user must be online once: %+v", entry)
				}
			},
		},
		{
			Requirement: "socket-incoming-auth", Invariant: "INV-SOCKET-INCOMING-AUTH", Case: "normal", State: "repeat", Assertion: "auth-repeat-moves-last-seen",
			Sources: []string{association, persistLastSeen, "client/layouts/default.vue:131-143"},
			Claim:   "Authenticating the same socket twice for one user emits two inits for that user and moves the lastSeen the second user_online reports from null to a time at or after the first auth.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				id, token := createUser(t, e, "rt-auth-repeat")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket := connectAnon(t, e)
				mark := socket.Mark()

				first := authenticate(t, socket, token)
				waitUserEvents(t, admin, adminMark, "user_online", id, 1)
				before := userRow(t, e, id)
				second := authenticate(t, socket, token)
				events := waitUserEvents(t, admin, adminMark, "user_online", id, 2)
				if first.UserID != id || second.UserID != id {
					t.Fatalf("both inits must name %s: %+v %+v", id, first, second)
				}
				if count := countEvents(socket, mark, "init"); count != 2 {
					t.Fatalf("want two init events for the second auth, got %d", count)
				}
				firstOnline, secondOnline := decodeArg[publicUser](t, events[0]), decodeArg[publicUser](t, events[1])
				if firstOnline.LastSeen != nil {
					t.Fatalf("a user's first user_online must report lastSeen null, got %d", *firstOnline.LastSeen)
				}
				if secondOnline.LastSeen == nil || *secondOnline.LastSeen < started {
					t.Fatalf("the repeat user_online must report the first auth time, got %+v", secondOnline.LastSeen)
				}
				after := userRow(t, e, id)
				if dbMillis(t, after["lastSeen"]) < dbMillis(t, before["lastSeen"]) {
					t.Fatalf("lastSeen must not go backwards: before %v after %v", before["lastSeen"], after["lastSeen"])
				}
				if entry := onlineEntry(t, e, id); entry == nil || entry.Connections != 1 {
					t.Fatalf("re-authenticating one socket keeps one connection: %+v", entry)
				}
			},
		},
		{
			Requirement: "socket-incoming-auth", Invariant: "INV-SOCKET-INCOMING-AUTH", Case: "error", State: "failure", Assertion: "auth-rejected-leaves-user-row",
			Sources: []string{rejectInvalidToken, jwtBranch, "server/SocketAuthority.js:272-278", association},
			Claim:   "Rejected credentials change nothing: the users row (lastSeen and isActive) is identical afterwards, no init arrives and the user is not online.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-auth-rejected")
				before := userRow(t, e, id)
				socket := connectAnon(t, e)
				mark := socket.Mark()
				for _, bad := range []string{token + "x", "eyJhbGciOiJIUzI1NiJ9.bm90.a.bad"} {
					event := rejectAuth(t, socket, bad)
					if message := argObject(t, event)["message"]; message != "Invalid token" {
						t.Fatalf("bad credential: want Invalid token, got %v", message)
					}
				}
				flush(t, socket)
				requireQuiet(t, socket, mark, "init")
				requireRowUnchanged(t, e, id, before)
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("a rejected user must not be online: %+v", entry)
				}
			},
		},
	}
}

// setUserActive activates or deactivates a user through the admin API.
func setUserActive(t *testing.T, e *harness.Env, id string, active bool) {
	t.Helper()
	requireStatus(t, e.Patch(t, e.Token(t, "root"), "/api/users/"+id, map[string]any{"isActive": active}), http.StatusOK, "PATCH user isActive")
}
