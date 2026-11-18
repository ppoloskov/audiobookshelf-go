package realtime

// The Socket.IO/Engine.IO transport contract: the engine endpoint exists on
// the configured-prefixed path (/audiobookshelf/socket.io, opened for the web
// client and the mobile apps) and on the legacy path (/socket.io, opened as a
// second Socket.IO server whenever ROUTER_BASE_PATH is set), both servers
// share the client registry, the heartbeat follows the engine.io defaults the
// open packet advertises, and a server shutdown closes every connection.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// socketIOServers are the two Socket.IO servers of SocketAuthority.
	socketIOServers = "server/SocketAuthority.js:157-171,178-183"
	// socketIOOptions are the options upstream passes: only CORS, so the
	// heartbeat and buffer values are the engine.io defaults.
	socketIOOptions = "server/SocketAuthority.js:158-165"
	// socketIOClose is the shutdown path that closes every client connection.
	socketIOClose = "server/SocketAuthority.js:147-185"
	// sioClient is the web client connection: websocket transport only, no
	// upgrade, on the configured-prefixed path.
	sioClient = "client/layouts/default.vue:389-396"
	// legacyPathComment is the upstream comment: the legacy path stays open for
	// legacy clients.
	legacyPathComment = "server/SocketAuthority.js:164-167"
	// sioAuthDispatch is the auth handler every socket gets, on either server.
	sioAuthDispatch = "server/SocketAuthority.js:187-188,269-361"
)

// heartbeatProbeTimeout bounds one heartbeat wait; the engine.io defaults are
// 25 s ping interval and 20 s ping timeout (node_modules/engine.io/build/server.js:38-43).
const heartbeatProbeTimeout = 40 * time.Second

// socketioScenarios covers sub-realtime-socketio: the transport-level
// contract on both engine paths.
func socketioScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "sub-realtime-socketio", Invariant: "INV-SUB-REALTIME-SOCKETIO", Case: "normal", State: "initial", Assertion: "socketio-legacy-path-handshake-and-auth",
			Sources: []string{socketIOServers, socketIOOptions, legacyPathComment, sioAuthDispatch},
			Claim:   "The legacy /socket.io path is a second engine endpoint with the same open-packet values as the prefixed path (engine.io defaults), completes the namespace connect with a sid and authenticates: the admin sees the user_online and the user is listed online.",
			Run: func(t *testing.T, e *harness.Env) {
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()

				legacy := dialEngineIO(t, e, "/socket.io/")
				open := legacy.OpenPacket()
				if !strings.HasPrefix(open, "0") {
					t.Fatalf("open packet: %q", open)
				}
				handshake := legacy.Handshake()
				if handshake.SID == "" || handshake.PingInterval != 25000 || handshake.PingTimeout != 20000 || handshake.MaxPayload != 1000000 {
					t.Fatalf("legacy open packet: %+v", handshake)
				}
				// The connection opened straight on the websocket transport, so no
				// further upgrade is offered.
				if len(handshake.Upgrades) != 0 {
					t.Fatalf("legacy upgrades for a direct websocket connection: %v", handshake.Upgrades)
				}
				ack := legacy.NamespaceConnect(t)
				if sid, _ := ack["sid"].(string); sid == "" {
					t.Fatalf("namespace connect ack: %v", ack)
				}

				prefixed := dialEngineIO(t, e, "/audiobookshelf/socket.io/")
				if prefixedHandshake := prefixed.Handshake(); prefixedHandshake.PingInterval != handshake.PingInterval ||
					prefixedHandshake.PingTimeout != handshake.PingTimeout || prefixedHandshake.MaxPayload != handshake.MaxPayload {
					t.Fatalf("prefixed open packet differs: %+v vs %+v", prefixedHandshake, handshake)
				}

				listenerID := e.IDs["listener"]
				mark := legacy.Mark()
				legacy.EmitApp(t, "auth", e.Token(t, "listener"))
				parsed := waitLegacyEvent(t, legacy, mark, "init")
				var payload initPayload
				if err := json.Unmarshal(parsed.Args[0], &payload); err != nil {
					t.Fatalf("init payload: %v", err)
				}
				if payload.UserID != listenerID || payload.Username != "trial-listener" {
					t.Fatalf("init: want the listener, got %+v", payload)
				}
				waitUserEvent(t, admin, adminMark, "user_online", listenerID)
				entry := onlineEntry(t, e, listenerID)
				if entry == nil || entry.Connections != 1 {
					t.Fatalf("the legacy connection must be listed online once: %+v", entry)
				}
				if got := countLegacyEvents(legacy, mark, "init"); got != 1 {
					t.Fatalf("want exactly one init on the legacy path, got %d", got)
				}
			},
		},
		{
			Requirement: "sub-realtime-socketio", Invariant: "INV-SUB-REALTIME-SOCKETIO", Case: "normal", State: "changed", Assertion: "socketio-shared-state-across-paths",
			Sources: []string{socketIOServers, legacyPathComment, sioClient},
			Claim:   "Both engine paths serve one client registry: a connection authenticated on the legacy path is listed online and receives the admin_message that an admin on the prefixed path broadcasts, while an anonymous legacy connection receives none.",
			Run: func(t *testing.T, e *harness.Env) {
				root, _ := connectAuth(t, e, e.Token(t, "root"))
				rootMark := root.Mark()
				listener := dialEngineIO(t, e, "/socket.io/")
				listener.NamespaceConnect(t)
				listener.EmitApp(t, "auth", e.Token(t, "listener"))
				listenerMark := listener.Mark()
				waitLegacyEvent(t, listener, listenerMark, "init")

				anon := dialEngineIO(t, e, "/socket.io/")
				anon.NamespaceConnect(t)
				anonMark := anon.Mark()

				root.Emit(t, "message_all_users", map[string]any{"message": "across the paths"})
				root.Emit(t, "ping")
				root.Wait(t, rootMark, "pong", socketWait, nil) // barrier: the broadcast is done
				// Barrier the two raw connections with their application pings.
				listener.EmitApp(t, "ping")
				waitLegacyEvent(t, listener, listenerMark, "pong")
				anon.EmitApp(t, "ping")
				waitLegacyEvent(t, anon, anonMark, "pong")

				parsed := waitLegacyEvent(t, listener, listenerMark, "admin_message")
				var message string
				if err := json.Unmarshal(parsed.Args[0], &message); err != nil {
					t.Fatalf("admin_message payload: %v", err)
				}
				if message != "across the paths" {
					t.Fatalf("admin_message on the legacy socket: %q", message)
				}
				if got := countLegacyEvents(anon, anonMark, "admin_message"); got != 0 {
					t.Fatalf("an anonymous connection must not receive admin_message, got %d", got)
				}
				entry := onlineEntry(t, e, e.IDs["listener"])
				if entry == nil || entry.Connections != 1 {
					t.Fatalf("the legacy connection must be listed online once: %+v", entry)
				}
			},
		},
		{
			Requirement: "sub-realtime-socketio", Invariant: "INV-SUB-REALTIME-SOCKETIO", Case: "edge", State: "repeat", Assertion: "socketio-heartbeat-cycle",
			Sources: []string{socketIOOptions},
			Claim:   "On a namespace-connected connection the server pings at the advertised interval and accepts the pong (the next ping follows, because an unanswered ping closes the connection within the ping timeout first); once the client stops answering, the server closes the connection.",
			Run: func(t *testing.T, e *harness.Env) {
				conn := dialEngineIO(t, e, "/socket.io/")
				conn.NamespaceConnect(t) // a client that never joins the namespace is closed by socket.io's connect timeout instead
				first, _ := conn.WaitFrameFrom(t, 0, "2", heartbeatProbeTimeout)
				// The first ping is answered automatically; the next ping proves
				// the server accepted the pong, because an unanswered ping closes
				// the connection after the ping timeout, before the next interval
				// would elapse.
				_, second := conn.WaitFrameFrom(t, first+1, "2", heartbeatProbeTimeout)

				// From here the client stays silent: the automatic pong for the
				// second ping may still have been written, so the server closes
				// the connection at the latest one interval plus ping timeout
				// after the last answered ping.
				conn.setPonging(false)
				conn.WaitClosed(t, heartbeatProbeTimeout+20*time.Second)
				if !strings.Contains(strings.Join(conn.Frames(), " | "), second) {
					t.Fatalf("the second ping is missing from the frames: %s", strings.Join(conn.Frames(), " | "))
				}
			},
		},
		{
			Requirement: "sub-realtime-socketio", Invariant: "INV-SUB-REALTIME-SOCKETIO", Case: "error", State: "failure", Assertion: "socketio-legacy-rejection-leaves-no-state",
			Sources: []string{socketIOServers, sioAuthDispatch},
			Claim:   "On the legacy path a rejected auth answers auth_failed with Invalid token and no init, writes no users.lastSeen, announces no user_online, and the connection's disconnect announces no user_offline.",
			Run: func(t *testing.T, e *harness.Env) {
				listenerID := e.IDs["listener"]
				before := userRow(t, e, listenerID)
				admin, _ := connectAuth(t, e, e.Token(t, "root"))
				adminMark := admin.Mark()

				conn := dialEngineIO(t, e, "/socket.io/")
				conn.NamespaceConnect(t)
				mark := conn.Mark()
				conn.EmitApp(t, "auth", "not-a-token")
				parsed := waitLegacyEvent(t, conn, mark, "auth_failed")
				var answer map[string]any
				if err := json.Unmarshal(parsed.Args[0], &answer); err != nil || answer["message"] != "Invalid token" {
					t.Fatalf("auth_failed: %v (%v)", parsed.Args, err)
				}
				flush(t, admin)
				requireLegacyQuiet(t, conn, mark, "init")
				if row := userRow(t, e, listenerID); row == nil || row["lastSeen"] != before["lastSeen"] {
					t.Fatalf("a rejected auth must not write lastSeen: %+v", row)
				}
				if got := countEvents(admin, adminMark, "user_online"); got != 0 {
					t.Fatalf("a rejected auth must not announce user_online, got %d", got)
				}
				conn.Close()
				admin.None(t, adminMark, "user_offline", socketQuiet, nil)
			},
		},
		{
			Requirement: "sub-realtime-socketio", Invariant: "INV-SUB-REALTIME-SOCKETIO", Case: "edge", State: "initial", Assertion: "socketio-shutdown-closes-clients",
			Sources: []string{socketIOClose},
			Claim:   "Shutting the server down closes every client connection on both engine paths.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				prefixed := dialEngineIO(t, own, "/audiobookshelf/socket.io/")
				legacy := dialEngineIO(t, own, "/socket.io/")
				for _, conn := range []*eioConn{prefixed, legacy} {
					conn.NamespaceConnect(t)
				}

				// Close the client sides as soon as each connection ends, so the
				// server's HTTP close handshake completes.
				for _, conn := range []*eioConn{prefixed, legacy} {
					go func(conn *eioConn) {
						<-conn.doneInternal()
						conn.Close()
					}(conn)
				}

				own.Stop(t)
				for _, conn := range []*eioConn{prefixed, legacy} {
					conn.WaitClosed(t, 15*time.Second)
				}
			},
		},
	}
}

// waitLegacyEvent waits for the next Socket.IO event frame with that name at
// index >= from and fails the test otherwise.
func waitLegacyEvent(t *testing.T, conn *eioConn, from int, name string) eioEvent {
	t.Helper()
	for {
		i, frame := conn.WaitFrameFrom(t, from, "42", socketWait)
		parsed, err := eventFromFrame(frame)
		if err != nil {
			t.Fatalf("event frame %q: %v", frame, err)
		}
		if parsed.Name == name {
			return parsed
		}
		from = i + 1
	}
}
