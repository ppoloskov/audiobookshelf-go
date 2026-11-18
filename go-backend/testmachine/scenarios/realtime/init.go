package realtime

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// initPayloadSource builds init: userId/username always, usersOnline for
	// admins only.
	initPayloadSource = "server/SocketAuthority.js:354-361"
	// onlineUsersSource is the usersOnline list with per-user connections.
	onlineUsersSource = "server/SocketAuthority.js:25-45"
	// publicUserSource is the user object inside init and user_online.
	publicUserSource = "server/models/User.js:584-594"
	// sessionForClientSource is the session object a public user carries.
	sessionForClientSource = "server/objects/PlaybackSession.js:86-115"
	// initClient is the web client's init handler (usersOnline store).
	initClient = "client/layouts/default.vue:147-155"
)

// initScenarios covers socket-outgoing-init: the payload, the admin-only
// usersOnline list and the rule that only a successful auth initializes.
func initScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-init", Invariant: "INV-SOCKET-OUTGOING-INIT", Case: "normal", State: "initial", Assertion: "init-user-payload",
			Sources: []string{initPayloadSource, initClient},
			Claim:   "A non-admin init is exactly one object with userId and username of the authenticated user and no usersOnline key.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				socket.Emit(t, "auth", e.Token(t, "listener"))
				object := argObject(t, waitEvent(t, socket, mark, "init"))
				requireExactKeys(t, "non-admin init", object, "userId", "username")
				if object["userId"] != e.IDs["listener"] || object["username"] != "trial-listener" {
					t.Fatalf("init names the wrong user: %v", object)
				}
			},
		},
		{
			Requirement: "socket-outgoing-init", Invariant: "INV-SOCKET-OUTGOING-INIT", Case: "edge", State: "changed", Assertion: "init-admin-users-online",
			Sources: []string{initPayloadSource, onlineUsersSource, publicUserSource, sessionForClientSource},
			Claim:   "An admin init carries usersOnline with every authenticated client: the admin's own entry, and a user entry whose session is the user's open playback session with its id, item and device.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				listener := e.Token(t, "listener")
				e.Socket(t, listener) // one authenticated listener connection
				session := play(t, e, listener, e.IDs["novel"], "rt-init-device")
				defer closePlayback(t, e, listener, session)

				admin := e.Socket(t, e.Token(t, "root"))
				adminsInit := decodeArg[initPayload](t, initEvent(t, admin))
				if adminsInit.UsersOnline == nil {
					t.Fatalf("an admin init must carry usersOnline: %+v", adminsInit)
				}
				entries := map[string]publicUser{}
				for _, entry := range adminsInit.UsersOnline {
					entries[entry.ID] = entry
				}
				own, ok := entries[userID(t, e, "trial-root")]
				if !ok || own.Type != "root" || own.Connections != 1 || own.Session != nil || own.LastSeen == nil || *own.LastSeen < started {
					t.Fatalf("the admin's own usersOnline entry is wrong: %+v", own)
				}
				userEntry, ok := entries[e.IDs["listener"]]
				if !ok || userEntry.Type != "user" || userEntry.Connections != 1 || userEntry.Username != "trial-listener" || userEntry.CreatedAt <= 0 {
					t.Fatalf("the listener's usersOnline entry is wrong: %+v", userEntry)
				}
				if userEntry.Session == nil || userEntry.Session.ID != session || userEntry.Session.UserID != e.IDs["listener"] ||
					userEntry.Session.LibraryItemID != e.IDs["novel"] || userEntry.Session.MediaPlayer != "realtime-packet" ||
					userEntry.Session.DeviceInfo["deviceId"] != "rt-init-device" || userEntry.Session.DisplayTitle != "Single File Novel" {
					t.Fatalf("the usersOnline session must be the open playback session %s: %+v", session, userEntry.Session)
				}
			},
		},
		{
			Requirement: "socket-outgoing-init", Invariant: "INV-SOCKET-OUTGOING-INIT", Case: "normal", State: "repeat", Assertion: "init-admin-repeat-connection",
			Sources: []string{initPayloadSource, onlineUsersSource},
			Claim:   "A second admin connection gets its own init whose usersOnline counts the admin twice and no other user changes, while the first admin init counted it once.",
			Run: func(t *testing.T, e *harness.Env) {
				first := e.Socket(t, e.Token(t, "root"))
				firstInit := decodeArg[initPayload](t, initEvent(t, first))
				second := e.Socket(t, e.Token(t, "root"))
				secondInit := decodeArg[initPayload](t, initEvent(t, second))
				rootID := userID(t, e, "trial-root")
				if count := connections(firstInit.UsersOnline, rootID); count != 1 {
					t.Fatalf("the first admin init must count one connection, got %d (%+v)", count, firstInit.UsersOnline)
				}
				if count := connections(secondInit.UsersOnline, rootID); count != 2 {
					t.Fatalf("the second admin init must count two connections, got %d (%+v)", count, secondInit.UsersOnline)
				}
				if len(firstInit.UsersOnline) != len(secondInit.UsersOnline) {
					t.Fatalf("reconnecting the same admin must not add users: %d then %d", len(firstInit.UsersOnline), len(secondInit.UsersOnline))
				}
			},
		},
		{
			Requirement: "socket-outgoing-init", Invariant: "INV-SOCKET-OUTGOING-INIT", Case: "error", State: "failure", Assertion: "init-only-after-successful-auth",
			Sources: []string{initPayloadSource, inactiveUser, association},
			Claim:   "A rejected auth emits auth_failed and no init, and the same socket receives exactly one init after the user becomes active.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-init-inactive")
				setUserActive(t, e, id, false)
				socket := connectAnon(t, e)
				mark := socket.Mark()
				if event := rejectAuth(t, socket, token); argObject(t, event)["message"] != "Invalid user" {
					t.Fatalf("want Invalid user, got %v", argObject(t, event))
				}
				setUserActive(t, e, id, true)
				if payload := authenticate(t, socket, token); payload.UserID != id || payload.UsersOnline != nil {
					t.Fatalf("init after reactivation must name %s without usersOnline: %+v", id, payload)
				}
				if count := countEvents(socket, mark, "init"); count != 1 {
					t.Fatalf("want exactly one init, got %d", count)
				}
			},
		},
	}
}

// connections counts the usersOnline connection count of a user id.
func connections(users []publicUser, id string) int {
	for _, user := range users {
		if user.ID == id {
			return user.Connections
		}
	}
	return 0
}
