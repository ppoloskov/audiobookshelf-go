package realtime

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// pingHandler answers the application-level ping with a pong on the same
	// socket, authenticated or not.
	pingHandler = "server/SocketAuthority.js:249-254"
)

// pingScenarios covers socket-incoming-ping: the client action and its answer.
func pingScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-ping", Invariant: "INV-SOCKET-INCOMING-PING", Case: "normal", State: "initial", Assertion: "ping-answers-once",
			Sources: []string{pingHandler},
			Claim:   "One ping from an authenticated socket produces exactly one pong on that socket.",
			Run: func(t *testing.T, e *harness.Env) {
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				socket.Emit(t, "ping")
				requireNoArgs(t, waitEvent(t, socket, mark, "pong"))
				if count := countEvents(socket, mark, "pong"); count != 1 {
					t.Fatalf("one ping must produce one pong, got %d", count)
				}
				socket.None(t, len(socket.Events()), "pong", socketQuiet, nil)
			},
		},
		{
			Requirement: "socket-incoming-ping", Invariant: "INV-SOCKET-INCOMING-PING", Case: "error", State: "repeat", Assertion: "ping-answers-only-its-sender",
			Sources: []string{pingHandler},
			Claim:   "Two pings from one socket produce two pongs in order on that socket and none on another authenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				sender, _ := connectAuth(t, e, e.Token(t, "listener"))
				other, _ := connectAuth(t, e, e.Token(t, "finisher"))
				senderMark, otherMark := sender.Mark(), other.Mark()
				sender.Emit(t, "ping")
				sender.Emit(t, "ping")
				waitEvent(t, sender, senderMark, "pong")
				waitEvent(t, sender, senderMark+1, "pong")
				flush(t, other)
				if count := countEvents(other, otherMark, "pong"); count != 1 {
					t.Fatalf("only the barrier ping may answer on the other socket, got %d pongs", count)
				}
				if count := countEvents(sender, senderMark, "pong"); count != 2 {
					t.Fatalf("two pings must produce two pongs, got %d", count)
				}
			},
		},
		{
			Requirement: "socket-incoming-ping", Invariant: "INV-SOCKET-INCOMING-PING", Case: "edge", State: "changed", Assertion: "ping-unauthenticated-answers",
			Sources: []string{pingHandler, registerClient},
			Claim:   "The ping action is not behind auth: an unauthenticated socket gets its pong before and after a later authentication.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				socket.Emit(t, "ping")
				requireNoArgs(t, waitEvent(t, socket, mark, "pong"))
				authenticate(t, socket, e.Token(t, "listener"))
				authedMark := socket.Mark()
				socket.Emit(t, "ping")
				waitEvent(t, socket, authedMark, "pong")
				if count := countEvents(socket, mark, "pong"); count != 2 {
					t.Fatalf("want one pong before and after auth, got %d", count)
				}
				requireQuiet(t, socket, mark, "auth_failed")
			},
		},
		{
			Requirement: "socket-incoming-ping", Invariant: "INV-SOCKET-INCOMING-PING", Case: "error", State: "failure", Assertion: "ping-after-rejected-auth-answers",
			Sources: []string{pingHandler, rejectInvalidToken},
			Claim:   "A socket whose auth was rejected still answers ping, and the ping leaves it unauthenticated.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				rejectAuth(t, socket, "garbage")
				socket.Emit(t, "ping")
				requireNoArgs(t, waitEvent(t, socket, mark, "pong"))
				flush(t, socket)
				requireQuiet(t, socket, mark, "init")
				if got := countEvents(socket, mark, "auth_failed"); got != 1 {
					t.Fatalf("want the one rejection only, got %d", got)
				}
			},
		},
	}
}

// pongScenarios covers socket-outgoing-pong: the event's payload, audience and
// ordering.
func pongScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-pong", Invariant: "INV-SOCKET-OUTGOING-PONG", Case: "normal", State: "initial", Assertion: "pong-carries-no-arguments",
			Sources: []string{pingHandler},
			Claim:   "pong is a bare event with no arguments.",
			Run: func(t *testing.T, e *harness.Env) {
				socket, _ := connectAuth(t, e, e.Token(t, "listener"))
				mark := socket.Mark()
				socket.Emit(t, "ping")
				event := waitEvent(t, socket, mark, "pong")
				requireNoArgs(t, event)
				if len(event.Args) != 0 {
					t.Fatalf("pong arguments: %v", event.Args)
				}
			},
		},
		{
			Requirement: "socket-outgoing-pong", Invariant: "INV-SOCKET-OUTGOING-PONG", Case: "error", State: "repeat", Assertion: "pong-audience-is-the-sender",
			Sources: []string{pingHandler},
			Claim:   "Each ping produces one pong on the pinging socket only, and no pong reaches a second authenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				sender, _ := connectAuth(t, e, e.Token(t, "listener"))
				other, _ := connectAuth(t, e, e.Token(t, "finisher"))
				senderMark, otherMark := sender.Mark(), other.Mark()
				for i := 0; i < 3; i++ {
					sender.Emit(t, "ping")
					waitEvent(t, sender, senderMark+i, "pong")
				}
				flush(t, other)
				if count := countEvents(other, otherMark, "pong"); count != 1 {
					t.Fatalf("only the barrier ping may answer on the other socket, got %d pongs", count)
				}
				if count := countEvents(sender, senderMark, "pong"); count != 3 {
					t.Fatalf("want three pongs on the sender, got %d", count)
				}
			},
		},
		{
			Requirement: "socket-outgoing-pong", Invariant: "INV-SOCKET-OUTGOING-PONG", Case: "edge", State: "changed", Assertion: "pong-for-unauthenticated-socket",
			Sources: []string{pingHandler, registerClient},
			Claim:   "pong is delivered to a socket that never authenticated and to the same socket after it authenticates.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				socket.Emit(t, "ping")
				requireNoArgs(t, waitEvent(t, socket, mark, "pong"))
				authenticate(t, socket, e.Token(t, "finisher"))
				authedMark := socket.Mark()
				socket.Emit(t, "ping")
				requireNoArgs(t, waitEvent(t, socket, authedMark, "pong"))
				if count := countEvents(socket, mark, "pong"); count != 2 {
					t.Fatalf("want two pongs across the auth, got %d", count)
				}
			},
		},
		{
			Requirement: "socket-outgoing-pong", Invariant: "INV-SOCKET-OUTGOING-PONG", Case: "error", State: "failure", Assertion: "pong-after-failed-auth",
			Sources: []string{pingHandler, rejectInvalidToken},
			Claim:   "A rejected auth does not disturb the heartbeat: the socket still receives pong and no init.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := connectAnon(t, e)
				mark := socket.Mark()
				rejectAuth(t, socket, "garbage")
				socket.Emit(t, "ping")
				event := waitEvent(t, socket, mark, "pong")
				requireNoArgs(t, event)
				flush(t, socket)
				requireQuiet(t, socket, mark, "init", "user_online")
			},
		},
	}
}
