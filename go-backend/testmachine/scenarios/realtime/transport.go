package realtime

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// registerClient is the connection hook: every Engine.IO connection becomes
	// a client record; only the auth event associates a user.
	registerClient = "server/SocketAuthority.js:176-183"
	// authHook registers the auth handler before any client event.
	authHook = "server/SocketAuthority.js:187-188"
	// rejectInvalidToken is the invalid-token answer of authenticateSocket.
	rejectInvalidToken = "server/SocketAuthority.js:272-278"
	// persistLastSeen is the lastSeen write after a successful auth.
	persistLastSeen = "server/SocketAuthority.js:350-352"
	// onlineList is GET /api/users/online, built from the socket clients.
	onlineList = "server/controllers/UserController.js:484-493"
)

// transportScenarios covers socket-incoming-connection: the connection hook
// registers a client, and no user data is associated before a successful auth.
func transportScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-connection", Invariant: "INV-SOCKET-INCOMING-CONNECTION", Case: "normal", State: "initial", Assertion: "transport-connect-gates-initialization",
			Sources: []string{registerClient, authHook, "server/SocketAuthority.js:269-278", onlineList},
			Claim:   "A socket that connects without auth receives no init and answers the application ping, and gets init exactly once after a valid auth while the connection is listed online.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				anon := connectAnon(t, e)
				mark := anon.Mark()
				flush(t, anon) // an unauthenticated connection still answers the application ping
				requireQuiet(t, anon, mark, "init", "auth_failed")
				if got := onlineUsers(t, e); len(got) != 0 {
					t.Fatalf("connections without auth must not be online users: %+v", got)
				}

				payload := authenticate(t, anon, e.Token(t, "listener"))
				if payload.UserID != e.IDs["listener"] || payload.Username != "trial-listener" {
					t.Fatalf("init after auth: want the listener, got %+v", payload)
				}
				if payload.UsersOnline != nil {
					t.Fatalf("a non-admin init must not carry usersOnline: %+v", payload.UsersOnline)
				}
				if count := countEvents(anon, mark, "init"); count != 1 {
					t.Fatalf("want one init after the auth, got %d", count)
				}
				requireQuiet(t, anon, mark, "auth_failed")

				entry := onlineEntry(t, e, e.IDs["listener"])
				if entry == nil || entry.Connections != 1 || entry.Username != "trial-listener" {
					t.Fatalf("the authenticated socket must be listed online once: %+v", entry)
				}
				if row := userRow(t, e, e.IDs["listener"]); row == nil || dbMillis(t, row["lastSeen"]) < started {
					t.Fatalf("a successful auth writes lastSeen: %+v", row)
				}
			},
		},
		{
			Requirement: "socket-incoming-connection", Invariant: "INV-SOCKET-INCOMING-CONNECTION", Case: "error", State: "repeat", Assertion: "transport-rejected-auth-stays-anonymous",
			Sources: []string{registerClient, rejectInvalidToken, "server/SocketAuthority.js:216-221", onlineList},
			Claim:   "A connection whose auth is rejected twice stays anonymous: it receives no init, is never listed online, and its disconnect announces no user_offline to admins.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-conn-rejected")
				anon := connectAnon(t, e)
				mark := anon.Mark()
				for i, bad := range []string{"not-a-token", token + "x"} {
					event := rejectAuth(t, anon, bad)
					if message := argObject(t, event)["message"]; message != "Invalid token" {
						t.Fatalf("attempt %d: want Invalid token, got %v", i, message)
					}
				}
				flush(t, anon)
				requireQuiet(t, anon, mark, "init")
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("a rejected connection must not be online: %+v", entry)
				}
				if row := userRow(t, e, id); row == nil || row["lastSeen"] != nil {
					t.Fatalf("a rejected auth must not write lastSeen: %+v", row)
				}

				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				anon.Close()
				admin.None(t, adminMark, "user_offline", socketQuiet, nil)
			},
		},
		{
			Requirement: "socket-incoming-connection", Invariant: "INV-SOCKET-INCOMING-CONNECTION", Case: "edge", State: "changed", Assertion: "transport-sockets-authenticate-independently",
			Sources: []string{registerClient, authHook, "server/SocketAuthority.js:329-347", onlineList},
			Claim:   "Two connections on one server authenticate as different users independently: each receives its own init for its own user and each user's usersOnline connections count stays one.",
			Run: func(t *testing.T, e *harness.Env) {
				first, second := connectAnon(t, e), connectAnon(t, e)
				firstMark, secondMark := first.Mark(), second.Mark()
				finisherID := userID(t, e, "trial-finisher")

				if payload := authenticate(t, first, e.Token(t, "listener")); payload.UserID != e.IDs["listener"] {
					t.Fatalf("first socket init: want the listener, got %+v", payload)
				}
				requireQuiet(t, second, secondMark, "init")
				if entry := onlineEntry(t, e, e.IDs["listener"]); entry == nil || entry.Connections != 1 {
					t.Fatalf("the listener must be online once: %+v", entry)
				}

				if payload := authenticate(t, second, e.Token(t, "finisher")); payload.UserID != finisherID {
					t.Fatalf("second socket init: want the finisher %s, got %+v", finisherID, payload)
				}
				if got := countEvents(first, firstMark, "init"); got != 1 {
					t.Fatalf("the first socket must receive exactly its own init, got %d", got)
				}
				for name, id := range map[string]string{"listener": e.IDs["listener"], "finisher": finisherID} {
					entry := onlineEntry(t, e, id)
					if entry == nil || entry.Connections != 1 {
						t.Fatalf("%s must be online with one connection: %+v", name, entry)
					}
				}
			},
		},
		{
			Requirement: "socket-incoming-connection", Invariant: "INV-SOCKET-INCOMING-CONNECTION", Case: "error", State: "failure", Assertion: "transport-failed-auth-then-authentication",
			Sources: []string{registerClient, rejectInvalidToken, "server/SocketAuthority.js:343-361", persistLastSeen},
			Claim:   "A socket rejected once authenticates on the next attempt: it receives exactly one init, the admin sees exactly one user_online for it, and the association is written to the users row.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				id, token := createUser(t, e, "rt-conn-recovered")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket := connectAnon(t, e)
				mark := socket.Mark()

				rejectAuth(t, socket, "garbage")
				if payload := authenticate(t, socket, token); payload.UserID != id {
					t.Fatalf("init after the recovery: want %s, got %+v", id, payload)
				}
				waitUserEvent(t, admin, adminMark, "user_online", id)
				flush(t, socket)
				if got := countEvents(socket, mark, "init"); got != 1 {
					t.Fatalf("want exactly one init across the failed and successful auth, got %d", got)
				}
				if got := countEvents(admin, adminMark, "user_online"); got != 1 {
					t.Fatalf("the admin must see exactly one user_online, got %d", got)
				}
				entry := onlineEntry(t, e, id)
				if entry == nil || entry.Connections != 1 {
					t.Fatalf("the recovered socket must be online once: %+v", entry)
				}
				if row := userRow(t, e, id); row == nil || dbMillis(t, row["lastSeen"]) < started {
					t.Fatalf("the successful auth writes lastSeen: %+v", row)
				}
			},
		},
	}
}
