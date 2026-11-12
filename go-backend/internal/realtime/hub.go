// Package realtime implements the Socket.IO v4 / Engine.IO transport and the
// Audiobookshelf v2.37.2 SocketAuthority connection and event boundary.
package realtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
)

// Backend is the running server as the hub needs it: it authenticates the
// token of a Socket.IO "auth" event (the HTTP bearer check), reaches the
// database for last-seen writes and permission refreshes, describes a user's
// open playback session, and performs the client actions. The hub outlives a
// backup restore, so it asks for the current Backend on every use.
type Backend interface {
	Authenticate(*http.Request) (*auth.Principal, error)
	DB() *db.DB
	Session(userID string) any
	Actions
}

type client struct {
	socket      *Socket
	user        *auth.Principal // nil until the socket authenticated
	revoked     bool            // the user was deleted or deactivated; the socket receives nothing
	connectedAt time.Time
}

// Hub owns one or two Socket.IO servers: the legacy /socket.io path and, when
// configured, the additional router-prefix path used by upstream.
type Hub struct {
	backend func() Backend

	mu      sync.RWMutex
	clients map[string]*client

	paths        []string
	pingInterval time.Duration
	pingTimeout  time.Duration

	connMu sync.Mutex
	conns  map[*conn]struct{}
	closed bool
}

// New creates an Engine.IO + Socket.IO server for compatible polling and
// WebSocket-upgrade clients. Authentication is deferred to the upstream-style
// Socket.IO "auth" event; HTTP bearer headers alone do not associate a user.
func New(cfg config.Config, backend func() Backend) *Hub {
	h := &Hub{
		backend: backend,
		clients: make(map[string]*client), conns: make(map[*conn]struct{}),
		pingInterval: defaultPingInterval, pingTimeout: defaultPingTimeout,
		paths: []string{"/socket.io"},
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.RouterBasePath), "/")
	if base != "" && base != "/" {
		h.paths = append(h.paths, base+"/socket.io")
	}
	return h
}

// ServeHTTP serves the Engine.IO WebSocket endpoint on the configured paths.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	for _, p := range h.paths {
		if p == path {
			h.serveEngine(w, r, path)
			return
		}
	}
	http.NotFound(w, r)
}

func (h *Hub) Paths() []string { return append([]string(nil), h.paths...) }

// Close closes every connection and refuses new ones.
func (h *Hub) Close() {
	h.connMu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.connMu.Unlock()
	for _, c := range conns {
		c.close("server shutting down")
	}
}

func (h *Hub) addConn(c *conn) bool {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	if h.closed {
		return false
	}
	h.conns[c] = struct{}{}
	return true
}

func (h *Hub) removeConn(c *conn) {
	h.connMu.Lock()
	delete(h.conns, c)
	h.connMu.Unlock()
}

func (h *Hub) connected(s *Socket, path string) {
	id := s.ID()
	c := &client{socket: s, connectedAt: time.Now()}
	h.mu.Lock()
	h.clients[id] = c
	h.mu.Unlock()
	slog.Info(fmt.Sprintf("[SocketAuthority] Socket Connected to %s %s", path, id)) // SocketAuthority.js:185

	// Handlers run in order on the connection's reader goroutine, so none may
	// block for long. Auth does bounded database work; the actions only signal
	// other components.
	s.On("auth", func(args []any, _ Ack) { h.handleAuth(s, first(args)) })
	s.On("cancel_scan", func(args []any, _ Ack) {
		if !h.requireAdmin(id, "cancel_scan") {
			return
		}
		if libraryID, ok := decode[string](id, "cancel_scan", args); ok && libraryID != "" {
			h.backend().CancelScan(libraryID)
		}
	})
	s.On("search_covers", func(args []any, _ Ack) { h.handleSearchCovers(s, args) })
	s.On("cancel_cover_search", func(args []any, _ Ack) { h.handleCancelCoverSearch(s, args) })
	s.On("set_log_listener", func(args []any, _ Ack) {
		if !h.requireAdmin(id, "set_log_listener") {
			return
		}
		level, ok := decode[int](id, "set_log_listener", args)
		if !ok || level < 0 || level > 6 {
			slog.Warn(fmt.Sprintf("[SocketAuthority] Invalid set_log_listener level from socket %s", id))
			return
		}
		h.backend().SetLogListener(id, level)
	})
	s.On("remove_log_listener", func([]any, Ack) {
		// Upstream allows this event without checking authentication.
		h.backend().RemoveLogListener(id)
	})
	s.On("message_all_users", func(args []any, _ Ack) {
		if user, ok := h.userFor(id); !ok || !user.IsAdmin() {
			slog.Error("[SocketAuthority] Non-admin user sent the message_all_users event") // SocketAuthority.js:246
			return
		}
		if body, ok := decode[adminMessage](id, "message_all_users", args); ok {
			h.Emit(events.AllUsers, "admin_message", body.Message)
		}
	})
	s.On("ping", func([]any, Ack) { s.Emit("pong") })
	s.On("disconnect", func(args []any, _ Ack) { h.disconnected(id, first(args)) })
}

// adminMessage is the payload of message_all_users.
type adminMessage struct {
	Message string `json:"message"`
}

func (h *Hub) disconnected(id string, reason any) {
	h.mu.RLock()
	c := h.clients[id]
	var user *auth.Principal
	var connectedFor time.Duration
	if c != nil {
		user = c.user
		connectedFor = time.Since(c.connectedAt)
	}
	h.mu.RUnlock()
	// SocketAuthority.js:216-228
	switch {
	case c == nil:
		slog.Warn(fmt.Sprintf("[SocketAuthority] Socket %s disconnect, no client (Reason: %v)", id, reason))
	case user == nil:
		slog.Info(fmt.Sprintf("[SocketAuthority] Unauth socket %s disconnected (Reason: %v)", id, reason))
	default:
		slog.Info(fmt.Sprintf("[SocketAuthority] Socket %s disconnected from client %q after %dms (Reason: %v)", id, db.Val(user.Username), connectedFor.Milliseconds(), reason))
	}
	backend := h.backend()
	var offline OnlineUser
	if user != nil {
		offline = onlineUserOf(user, backend.Session(user.ID))
	}
	backend.RemoveLogListener(id)
	if user != nil {
		h.Emit(events.Admins, "user_offline", offline)
	}
	h.mu.Lock()
	delete(h.clients, id)
	h.mu.Unlock()
}

// Emit sends one event to a user, administrators/root, or all authenticated
// users. Broadcasts filter item-shaped payloads against library/tag/explicit
// permissions (see payloadForUser). Sockets of a revoked user receive nothing.
// Recipients are chosen under the lock; sending only enqueues, outside it.
func (h *Hub) Emit(userID, event string, payload any) {
	type delivery struct {
		client  *client
		payload any
	}
	h.mu.RLock()
	var send []delivery
	for _, c := range h.clients {
		if c.user == nil || c.revoked || c.socket == nil {
			continue
		}
		allowed := false
		userPayload := payload
		switch userID {
		case events.AllUsers:
			userPayload, allowed = payloadForUser(c.user, payload)
		case events.Admins:
			allowed = c.user.IsAdmin()
		default:
			allowed = c.user.ID == userID
		}
		if allowed {
			send = append(send, delivery{c, userPayload})
		}
	}
	h.mu.RUnlock()
	for _, d := range send {
		d.client.socket.Emit(event, d.payload)
	}
}

// Refresh re-reads a user's row after an admin changed or deleted it and
// applies it to that user's connections. Upstream keeps the user object loaded
// at socket auth (UserController.js:347 and :402 only emit to the actor and to
// admins), so a revoked user keeps receiving events; we fail closed instead:
// connections take the new permissions, and those of a deleted or deactivated
// user stop receiving events and are disconnected, as socket auth already
// refuses them.
func (h *Hub) Refresh(ctx context.Context, userID string) error {
	database := h.backend().DB()
	if database == nil {
		return errors.New("realtime: database unavailable")
	}
	var row db.User
	err := database.Get(ctx, &row, `SELECT * FROM users WHERE id = ?`, userID)
	gone := errors.Is(err, sql.ErrNoRows)
	if err != nil && !gone {
		return fmt.Errorf("refresh user: %w", err)
	}
	principal := auth.NewPrincipal(row)
	var drop []*Socket
	h.mu.Lock()
	for _, c := range h.clients {
		if c.user == nil || c.user.ID != userID {
			continue
		}
		if gone || !bool(row.IsActive) {
			c.revoked = true
			if c.socket != nil {
				drop = append(drop, c.socket)
			}
			continue
		}
		c.user = principal
	}
	h.mu.Unlock()
	for _, s := range drop {
		s.Disconnect()
	}
	return nil
}

// EmitSocket sends one event to one socket, authenticated or not. It reports
// whether the packet was queued.
func (h *Hub) EmitSocket(socketID, event string, payload any) bool {
	h.mu.RLock()
	client := h.clients[socketID]
	h.mu.RUnlock()
	if client == nil || client.socket == nil {
		return false
	}
	return client.socket.Emit(event, payload)
}

// requireAdmin is SocketAuthority.requireAdminSocket (SocketAuthority.js:88).
func (h *Hub) requireAdmin(id, eventName string) bool {
	if user, ok := h.userFor(id); ok && user.IsAdmin() {
		return true
	}
	slog.Warn(fmt.Sprintf("[SocketAuthority] Unauthorized %s socket event from socket %s", eventName, id))
	return false
}

// userFor returns the principal a socket authenticated as.
func (h *Hub) userFor(id string) (*auth.Principal, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c := h.clients[id]
	if c == nil || c.user == nil {
		return nil, false
	}
	return c.user, true
}

// UsersOnline returns the deduplicated connected-user list in the shape sent
// to administrators by Socket.IO init and user_online events, ordered by user
// id. The playback lookup runs outside the hub's lock.
func (h *Hub) UsersOnline() []OnlineUser {
	h.mu.RLock()
	type userCount struct {
		user        *auth.Principal
		connections int
	}
	online := make(map[string]userCount)
	for _, c := range h.clients {
		if c.user != nil {
			entry := online[c.user.ID]
			entry.user = c.user
			entry.connections++
			online[c.user.ID] = entry
		}
	}
	h.mu.RUnlock()
	ids := make([]string, 0, len(online))
	for id := range online {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]OnlineUser, 0, len(ids))
	for _, id := range ids {
		entry := online[id]
		public := onlineUserOf(entry.user, h.backend().Session(id))
		public.Connections = entry.connections
		result = append(result, public)
	}
	return result
}

func first(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}
