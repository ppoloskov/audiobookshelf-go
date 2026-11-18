package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestParseEvent(t *testing.T) {
	event, err := parseEvent(`42["items_updated",[1,"a"],{"k":2}]`)
	if err != nil || event.Name != "items_updated" || len(event.Args) != 2 || string(event.Args[1]) != `{"k":2}` {
		t.Fatalf("parsed %+v: %v", event, err)
	}
	if event, err := parseEvent(`42["ping"]`); err != nil || event.Name != "ping" || len(event.Args) != 0 {
		t.Fatalf("no-arg event: %+v %v", event, err)
	}
	for _, frame := range []string{`42[]`, `42[7,{}]`, `42{"a":1}`, `42`} {
		if _, err := parseEvent(frame); err == nil {
			t.Errorf("%s accepted", frame)
		}
	}
}

// fakeSocketServer speaks just enough Engine.IO/Socket.IO to test the client:
// it acknowledges the namespace, answers "auth" with "init", echoes "echo" as
// "echoed", pings once, and closes the connection on "bye".
func fakeSocketServer(t *testing.T) *Env {
	t.Helper()
	handler := websocket.Handler(func(ws *websocket.Conn) {
		_ = websocket.Message.Send(ws, `0{"sid":"x","pingInterval":25000,"pingTimeout":20000}`)
		for {
			var frame string
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				return
			}
			switch {
			case frame == "40":
				_ = websocket.Message.Send(ws, `40{"sid":"y"}`)
				_ = websocket.Message.Send(ws, "2")
			case strings.HasPrefix(frame, `42["auth"`):
				_ = websocket.Message.Send(ws, `42["init",{"userId":"u1"}]`)
			case strings.HasPrefix(frame, `42["echo"`):
				_ = websocket.Message.Send(ws, `42["echoed",`+strings.TrimSuffix(strings.TrimPrefix(frame, `42["echo",`), "]")+`]`)
			case strings.HasPrefix(frame, `42["bye"`):
				return
			}
		}
	})
	mux := http.NewServeMux()
	mux.Handle("/audiobookshelf/socket.io/", handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &Env{URL: server.URL}
}

func TestSocketRecordsEventsAndMatches(t *testing.T) {
	e := fakeSocketServer(t)
	s := e.Socket(t, "token")
	if events := s.Events(); len(events) != 1 || events[0].Name != "init" {
		t.Fatalf("events after auth: %+v", events)
	}
	mark := s.Mark()
	s.Emit(t, "echo", map[string]int{"n": 7})
	event := s.Wait(t, mark, "echoed", 2*time.Second, func(args []json.RawMessage) bool { return string(args[0]) == `{"n":7}` })
	if event.At.IsZero() {
		t.Fatal("event has no timestamp")
	}
	// An event before the mark does not satisfy None for a later window.
	s.None(t, s.Mark(), "echoed", 100*time.Millisecond, nil)
	s.None(t, mark, "other", 50*time.Millisecond, nil)
	// A match that rejects everything must time out; checked through find to avoid failing this test.
	if _, ok, seen := s.find(mark, "echoed", func([]json.RawMessage) bool { return false }); ok || len(seen) != 1 {
		t.Fatalf("find ignored the matcher: %v %v", ok, seen)
	}
	s.Emit(t, "bye")
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not end after server close")
	}
	s.Close()
	s.Close()
}
