package oracle

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const socketDialTimeout = 10 * time.Second

// SocketEvent is one Socket.IO event received on a role-tagged socket.
type SocketEvent struct {
	Role string
	Name string
	Args []json.RawMessage
	At   time.Time
}

// Socket is a minimal Socket.IO v4 client (Engine.IO v4 over WebSocket)
// connected to a Target and recording received events.
type Socket struct {
	role    string
	isTS    bool
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	events  []SocketEvent
	opened  chan struct{}
	joined  chan struct{}
	done    chan struct{}
	closing sync.Once
}

// Socket connects a WebSocket to /audiobookshelf/socket.io, completes the
// Engine.IO/Socket.IO v4 handshake, and if token is non-empty emits "auth" and
// waits for "init". All recorded events are tagged with role.
func (tgt *Target) Socket(t *testing.T, role, token string) *Socket {
	t.Helper()
	parsed, err := url.Parse(tgt.URL)
	if err != nil {
		t.Fatalf("parse %s url: %v", tgt.Name, err)
	}
	config, err := websocket.NewConfig(
		"ws://"+parsed.Host+"/audiobookshelf/socket.io/?EIO=4&transport=websocket",
		"http://"+parsed.Host+"/",
	)
	if err != nil {
		t.Fatalf("build %s socket config: %v", tgt.Name, err)
	}
	config.Dialer = &net.Dialer{Timeout: socketDialTimeout}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("%s socket dial (%s): %v", tgt.Name, role, err)
	}

	s := &Socket{
		role:   role,
		isTS:   tgt.isTS,
		ws:     ws,
		opened: make(chan struct{}),
		joined: make(chan struct{}),
		done:   make(chan struct{}),
	}
	t.Cleanup(s.Close)

	tgt.socketMu.Lock()
	tgt.sockets = append(tgt.sockets, s)
	tgt.socketMu.Unlock()

	go s.read()

	if err := s.waitSignal(s.opened, "Engine.IO open packet"); err != nil {
		t.Fatalf("%s socket (%s): %v", tgt.Name, role, err)
	}
	if err := s.write("40"); err != nil {
		t.Fatalf("%s socket (%s) connect: %v", tgt.Name, role, err)
	}
	if err := s.waitSignal(s.joined, "namespace connect"); err != nil {
		t.Fatalf("%s socket (%s): %v", tgt.Name, role, err)
	}
	if token != "" {
		s.Emit(t, "auth", token)
		s.Wait(t, 0, "init", socketDialTimeout, nil)
	}
	return s
}

func (s *Socket) waitSignal(ch chan struct{}, what string) error {
	timer := time.NewTimer(socketDialTimeout)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-s.done:
		return fmt.Errorf("socket closed before %s", what)
	case <-timer.C:
		return fmt.Errorf("no %s within %s", what, socketDialTimeout)
	}
}

func (s *Socket) write(frame string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return websocket.Message.Send(s.ws, frame)
}

// Emit sends a Socket.IO event with args marshalled as JSON.
func (s *Socket) Emit(t *testing.T, name string, args ...any) {
	t.Helper()
	payload := append([]any{name}, args...)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal socket emit %s: %v", name, err)
	}
	if err := s.write("42" + string(raw)); err != nil {
		t.Fatalf("emit %s (%s): %v", name, s.role, err)
	}
	surfaceTracker.RecordSocketInbound(s.isTS, name)
}

func (s *Socket) read() {
	defer close(s.done)
	for {
		var frame string
		if err := websocket.Message.Receive(s.ws, &frame); err != nil {
			return
		}
		switch {
		case frame == "2": // Engine.IO ping
			_ = s.write("3") // a failed pong terminates the next Receive
		case strings.HasPrefix(frame, "42"):
			event, err := parseEvent(s.role, frame)
			if err != nil {
				continue // ignore malformed event packets
			}
			surfaceTracker.RecordSocketOutbound(s.isTS, event.Name)
			s.mu.Lock()
			s.events = append(s.events, event)
			s.mu.Unlock()
		case strings.HasPrefix(frame, "40"):
			closeOnce(s.joined)
		case strings.HasPrefix(frame, "0"):
			closeOnce(s.opened)
		}
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func parseEvent(role, frame string) (SocketEvent, error) {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "42")), &items); err != nil {
		return SocketEvent{}, fmt.Errorf("event packet %.20q: %w", frame, err)
	}
	if len(items) == 0 {
		return SocketEvent{}, errors.New("event packet without a name")
	}
	var name string
	if err := json.Unmarshal(items[0], &name); err != nil {
		return SocketEvent{}, fmt.Errorf("event name: %w", err)
	}
	return SocketEvent{
		Role: role,
		Name: name,
		Args: items[1:],
		At:   time.Now(),
	}, nil
}

// Mark returns the current event slice length as a cursor for later Wait/Since calls.
func (s *Socket) Mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// Events returns a shallow copy of all events received so far.
func (s *Socket) Events() []SocketEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *Socket) find(from int, name string, match func([]json.RawMessage) bool) (SocketEvent, bool, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var seen []string
	for i := max(from, 0); i < len(s.events); i++ {
		ev := s.events[i]
		seen = append(seen, ev.Name)
		if ev.Name == name && (match == nil || match(ev.Args)) {
			return ev, true, seen
		}
	}
	return SocketEvent{}, false, seen
}

// Wait waits up to timeout for an event at index >= from matching name and
// optional match predicate, failing the test if it does not arrive in time.
func (s *Socket) Wait(t *testing.T, from int, name string, timeout time.Duration, match func([]json.RawMessage) bool) SocketEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ev, ok, _ := s.find(from, name, match); ok {
			return ev
		}
		if time.Now().After(deadline) {
			_, _, seen := s.find(from, name, match)
			t.Fatalf("no %q event on %s socket within %s; events since %d: %v", name, s.role, timeout, from, seen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close closes the underlying WebSocket connection. It is idempotent.
func (s *Socket) Close() {
	s.closing.Do(func() {
		_ = s.ws.Close() // closing an already-broken socket is expected
	})
}

// Audience manages multiple named-role sockets connected to one Target.
type Audience struct {
	roles   []string
	sockets map[string]*Socket
}

// NewAudience connects and authenticates one socket per fixture role on tgt.
func NewAudience(t *testing.T, tgt *Target, roles ...string) *Audience {
	t.Helper()
	a := &Audience{
		roles:   slices.Clone(roles),
		sockets: make(map[string]*Socket, len(roles)),
	}
	for _, role := range roles {
		token := tgt.Token(t, role)
		a.sockets[role] = tgt.Socket(t, role, token)
	}
	t.Cleanup(a.Close)
	return a
}

// Mark returns the current event index per role.
func (a *Audience) Mark() map[string]int {
	if a == nil {
		return nil
	}
	marks := make(map[string]int, len(a.roles))
	for _, role := range a.roles {
		if sock := a.sockets[role]; sock != nil {
			marks[role] = sock.Mark()
		}
	}
	return marks
}

// Since waits settle (if > 0) and returns all events across all roles received
// since mark, sorted chronologically by arrival time.
func (a *Audience) Since(mark map[string]int, settle time.Duration) []SocketEvent {
	if settle > 0 {
		time.Sleep(settle)
	}
	if a == nil {
		return nil
	}
	var out []SocketEvent
	for _, role := range a.roles {
		sock := a.sockets[role]
		if sock == nil {
			continue
		}
		events := sock.Events()
		from := max(mark[role], 0)
		if from < len(events) {
			out = append(out, events[from:]...)
		}
	}
	slices.SortStableFunc(out, func(x, y SocketEvent) int {
		return x.At.Compare(y.At)
	})
	return out
}

// Socket returns the socket for role, or nil if not present.
func (a *Audience) Socket(role string) *Socket {
	if a == nil {
		return nil
	}
	return a.sockets[role]
}

// Close closes every socket in the audience.
func (a *Audience) Close() {
	if a == nil {
		return
	}
	for _, role := range a.roles {
		if sock := a.sockets[role]; sock != nil {
			sock.Close()
		}
	}
}
