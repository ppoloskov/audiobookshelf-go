package realtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"

	"golang.org/x/net/websocket"
)

func TestParsePacket(t *testing.T) {
	cases := []struct {
		in      string
		want    packet
		wantErr bool
	}{
		{in: "0", want: packet{kind: '0', nsp: "/", id: -1}},
		{in: `0{"token":"x"}`, want: packet{kind: '0', nsp: "/", id: -1, data: `{"token":"x"}`}},
		{in: "0/chat,", want: packet{kind: '0', nsp: "/chat", id: -1}},
		{in: "0/chat", want: packet{kind: '0', nsp: "/chat", id: -1}},
		{in: "1", want: packet{kind: '1', nsp: "/", id: -1}},
		{in: `2["auth","t"]`, want: packet{kind: '2', nsp: "/", id: -1, data: `["auth","t"]`}},
		{in: `212["a",1]`, want: packet{kind: '2', nsp: "/", id: 12, data: `["a",1]`}},
		{in: `2/x,5["a"]`, want: packet{kind: '2', nsp: "/x", id: 5, data: `["a"]`}},
		{in: `37["ok"]`, want: packet{kind: '3', nsp: "/", id: 7, data: `["ok"]`}},
		{in: `51-["a",{"_placeholder":true,"num":0}]`, wantErr: true},
		{in: `61-0["a"]`, wantErr: true},
		{in: "", wantErr: true},
		{in: "x", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parsePacket(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
}

func TestEncodeArrayDoesNotEscapeHTML(t *testing.T) {
	got, err := encodeArray([]any{"e", map[string]any{"a": "<b>&"}, nil})
	if err != nil || got != `["e",{"a":"<b>&"},null]` {
		t.Fatalf("encodeArray = %q, %v", got, err)
	}
	if _, err := encodeArray([]any{make(chan int)}); err == nil {
		t.Fatal("unencodable value must fail")
	}
}

// testServer serves a Hub whose connect hook is replaced by connect, so the
// transport is tested without the application handlers.
func testServer(t *testing.T, connect func(*Socket)) (*Hub, string) {
	t.Helper()
	db, _ := testDB(t)
	hub := newTestHub(config.Config{}, &fakeBackend{db: db})
	hub.connectFn = func(s *Socket, _ string) { connect(s) }
	server := httptest.NewServer(hub)
	t.Cleanup(func() { server.Close(); hub.Close() })
	return hub, "ws" + strings.TrimPrefix(server.URL, "http") + "/socket.io/?EIO=4&transport=websocket"
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

func TestOpenPacketAndSocketConnectAck(t *testing.T) {
	_, url := testServer(t, func(*Socket) {})
	ws := dial(t, url)
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

func TestHandlersAreAttachedBeforeConnectAck(t *testing.T) {
	_, url := testServer(t, func(s *Socket) {
		s.On("auth", func(args []any, _ Ack) { s.Emit("init", args[0]) })
	})
	ws := dial(t, url)
	open(t, ws)
	// CONNECT and the first event in one burst, as a fast client does.
	send(t, ws, "40")
	send(t, ws, `42["auth","tok"]`)
	if got := recv(t, ws); !strings.HasPrefix(got, "40{") {
		t.Fatalf("connect ack = %q", got)
	}
	if got := recv(t, ws); got != `42["init","tok"]` {
		t.Fatalf("init = %q", got)
	}
}

func TestInvalidNamespace(t *testing.T) {
	_, url := testServer(t, func(*Socket) { t.Error("connect hook ran for another namespace") })
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40/admin,")
	if got := recv(t, ws); got != `44/admin,{"message":"Invalid namespace"}` {
		t.Fatalf("got %q", got)
	}
}

func TestAckRoundTrip(t *testing.T) {
	_, url := testServer(t, func(s *Socket) {
		s.On("echo", func(args []any, ack Ack) {
			if ack == nil {
				s.Emit("no-ack", args[0])
				return
			}
			ack(args...)
		})
	})
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	send(t, ws, `4212["echo","x",{"n":1}]`)
	if got := recv(t, ws); got != `4312["x",{"n":1}]` {
		t.Fatalf("ack = %q", got)
	}
	send(t, ws, `42["echo","y"]`)
	if got := recv(t, ws); got != `42["no-ack","y"]` {
		t.Fatalf("got %q", got)
	}
}

func TestEventBeforeConnectClosesConnection(t *testing.T) {
	_, url := testServer(t, func(*Socket) {})
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, `42["auth","t"]`)
	expectClosed(t, ws)
}

func TestBinaryPacketClosesConnection(t *testing.T) {
	_, url := testServer(t, func(*Socket) {})
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	send(t, ws, `451-["up",{"_placeholder":true,"num":0}]`)
	expectClosed(t, ws)
}

func TestClientDisconnectAndTransportCloseReasons(t *testing.T) {
	reasons := make(chan string, 2)
	_, url := testServer(t, func(s *Socket) {
		s.On("disconnect", func(args []any, _ Ack) { reasons <- args[0].(string) })
	})
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	send(t, ws, "41")
	if got := <-reasons; got != "client namespace disconnect" {
		t.Fatalf("reason = %q", got)
	}
	ws2 := dial(t, url)
	open(t, ws2)
	send(t, ws2, "40")
	recv(t, ws2)
	_ = ws2.Close()
	select {
	case got := <-reasons:
		if got != "transport close" {
			t.Fatalf("reason = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no disconnect after websocket close")
	}
}

func TestServerDisconnectSendsPacketThenCloses(t *testing.T) {
	reasons := make(chan string, 1)
	_, url := testServer(t, func(s *Socket) {
		s.On("disconnect", func(args []any, _ Ack) { reasons <- args[0].(string) })
		s.On("bye", func([]any, Ack) { s.Disconnect() })
	})
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	send(t, ws, `42["bye"]`)
	if got := recv(t, ws); got != "41" {
		t.Fatalf("got %q, want 41", got)
	}
	expectClosed(t, ws)
	if got := <-reasons; got != "server namespace disconnect" {
		t.Fatalf("reason = %q", got)
	}
}

func TestPingTimeout(t *testing.T) {
	reasons := make(chan string, 1)
	hub, url := testServer(t, func(s *Socket) {
		s.On("disconnect", func(args []any, _ Ack) { reasons <- args[0].(string) })
	})
	hub.pingInterval, hub.pingTimeout = 20*time.Millisecond, 60*time.Millisecond
	ws := dial(t, url)
	if info := open(t, ws); info["pingInterval"] != float64(20) || info["pingTimeout"] != float64(60) {
		t.Fatalf("open = %v", info)
	}
	send(t, ws, "40")
	recv(t, ws)
	if got := recv(t, ws); got != "2" {
		t.Fatalf("got %q, want ping", got)
	}
	select {
	case got := <-reasons:
		if got != "ping timeout" {
			t.Fatalf("reason = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ping timeout")
	}
}

func TestAnsweredPingsKeepConnectionOpen(t *testing.T) {
	reasons := make(chan string, 1)
	hub, url := testServer(t, func(s *Socket) {
		s.On("disconnect", func(args []any, _ Ack) { reasons <- args[0].(string) })
	})
	hub.pingInterval, hub.pingTimeout = 10*time.Millisecond, 50*time.Millisecond
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	deadline := time.Now().Add(300 * time.Millisecond)
	for pings := 0; time.Now().Before(deadline); {
		if recv(t, ws) == "2" {
			pings++
			send(t, ws, "3")
		}
	}
	select {
	case got := <-reasons:
		t.Fatalf("disconnected: %s", got)
	default:
	}
}

func TestEngineRejectsOtherTransportsAndVersions(t *testing.T) {
	hub, _ := testServer(t, func(*Socket) {})
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
		hub.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/socket.io/?"+tc.query, nil))
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

func TestEmitOnClosedConnectionIsNoOp(t *testing.T) {
	sockets := make(chan *Socket, 1)
	_, url := testServer(t, func(s *Socket) { sockets <- s })
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	s := <-sockets
	if !s.Emit("hello", 1) {
		t.Fatal("emit on open connection failed")
	}
	if got := recv(t, ws); got != `42["hello",1]` {
		t.Fatalf("got %q", got)
	}
	s.c.close("test")
	expectClosed(t, ws)
	if s.Emit("late", 1) {
		t.Fatal("emit after close reported success")
	}
}

func TestFullBufferClosesConnectionWithoutBlocking(t *testing.T) {
	sockets := make(chan *Socket, 1)
	_, url := testServer(t, func(s *Socket) { sockets <- s })
	ws := dial(t, url)
	open(t, ws)
	send(t, ws, "40")
	recv(t, ws)
	s := <-sockets
	// Never read from ws: stop the writer by filling the queue faster than the
	// socket drains. Emit must return promptly and close once the buffer is full.
	payload := strings.Repeat("x", 1<<18)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			if !s.Emit("big", payload) {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked on a client that does not read")
	}
	select {
	case <-s.c.done:
	default:
		t.Fatal("connection was not closed")
	}
}

func TestHubCloseClosesConnections(t *testing.T) {
	hub, url := testServer(t, func(*Socket) {})
	ws := dial(t, url)
	open(t, ws)
	hub.Close()
	expectClosed(t, ws)
	late, err := websocket.Dial(url, "", "http://example.test/")
	if err == nil {
		expectClosed(t, late)
	}
}
