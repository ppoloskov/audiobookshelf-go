package realtime

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// messageHandler is the admin-only test action: it turns the payload's
	// message into admin_message for all authorized clients.
	messageHandler = "server/SocketAuthority.js:239-247"
	// allAuthorizedClients is emitter(), the audience of admin_message.
	allAuthorizedClients = "server/SocketAuthority.js:57-65"
	// adminMessageClient is the web client's toast for admin_message.
	adminMessageClient = "client/layouts/default.vue:364-366"
)

// messageScenarios covers socket-incoming-message-all-users and
// socket-outgoing-admin-message: the admin-only action and the event it emits.
func messageScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-message-all-users", Invariant: "INV-SOCKET-INCOMING-MESSAGE-ALL-USERS", Case: "normal", State: "initial", Assertion: "message-all-users-broadcast",
			Sources: []string{messageHandler, allAuthorizedClients, adminMessageClient},
			Claim:   "An admin's message_all_users delivers admin_message with its message to the sender and every other authenticated socket, and to no anonymous socket.",
			Run: func(t *testing.T, e *harness.Env) {
				sender := e.Socket(t, e.Token(t, "root"))
				secondAdmin, _ := connectAuth(t, e, e.Token(t, "root"))
				user, _ := connectAuth(t, e, e.Token(t, "listener"))
				anonymous := connectAnon(t, e)
				senderMark, secondMark := sender.Mark(), secondAdmin.Mark()
				userMark, anonMark := user.Mark(), anonymous.Mark()

				sender.Emit(t, "message_all_users", map[string]any{"message": "Realtime hello"})
				for name, socket := range map[string]*harness.Socket{"sender": sender, "second admin": secondAdmin, "user": user} {
					mark := map[string]int{"sender": senderMark, "second admin": secondMark, "user": userMark}[name]
					event := waitEvent(t, socket, mark, "admin_message")
					if message := decodeArg[string](t, event); message != "Realtime hello" {
						t.Fatalf("%s received admin_message %q", name, message)
					}
				}
				requireQuiet(t, anonymous, anonMark, "admin_message")
			},
		},
		{
			Requirement: "socket-incoming-message-all-users", Invariant: "INV-SOCKET-INCOMING-MESSAGE-ALL-USERS", Case: "error", State: "repeat", Assertion: "message-all-users-non-admin-silent",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "Two message_all_users events from a non-admin user emit no admin_message on any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				_, token := createUser(t, e, "rt-msg-nonadmin")
				sender, _ := connectAuth(t, e, token)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				other, _ := connectAuth(t, e, e.Token(t, "finisher"))
				senderMark, adminMark, otherMark := sender.Mark(), admin.Mark(), other.Mark()
				for i := 0; i < 2; i++ {
					sender.Emit(t, "message_all_users", map[string]any{"message": "not allowed"})
				}
				flush(t, sender)
				requireQuiet(t, sender, senderMark, "admin_message")
				requireQuiet(t, admin, adminMark, "admin_message")
				requireQuiet(t, other, otherMark, "admin_message")
			},
		},
		{
			Requirement: "socket-incoming-message-all-users", Invariant: "INV-SOCKET-INCOMING-MESSAGE-ALL-USERS", Case: "edge", State: "changed", Assertion: "message-all-users-payload-and-new-audience",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "A payload without message sends an empty string, a Unicode message is preserved exactly, and a socket that authenticated after earlier messages receives the next one.",
			Run: func(t *testing.T, e *harness.Env) {
				sender := e.Socket(t, e.Token(t, "root"))
				user, _ := connectAuth(t, e, e.Token(t, "listener"))
				userMark := user.Mark()
				sender.Emit(t, "message_all_users", map[string]any{})
				if message := decodeArg[string](t, waitEvent(t, user, userMark, "admin_message")); message != "" {
					t.Fatalf("a missing message must become the empty string, got %q", message)
				}
				sender.Emit(t, "message_all_users", map[string]any{"message": "héllo ✓  spaced"})
				if message := decodeArg[string](t, waitEvent(t, user, userMark+1, "admin_message")); message != "héllo ✓  spaced" {
					t.Fatalf("the message must be preserved, got %q", message)
				}

				late, _ := connectAuth(t, e, e.Token(t, "finisher"))
				lateMark := late.Mark()
				sender.Emit(t, "message_all_users", map[string]any{"message": "after join"})
				if message := decodeArg[string](t, waitEvent(t, late, lateMark, "admin_message")); message != "after join" {
					t.Fatalf("the socket that joined later received %q", message)
				}
				if message := decodeArg[string](t, waitEvent(t, user, userMark+2, "admin_message")); message != "after join" {
					t.Fatalf("the earlier socket received %q", message)
				}
			},
		},
		{
			Requirement: "socket-incoming-message-all-users", Invariant: "INV-SOCKET-INCOMING-MESSAGE-ALL-USERS", Case: "error", State: "failure", Assertion: "message-all-users-unauthenticated-silent",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "A message_all_users event from an unauthenticated socket emits nothing and does not authenticate the socket.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()
				anonymous := connectAnon(t, e)
				anonMark := anonymous.Mark()
				anonymous.Emit(t, "message_all_users", map[string]any{"message": "anonymous"})
				flush(t, anonymous)
				requireQuiet(t, admin, adminMark, "admin_message")
				requireQuiet(t, anonymous, anonMark, "admin_message", "init")
			},
		},
		{
			Requirement: "socket-outgoing-admin-message", Invariant: "INV-SOCKET-OUTGOING-ADMIN-MESSAGE", Case: "normal", State: "initial", Assertion: "admin-message-string-payload",
			Sources: []string{messageHandler, allAuthorizedClients, adminMessageClient},
			Claim:   "admin_message is one bare JSON string argument equal to the sent message and reaches every authenticated socket, the sender included.",
			Run: func(t *testing.T, e *harness.Env) {
				sender := e.Socket(t, e.Token(t, "root"))
				user, _ := connectAuth(t, e, e.Token(t, "listener"))
				senderMark, userMark := sender.Mark(), user.Mark()
				sender.Emit(t, "message_all_users", map[string]any{"message": "to everyone"})
				for name, socket := range map[string]*harness.Socket{"sender": sender, "user": user} {
					mark := map[string]int{"sender": senderMark, "user": userMark}[name]
					event := waitEvent(t, socket, mark, "admin_message")
					if len(event.Args) != 1 {
						t.Fatalf("%s: admin_message must have one argument, got %d", name, len(event.Args))
					}
					if got := decodeArg[string](t, event); got != "to everyone" {
						t.Fatalf("%s: admin_message %q", name, got)
					}
				}
			},
		},
		{
			Requirement: "socket-outgoing-admin-message", Invariant: "INV-SOCKET-OUTGOING-ADMIN-MESSAGE", Case: "error", State: "repeat", Assertion: "admin-message-no-event-for-rejected-sender",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "A non-admin sender produces no admin_message on any socket, twice in a row.",
			Run: func(t *testing.T, e *harness.Env) {
				_, token := createUser(t, e, "rt-am-nonadmin")
				sender, _ := connectAuth(t, e, token)
				admin := e.Socket(t, e.Token(t, "root"))
				senderMark, adminMark := sender.Mark(), admin.Mark()
				for i := 0; i < 2; i++ {
					sender.Emit(t, "message_all_users", map[string]any{"message": "rejected"})
				}
				flush(t, sender)
				requireQuiet(t, sender, senderMark, "admin_message")
				requireQuiet(t, admin, adminMark, "admin_message")
			},
		},
		{
			Requirement: "socket-outgoing-admin-message", Invariant: "INV-SOCKET-OUTGOING-ADMIN-MESSAGE", Case: "edge", State: "changed", Assertion: "admin-message-empty-and-late-recipient",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "admin_message is the empty string for a payload without message, and a socket that authenticates later receives the following message.",
			Run: func(t *testing.T, e *harness.Env) {
				sender := e.Socket(t, e.Token(t, "root"))
				user, _ := connectAuth(t, e, e.Token(t, "listener"))
				userMark := user.Mark()
				sender.Emit(t, "message_all_users", map[string]any{})
				event := waitEvent(t, user, userMark, "admin_message")
				if len(event.Args) != 1 {
					t.Fatalf("admin_message must have one argument, got %d", len(event.Args))
				}
				if got := decodeArg[string](t, event); got != "" {
					t.Fatalf("a missing message must be the empty string, got %q", got)
				}

				late, _ := connectAuth(t, e, e.Token(t, "finisher"))
				lateMark := late.Mark()
				sender.Emit(t, "message_all_users", map[string]any{"message": "later"})
				if got := decodeArg[string](t, waitEvent(t, late, lateMark, "admin_message")); got != "later" {
					t.Fatalf("the late socket received %q", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-admin-message", Invariant: "INV-SOCKET-OUTGOING-ADMIN-MESSAGE", Case: "error", State: "failure", Assertion: "admin-message-unauthenticated-sender",
			Sources: []string{messageHandler, allAuthorizedClients},
			Claim:   "admin_message is never emitted for a message_all_users event from an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, e.Token(t, "root"))
				adminMark := admin.Mark()
				anonymous := connectAnon(t, e)
				anonymous.Emit(t, "message_all_users", map[string]any{"message": "anonymous"})
				flush(t, anonymous)
				requireQuiet(t, admin, adminMark, "admin_message")
			},
		},
	}
}
