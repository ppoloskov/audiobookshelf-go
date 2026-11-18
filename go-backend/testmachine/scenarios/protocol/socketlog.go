package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// eventWait bounds one log event wait; quietWindow observes a socket that must
// not receive one.
const (
	eventWait   = 5 * time.Second
	quietWindow = 400 * time.Millisecond
)

// decodeLogEvent decodes the single object argument of a `log` event.
func decodeLogEvent(t *testing.T, event harness.Event) logRecord {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("log event has %d arguments: %s", len(event.Args), event.Args)
	}
	var record logRecord
	if err := json.Unmarshal(event.Args[0], &record); err != nil {
		t.Fatalf("log event argument is not a log object: %v: %.200s", err, event.Args[0])
	}
	return record
}

// pingLog waits for the INFO record of a /ping request.
func pingLog(t *testing.T, e *harness.Env, socket *harness.Socket) logRecord {
	t.Helper()
	mark := socket.Mark()
	e.Get(t, "", "/ping")
	return decodeLogEvent(t, socket.Wait(t, mark, "log", eventWait, func(args []json.RawMessage) bool {
		var record logRecord
		return len(args) == 1 && json.Unmarshal(args[0], &record) == nil && strings.Contains(record.Message, "Received ping")
	}))
}

// logEventScenarios covers the outgoing log socket event.
func logEventScenarios() []harness.Scenario {
	const req = "socket-outgoing-log"
	const inv = "INV-SOCKET-OUTGOING-LOG"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "admin-log-listener-receives-the-log-payload",
			Sources: []string{"server/Logger.js:73-115", "server/SocketAuthority.js:201-210"},
			Claim:   "A root socket that sends set_log_listener(2) receives a `log` event for a subsequent INFO server log, with one argument object carrying string timestamp, string source, the message and levelName INFO with numeric level 2; a second root socket that did not subscribe receives no log event.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				bare := e.Socket(t, e.Token(t, "root"))
				subscribe(t, root, 2)
				bareMark := bare.Mark()
				record := pingLog(t, e, root)
				if !strings.Contains(record.Message, "Received ping") {
					t.Fatalf("log message = %q", record.Message)
				}
				if record.LevelName != "INFO" || record.Level != logLevels["INFO"] {
					t.Fatalf("log level = %s/%v, want INFO/2", record.LevelName, record.Level)
				}
				if !logTimestamp.MatchString(record.Timestamp) || !logSource.MatchString(record.Source) {
					t.Fatalf("log timestamp/source = %q/%q", record.Timestamp, record.Source)
				}
				bare.None(t, bareMark, "log", quietWindow, nil)
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "log-level-filters-records-and-repeats-are-delivered",
			Sources: []string{"server/Logger.js:73-93", "server/SocketAuthority.js:201-210", "server/utils/constants.js:19-27"},
			Claim:   "A socket subscribed at level 3 (WARN) receives no INFO ping event, then receives the WARN event produced by an invalid set_log_listener level, with levelName WARN and level 3; resubscribing at level 0 delivers two successive INFO ping events.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				subscribe(t, root, logLevels["WARN"])
				quiet := root.Mark()
				e.Get(t, "", "/ping")
				root.None(t, quiet, "log", quietWindow, nil)

				mark := root.Mark()
				root.Emit(t, "set_log_listener", 99)
				warn := decodeLogEvent(t, root.Wait(t, mark, "log", eventWait, func(args []json.RawMessage) bool {
					var record logRecord
					return len(args) == 1 && json.Unmarshal(args[0], &record) == nil && record.LevelName == "WARN"
				}))
				if warn.Level != logLevels["WARN"] {
					t.Fatalf("WARN record level = %v", warn.Level)
				}
				subscribe(t, root, logLevels["TRACE"])
				for i := range 2 {
					record := pingLog(t, e, root)
					if record.Message != "Received ping" && !strings.Contains(record.Message, "Received ping") {
						t.Fatalf("repeat %d message = %q", i, record.Message)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "non-admin-and-invalid-level-never-subscribe",
			Sources: []string{"server/SocketAuthority.js:89-95,201-210"},
			Claim:   "A listener (non-admin) socket that sends set_log_listener(0) receives no log events, and a root socket that sends an invalid level 99 receives neither the WARN it caused nor later INFO ping events.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Socket(t, e.Token(t, "listener"))
				listenerMark := listener.Mark()
				listener.Emit(t, "set_log_listener", 0)
				listener.None(t, listenerMark, "log", quietWindow, nil)
				ping := listener.Mark()
				e.Get(t, "", "/ping")
				listener.None(t, ping, "log", quietWindow, nil)

				root := e.Socket(t, e.Token(t, "root"))
				rootMark := root.Mark()
				root.Emit(t, "set_log_listener", 99)
				root.None(t, rootMark, "log", quietWindow, nil)
				ping = root.Mark()
				e.Get(t, "", "/ping")
				root.None(t, ping, "log", quietWindow, nil)
			},
		},
		{
			Requirement: req, Invariant: "INV-SOCKET-OUTGOING-LOG-SUBSCRIPTION-001", Case: "normal", State: "changed", Assertion: "remove-log-listener-stops-delivery",
			Sources: []string{"server/SocketAuthority.js:201-210", "server/Logger.js:52-69"},
			Claim:   "After remove_log_listener the same socket that received an earlier ping log receives no further log event for a later ping.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Socket(t, e.Token(t, "root"))
				subscribe(t, root, logLevels["TRACE"])
				before := pingLog(t, e, root)
				if !strings.Contains(before.Message, "Received ping") {
					t.Fatalf("first event message = %q", before.Message)
				}
				root.Emit(t, "remove_log_listener")
				root.None(t, root.Mark(), "log", quietWindow, nil)
				mark := root.Mark()
				e.Get(t, "", "/ping")
				root.None(t, mark, "log", quietWindow, nil)
			},
		},
	}
}

// subscribe sends set_log_listener and gives the server a window to register it.
func subscribe(t *testing.T, socket *harness.Socket, level float64) {
	t.Helper()
	mark := socket.Mark()
	socket.Emit(t, "set_log_listener", level)
	socket.None(t, mark, "log", quietWindow, nil)
}
