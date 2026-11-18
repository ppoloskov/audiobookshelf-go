package realtime

// The Socket.IO log listeners: set_log_listener registers the emitting socket
// with a level at the Logger and remove_log_listener removes it again; the
// Logger then delivers every log call as a log event to that socket while the
// event level is at least the listener level.
//
// Deterministic offline triggers of the delivered log levels:
//   - INFO: a new Engine.IO connection (server/SocketAuthority.js:177);
//   - WARN: an invalid set_log_listener level (server/SocketAuthority.js:204-206)
//     and an unauthorized socket action (server/SocketAuthority.js:89-94).
//
// DEBUG and TRACE log calls never reach a listener in production: the server's
// own log-level gate drops them before delivery (server/Logger.js:111-112,
// NODE_ENV=production defaults to INFO), which the scenarios must not hide.
// Every set or removal is flushed (application ping answered by pong) before
// the next trigger, because a trigger socket connects on another connection
// and can otherwise be logged before the listener registration is processed.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// setLogListenerHandler dispatches set_log_listener.
	setLogListenerHandler = "server/SocketAuthority.js:201-209"
	// removeLogListenerHandler dispatches remove_log_listener.
	removeLogListenerHandler = "server/SocketAuthority.js:210"
	// logListenerRegistry is the listener registration of the Logger.
	logListenerRegistry = "server/Logger.js:44-63"
	// logDelivery is the log event delivery to the registered sockets.
	logDelivery = "server/Logger.js:72-93"
	// logGate is the server-side log-level gate that runs before delivery.
	logGate = "server/Logger.js:111-114"
	// logClient is the web client that sets the listener at the server settings
	// level and listens for log events to render them.
	logClient = "client/pages/config/log.vue:127,171,182"

	// The identifying message substrings of the log triggers. Only these
	// substrings identify the events; the full message text is not pinned.
	logTriggerInfo         = "Socket Connected to"
	logTriggerWarn         = "Invalid set_log_listener level"
	logTriggerUnauthorized = "Unauthorized set_log_listener socket event"
	logTriggerCancelScan   = "Unauthorized cancel_scan socket event"
)

// logEvent is the log event payload the web client renders
// (client/pages/config/log.vue).
type logEvent struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	LevelName string `json:"levelName"`
	Level     int    `json:"level"`
}

// logTimestampFormat is the format of the timestamp field
// (server/Logger.js:28-30).
var logTimestampFormat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$`)

// logSourceFormat is the basename:line the Logger writes into source
// (server/Logger.js:31-33).
var logSourceFormat = regexp.MustCompile(`^[^/\\]+\.js:\d+$`)

// waitLog waits for the next log event at index >= from whose message contains
// the substring, and checks the event shape.
func waitLog(t *testing.T, s *harness.Socket, from int, messageContains string) logEvent {
	t.Helper()
	event := s.Wait(t, from, "log", socketWait, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		return strings.Contains(decodeRaw[logEvent](t, args[0]).Message, messageContains)
	})
	payload := decodeArg[logEvent](t, event)
	if !logTimestampFormat.MatchString(payload.Timestamp) {
		t.Fatalf("log timestamp %q does not follow the server format", payload.Timestamp)
	}
	if !logSourceFormat.MatchString(payload.Source) {
		t.Fatalf("log source %q is not basename:line", payload.Source)
	}
	if payload.Message == "" {
		t.Fatal("the log message must not be empty")
	}
	return payload
}

// logsOf returns the log events at index >= from whose message contains the
// substring.
func logsOf(t *testing.T, s *harness.Socket, from int, messageContains string) []logEvent {
	t.Helper()
	var found []logEvent
	for i, event := range s.Events() {
		if i < from || event.Name != "log" || len(event.Args) == 0 {
			continue
		}
		if payload := decodeRaw[logEvent](t, event.Args[0]); strings.Contains(payload.Message, messageContains) {
			found = append(found, payload)
		}
	}
	return found
}

// waitLogCount waits until count log events with the message substring have
// arrived at index >= from and returns them.
func waitLogCount(t *testing.T, s *harness.Socket, from int, messageContains string, count int) []logEvent {
	t.Helper()
	var found []logEvent
	harness.Eventually(t, socketWait, fmt.Sprintf("%d log events containing %q", count, messageContains), func() bool {
		found = logsOf(t, s, from, messageContains)
		return len(found) >= count
	})
	return found[:count]
}

// requireNoLog requires that no log event with the message substring arrived
// at index >= from, waiting briefly so a late delivery is still caught.
func requireNoLog(t *testing.T, s *harness.Socket, from int, messageContains string) {
	t.Helper()
	s.None(t, from, "log", socketQuiet, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		return strings.Contains(decodeRaw[logEvent](t, args[0]).Message, messageContains)
	})
}

// triggerLogWarn makes the server log a WARN: an invalid set_log_listener
// level from the admin socket is always rejected with one. The call and its
// warning are ordered on the same connection.
func triggerLogWarn(t *testing.T, s *harness.Socket) {
	t.Helper()
	s.Emit(t, "set_log_listener", "not-a-level")
}

// triggerLogInfo makes the server log an INFO: a new Engine.IO connection is
// announced at INFO level. The caller must have flushed the listener socket
// first, or the connection can be logged before the registration is processed.
func triggerLogInfo(t *testing.T, e *harness.Env) {
	t.Helper()
	_ = connectAnon(t, e)
}

// requireListenerAtWARN proves the listener still filters at WARN level: an
// INFO trigger is not delivered while an unauthorized action's WARN trigger is.
// The offender is an authenticated non-admin socket whose cancel_scan logs the
// WARN.
func requireListenerAtWARN(t *testing.T, e *harness.Env, admin, offender *harness.Socket) {
	t.Helper()
	mark := admin.Mark()
	offender.Emit(t, "cancel_scan", "no-such-library")
	flush(t, offender)
	triggerLogInfo(t, e)
	requireNoLog(t, admin, mark, logTriggerInfo)
	waitLog(t, admin, mark, logTriggerCancelScan)
}

// setLogListenerScenarios covers socket-incoming-set-log-listener.
func setLogListenerScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-set-log-listener", Invariant: "INV-SOCKET-INCOMING-SET-LOG-LISTENER", Case: "normal", State: "initial", Assertion: "log-listener-delivers-matching-levels",
			Sources: []string{setLogListenerHandler, logListenerRegistry, logDelivery, logClient},
			Claim:   "An admin socket that sets the listener receives the server's later INFO and WARN log calls as log events with the timestamp, source, message, levelName and level fields, once per trigger.",
			Run: func(t *testing.T, e *harness.Env) {
				socket, _ := connectAuth(t, e, e.Token(t, "root"))
				socket.Emit(t, "set_log_listener", 1) // LogLevel.DEBUG
				flush(t, socket)
				mark := socket.Mark()

				triggerLogInfo(t, e)
				if event := waitLog(t, socket, mark, logTriggerInfo); event.LevelName != "INFO" || event.Level != 2 || !logSourceFileIs(event.Source, "SocketAuthority.js") {
					t.Fatalf("info log event: %+v", event)
				}
				triggerLogWarn(t, socket)
				if event := waitLog(t, socket, mark, logTriggerWarn); event.LevelName != "WARN" || event.Level != 3 || !logSourceFileIs(event.Source, "SocketAuthority.js") {
					t.Fatalf("warn log event: %+v", event)
				}

				// Each trigger is delivered once.
				for _, trigger := range []string{logTriggerInfo, logTriggerWarn} {
					if got := len(logsOf(t, socket, mark, trigger)); got != 1 {
						t.Fatalf("want exactly one delivery of %q, got %d", trigger, got)
					}
				}
			},
		},
		{
			Requirement: "socket-incoming-set-log-listener", Invariant: "INV-SOCKET-INCOMING-SET-LOG-LISTENER", Case: "error", State: "failure", Assertion: "log-listener-admin-gated",
			Sources: []string{setLogListenerHandler, logListenerRegistry, logDelivery},
			Claim:   "A set_log_listener from a non-admin or unauthenticated socket is rejected: the previous listener keeps its level, the rejected calls themselves are delivered as the WARN events they log, and no INFO arrives at a WARN-level listener.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 3) // LogLevel.WARN
				flush(t, admin)
				mark := admin.Mark()

				listener, _ := connectAuth(t, e, e.Token(t, "listener"))
				anon := connectAnon(t, e)
				listener.Emit(t, "set_log_listener", 0)
				anon.Emit(t, "set_log_listener", 0)
				// Both rejected calls log their own warning.
				if got := len(waitLogCount(t, admin, mark, logTriggerUnauthorized, 2)); got != 2 {
					t.Fatalf("want the two rejection warnings, got %d", got)
				}
				// The listener is still at WARN: a new connection logs no INFO and
				// an unauthorized action still logs its warning to the listener.
				requireListenerAtWARN(t, e, admin, listener)

				// And the next rejected call is still delivered.
				listener.Emit(t, "set_log_listener", 6)
				waitLog(t, admin, mark, logTriggerUnauthorized)
				requireListenerAtWARN(t, e, admin, listener)
			},
		},
		{
			Requirement: "socket-incoming-set-log-listener", Invariant: "INV-SOCKET-INCOMING-SET-LOG-LISTENER", Case: "edge", State: "changed", Assertion: "log-listener-replaces-level",
			Sources: []string{setLogListenerHandler, logListenerRegistry, logDelivery, logGate},
			Claim:   "A second set_log_listener on the same socket replaces the level instead of adding a listener: at WARN no INFO arrives, after lowering to INFO it does; invalid levels (not an integer, a string, out of range, absent) are rejected with their own warning and replace nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				listener, _ := connectAuth(t, e, e.Token(t, "listener"))
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 3) // LogLevel.WARN
				flush(t, admin)
				mark := admin.Mark()

				requireListenerAtWARN(t, e, admin, listener)

				admin.Emit(t, "set_log_listener", 2) // LogLevel.INFO
				flush(t, admin)
				triggerLogInfo(t, e)
				waitLog(t, admin, mark, logTriggerInfo)

				// Every invalid level is rejected: the rejection itself is logged
				// at WARN (and therefore delivered at level 2), and the listener
				// still filters like level 2 afterwards.
				for _, bad := range []any{"2", 2.5, 9, nil} {
					invalidMark := admin.Mark()
					admin.Emit(t, "set_log_listener", bad)
					waitLog(t, admin, invalidMark, logTriggerWarn)
					infoMark := admin.Mark()
					triggerLogInfo(t, e)
					waitLog(t, admin, infoMark, logTriggerInfo)
				}
			},
		},
		{
			Requirement: "socket-incoming-set-log-listener", Invariant: "INV-SOCKET-INCOMING-SET-LOG-LISTENER", Case: "edge", State: "repeat", Assertion: "log-listener-set-twice-no-duplicates",
			Sources: []string{setLogListenerHandler, logListenerRegistry, logDelivery},
			Claim:   "Setting the same level twice registers one listener: an INFO trigger is delivered exactly once, and a second admin socket without a listener receives no log events.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				other, _ := connectAuth(t, e, e.Token(t, "finisher"))
				admin.Emit(t, "set_log_listener", 2)
				admin.Emit(t, "set_log_listener", 2)
				flush(t, admin)
				adminMark := admin.Mark()
				otherMark := other.Mark()

				triggerLogInfo(t, e)
				waitLog(t, admin, adminMark, logTriggerInfo)
				if got := len(logsOf(t, admin, adminMark, logTriggerInfo)); got != 1 {
					t.Fatalf("want exactly one INFO delivery, got %d", got)
				}
				// The log event is socket-local: the other admin socket without a
				// listener receives none.
				flush(t, other)
				if got := countEvents(other, otherMark, "log"); got != 0 {
					t.Fatalf("a socket without a listener must not receive log events, got %d", got)
				}
			},
		},
	}
}

// logSourceFileIs reports whether the source field names that file.
func logSourceFileIs(source, file string) bool {
	return strings.HasPrefix(source, file+":")
}

// removeLogListenerScenarios covers socket-incoming-remove-log-listener.
func removeLogListenerScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-incoming-remove-log-listener", Invariant: "INV-SOCKET-INCOMING-REMOVE-LOG-LISTENER", Case: "normal", State: "initial", Assertion: "log-listener-removal-stops-delivery",
			Sources: []string{removeLogListenerHandler, logListenerRegistry, logClient},
			Claim:   "remove_log_listener stops the deliveries on that socket: a WARN trigger that arrived before removal arrives no longer after it, and the removal answers nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 3)
				flush(t, admin)
				mark := admin.Mark()
				triggerLogWarn(t, admin)
				waitLog(t, admin, mark, logTriggerWarn)

				admin.Emit(t, "remove_log_listener")
				flush(t, admin)
				if got := len(logsOf(t, admin, mark, logTriggerWarn)); got != 1 {
					t.Fatalf("the removal must answer nothing, got %d log events", got)
				}
				afterRemoval := admin.Mark()
				triggerLogWarn(t, admin)
				flush(t, admin)
				requireNoLog(t, admin, afterRemoval, logTriggerWarn)
			},
		},
		{
			Requirement: "socket-incoming-remove-log-listener", Invariant: "INV-SOCKET-INCOMING-REMOVE-LOG-LISTENER", Case: "error", State: "failure", Assertion: "log-listener-removal-is-not-gated",
			Sources: []string{removeLogListenerHandler, logListenerRegistry},
			Claim:   "remove_log_listener is not behind the admin gate: an unauthenticated and a non-admin socket can call it, which answers nothing and leaves another socket's listener untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 3)
				flush(t, admin)

				anon := connectAnon(t, e)
				anonMark := anon.Mark()
				listener, _ := connectAuth(t, e, e.Token(t, "listener"))
				listenerMark := listener.Mark()
				anon.Emit(t, "remove_log_listener")
				listener.Emit(t, "remove_log_listener")
				flush(t, anon)
				flush(t, listener)
				if got := countEvents(anon, anonMark, "log") + countEvents(listener, listenerMark, "log"); got != 0 {
					t.Fatalf("the removals must answer nothing, got %d log events", got)
				}
				// The admin listener is untouched: an unauthorized action's WARN
				// is still delivered and an INFO trigger is not.
				requireListenerAtWARN(t, e, admin, listener)
			},
		},
		{
			Requirement: "socket-incoming-remove-log-listener", Invariant: "INV-SOCKET-INCOMING-REMOVE-LOG-LISTENER", Case: "edge", State: "changed", Assertion: "log-listener-removed-then-reset",
			Sources: []string{removeLogListenerHandler, logListenerRegistry, setLogListenerHandler},
			Claim:   "After a removal the same socket can register a listener again: an INFO trigger that was filtered after removal is delivered once the listener is set again.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 2)
				flush(t, admin)
				mark := admin.Mark()
				triggerLogInfo(t, e)
				waitLog(t, admin, mark, logTriggerInfo)

				admin.Emit(t, "remove_log_listener")
				flush(t, admin)
				afterRemoval := admin.Mark()
				triggerLogInfo(t, e)
				requireNoLog(t, admin, afterRemoval, logTriggerInfo)

				admin.Emit(t, "set_log_listener", 2)
				flush(t, admin)
				afterReset := admin.Mark()
				triggerLogInfo(t, e)
				waitLog(t, admin, afterReset, logTriggerInfo)
			},
		},
		{
			Requirement: "socket-incoming-remove-log-listener", Invariant: "INV-SOCKET-INCOMING-REMOVE-LOG-LISTENER", Case: "edge", State: "repeat", Assertion: "log-listener-double-remove-still-removed",
			Sources: []string{removeLogListenerHandler, logListenerRegistry},
			Claim:   "Removing a listener twice is idempotent: no error and no delivery after the second removal.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				admin.Emit(t, "set_log_listener", 3)
				flush(t, admin)
				mark := admin.Mark()
				triggerLogWarn(t, admin)
				waitLog(t, admin, mark, logTriggerWarn)

				admin.Emit(t, "remove_log_listener")
				admin.Emit(t, "remove_log_listener")
				flush(t, admin)
				if got := len(logsOf(t, admin, mark, logTriggerWarn)); got != 1 {
					t.Fatalf("the removals must answer nothing, got %d log events", got)
				}
				afterRemoval := admin.Mark()
				triggerLogWarn(t, admin)
				flush(t, admin)
				requireNoLog(t, admin, afterRemoval, logTriggerWarn)
			},
		},
	}
}
