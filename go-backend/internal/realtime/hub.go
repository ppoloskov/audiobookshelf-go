// Package realtime implements the Socket.IO v4 / Engine.IO transport and the
// Audiobookshelf v2.37.2 SocketAuthority connection and event boundary.
package realtime

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/jsonvalue"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

// Action is a request delegated to another domain package. Replies may be
// emitted asynchronously for streaming actions such as cover search.
type Action struct {
	SocketID string
	User     store.User
	Event    string
	Payload  any
	Reply    func(event string, payload any)
	Ack      Ack
}

// Backend is the running server as the hub needs it: it authenticates the
// token of a Socket.IO "auth" event (the HTTP bearer check), reaches the
// database for last-seen writes and permission refreshes, performs domain
// actions, and describes a user's open playback session. The hub outlives a
// backup restore, so it asks for the current Backend on every use.
type Backend interface {
	Authenticate(*http.Request) (store.User, error)
	DB() *sql.DB
	Dispatch(context.Context, Action) error
	Session(userID string) any
}

type client struct {
	socket      *Socket
	user        *store.User
	connectedAt time.Time
	testEmit    func(string)
	grants      access.Permissions // decoded from user.Permissions when the user is set
}

// AddTestClient registers an authenticated event recipient without a network
// transport. It is intended for cross-package lifecycle regression tests.
func (h *Hub) AddTestClient(id string, user store.User) {
	h.mu.Lock()
	copy := user
	h.clients[id] = &client{user: &copy, grants: access.Of(copy)}
	h.mu.Unlock()
}

// SetTestEmitter attaches an event observer to the named test client.
func (h *Hub) SetTestEmitter(id string, emit func(string)) {
	h.mu.Lock()
	if c := h.clients[id]; c != nil {
		c.testEmit = emit
	}
	h.mu.Unlock()
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
	logging.Infof("[SocketAuthority] Socket Connected to %s %s", path, id) // SocketAuthority.js:185

	// Handlers run in order on the connection's reader goroutine, so none may
	// block for long. Auth does bounded database work; the dispatcher actions
	// only signal other components.
	s.On("auth", func(args []any, _ Ack) { h.handleAuth(s, first(args)) })
	s.On("cancel_scan", func(args []any, ack Ack) {
		user, ok := h.requireAdmin(id, "cancel_scan")
		if !ok {
			return
		}
		h.dispatchAction(s, user, "cancel_scan", first(args), nil, ack)
	})
	s.On("search_covers", func(args []any, ack Ack) { h.handleSearchCovers(s, args, ack) })
	s.On("cancel_cover_search", func(args []any, ack Ack) { h.handleCancelCoverSearch(s, args, ack) })
	s.On("set_log_listener", func(args []any, ack Ack) {
		user, ok := h.requireAdmin(id, "set_log_listener")
		if !ok {
			return
		}
		level, ok := integer(first(args))
		if !ok || level < 0 || level > 6 {
			logging.Warnf("[SocketAuthority] Invalid set_log_listener level from socket %s", id)
			return
		}
		h.dispatchAction(s, user, "set_log_listener", level, nil, ack)
	})
	s.On("remove_log_listener", func([]any, Ack) {
		// Upstream allows this event without checking authentication.
		h.dispatchAction(s, store.User{}, "remove_log_listener", nil, nil, nil)
	})
	s.On("message_all_users", func(args []any, _ Ack) {
		user, ok := h.userFor(id)
		if !ok || !access.AdminUser(user) {
			logging.Error("[SocketAuthority] Non-admin user sent the message_all_users event") // SocketAuthority.js:246
			return
		}
		payload := object(first(args))
		message := any("")
		if candidate, exists := payload["message"]; exists && jsonvalue.Truthy(candidate) {
			message = candidate
		}
		h.Emit(events.AllUsers, "admin_message", message)
	})
	s.On("ping", func([]any, Ack) { s.Emit("pong") })
	s.On("disconnect", func(args []any, _ Ack) { h.disconnected(id, first(args)) })
}

func (h *Hub) disconnected(id string, reason any) {
	h.mu.RLock()
	c := h.clients[id]
	var user store.User
	online := false
	var connectedFor time.Duration
	if c != nil && c.user != nil {
		user = *c.user
		online = true
	}
	if c != nil {
		connectedFor = time.Since(c.connectedAt)
	}
	h.mu.RUnlock()
	// SocketAuthority.js:216-228
	switch {
	case c == nil:
		logging.Warnf("[SocketAuthority] Socket %s disconnect, no client (Reason: %v)", id, reason)
	case !online:
		logging.Infof("[SocketAuthority] Unauth socket %s disconnected (Reason: %v)", id, reason)
	default:
		logging.Infof("[SocketAuthority] Socket %s disconnected from client %q after %dms (Reason: %v)", id, user.Username, connectedFor.Milliseconds(), reason)
	}
	var offline map[string]any
	if online {
		offline = users.OnlineUserDTO(user, h.backend().Session(user.ID))
	}
	_ = h.backend().Dispatch(context.Background(), Action{SocketID: id, User: user, Event: "remove_log_listener"})
	if offline != nil {
		h.Emit(events.Admins, "user_offline", offline)
		_ = h.backend().Dispatch(context.Background(), Action{SocketID: id, User: user, Event: "disconnect"})
	}
	h.mu.Lock()
	delete(h.clients, id)
	h.mu.Unlock()
}

func (h *Hub) dispatchAction(s *Socket, user store.User, event string, payload any, reply func(string, any), ack Ack) {
	action := Action{SocketID: s.ID(), User: user, Event: event, Payload: payload, Reply: reply, Ack: ack}
	if err := h.backend().Dispatch(context.Background(), action); err != nil && event == "search_covers" {
		h.emitCoverError(s, object(payload)["requestId"], err.Error())
	}
}

// Emit sends one event to a user, administrators/root, or all authenticated
// users. Broadcasts filter item-shaped payloads against library/tag/explicit
// permissions when the payload contains enough metadata to make that decision.
// Recipients are chosen under the lock; sending only enqueues, outside it.
func (h *Hub) Emit(userID, event string, payload any) {
	type delivery struct {
		client  *client
		payload any
	}
	h.mu.RLock()
	var send []delivery
	for _, c := range h.clients {
		if c.user == nil || (c.socket == nil && c.testEmit == nil) {
			continue
		}
		allowed := false
		userPayload := payload
		switch userID {
		case events.AllUsers:
			userPayload, allowed = payloadForUser(&c.grants, payload)
		case events.Admins:
			allowed = access.AdminUser(*c.user)
		default:
			allowed = c.user.ID == userID
		}
		if allowed {
			send = append(send, delivery{c, userPayload})
		}
	}
	h.mu.RUnlock()
	for _, d := range send {
		if d.client.testEmit != nil {
			d.client.testEmit(event)
		} else {
			d.client.socket.Emit(event, d.payload)
		}
	}
}

// Refresh re-reads a user's row after an admin changed or deleted it and
// applies it to that user's connections. Upstream keeps the user object loaded
// at socket auth (UserController.js:347 and :402 only emit to the actor and to
// admins), so a revoked user keeps receiving events; we fail closed instead:
// connections take the new permissions, and those of a deleted or deactivated
// user are disconnected, as socket auth already refuses them.
func (h *Hub) Refresh(ctx context.Context, userID string) error {
	row, err := sqlc.New(h.backend().DB()).GetUserByID(ctx, userID)
	var user store.User
	if err == nil {
		user, err = store.UserFromRow(row)
	}
	gone := errors.Is(err, sql.ErrNoRows)
	if err != nil && !gone {
		return err
	}
	var drop []*Socket
	h.mu.Lock()
	for _, c := range h.clients {
		if c.user == nil || c.user.ID != userID {
			continue
		}
		if gone || !user.IsActive {
			c.grants = access.Permissions{} // denies item events until the disconnect lands
			if c.socket != nil {
				drop = append(drop, c.socket)
			}
			continue
		}
		user.LastSeen = c.user.LastSeen
		copy := user
		c.user, c.grants = &copy, access.Of(copy)
	}
	h.mu.Unlock()
	for _, s := range drop {
		s.Disconnect()
	}
	return nil
}

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
func (h *Hub) requireAdmin(id, eventName string) (store.User, bool) {
	user, ok := h.userFor(id)
	if ok && access.AdminUser(user) {
		return user, true
	}
	logging.Warnf("[SocketAuthority] Unauthorized %s socket event from socket %s", eventName, id)
	return user, false
}

func (h *Hub) userFor(id string) (store.User, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c := h.clients[id]
	if c == nil || c.user == nil {
		return store.User{}, false
	}
	return *c.user, true
}

// UsersOnline returns the deduplicated connected-user list in the same shape
// sent to administrators by Socket.IO init and user_online events.
func (h *Hub) UsersOnline() []map[string]any {
	h.mu.RLock()
	type userCount struct {
		user        store.User
		connections int
	}
	online := make(map[string]userCount)
	for _, c := range h.clients {
		if c.user != nil {
			entry := online[c.user.ID]
			entry.user = *c.user
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
	result := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entry := online[id]
		public := users.OnlineUserDTO(entry.user, h.backend().Session(entry.user.ID))
		public["connections"] = entry.connections
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

func object(value any) map[string]any {
	if result, ok := value.(map[string]any); ok && result != nil {
		return result
	}
	return map[string]any{}
}

func integer(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}
