package realtime

import (
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// disconnectHandler removes the client, skips anonymous sockets and
	// announces authenticated users.
	disconnectHandler = "server/SocketAuthority.js:212-234"
	// deleteAnonymousClient is the branch that only deletes the record.
	deleteAnonymousClient = "server/SocketAuthority.js:216-221"
	// offlineEmitter announces the user to admin sockets.
	offlineEmitter = "server/SocketAuthority.js:222-225"
)

// disconnectScenarios covers socket-incoming-disconnect: which clients leave the
// registry, what the server does per branch and that nothing is announced for a
// socket without a user.
func disconnectScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-disconnect", Invariant: "INV-SOCKET-INCOMING-DISCONNECT", Case: "normal", State: "initial", Assertion: "disconnect-removes-authenticated-client",
			Sources: []string{disconnectHandler, offlineEmitter, onlineList},
			Claim:   "Closing an authenticated socket removes its client from usersOnline and announces user_offline for that user to the admin sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-disc-user")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket, _ := connectAuth(t, e, token)
				waitUserEvent(t, admin, adminMark, "user_online", id)
				socket.Close()
				waitUserEvent(t, admin, adminMark, "user_offline", id)
				harness.Eventually(t, 10*time.Second, "the disconnected client to leave usersOnline", func() bool {
					return onlineEntry(t, e, id) == nil
				})
			},
		},
		{
			Requirement: "socket-incoming-disconnect", Invariant: "INV-SOCKET-INCOMING-DISCONNECT", Case: "error", State: "repeat", Assertion: "disconnect-anonymous-has-no-effect",
			Sources: []string{disconnectHandler, deleteAnonymousClient},
			Claim:   "Closing a socket that never authenticated twice announces no user_offline to admins and leaves the creating user's row and online state untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				id, _ := createUser(t, e, "rt-disc-anon")
				before := userRow(t, e, id)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				for i := 0; i < 2; i++ {
					anonymous := connectAnon(t, e)
					anonymous.Close()
					admin.None(t, adminMark, "user_offline", socketQuiet, nil)
				}
				requireRowUnchanged(t, e, id, before)
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("an anonymous disconnect must not make anyone online: %+v", entry)
				}
			},
		},
		{
			Requirement: "socket-incoming-disconnect", Invariant: "INV-SOCKET-INCOMING-DISCONNECT", Case: "edge", State: "changed", Assertion: "disconnect-one-of-two-sockets",
			Sources: []string{disconnectHandler, offlineEmitter, onlineUsersSource},
			Claim:   "Closing one of a user's two sockets announces user_offline while usersOnline still lists the user with one remaining connection.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-disc-two")
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				first, _ := connectAuth(t, e, token)
				_, _ = connectAuth(t, e, token)
				waitUserEvents(t, admin, adminMark, "user_online", id, 2)
				if entry := onlineEntry(t, e, id); entry == nil || entry.Connections != 2 {
					t.Fatalf("two sockets must be two connections: %+v", entry)
				}
				first.Close()
				waitUserEvents(t, admin, adminMark, "user_offline", id, 1)
				harness.Eventually(t, 10*time.Second, "the remaining connection only", func() bool {
					entry := onlineEntry(t, e, id)
					return entry != nil && entry.Connections == 1
				})
			},
		},
		{
			Requirement: "socket-incoming-disconnect", Invariant: "INV-SOCKET-INCOMING-DISCONNECT", Case: "error", State: "failure", Assertion: "disconnect-after-rejected-auth-silent",
			Sources: []string{disconnectHandler, deleteAnonymousClient, rejectInvalidToken},
			Claim:   "A socket whose auth was rejected disconnects like an anonymous one: no user_offline, no usersOnline entry and no row change.",
			Run: func(t *testing.T, e *harness.Env) {
				id, token := createUser(t, e, "rt-disc-rejected")
				before := userRow(t, e, id)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				socket := connectAnon(t, e)
				rejectAuth(t, socket, token+"x")
				socket.Close()
				admin.None(t, adminMark, "user_offline", socketQuiet, nil)
				requireRowUnchanged(t, e, id, before)
				if entry := onlineEntry(t, e, id); entry != nil {
					t.Fatalf("a rejected socket must never be online: %+v", entry)
				}
			},
		},
	}
}
