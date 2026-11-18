package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// levelMatch matches a log event at the wanted level whose message contains
// marker ("" matches any message).
func levelMatch(level int, marker string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var payload logPayload
		if json.Unmarshal(args[0], &payload) != nil {
			return false
		}
		return payload.Level == level && (marker == "" || strings.Contains(payload.Message, marker))
	}
}

func (s *suite) logScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "normal", State: "initial", Assertion: "log-subscribed-admin-receives-shape",
			Sources: []string{"server/Logger.js:69-90", "server/SocketAuthority.js:201-208", "client/pages/config/log.vue:85-130"},
			Claim:   "An admin socket subscribed at INFO receives one log event for one ping with the upstream LogObject fields: string timestamp, source and message, levelName INFO and numeric level 2.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				socket.Emit(t, "set_log_listener", 2)
				mark := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				payload := waitLog(t, socket, mark, 2, "Received ping")
				if payload.LevelName != "INFO" {
					t.Fatalf("log levelName = %q, want INFO", payload.LevelName)
				}
				events := countEvents(socket, mark, "log")
				if len(events) != 1 {
					t.Fatalf("one ping produced %d log events, want 1", len(events))
				}
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "error", State: "failure", Assertion: "log-non-admin-subscription-rejected",
			Sources: []string{"server/SocketAuthority.js:201-208", "server/SocketAuthority.js:89-95"},
			Claim:   "Ordinary-user and unauthenticated sockets cannot subscribe to logs: their set_log_listener attempts produce no log events for them.",
			Run: func(t *testing.T, e *harness.Env) {
				ordinary := e.Socket(t, e.Token(t, "listener"))
				anonymous := e.Socket(t, "")
				ordinary.Emit(t, "set_log_listener", 2)
				anonymous.Emit(t, "set_log_listener", 2)
				ordinaryMark, anonymousMark := ordinary.Mark(), anonymous.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				ordinary.None(t, ordinaryMark, "log", 500*time.Millisecond, nil)
				anonymous.None(t, anonymousMark, "log", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "edge", State: "repeat", Assertion: "log-level-threshold",
			Sources: []string{"server/Logger.js:72-90", "client/pages/config/log.vue:100-130"},
			Claim:   "A WARN subscription filters out an INFO log, while an INFO subscription receives both that INFO log and a later WARN log.",
			Run: func(t *testing.T, e *harness.Env) {
				warnSocket := e.Socket(t, root(t, e))
				infoSocket := e.Socket(t, root(t, e))
				warnSocket.Emit(t, "set_log_listener", 3)
				infoSocket.Emit(t, "set_log_listener", 2)
				warnMark, infoMark := warnSocket.Mark(), infoSocket.Mark()

				checkPing(t, e.Get(t, "", "/ping"))
				waitLog(t, infoSocket, infoMark, 2, "Received ping")
				warnSocket.None(t, warnMark, "log", 500*time.Millisecond, levelMatch(2, ""))

				// An ordinary user's rejected subscription is logged at WARN.
				ordinary := e.Socket(t, e.Token(t, "listener"))
				ordinary.Emit(t, "set_log_listener", 2)
				waitLog(t, infoSocket, infoMark, 3, "Unauthorized set_log_listener")
				waitLog(t, warnSocket, warnMark, 3, "Unauthorized set_log_listener")
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "normal", State: "initial", Assertion: "log-no-events-before-subscription",
			Sources: []string{"server/SocketAuthority.js:201-208", "server/Logger.js:83-87"},
			Claim:   "An authenticated socket that has not sent set_log_listener receives no log event for later log lines.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				mark := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				checkPing(t, e.Get(t, "", "/ping"))
				socket.None(t, mark, "log", 500*time.Millisecond, nil)
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "normal", State: "repeat", Assertion: "log-resubscribe-single-delivery",
			Sources: []string{"server/Logger.js:44-58", "server/SocketAuthority.js:201-208"},
			Claim:   "Sending set_log_listener repeatedly replaces the subscription instead of duplicating it: two pings deliver exactly two INFO log events.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				for range 3 {
					socket.Emit(t, "set_log_listener", 2)
				}
				mark := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				waitLog(t, socket, mark, 2, "Received ping")
				second := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				waitLog(t, socket, second, 2, "Received ping")
				if got := len(countEvents(socket, mark, "log")); got != 2 {
					t.Fatalf("two pings delivered %d log events to a re-subscribed socket, want 2", got)
				}
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "edge", State: "changed", Assertion: "log-level-change-and-removal",
			Sources: []string{"server/Logger.js:44-58", "server/SocketAuthority.js:201-210", "client/pages/config/log.vue:120-185"},
			Claim:   "Raising a subscription from INFO to WARN stops INFO delivery, lowering it restores delivery, and remove_log_listener stops it entirely.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				socket.Emit(t, "set_log_listener", 2)
				first := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				waitLog(t, socket, first, 2, "Received ping")

				socket.Emit(t, "set_log_listener", 3)
				raised := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				socket.None(t, raised, "log", 500*time.Millisecond, levelMatch(2, ""))

				socket.Emit(t, "set_log_listener", 2)
				lowered := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				waitLog(t, socket, lowered, 2, "Received ping")

				socket.Emit(t, "remove_log_listener")
				removed := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				socket.None(t, removed, "log", 500*time.Millisecond, levelMatch(2, ""))
			},
		},
		{
			Requirement: "cap-event-log", Invariant: "INV-CAP-EVENT-LOG", Case: "error", State: "failure", Assertion: "log-invalid-level-not-subscribed",
			Sources: []string{"server/SocketAuthority.js:201-208", "server/utils/constants.js:19-27"},
			Claim:   "An admin socket sending an invalid set_log_listener level (unknown number, numeric string or fraction) is not subscribed and receives no log event.",
			Run: func(t *testing.T, e *harness.Env) {
				socket := e.Socket(t, root(t, e))
				socket.Emit(t, "set_log_listener", 7)
				socket.Emit(t, "set_log_listener", "2")
				socket.Emit(t, "set_log_listener", 0.5)
				mark := socket.Mark()
				checkPing(t, e.Get(t, "", "/ping"))
				socket.None(t, mark, "log", 500*time.Millisecond, nil)
			},
		},
	}
}
