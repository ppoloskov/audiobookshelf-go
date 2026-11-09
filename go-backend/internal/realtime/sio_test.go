package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"

	"golang.org/x/net/websocket"
)

// sioServer serves a Hub with the application handlers over a real listener.
// Disconnects are observed through the Backend (Dispatch "remove_log_listener",
// which the hub sends for every closed socket) and their reasons through the
// [SocketAuthority] log entries, as the log stream delivers them.
type sioServer struct {
	hub         *Hub
	url         string
	backend     *fakeBackend
	disconnects chan string // socket id of every closed socket, in order
	actions     chan Action // other dispatched actions
	reasons     chan logged
}

type logged struct{ id, reason string }

var reasonRe = regexp.MustCompile(`[Ss]ocket (\S+) disconnect.*\(Reason: (.*)\)`)

func testServer(t *testing.T) *sioServer {
	t.Helper()
	db, _ := testDB(t)
	srv := &sioServer{disconnects: make(chan string, 64), actions: make(chan Action, 64), reasons: make(chan logged, 64)}
	srv.backend = &fakeBackend{db: db, auth: func(r *http.Request) (store.User, error) {
		return userForToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}}
	srv.backend.dispatch = func(_ context.Context, a Action) error {
		if a.Event == "remove_log_listener" {
			srv.disconnects <- a.SocketID
		} else {
			srv.actions <- a
		}
		return nil
	}
	srv.hub = newTestHub(config.Config{RouterBasePath: "/abs"}, srv.backend)
	logging.Default().BindSockets(func(_ string, e logging.LogEntry) {
		if m := reasonRe.FindStringSubmatch(e.Message); m != nil {
			srv.reasons <- logged{m[1], m[2]}
		}
	})
	logging.Default().AddSocketListener("sio-test", logging.LevelInfo)
	server := httptest.NewServer(srv.hub)
	t.Cleanup(func() {
		server.Close()
		srv.hub.Close()
		logging.Default().RemoveSocketListener("sio-test")
		logging.Default().BindSockets(nil)
	})
	srv.url = "ws" + strings.TrimPrefix(server.URL, "http") + "/socket.io/?EIO=4&transport=websocket"
	return srv
}

// reasonFor waits for the disconnect log of the socket and returns its reason.
func (s *sioServer) reasonFor(t *testing.T, id string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case l := <-s.reasons:
			if l.id == id {
				return l.reason
			}
		case <-deadline:
			t.Fatalf("no disconnect logged for socket %s", id)
		}
	}
}

func (s *sioServer) waitDisconnect(t *testing.T, id string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-s.disconnects:
			if got == id {
				return
			}
		case <-deadline:
			t.Fatalf("socket %s was never disconnected on the server", id)
		}
	}
}

// connect opens the Engine.IO connection and joins the default namespace; it
// returns the socket id from the connect acknowledgement.
func connect(t *testing.T, url string) (*websocket.Conn, string) {
	t.Helper()
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	ack := recv(t, ws)
	var info struct{ Sid string }
	if !strings.HasPrefix(ack, "40") || json.Unmarshal([]byte(ack[2:]), &info) != nil || info.Sid == "" {
		t.Fatalf("connect ack = %q", ack)
	}
	return ws, info.Sid
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ws, err := websocket.Dial(url, "", "http://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func recv(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg string
	if err := websocket.Message.Receive(ws, &msg); err != nil {
		t.Fatalf("receive: %v", err)
	}
	return msg
}

func send(t *testing.T, ws *websocket.Conn, msg string) {
	t.Helper()
	if err := websocket.Message.Send(ws, msg); err != nil {
		t.Fatal(err)
	}
}

// expectClosed reads until the server closes the connection.
func expectClosed(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("connection still open")
			}
			return
		}
	}
}

func open(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	msg := recv(t, ws)
	if !strings.HasPrefix(msg, "0{") {
		t.Fatalf("open packet = %q", msg)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(msg[1:]), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func userForToken(token string) (store.User, error) {
	byToken := map[string]store.User{
		"tok-alice":    {ID: "user-1", Username: "alice", Type: "user", IsActive: true, Permissions: []byte(`{"librariesAccessible":["lib-1"],"itemTagsSelected":["mystery"]}`)},
		"tok-bob":      {ID: "limited-1", Username: "bob", Type: "user", IsActive: true, Permissions: []byte(`{"librariesAccessible":["lib-2"],"itemTagsSelected":["mystery"]}`)},
		"tok-admin":    {ID: "admin-1", Username: "root", Type: "admin", IsActive: true, Permissions: []byte(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)},
		"tok-inactive": {ID: "user-1", Username: "alice", Type: "user", IsActive: false},
	}
	if u, ok := byToken[token]; ok {
		return u, nil
	}
	return store.User{}, errors.New("invalid token")
}

type event struct {
	name    string
	payload json.RawMessage
}

// next reads the next application event, answering Engine.IO pings.
func next(t *testing.T, ws *websocket.Conn) event {
	t.Helper()
	for {
		msg := recv(t, ws)
		switch {
		case msg == "2":
			send(t, ws, "3")
		case strings.HasPrefix(msg, "42"):
			var parts []json.RawMessage
			if err := json.Unmarshal([]byte(msg[2:]), &parts); err != nil || len(parts) == 0 {
				t.Fatalf("event frame %q: %v", msg, err)
			}
			var e event
			_ = json.Unmarshal(parts[0], &e.name)
			if len(parts) > 1 {
				e.payload = parts[1]
			}
			return e
		}
	}
}

// login authenticates the socket and returns its init payload.
func login(t *testing.T, ws *websocket.Conn, token string) map[string]any {
	t.Helper()
	send(t, ws, `42["auth","`+token+`"]`)
	e := next(t, ws)
	if e.name == "user_online" { // an admin is told about its own login first
		e = next(t, ws)
	}
	if e.name != "init" {
		t.Fatalf("after auth got %s %s, want init", e.name, e.payload)
	}
	var init map[string]any
	_ = json.Unmarshal(e.payload, &init)
	return init
}

func TestOpenPacketAndSocketConnectAck(t *testing.T) {
	srv := testServer(t)
	ws := dial(t, srv.url)
	info := open(t, ws)
	if sid, _ := info["sid"].(string); len(sid) != 20 {
		t.Fatalf("sid = %v", info["sid"])
	}
	if info["pingInterval"] != float64(25000) || info["pingTimeout"] != float64(20000) || info["maxPayload"] != float64(1000000) {
		t.Fatalf("open = %v", info)
	}
	send(t, ws, `40{"token":"ignored"}`)
	ack := recv(t, ws)
	if !strings.HasPrefix(ack, `40{"sid":"`) {
		t.Fatalf("connect ack = %q", ack)
	}
}

// Packet grammar (socket.io-parser v4): a namespace, an optional ack id and a
// JSON array; anything else, and binary packets, end the connection.
func TestPacketGrammar(t *testing.T) {
	srv := testServer(t)
	// Ping handler answers whatever namespace/ack framing a client uses for a plain event.
	ws, _ := connect(t, srv.url)
	send(t, ws, `42["ping"]`)
	if got := recv(t, ws); got != `42["pong"]` {
		t.Fatalf("pong frame = %q", got)
	}
	for _, bad := range []string{"4", "4x", `42notjson`, `451-["a",{"_placeholder":true,"num":0}]`, `461-0["a"]`} {
		ws, _ := connect(t, srv.url)
		send(t, ws, bad)
		expectClosed(t, ws)
	}
	// An unknown namespace is refused but the connection stays usable.
	ws, _ = connect(t, srv.url)
	send(t, ws, "40/chat,")
	if got := recv(t, ws); got != `44/chat,{"message":"Invalid namespace"}` {
		t.Fatalf("got %q", got)
	}
}

func TestHandlersAreAttachedBeforeConnectAck(t *testing.T) {
	srv := testServer(t)
	ws := dial(t, srv.url)
	open(t, ws)
	// CONNECT and the first event in one burst, as a fast client does.
	send(t, ws, "40")
	send(t, ws, `42["ping"]`)
	if got := recv(t, ws); !strings.HasPrefix(got, "40{") {
		t.Fatalf("connect ack = %q", got)
	}
	if got := recv(t, ws); got != `42["pong"]` {
		t.Fatalf("pong = %q", got)
	}
}

func TestInvalidNamespace(t *testing.T) {
	srv := testServer(t)
	ws := dial(t, srv.url)
	open(t, ws)
	send(t, ws, "40/admin,")
	if got := recv(t, ws); got != `44/admin,{"message":"Invalid namespace"}` {
		t.Fatalf("got %q", got)
	}
}

// An event with an ack id is answered with a 43 packet carrying the handler's
// arguments; one without gets no ack function.
func TestAckRoundTrip(t *testing.T) {
	srv := testServer(t)
	srv.backend.dispatch = func(_ context.Context, a Action) error {
		if a.Event == "cancel_scan" {
			if a.Ack == nil {
				srv.actions <- a
				return nil
			}
			a.Ack("x", map[string]any{"n": 1})
		}
		return nil
	}
	ws, _ := connect(t, srv.url)
	login(t, ws, "tok-admin")
	send(t, ws, `4212["cancel_scan","lib-1"]`)
	if got := recv(t, ws); got != `4312["x",{"n":1}]` {
		t.Fatalf("ack = %q", got)
	}
	send(t, ws, `42["cancel_scan","lib-1"]`)
	select {
	case a := <-srv.actions:
		if a.Ack != nil {
			t.Fatal("an event without an ack id got an ack function")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel_scan without ack id was not dispatched")
	}
}

func TestEventBeforeConnectClosesConnection(t *testing.T) {
	srv := testServer(t)
	ws := dial(t, srv.url)
	open(t, ws)
	send(t, ws, `42["auth","t"]`)
	expectClosed(t, ws)
}

func TestBinaryPacketClosesConnection(t *testing.T) {
	srv := testServer(t)
	ws, id := connect(t, srv.url)
	send(t, ws, `451-["up",{"_placeholder":true,"num":0}]`)
	expectClosed(t, ws)
	srv.waitDisconnect(t, id)
}

// Every way a socket ends runs the hub's disconnect once, with the reason
// socket.io reports.
func TestDisconnectReasons(t *testing.T) {
	srv := testServer(t)

	ws, id := connect(t, srv.url)
	send(t, ws, "41")
	srv.waitDisconnect(t, id)
	if got := srv.reasonFor(t, id); got != "client namespace disconnect" {
		t.Fatalf("client disconnect reason = %q", got)
	}

	ws, id = connect(t, srv.url)
	_ = ws.Close()
	srv.waitDisconnect(t, id)
	if got := srv.reasonFor(t, id); got != "transport close" {
		t.Fatalf("websocket close reason = %q", got)
	}
}

// A user whose permissions are revoked is disconnected by the server: the
// client gets the disconnect packet, then the transport closes.
func TestServerDisconnectSendsPacketThenCloses(t *testing.T) {
	srv := testServer(t)
	ws, id := connect(t, srv.url)
	login(t, ws, "tok-alice")
	if _, err := srv.backend.db.Exec(`UPDATE users SET isActive=0 WHERE id='user-1'`); err != nil {
		t.Fatal(err)
	}
	if err := srv.hub.Refresh(context.Background(), "user-1"); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, ws); got != "41" {
		t.Fatalf("got %q, want 41", got)
	}
	expectClosed(t, ws)
	srv.waitDisconnect(t, id)
	if got := srv.reasonFor(t, id); got != "server namespace disconnect" {
		t.Fatalf("reason = %q", got)
	}
}

func TestPingTimeout(t *testing.T) {
	srv := testServer(t)
	srv.hub.pingInterval, srv.hub.pingTimeout = 20*time.Millisecond, 60*time.Millisecond
	ws := dial(t, srv.url)
	if info := open(t, ws); info["pingInterval"] != float64(20) || info["pingTimeout"] != float64(60) {
		t.Fatalf("open = %v", info)
	}
	send(t, ws, "40")
	ack := recv(t, ws)
	var info struct{ Sid string }
	_ = json.Unmarshal([]byte(ack[2:]), &info)
	if got := recv(t, ws); got != "2" {
		t.Fatalf("got %q, want ping", got)
	}
	// Never answered: the server gives up after pingTimeout.
	srv.waitDisconnect(t, info.Sid)
	if got := srv.reasonFor(t, info.Sid); got != "ping timeout" {
		t.Fatalf("reason = %q", got)
	}
}

func TestAnsweredPingsKeepConnectionOpen(t *testing.T) {
	srv := testServer(t)
	srv.hub.pingInterval, srv.hub.pingTimeout = 10*time.Millisecond, 50*time.Millisecond
	ws := dial(t, srv.url)
	open(t, ws)
	send(t, ws, "40")
	pings := 0
	// Sleep-free: stop after enough pings that an unanswered one would have timed out several times over.
	for pings < 20 {
		if recv(t, ws) == "2" {
			pings++
			send(t, ws, "3")
		}
	}
	select {
	case id := <-srv.disconnects:
		t.Fatalf("socket %s was disconnected although every ping was answered", id)
	default:
	}
}

func TestEngineRejectsOtherTransportsAndVersions(t *testing.T) {
	srv := testServer(t)
	cases := []struct {
		query string
		code  int
		msg   string
	}{
		{"EIO=4&transport=polling", 0, "Transport unknown"},
		{"EIO=4&transport=webtransport", 0, "Transport unknown"},
		{"EIO=4", 0, "Transport unknown"},
		{"transport=websocket", 5, "Unsupported protocol version"},
		{"EIO=3&transport=websocket", 5, "Unsupported protocol version"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		srv.hub.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/socket.io/?"+tc.query, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if rec.Code != http.StatusBadRequest || body["code"] != float64(tc.code) || body["message"] != tc.msg {
			t.Errorf("%s: %d %v", tc.query, rec.Code, body)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: missing CORS header", tc.query)
		}
	}
}

// Emitted payloads are JSON without HTML escaping (JSON.stringify), and one
// that cannot be encoded neither breaks the connection nor later events.
func TestEmitEncodesLikeJSONStringify(t *testing.T) {
	srv := testServer(t)
	ws, _ := connect(t, srv.url)
	login(t, ws, "tok-alice")
	srv.hub.Emit("user-1", "e", map[string]any{"a": "<b>&"})
	if got := recv(t, ws); got != `42["e",{"a":"<b>&"}]` {
		t.Fatalf("frame = %q", got)
	}
	srv.hub.Emit("user-1", "bad", make(chan int))
	srv.hub.Emit("user-1", "after", 1)
	if e := next(t, ws); e.name != "after" {
		t.Fatalf("event after an unencodable payload = %s", e.name)
	}
}

// A client that stops reading is cut off instead of blocking the emitter or
// growing without bound; emitting to a socket that is gone is harmless.
func TestFullBufferClosesConnectionWithoutBlocking(t *testing.T) {
	srv := testServer(t)
	ws, id := connect(t, srv.url)
	login(t, ws, "tok-alice")
	payload := strings.Repeat("x", 1<<18)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			srv.hub.Emit("user-1", "big", payload)
			select {
			case got := <-srv.disconnects:
				srv.disconnects <- got
				return
			default:
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked on a client that does not read")
	}
	srv.waitDisconnect(t, id)
	srv.hub.Emit("user-1", "late", 1) // must not panic or block
}

func TestHubCloseClosesConnections(t *testing.T) {
	srv := testServer(t)
	ws, id := connect(t, srv.url)
	srv.hub.Close()
	expectClosed(t, ws)
	srv.waitDisconnect(t, id)
	late, err := websocket.Dial(srv.url, "", "http://example.test/")
	if err == nil {
		expectClosed(t, late)
	}
}

// Namespace and ack-id framing of the packet grammar: `<type>[/<nsp>,][<id>]<json>`.
func TestParsePacket(t *testing.T) {
	cases := []struct {
		in   string
		want packet
	}{
		{"0", packet{kind: '0', nsp: "/", id: -1}},
		{`0{"token":"x"}`, packet{kind: '0', nsp: "/", id: -1, data: `{"token":"x"}`}},
		{"0/chat,", packet{kind: '0', nsp: "/chat", id: -1}},
		{"0/chat", packet{kind: '0', nsp: "/chat", id: -1}}, // no comma: the whole rest is the namespace
		{`2["auth","t"]`, packet{kind: '2', nsp: "/", id: -1, data: `["auth","t"]`}},
		{`212["a",1]`, packet{kind: '2', nsp: "/", id: 12, data: `["a",1]`}},
		{`2/x,5["a"]`, packet{kind: '2', nsp: "/x", id: 5, data: `["a"]`}},
		{`37["ok"]`, packet{kind: '3', nsp: "/", id: 7, data: `["ok"]`}},
	}
	for _, tc := range cases {
		got, err := parsePacket(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parsePacket(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "x", `51-["a",{"_placeholder":true,"num":0}]`, `61-0["a"]`} {
		if got, err := parsePacket(bad); err == nil {
			t.Errorf("parsePacket(%q) = %+v, want an error", bad, got)
		}
	}
}

// Socket.Emit reports whether the packet was queued: true while the
// connection is open, false once it has closed.
func TestSocketEmitReportsWhetherTheConnectionIsOpen(t *testing.T) {
	srv := testServer(t)
	ws, id := connect(t, srv.url)
	srv.hub.mu.RLock()
	s := srv.hub.clients[id].socket
	srv.hub.mu.RUnlock()
	if !s.Emit("hello", 1) {
		t.Fatal("Emit on an open connection reported failure")
	}
	if got := recv(t, ws); got != `42["hello",1]` {
		t.Fatalf("frame = %q", got)
	}
	if s.Emit("bad", make(chan int)) {
		t.Fatal("Emit of an unencodable payload reported success")
	}
	s.Disconnect()
	expectClosed(t, ws)
	if s.Emit("late", 1) {
		t.Fatal("Emit after close reported success")
	}
}
