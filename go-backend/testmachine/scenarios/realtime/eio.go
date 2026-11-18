package realtime

// A minimal raw Engine.IO v4 client for the transport-level scenarios. The
// harness Socket always connects to the prefixed path and answers Engine.IO
// pings itself; these scenarios need the legacy path, the raw open-packet
// values, the heartbeat frames and an unanswerable connection, so they drive
// the frames directly.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const eioDialTimeout = 10 * time.Second

// eioHandshake is the payload of the Engine.IO open packet (server/SocketAuthority.js:157-171
// passes only the CORS option, so the values are the engine.io defaults).
type eioHandshake struct {
	SID          string   `json:"sid"`
	Upgrades     []string `json:"upgrades"`
	PingInterval int      `json:"pingInterval"`
	PingTimeout  int      `json:"pingTimeout"`
	MaxPayload   int64    `json:"maxPayload"`
}

// eioConn is one Engine.IO connection with every received frame recorded.
type eioConn struct {
	t   *testing.T
	ws  *websocket.Conn
	url string

	writeMu   sync.Mutex
	pongMu    sync.Mutex
	ponging   bool // whether the read loop answers server pings
	closing   sync.Once
	mu        sync.Mutex
	frames    []string
	opened    chan struct{}
	done      chan struct{}
	handshake eioHandshake
}

// dialEngineIO opens a websocket to the Engine.IO endpoint at path (for
// example "/socket.io/" or "/audiobookshelf/socket.io/") and waits for the
// open packet. It fails the test on any error.
func dialEngineIO(t *testing.T, e *harness.Env, path string) *eioConn {
	t.Helper()
	parsed, err := url.Parse(e.URL)
	if err != nil {
		t.Fatal(err)
	}
	config, err := websocket.NewConfig("ws://"+parsed.Host+path+"?EIO=4&transport=websocket", "http://"+parsed.Host+"/")
	if err != nil {
		t.Fatal(err)
	}
	config.Dialer = &net.Dialer{Timeout: eioDialTimeout}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	conn := &eioConn{t: t, ws: ws, url: path, opened: make(chan struct{}), done: make(chan struct{}), ponging: true}
	t.Cleanup(conn.Close)
	go conn.read()
	select {
	case <-conn.opened:
	case <-conn.done:
		t.Fatalf("connection to %s closed before the open packet", path)
	case <-time.After(eioDialTimeout):
		t.Fatalf("no Engine.IO open packet on %s within %s", path, eioDialTimeout)
	}
	return conn
}

// Close ends the connection; it is idempotent.
func (c *eioConn) Close() {
	c.closing.Do(func() { _ = c.ws.Close() }) // closing an already-broken connection is fine
}

// read records every frame; it answers server pings while ponging is set.
func (c *eioConn) read() {
	defer close(c.done)
	for {
		var frame string
		if err := websocket.Message.Receive(c.ws, &frame); err != nil {
			return
		}
		c.record(frame)
		switch {
		case strings.HasPrefix(frame, "0"):
			var handshake eioHandshake
			if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "0")), &handshake); err != nil {
				c.t.Logf("open packet %q: %v", frame, err)
				return
			}
			c.mu.Lock()
			c.handshake = handshake
			c.mu.Unlock()
			closeChannel(c.opened)
		case frame == "2":
			if c.isPonging() {
				c.write("3") // a failed pong ends the next read
			}
		}
	}
}

// record appends a frame and wakes open-packet waiters.
func (c *eioConn) record(frame string) {
	c.mu.Lock()
	c.frames = append(c.frames, frame)
	c.mu.Unlock()
}

// isPonging reports whether the read loop still answers pings.
func (c *eioConn) isPonging() bool {
	c.pongMu.Lock()
	defer c.pongMu.Unlock()
	return c.ponging
}

// setPonging turns the automatic pong on or off.
func (c *eioConn) setPonging(on bool) {
	c.pongMu.Lock()
	defer c.pongMu.Unlock()
	c.ponging = on
}

// write sends one frame.
func (c *eioConn) write(frame string) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := websocket.Message.Send(c.ws, frame); err != nil {
		c.t.Fatalf("write %s frame: %v", framePrefix(frame), err)
	}
}

// framePrefix renders a frame for failure messages.
func framePrefix(frame string) string {
	if len(frame) > 24 {
		return frame[:24] + "…"
	}
	return frame
}

// Handshake returns the parsed open-packet values.
func (c *eioConn) Handshake() eioHandshake {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handshake
}

// Mark returns the index after the last frame received so far.
func (c *eioConn) Mark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

// OpenPacket returns the raw open packet frame.
func (c *eioConn) OpenPacket() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, frame := range c.frames {
		if strings.HasPrefix(frame, "0") {
			return frame
		}
	}
	return ""
}

// NamespaceConnect finishes the Socket.IO namespace handshake and returns the
// connect ack payload.
func (c *eioConn) NamespaceConnect(t *testing.T) map[string]any {
	t.Helper()
	c.write("40")
	frame := c.WaitFrame(t, "40", eioDialTimeout)
	var ack map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "40")), &ack); err != nil || ack == nil {
		t.Fatalf("namespace connect ack is not a JSON object: %q (%v)", frame, err)
	}
	return ack
}

// EmitApp sends a Socket.IO event packet.
func (c *eioConn) EmitApp(t *testing.T, name string, args ...any) {
	t.Helper()
	raw, err := json.Marshal(append([]any{name}, args...))
	if err != nil {
		t.Fatalf("emit %s: %v", name, err)
	}
	c.write("42" + string(raw))
}

// WaitFrame returns the first frame at index >= from that has the prefix,
// waiting up to timeout, and fails the test otherwise.
func (c *eioConn) WaitFrame(t *testing.T, prefix string, timeout time.Duration) string {
	t.Helper()
	_, frame := c.WaitFrameFrom(t, 0, prefix, timeout)
	return frame
}

// WaitFrameFrom returns the index and value of the first frame at index >=
// from that has the prefix, waiting up to timeout, and fails the test
// otherwise.
func (c *eioConn) WaitFrameFrom(t *testing.T, from int, prefix string, timeout time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		frames := append([]string(nil), c.frames...)
		c.mu.Unlock()
		for i := max(from, 0); i < len(frames); i++ {
			if strings.HasPrefix(frames[i], prefix) {
				return i, frames[i]
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no frame with prefix %q within %s; frames: %s", prefix, timeout, strings.Join(frames, " | "))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WaitClosed fails the test unless the connection ends within timeout.
func (c *eioConn) WaitClosed(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(timeout):
		t.Fatalf("the connection stayed open beyond %s; frames: %s", timeout, strings.Join(c.Frames(), " | "))
	}
}

// doneInternal exposes the channel that closes when the connection ended
// (debug only).
func (c *eioConn) doneInternal() <-chan struct{} { return c.done }

// Frames returns a copy of the recorded frames.
func (c *eioConn) Frames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.frames...)
}

// eioEvent is one Socket.IO event parsed from a frame.
type eioEvent struct {
	Name string
	Args []json.RawMessage
}

// countLegacyEvents counts the Socket.IO events with that name at index >=
// from.
func countLegacyEvents(conn *eioConn, from int, name string) int {
	count := 0
	for i, frame := range conn.Frames() {
		if i < from || !strings.HasPrefix(frame, "42") {
			continue
		}
		if parsed, err := eventFromFrame(frame); err == nil && parsed.Name == name {
			count++
		}
	}
	return count
}

// requireLegacyQuiet requires that none of the names is among the events at
// index >= from.
func requireLegacyQuiet(t *testing.T, conn *eioConn, from int, names ...string) {
	t.Helper()
	for i, frame := range conn.Frames() {
		if i < from || !strings.HasPrefix(frame, "42") {
			continue
		}
		parsed, err := eventFromFrame(frame)
		if err == nil && slices.Contains(names, parsed.Name) {
			t.Fatalf("unexpected %q event on the raw connection: %s", parsed.Name, frame)
		}
	}
}

// closeChannel closes ch unless it is already closed.
func closeChannel(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// eventFromFrame parses a 42-frame of the default namespace.
func eventFromFrame(frame string) (eioEvent, error) {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "42")), &items); err != nil {
		return eioEvent{}, fmt.Errorf("event packet %.24q: %w", frame, err)
	}
	if len(items) == 0 {
		return eioEvent{}, fmt.Errorf("event packet %.24q without a name", frame)
	}
	var name string
	if err := json.Unmarshal(items[0], &name); err != nil {
		return eioEvent{}, fmt.Errorf("event packet %.24q: name is not a string", frame)
	}
	return eioEvent{Name: name, Args: items[1:]}, nil
}
