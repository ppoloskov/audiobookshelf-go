package realtime

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Engine.IO v4 over WebSocket. Only transport=websocket is served; the
// clients (web client and apps) are configured with transports: ['websocket'].

const (
	defaultPingInterval = 25 * time.Second
	defaultPingTimeout  = 20 * time.Second
	maxPayload          = 1000000
	outBuffer           = 256
	writeTimeout        = 5 * time.Second
)

// conn is one Engine.IO connection: a reader (the HTTP handler goroutine), one
// writer goroutine draining out, and a heartbeat goroutine.
type conn struct {
	ws   *websocket.Conn
	hub  *Hub
	path string
	out  chan string
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	reason  string
	aborted bool
	pong    chan struct{}
	sock    *Socket
}

func randomID() string {
	var b [15]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func engineError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

// serveEngine validates the Engine.IO query and upgrades to a WebSocket.
func (h *Hub) serveEngine(w http.ResponseWriter, r *http.Request, path string) {
	q := r.URL.Query()
	if q.Get("transport") != "websocket" {
		engineError(w, 0, "Transport unknown")
		return
	}
	if q.Get("EIO") != "4" {
		engineError(w, 5, "Unsupported protocol version")
		return
	}
	// Any Origin is accepted, like the TypeScript server's CORS origin "*".
	server := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   func(ws *websocket.Conn) { h.serveConn(ws, r, path) },
	}
	server.ServeHTTP(w, r)
}

func (h *Hub) serveConn(ws *websocket.Conn, r *http.Request, path string) {
	c := &conn{ws: ws, hub: h, path: path, out: make(chan string, outBuffer), done: make(chan struct{}), pong: make(chan struct{}, 1)}
	if !h.addConn(c) {
		_ = ws.Close()
		return
	}
	defer h.removeConn(c)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop() }()
	go func() { defer wg.Done(); c.heartbeat() }()
	open, _ := json.Marshal(struct {
		SID          string   `json:"sid"`
		Upgrades     []string `json:"upgrades"`
		PingInterval int64    `json:"pingInterval"`
		PingTimeout  int64    `json:"pingTimeout"`
		MaxPayload   int      `json:"maxPayload"`
	}{randomID(), []string{}, h.pingInterval.Milliseconds(), h.pingTimeout.Milliseconds(), maxPayload})
	c.send("0" + string(open))
	c.readLoop(r)
	c.close("transport close")
	wg.Wait()
	c.fireDisconnect()
}

// send queues one Engine.IO packet. It never blocks: when the buffer is full
// the connection is closed. Sending on a closed connection is a no-op.
func (c *conn) send(packet string) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.out <- packet:
		return true
	default:
		slog.Warn("[SocketAuthority] Socket send buffer full, closing connection")
		c.abort("transport error")
		return false
	}
}

// close ends the connection once; the first reason wins. Queued packets are
// flushed by the writer before the WebSocket closes.
func (c *conn) close(reason string) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *conn) abort(reason string) {
	c.close(reason)
	c.mu.Lock()
	c.aborted = true
	_ = c.ws.SetWriteDeadline(time.Now())
	c.mu.Unlock()
}

func (c *conn) closeReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *conn) write(packet string) error {
	c.mu.Lock()
	if c.aborted {
		c.mu.Unlock()
		return net.ErrClosed
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	c.mu.Unlock()
	return websocket.Message.Send(c.ws, packet)
}

func (c *conn) writeLoop() {
	defer func() { _ = c.ws.Close() }()
	for {
		select {
		case packet := <-c.out:
			if c.write(packet) != nil {
				c.close("transport error")
				return
			}
		case <-c.done:
			for {
				select {
				case packet := <-c.out:
					if c.write(packet) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// heartbeat sends "2" every pingInterval and closes the connection when no "3"
// arrives within pingTimeout.
func (c *conn) heartbeat() {
	for {
		select {
		case <-c.done:
			return
		case <-time.After(c.hub.pingInterval):
		}
		select {
		case <-c.pong:
		default:
		}
		if !c.send("2") {
			return
		}
		select {
		case <-c.done:
			return
		case <-c.pong:
		case <-time.After(c.hub.pingTimeout):
			c.close("ping timeout")
			return
		}
	}
}

func (c *conn) readLoop(r *http.Request) {
	for {
		var msg string
		if err := websocket.Message.Receive(c.ws, &msg); err != nil {
			return
		}
		select {
		case <-c.done:
			return
		default:
		}
		if msg == "" {
			continue
		}
		switch msg[0] {
		case '3':
			select {
			case c.pong <- struct{}{}:
			default:
			}
		case '1':
			c.close("transport close")
			return
		case '4':
			if !c.handleSocketPacket(msg[1:], r) {
				return
			}
		}
	}
}
