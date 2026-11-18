package realtime

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// onlineEmitter announces a newly authenticated user to admins.
	onlineEmitter = "server/SocketAuthority.js:347-348"
	// adminEmitterSource is the admin-only audience of user_online/user_offline.
	adminEmitterSource = "server/SocketAuthority.js:80-87"
	// onlineClient stores the user in the web client's online list.
	onlineClient = "client/layouts/default.vue:275-280"
)

// userOnlineScenarios covers socket-outgoing-user-online.
func userOnlineScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-user-online", Invariant: "INV-SOCKET-OUTGOING-USER-ONLINE", Case: "normal", State: "initial", Assertion: "user-online-admin-audience",
			Sources: []string{onlineEmitter, adminEmitterSource, publicUserSource, onlineClient},
			Claim:   "Every admin socket receives one user_online per authenticated user with the six public fields, non-admin and anonymous sockets receive none, and the authenticating admin's own event precedes its init.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-uo-audience")
				firstAdmin, _ := connectAuth(t, e, e.Token(t, "root"))
				secondAdmin, _ := connectAuth(t, e, e.Token(t, "root"))
				anonymous := connectAnon(t, e)
				firstMark, secondMark, anonMark := firstAdmin.Mark(), secondAdmin.Mark(), anonymous.Mark()

				user, _ := connectAuth(t, e, token)
				event := waitUserEvent(t, firstAdmin, firstMark, "user_online", id)
				object := argObject(t, event)
				requireExactKeys(t, "user_online", object, "id", "username", "type", "session", "lastSeen", "createdAt")
				decoded := decodeArg[publicUser](t, event)
				if decoded.ID != id || decoded.Username != "rt-uo-audience" || decoded.Type != "user" || decoded.CreatedAt <= 0 {
					t.Fatalf("user_online payload: %+v", decoded)
				}
				if decoded.Session != nil || decoded.LastSeen != nil {
					t.Fatalf("a first user_online has a null session and a null lastSeen: %+v", decoded)
				}
				second := decodeArg[publicUser](t, waitUserEvent(t, secondAdmin, secondMark, "user_online", id))
				if !reflect.DeepEqual(second, decoded) {
					t.Fatalf("both admin sockets must receive the same payload:\n %+v\n %+v", second, decoded)
				}
				requireQuiet(t, anonymous, anonMark, "user_online")
				requireQuiet(t, user, 0, "user_online")

				late := connectAnon(t, e)
				authenticate(t, late, e.Token(t, "root"))
				order := eventOrder(late, "user_online", "init")
				if len(order) != 2 || order[0] != "user_online" || order[1] != "init" {
					t.Fatalf("an admin's own user_online must precede its init, got %v", order)
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-online", Invariant: "INV-SOCKET-OUTGOING-USER-ONLINE", Case: "error", State: "repeat", Assertion: "user-online-repeat-connection",
			Sources: []string{onlineEmitter, adminEmitterSource, persistLastSeen},
			Claim:   "A user's second socket produces a second user_online with the same id and createdAt but a non-null lastSeen written by the first authentication.",
			Run: func(t *testing.T, e *harness.Env) {
				started := time.Now().Add(-time.Second).UnixMilli()
				id, token := createUser(t, e, "rt-uo-repeat")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				_, _ = connectAuth(t, e, token)
				first := decodeArg[publicUser](t, waitUserEvents(t, admin, adminMark, "user_online", id, 1)[0])
				_, _ = connectAuth(t, e, token)
				second := decodeArg[publicUser](t, waitUserEvents(t, admin, adminMark, "user_online", id, 2)[1])
				if first.LastSeen != nil {
					t.Fatalf("the first authentication reports lastSeen null, got %d", *first.LastSeen)
				}
				if second.LastSeen == nil || *second.LastSeen < started {
					t.Fatalf("the second user_online must report the first authentication's lastSeen, got %+v", second.LastSeen)
				}
				if second.ID != first.ID || second.CreatedAt != first.CreatedAt || second.Type != first.Type {
					t.Fatalf("the repeat event must describe the same user:\n %+v\n %+v", first, second)
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-online", Invariant: "INV-SOCKET-OUTGOING-USER-ONLINE", Case: "edge", State: "changed", Assertion: "user-online-session-and-root",
			Sources: []string{onlineEmitter, publicUserSource, sessionForClientSource},
			Claim:   "user_online carries the user's open playback session with its id, item and device, and an admin's own event carries type root.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				session := play(t, e, listener, e.IDs["novel"], "rt-uo-device")
				_, _ = connectAuth(t, e, listener)
				defer closePlayback(t, e, listener, session)
				event := waitUserEvent(t, admin, adminMark, "user_online", e.IDs["listener"])
				decoded := decodeArg[publicUser](t, event)
				if decoded.Type != "user" || decoded.Session == nil {
					t.Fatalf("user_online must carry the session: %+v", decoded)
				}
				if decoded.Session.ID != session || decoded.Session.UserID != e.IDs["listener"] ||
					decoded.Session.LibraryItemID != e.IDs["novel"] || decoded.Session.MediaPlayer != "realtime-packet" ||
					decoded.Session.DeviceInfo["deviceId"] != "rt-uo-device" {
					t.Fatalf("session payload: %+v", decoded.Session)
				}

				lateAdmin := connectAnon(t, e)
				authenticate(t, lateAdmin, e.Token(t, "root"))
				adminEvent := waitUserEvent(t, admin, adminMark, "user_online", userID(t, e, "trial-root"))
				if root := decodeArg[publicUser](t, adminEvent); root.Type != "root" {
					t.Fatalf("the root user's user_online must carry type root: %+v", root)
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-online", Invariant: "INV-SOCKET-OUTGOING-USER-ONLINE", Case: "error", State: "failure", Assertion: "user-online-not-for-rejected-auth",
			Sources: []string{onlineEmitter, rejectInvalidToken, adminEmitterSource},
			Claim:   "A rejected credential emits no user_online on any admin socket and leaves the user offline.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-uo-rejected")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket := connectAnon(t, e)
				rejectAuth(t, socket, token+"x")
				flush(t, socket)
				requireQuiet(t, admin, adminMark, "user_online")
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("a rejected credential must not put the user online: %+v", entry)
				}
			},
		},
	}
}

// userOfflineScenarios covers socket-outgoing-user-offline.
func userOfflineScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-user-offline", Invariant: "INV-SOCKET-OUTGOING-USER-OFFLINE", Case: "normal", State: "initial", Assertion: "user-offline-payload-and-audience",
			Sources: []string{offlineEmitter, adminEmitterSource, publicUserSource, onlineClient},
			Claim:   "Closing an authenticated socket sends every admin one user_offline with the six public fields (non-null lastSeen) and no non-admin or anonymous socket receives it.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-uf-audience")
				firstAdmin, _ := connectAuth(t, e, e.Token(t, "root"))
				secondAdmin, _ := connectAuth(t, e, e.Token(t, "root"))
				other, _ := connectAuth(t, e, e.Token(t, "finisher"))
				anonymous := connectAnon(t, e)
				user, _ := connectAuth(t, e, token)
				firstMark, secondMark := firstAdmin.Mark(), secondAdmin.Mark()
				otherMark, anonMark := other.Mark(), anonymous.Mark()

				user.Close()
				event := waitUserEvent(t, firstAdmin, firstMark, "user_offline", id)
				requireExactKeys(t, "user_offline", argObject(t, event), "id", "username", "type", "session", "lastSeen", "createdAt")
				decoded := decodeArg[publicUser](t, event)
				if decoded.ID != id || decoded.Username != "rt-uf-audience" || decoded.Type != "user" || decoded.CreatedAt <= 0 {
					t.Fatalf("user_offline payload: %+v", decoded)
				}
				if decoded.LastSeen == nil || decoded.Session != nil {
					t.Fatalf("user_offline must carry the written lastSeen and a null session: %+v", decoded)
				}
				second := decodeArg[publicUser](t, waitUserEvent(t, secondAdmin, secondMark, "user_offline", id))
				if !reflect.DeepEqual(second, decoded) {
					t.Fatalf("both admin sockets must receive the same payload:\n %+v\n %+v", second, decoded)
				}
				requireQuiet(t, other, otherMark, "user_offline")
				requireQuiet(t, anonymous, anonMark, "user_offline")
			},
		},
		{
			Requirement: "socket-outgoing-user-offline", Invariant: "INV-SOCKET-OUTGOING-USER-OFFLINE", Case: "error", State: "repeat", Assertion: "user-offline-never-to-users",
			Sources: []string{offlineEmitter, adminEmitterSource},
			Claim:   "A non-admin socket receives no user_offline for two successive users that disconnect.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				watcher, _ := connectAuth(t, e, e.Token(t, "finisher"))
				adminMark, watcherMark := admin.Mark(), watcher.Mark()
				for _, username := range []string{"rt-uf-watch-a", "rt-uf-watch-b"} {
					id, token := createUser(t, e, username)
					socket, _ := connectAuth(t, e, token)
					socket.Close()
					waitUserEvent(t, admin, adminMark, "user_offline", id)
				}
				requireQuiet(t, watcher, watcherMark, "user_offline")
			},
		},
		{
			Requirement: "socket-outgoing-user-offline", Invariant: "INV-SOCKET-OUTGOING-USER-OFFLINE", Case: "edge", State: "changed", Assertion: "user-offline-per-socket",
			Sources: []string{offlineEmitter, "server/SocketAuthority.js:216-234", onlineUsersSource},
			Claim:   "Each authenticated socket disconnect announces user_offline even while the same user has another connection, and the second close empties usersOnline.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-uf-two")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				first, _ := connectAuth(t, e, token)
				second, _ := connectAuth(t, e, token)
				waitUserEvents(t, admin, adminMark, "user_online", id, 2)
				first.Close()
				firstOffline := waitUserEvents(t, admin, adminMark, "user_offline", id, 1)
				if entry := onlineEntry(t, e, id); entry == nil || entry.Connections != 1 {
					t.Fatalf("one of two connections must remain: %+v", entry)
				}
				second.Close()
				waitUserEvents(t, admin, adminMark, "user_offline", id, 2)
				if got := countEvents(admin, adminMark, "user_offline"); got != 2 {
					t.Fatalf("want two user_offline events, got %d", got)
				}
				if decoded := decodeArg[publicUser](t, firstOffline[0]); decoded.ID != id || decoded.LastSeen == nil {
					t.Fatalf("the first user_offline payload: %+v", decoded)
				}
				harness.Eventually(t, 10*time.Second, "the user to leave usersOnline", func() bool {
					return onlineEntry(t, e, id) == nil
				})
			},
		},
		{
			Requirement: "socket-outgoing-user-offline", Invariant: "INV-SOCKET-OUTGOING-USER-OFFLINE", Case: "error", State: "failure", Assertion: "user-offline-without-admin",
			Sources: []string{offlineEmitter, onlineUsersSource},
			Claim:   "A user that disconnects while no admin is connected leaves no online state: the next admin init does not list the user.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-uf-noadmin")
				socket, _ := connectAuth(t, e, token)
				if entry := onlineEntry(t, e, id); entry == nil {
					t.Fatalf("the user must be online before the disconnect")
				}
				socket.Close()
				harness.Eventually(t, 10*time.Second, "the user to leave usersOnline", func() bool {
					return onlineEntry(t, e, id) == nil
				})
				admin := e.Socket(t, e.Token(t, "root"))
				if connections(decodeArg[initPayload](t, initEvent(t, admin)).UsersOnline, id) != 0 {
					t.Fatalf("a disconnected user must not appear in the next admin init")
				}
			},
		},
	}
}

// eventOrder lists, in arrival order, the events of the given names.
func eventOrder(s *harness.Socket, names ...string) []string {
	var order []string
	for _, event := range s.Events() {
		if slices.Contains(names, event.Name) {
			order = append(order, event.Name)
		}
	}
	return order
}
