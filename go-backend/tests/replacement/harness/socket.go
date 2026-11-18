package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// Event is one Socket.IO event received from the server.
type Event struct {
	Name string
	Args []json.RawMessage
	At   time.Time
}

// Socket is a minimal Socket.IO v4 client (Engine.IO v4 over WebSocket, default
// namespace) that records every event it receives.
type Socket struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	events  []Event
	opened  chan struct{} // Engine.IO open packet received
	joined  chan struct{} // namespace connect acknowledged
	done    chan struct{} // closed when the connection ended
	closing sync.Once
}

const socketDialTimeout = 10 * time.Second

// Socket connects to /audiobookshelf/socket.io. With a token it emits "auth"
// and waits up to 10s for "init". The connection closes when the test ends or
// when the server is stopped or restarted.
func (e *Env) Socket(t *testing.T, token string) *Socket {
	t.Helper()
	parsed, err := url.Parse(e.URL)
	if err != nil {
		t.Fatal(err)
	}
	config, err := websocket.NewConfig("ws://"+parsed.Host+"/audiobookshelf/socket.io/?EIO=4&transport=websocket", "http://"+parsed.Host+"/")
	if err != nil {
		t.Fatal(err)
	}
	config.Dialer = &net.Dialer{Timeout: socketDialTimeout}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("socket dial: %v", err)
	}
	s := &Socket{ws: ws, opened: make(chan struct{}), joined: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(s.Close)
	e.socketMu.Lock()
	e.sockets = append(e.sockets, s)
	e.socketMu.Unlock()
	go s.read()
	if err := s.waitSignal(s.opened, "Engine.IO open packet"); err != nil {
		t.Fatal(err)
	}
	if err := s.write("40"); err != nil {
		t.Fatalf("socket connect: %v", err)
	}
	if err := s.waitSignal(s.joined, "namespace connect"); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		s.Emit(t, "auth", token)
		s.Wait(t, 0, "init", socketDialTimeout, nil)
	}
	return s
}

func (s *Socket) waitSignal(ch chan struct{}, what string) error {
	select {
	case <-ch:
		return nil
	case <-s.done:
		return fmt.Errorf("socket closed before %s", what)
	case <-time.After(socketDialTimeout):
		return fmt.Errorf("no %s within %s", what, socketDialTimeout)
	}
}

func (s *Socket) write(frame string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return websocket.Message.Send(s.ws, frame)
}

// Emit sends an event with the arguments marshalled as JSON.
func (s *Socket) Emit(t *testing.T, name string, args ...any) {
	t.Helper()
	raw, err := json.Marshal(append([]any{name}, args...))
	if err != nil {
		t.Fatalf("emit %s: %v", name, err)
	}
	if err := s.write("42" + string(raw)); err != nil {
		t.Fatalf("emit %s: %v", name, err)
	}
}

// read answers pings and records events until the connection ends; a server
// close is not an error.
func (s *Socket) read() {
	defer close(s.done)
	for {
		var frame string
		if err := websocket.Message.Receive(s.ws, &frame); err != nil {
			return
		}
		switch {
		case frame == "2": // Engine.IO ping
			_ = s.write("3") // a failed pong ends the next read
		case strings.HasPrefix(frame, "42"):
			if event, err := parseEvent(frame); err == nil {
				s.mu.Lock()
				s.events = append(s.events, event)
				s.mu.Unlock()
			}
		case strings.HasPrefix(frame, "40"):
			closeOnce(s.joined)
		case strings.HasPrefix(frame, "0"):
			closeOnce(s.opened)
		}
	}
}

// closeOnce closes ch unless it is already closed.
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// parseEvent parses an event packet of the default namespace: 42["name",arg...].
func parseEvent(frame string) (Event, error) {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "42")), &items); err != nil {
		return Event{}, fmt.Errorf("event packet %.20q: %w", frame, err)
	}
	if len(items) == 0 {
		return Event{}, errors.New("event packet without a name")
	}
	var name string
	if err := json.Unmarshal(items[0], &name); err != nil {
		return Event{}, errors.New("event name is not a string")
	}
	return Event{Name: name, Args: items[1:], At: time.Now()}, nil
}

// Mark returns the index after the last event received so far.
func (s *Socket) Mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// Events returns a copy of everything received.
func (s *Socket) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// find returns the first event at index >= from with the name for which match
// (nil = any) is true, and the names of all events since from.
func (s *Socket) find(from int, name string, match func([]json.RawMessage) bool) (Event, bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seen []string
	for i := max(from, 0); i < len(s.events); i++ {
		event := s.events[i]
		seen = append(seen, event.Name)
		if event.Name == name && (match == nil || match(event.Args)) {
			return event, true, seen
		}
	}
	return Event{}, false, seen
}

// Wait returns the first event at index >= from with that name for which match
// (nil = any) is true, waiting up to timeout. It fails the test listing the
// event names seen since from.
func (s *Socket) Wait(t *testing.T, from int, name string, timeout time.Duration, match func(args []json.RawMessage) bool) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		event, ok, seen := s.find(from, name, match)
		if ok {
			return event
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q event within %s; events since %d: %v", name, timeout, from, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// None fails the test if such an event exists at index >= from or arrives
// within window.
func (s *Socket) None(t *testing.T, from int, name string, window time.Duration, match func(args []json.RawMessage) bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for {
		if event, ok, _ := s.find(from, name, match); ok {
			t.Fatalf("unexpected %q event: %s", name, event.Args)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close ends the connection; it is idempotent.
func (s *Socket) Close() {
	s.closing.Do(func() { _ = s.ws.Close() }) // closing an already-broken connection is fine
}
