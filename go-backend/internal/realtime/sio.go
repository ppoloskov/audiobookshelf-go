package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Socket.IO v4 packets over Engine.IO messages, default namespace only.
const (
	pktConnect      = '0'
	pktDisconnect   = '1'
	pktEvent        = '2'
	pktAck          = '3'
	pktConnectError = '4'
)

// packet is a parsed Socket.IO packet. id is -1 when absent.
type packet struct {
	kind byte
	nsp  string
	id   int
	data string
}

// parsePacket parses `<type>[<nsp>,][<id>]<json>`. Binary packet types
// ('5' and '6') are rejected.
func parsePacket(s string) (packet, error) {
	p := packet{nsp: "/", id: -1}
	if s == "" || s[0] < pktConnect || s[0] > pktConnectError {
		return p, errors.New("unsupported packet type")
	}
	p.kind = s[0]
	s = s[1:]
	if strings.HasPrefix(s, "/") {
		i := strings.IndexByte(s, ',')
		if i < 0 {
			p.nsp, s = s, ""
		} else {
			p.nsp, s = s[:i], s[i+1:]
		}
	}
	if p.kind == pktEvent || p.kind == pktAck {
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n > 0 {
			id, err := strconv.Atoi(s[:n])
			if err != nil {
				return p, errors.New("bad ack id")
			}
			p.id, s = id, s[n:]
		}
	}
	p.data = s
	return p, nil
}

// encodeArray encodes an event or ack argument array as JSON, unescaped like
// JSON.stringify.
func encodeArray(values []any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(values); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// Ack replies to an event that carried an ack id.
type Ack func(args ...any)

// Handler receives an event's arguments and, when the client asked for an
// acknowledgement, a non-nil ack.
type Handler func(args []any, ack Ack)

// reservedEvents cannot be sent by a client.
var reservedEvents = map[string]bool{
	"connect": true, "connect_error": true, "disconnect": true, "disconnecting": true,
	"newListener": true, "removeListener": true,
}

// Socket is one connected client of the default namespace.
type Socket struct {
	id      string
	c       *conn
	request *http.Request

	mu       sync.Mutex
	handlers map[string]Handler
	left     sync.Once
}

// ID is the Socket.IO socket id sent in the CONNECT acknowledgement.
func (s *Socket) ID() string { return s.id }

// Request is the HTTP request that opened the WebSocket.
func (s *Socket) Request() *http.Request { return s.request }

// On registers the handler for an event; "disconnect" receives the reason.
func (s *Socket) On(event string, fn Handler) {
	s.mu.Lock()
	s.handlers[event] = fn
	s.mu.Unlock()
}

// Emit queues one event. It never blocks and reports whether it was queued.
func (s *Socket) Emit(event string, args ...any) bool {
	body, err := encodeArray(append([]any{event}, args...))
	if err != nil {
		return false
	}
	return s.c.send("42" + body)
}

// Disconnect tells the client and closes the connection.
func (s *Socket) Disconnect() {
	s.c.send("41")
	s.c.close("server namespace disconnect")
}

func (s *Socket) handler(event string) Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handlers[event]
}

// handleSocketPacket processes one Socket.IO packet. It returns false when the
// connection must close.
func (c *conn) handleSocketPacket(raw string, r *http.Request) bool {
	p, err := parsePacket(raw)
	if err != nil {
		c.close("parse error")
		return false
	}
	if p.kind == pktConnect {
		return c.handleConnect(p, r)
	}
	c.mu.Lock()
	s := c.sock
	c.mu.Unlock()
	if s == nil {
		c.close("parse error") // packet before CONNECT
		return false
	}
	if p.nsp != "/" {
		return true
	}
	switch p.kind {
	case pktDisconnect:
		s.fireDisconnect("client namespace disconnect")
		c.close("client namespace disconnect")
		return false
	case pktEvent:
		var args []any
		if json.Unmarshal([]byte(p.data), &args) != nil || len(args) == 0 {
			c.close("parse error")
			return false
		}
		name, ok := args[0].(string)
		if !ok {
			c.close("parse error")
			return false
		}
		if fn := s.handler(name); fn != nil && !reservedEvents[name] {
			var ack Ack
			if p.id >= 0 {
				ack = s.ackFunc(p.id)
			}
			fn(args[1:], ack)
		}
	}
	return true
}

func (s *Socket) ackFunc(id int) Ack {
	return func(args ...any) {
		body, err := encodeArray(args)
		if err != nil {
			return
		}
		s.c.send("43" + strconv.Itoa(id) + body)
	}
}

func (c *conn) handleConnect(p packet, r *http.Request) bool {
	if p.nsp != "/" {
		c.send(`44` + p.nsp + `,{"message":"Invalid namespace"}`)
		return true
	}
	c.mu.Lock()
	existing := c.sock
	c.mu.Unlock()
	if existing != nil {
		return true
	}
	s := &Socket{id: randomID(), c: c, request: r, handlers: map[string]Handler{}}
	c.mu.Lock()
	c.sock = s
	c.mu.Unlock()
	// Register the socket and attach every handler before the client can learn
	// it is connected; otherwise an event sent right after the acknowledgement
	// would find no listener.
	c.hub.connectFn(s, c.path)
	body, _ := json.Marshal(map[string]string{"sid": s.id})
	c.send("40" + string(body))
	return true
}

// fireDisconnect runs the "disconnect" handler once with the reason.
func (s *Socket) fireDisconnect(reason string) {
	s.left.Do(func() {
		if fn := s.handler("disconnect"); fn != nil {
			fn([]any{reason}, nil)
		}
	})
}

// fireDisconnect reports the end of the connection to the socket, if any.
func (c *conn) fireDisconnect() {
	c.mu.Lock()
	s := c.sock
	c.mu.Unlock()
	if s != nil {
		s.fireDisconnect(c.closeReason())
	}
}
