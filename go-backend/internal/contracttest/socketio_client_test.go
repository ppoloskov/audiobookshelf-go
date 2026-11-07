package contracttest

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

// sioClient is a minimal Socket.IO v4 client for the journey's root connection:
// Engine.IO v4 over WebSocket, the default namespace, events in both directions.
// It is test-only and speaks nothing else.
type sioClient struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	opened  chan struct{} // Engine.IO open packet ("0{...}") received
	joined  chan struct{} // namespace connect acknowledged ("40{...}") received
	done    chan struct{} // closed when the server ended the connection
	// onEvent is called from the read goroutine for each event packet.
	onEvent func(name string, payload any)
}

// dialSocketIO opens the WebSocket at baseURL+path and connects the default
// namespace. It returns once the server acknowledged the namespace connect; the
// caller may send events right away.
func dialSocketIO(baseURL, path string, timeout time.Duration) (*sioClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	cfg, err := websocket.NewConfig("ws://"+u.Host+path+"/?EIO=4&transport=websocket", "http://"+u.Host+"/")
	if err != nil {
		return nil, err
	}
	cfg.Dialer = &net.Dialer{Timeout: timeout}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	c := &sioClient{ws: ws, opened: make(chan struct{}), joined: make(chan struct{}), done: make(chan struct{})}
	go c.read()
	if err := c.waitFor(c.opened, timeout, "Engine.IO open packet"); err != nil {
		_ = ws.Close()
		return nil, err
	}
	if err := c.write("40"); err != nil {
		_ = ws.Close()
		return nil, err
	}
	if err := c.waitFor(c.joined, timeout, "namespace connect"); err != nil {
		_ = ws.Close()
		return nil, err
	}
	return c, nil
}

func (c *sioClient) waitFor(ch chan struct{}, timeout time.Duration, what string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("no %s within %s", what, timeout)
	}
}

// emit sends one event with one argument: 42["name",arg].
func (c *sioClient) emit(name string, arg any) error {
	raw, err := json.Marshal([]any{name, arg})
	if err != nil {
		return err
	}
	return c.write("42" + string(raw))
}

// close closes the WebSocket, which ends the read goroutine.
func (c *sioClient) close() {
	_ = c.ws.Close()
}

func (c *sioClient) write(frame string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return websocket.Message.Send(c.ws, frame)
}

// read handles incoming packets until the connection closes: pings are answered,
// the handshake packets are signalled, events are passed to onEvent.
func (c *sioClient) read() {
	defer close(c.done)
	for {
		var frame string
		if err := websocket.Message.Receive(c.ws, &frame); err != nil {
			return
		}
		switch {
		case strings.HasPrefix(frame, "0"):
			closeOnce(c.opened)
		case frame == "2": // Engine.IO ping
			_ = c.write("3")
		case strings.HasPrefix(frame, "40"):
			closeOnce(c.joined)
		case strings.HasPrefix(frame, "42"):
			name, payload, err := parseSocketIOEvent(frame)
			if err == nil && c.onEvent != nil {
				c.onEvent(name, payload)
			}
		}
	}
}

// closeOnce closes ch unless it is already closed (a repeated packet).
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// parseSocketIOEvent parses an event packet of the default namespace without an
// ack id: 42["name"], 42["name",arg] or 42["name",arg1,arg2...]. It returns the
// name and the payload: nil with no argument, the argument with one, and the
// argument list with more.
func parseSocketIOEvent(frame string) (string, any, error) {
	if !strings.HasPrefix(frame, "42") {
		return "", nil, fmt.Errorf("not an event packet: %.20q", frame)
	}
	var args []any
	if err := json.Unmarshal([]byte(frame[2:]), &args); err != nil {
		return "", nil, fmt.Errorf("event packet %.20q: %w", frame, err)
	}
	if len(args) == 0 {
		return "", nil, errors.New("event packet without a name")
	}
	name, ok := args[0].(string)
	if !ok {
		return "", nil, errors.New("event name is not a string")
	}
	switch len(args) {
	case 1:
		return name, nil, nil
	case 2:
		return name, args[1], nil
	default:
		return name, args[1:], nil
	}
}

func TestParseSocketIOEvent(t *testing.T) {
	cases := []struct {
		frame   string
		name    string
		payload any
		wantErr bool
	}{
		{frame: `42["init",{"userId":"u1","username":"trial-root"}]`, name: "init", payload: map[string]any{"userId": "u1", "username": "trial-root"}},
		{frame: `42["batch_quickmatch_complete",{"success":true,"updates":1}]`, name: "batch_quickmatch_complete", payload: map[string]any{"success": true, "updates": float64(1)}},
		{frame: `42["ping"]`, name: "ping"},
		{frame: `42["items_updated",[1,"a"]]`, name: "items_updated", payload: []any{float64(1), "a"}},
		{frame: `42["two",1,2]`, name: "two", payload: []any{float64(1), float64(2)}},
		{frame: `42[]`, wantErr: true},
		{frame: `42[7,{}]`, wantErr: true},
		{frame: `42{"a":1}`, wantErr: true},
		{frame: `421["ack",{}]`, wantErr: true},
		{frame: `41`, wantErr: true},
	}
	for _, tc := range cases {
		name, payload, err := parseSocketIOEvent(tc.frame)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: want an error, got %q %v", tc.frame, name, payload)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.frame, err)
			continue
		}
		if name != tc.name {
			t.Errorf("%s: name %q, want %q", tc.frame, name, tc.name)
		}
		if fmt.Sprint(payload) != fmt.Sprint(tc.payload) {
			t.Errorf("%s: payload %v, want %v", tc.frame, payload, tc.payload)
		}
	}
}
