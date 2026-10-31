// Package realtime implements the Socket.IO v4 / Engine.IO transport and the
// Audiobookshelf v2.37.2 SocketAuthority connection and event boundary.
package realtime

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	itemapi "audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/store"

	"github.com/zishang520/engine.io/v2/types"
	"github.com/zishang520/socket.io/v2/socket"
)

// Authenticate resolves the JWT carried in the Socket.IO "auth" event. The
// callback is reused from HTTP auth by placing the token in a Bearer header.
type Authenticate func(*http.Request) (store.User, error)

// Action is a request delegated to another domain package. Replies may be
// emitted asynchronously for streaming actions such as cover search.
type Action struct {
	SocketID string
	User     store.User
	Event    string
	Payload  any
	Reply    func(event string, payload any)
	Ack      socket.Ack
}

// DispatchFunc lets scan, logging, and cover-search packages provide the
// domain behavior while this package keeps transport and authorization.
type DispatchFunc func(context.Context, Action) error

type client struct {
	socket      *socket.Socket
	user        *store.User
	connectedAt time.Time
	testEmit    func(string)
}

// AddTestClient registers an authenticated event recipient without a network
// transport. It is intended for cross-package lifecycle regression tests.
func (h *Hub) AddTestClient(id string, user store.User) {
	h.mu.Lock()
	copy := user
	h.clients[id] = &client{user: &copy}
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
	db           *sql.DB
	cfg          config.Config
	authenticate Authenticate

	mu            sync.RWMutex
	clients       map[string]*client
	dispatch      DispatchFunc
	sessionLookup func(userID string) any

	servers []*socket.Server
	handles map[string]http.Handler
	paths   []string
	closeMu sync.Once
}

// New creates an Engine.IO + Socket.IO server for compatible polling and
// WebSocket-upgrade clients. Authentication is deferred to the upstream-style
// Socket.IO "auth" event; HTTP bearer headers alone do not associate a user.
func New(db *sql.DB, cfg config.Config, authenticate func(*http.Request) (store.User, error)) (*Hub, error) {
	if db == nil {
		return nil, errors.New("realtime: database is required")
	}
	if authenticate == nil {
		return nil, errors.New("realtime: authenticate callback is required")
	}
	h := &Hub{
		db: db, cfg: cfg, authenticate: authenticate,
		clients: make(map[string]*client), handles: make(map[string]http.Handler),
	}
	paths := []string{"/socket.io"}
	base := strings.TrimRight(strings.TrimSpace(cfg.RouterBasePath), "/")
	if base != "" && base != "/" {
		paths = append(paths, base+"/socket.io")
	}
	for _, path := range paths {
		options := socket.DefaultServerOptions()
		options.SetPath(path)
		options.SetServeClient(false)
		options.SetCors(&types.Cors{Origin: "*", Methods: []string{"GET", "POST"}})
		io := socket.NewServer(nil, options)
		if err := io.On("connection", func(args ...any) {
			if len(args) == 0 {
				return
			}
			s, ok := args[0].(*socket.Socket)
			if !ok {
				return
			}
			h.connected(s)
		}); err != nil {
			return nil, fmt.Errorf("realtime: register connection handler: %w", err)
		}
		h.servers = append(h.servers, io)
		h.paths = append(h.paths, path)
		h.handles[path] = io.ServeHandler(nil)
	}
	return h, nil
}

// SetDispatcher installs domain handlers. It is safe to call before or after
// connections are accepted.
func (h *Hub) SetDispatcher(dispatch DispatchFunc) {
	h.mu.Lock()
	h.dispatch = dispatch
	h.mu.Unlock()
}

// SetDatabase rebinds database-backed socket authentication writes after an
// in-process database restore. Existing socket connections remain untouched.
func (h *Hub) SetDatabase(db *sql.DB, authenticate Authenticate) error {
	if db == nil || authenticate == nil {
		return errors.New("realtime: database and authenticate callback are required")
	}
	h.mu.Lock()
	h.db = db
	h.authenticate = authenticate
	h.mu.Unlock()
	return nil
}

// UsersOnline returns the deduplicated connected-user list in the same shape
// sent to administrators by Socket.IO init and user_online events.
func (h *Hub) UsersOnline() []map[string]any { return h.usersOnline() }

// SetSessionLookup supplies the current public playback-session payload for
// GET /api/users/online. A nil callback keeps session fields null.
func (h *Hub) SetSessionLookup(lookup func(userID string) any) {
	h.mu.Lock()
	h.sessionLookup = lookup
	h.mu.Unlock()
}

// ServeHTTP dispatches both configured Engine.IO mount paths to their
// Socket.IO handlers. Polling, CORS/preflight, heartbeat, and transport
// upgrades are handled by the library implementation.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	h.mu.RLock()
	handler := h.handles[path]
	h.mu.RUnlock()
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	handler.ServeHTTP(w, r)
}

// Paths reports the registered legacy and prefixed Socket.IO request paths.
func (h *Hub) Paths() []string { return append([]string(nil), h.paths...) }

// Close closes every Engine.IO server and prevents future accepted sessions.
func (h *Hub) Close() {
	h.closeMu.Do(func() {
		for _, server := range h.servers {
			server.Close(nil)
		}
	})
}

func (h *Hub) connected(s *socket.Socket) {
	id := string(s.Id())
	c := &client{socket: s, connectedAt: time.Now()}
	h.mu.Lock()
	h.clients[id] = c
	h.mu.Unlock()

	bind := func(event string, handler func(...any)) {
		_ = s.On(event, handler)
	}
	bind("auth", func(args ...any) { h.handleAuth(s, first(args)) })
	bind("cancel_scan", func(args ...any) {
		user, ok := h.requireAdmin(id)
		if !ok {
			return
		}
		h.dispatchAction(s, user, "cancel_scan", first(args), nil, ackFrom(args)...)
	})
	bind("search_covers", func(args ...any) { h.handleSearchCovers(s, args...) })
	bind("cancel_cover_search", func(args ...any) { h.handleCancelCoverSearch(s, args...) })
	bind("set_log_listener", func(args ...any) {
		user, ok := h.requireAdmin(id)
		if !ok {
			return
		}
		level, ok := integer(first(args))
		if !ok || level < 0 || level > 6 {
			return
		}
		h.dispatchAction(s, user, "set_log_listener", level, nil, ackFrom(args)...)
	})
	bind("remove_log_listener", func(...any) {
		// Upstream allows this event without checking authentication.
		h.dispatchAction(s, store.User{}, "remove_log_listener", nil, nil)
	})
	bind("message_all_users", func(args ...any) {
		user, ok := h.userFor(id)
		if !ok || !isAdmin(user) {
			return
		}
		payload := object(first(args))
		message := any("")
		if candidate, exists := payload["message"]; exists && truthy(candidate) {
			message = candidate
		}
		h.Emit("*", "admin_message", message)
	})
	bind("ping", func(...any) { _ = s.Emit("pong") })
	bind("disconnect", func(args ...any) { h.disconnected(id, first(args)) })
}

func (h *Hub) disconnected(id string, _ any) {
	h.mu.RLock()
	c := h.clients[id]
	var user store.User
	online := false
	if c != nil && c.user != nil {
		user = *c.user
		online = true
	}
	h.mu.RUnlock()
	var offline map[string]any
	if online {
		offline = h.publicUser(user)
	}
	h.mu.RLock()
	dispatch := h.dispatch
	h.mu.RUnlock()
	if dispatch != nil {
		_ = dispatch(context.Background(), Action{SocketID: id, User: user, Event: "remove_log_listener"})
	}
	if offline != nil {
		h.Emit("@admins", "user_offline", offline)
		if dispatch != nil {
			_ = dispatch(context.Background(), Action{SocketID: id, User: user, Event: "disconnect"})
		}
	}
	h.mu.Lock()
	delete(h.clients, id)
	h.mu.Unlock()
	// Upstream removes log listeners and cancels searches associated with this
	// socket. The search manager itself says it does not track socket/request
	// ownership; downstream dispatchers can clean their own socket-scoped state.
}

func (h *Hub) handleAuth(s *socket.Socket, raw any) {
	token, ok := raw.(string)
	if !ok || token == "" {
		_ = s.Emit("auth_failed", map[string]any{"message": "Invalid token"})
		return
	}
	request := s.Request().Request()
	if request == nil {
		_ = s.Emit("auth_failed", map[string]any{"message": "Invalid token"})
		return
	}
	// The initial polling HTTP request may already have ended by the time the
	// Socket.IO auth event arrives. Give auth a connection-independent context.
	request = request.Clone(context.Background())
	request.Header = request.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+token)
	h.mu.RLock()
	authenticate := h.authenticate
	h.mu.RUnlock()
	user, err := authenticate(request)
	if err != nil {
		message := authFailureMessage(token, err)
		_ = s.Emit("auth_failed", map[string]any{"message": message})
		return
	}
	if !user.IsActive {
		_ = s.Emit("auth_failed", map[string]any{"message": "Invalid user"})
		return
	}
	if user.ID == "" {
		_ = s.Emit("auth_failed", map[string]any{"message": "Invalid user"})
		return
	}
	lastSeen := time.Now().UnixMilli()
	dbTimestamp := dbtime.Format(time.UnixMilli(lastSeen))
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h.mu.RLock()
	db := h.db
	h.mu.RUnlock()
	_, writeErr := db.ExecContext(writeCtx, `UPDATE users SET lastSeen = ?, updatedAt = ? WHERE id = ?`, dbTimestamp, dbTimestamp, user.ID)
	cancel()
	if writeErr != nil {
		// The upstream save must complete before init is emitted. Preserve that
		// ordering and leave the socket unauthenticated when the write fails.
		return
	}
	user.LastSeen = int64Pointer(lastSeen)
	id := string(s.Id())
	h.mu.Lock()
	if c := h.clients[id]; c != nil {
		copy := user
		c.user = &copy
	}
	h.mu.Unlock()

	h.Emit("@admins", "user_online", h.publicUser(user))
	initial := map[string]any{"userId": user.ID, "username": user.Username}
	if isAdmin(user) {
		initial["usersOnline"] = h.usersOnline()
	}
	_ = s.Emit("init", initial)
}

func (h *Hub) handleSearchCovers(s *socket.Socket, args ...any) {
	id := string(s.Id())
	user, authenticated := h.userFor(id)
	raw := first(args)
	payload := object(raw)
	if !authenticated {
		h.emitCoverError(s, payload["requestId"], "Unauthorized")
		return
	}
	requestID, _ := payload["requestId"].(string)
	title, _ := payload["title"].(string)
	if requestID == "" || title == "" {
		h.emitCoverError(s, payload["requestId"], "Invalid request parameters")
		return
	}
	h.dispatchAction(s, user, "search_covers", payload, func(event string, value any) {
		_ = s.Emit(event, value)
	}, ackFrom(args)...)
}

func (h *Hub) handleCancelCoverSearch(s *socket.Socket, args ...any) {
	user, ok := h.userFor(string(s.Id()))
	if !ok {
		return
	}
	h.dispatchAction(s, user, "cancel_cover_search", first(args), func(event string, value any) {
		_ = s.Emit(event, value)
	}, ackFrom(args)...)
}

func (h *Hub) emitCoverError(s *socket.Socket, requestID any, message string) {
	_ = s.Emit("cover_search_error", map[string]any{"requestId": requestID, "error": message})
}

func (h *Hub) dispatchAction(s *socket.Socket, user store.User, event string, payload any, reply func(string, any), ack ...socket.Ack) {
	h.mu.RLock()
	dispatch := h.dispatch
	h.mu.RUnlock()
	if dispatch == nil {
		if event == "search_covers" {
			h.emitCoverError(s, object(payload)["requestId"], "Cover search is unavailable")
		}
		return
	}
	action := Action{SocketID: string(s.Id()), User: user, Event: event, Payload: payload, Reply: reply}
	if len(ack) > 0 {
		action.Ack = ack[0]
	}
	if err := dispatch(context.Background(), action); err != nil && event == "search_covers" {
		h.emitCoverError(s, object(payload)["requestId"], err.Error())
	}
}

// Emit sends one event to a user, administrators/root, or all authenticated
// users. Broadcasts filter item-shaped payloads against library/tag/explicit
// permissions when the payload contains enough metadata to make that decision.
func (h *Hub) Emit(userID, event string, payload any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		if c.user == nil || (c.socket == nil && c.testEmit == nil) {
			continue
		}
		allowed := false
		userPayload := payload
		switch userID {
		case "*":
			userPayload, allowed = payloadForUser(*c.user, payload)
		case "@admins":
			allowed = isAdmin(*c.user)
		default:
			allowed = c.user.ID == userID
		}
		if allowed {
			if c.testEmit != nil {
				c.testEmit(event)
			} else if c.socket != nil {
				_ = c.socket.Emit(event, userPayload)
			}
		}
	}
}

// EmitSocket emits to one active socket connection identified by socket ID.
func (h *Hub) EmitSocket(socketID, event string, payload any) bool {
	h.mu.RLock()
	client := h.clients[socketID]
	h.mu.RUnlock()
	if client == nil || client.socket == nil {
		return false
	}
	if err := client.socket.Emit(event, payload); err != nil {
		return false
	}
	return true
}

func (h *Hub) requireAdmin(id string) (store.User, bool) {
	user, ok := h.userFor(id)
	return user, ok && isAdmin(user)
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

func (h *Hub) usersOnline() []map[string]any {
	h.mu.RLock()
	type userCount struct {
		user        store.User
		connections int
	}
	users := make(map[string]userCount)
	for _, c := range h.clients {
		if c.user != nil {
			entry := users[c.user.ID]
			entry.user = *c.user
			entry.connections++
			users[c.user.ID] = entry
		}
	}
	lookup := h.sessionLookup
	h.mu.RUnlock()
	ids := make([]string, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entry := users[id]
		public := h.publicUserWithLookup(entry.user, lookup)
		public["connections"] = entry.connections
		result = append(result, public)
	}
	return result
}

func (h *Hub) publicUser(user store.User) map[string]any {
	h.mu.RLock()
	lookup := h.sessionLookup
	h.mu.RUnlock()
	return h.publicUserWithLookup(user, lookup)
}

func (h *Hub) publicUserWithLookup(user store.User, lookup func(string) any) map[string]any {
	var lastSeen, createdAt any
	if user.LastSeen != nil && *user.LastSeen != 0 {
		lastSeen = *user.LastSeen
	}
	if user.CreatedAt != nil {
		createdAt = *user.CreatedAt
	}
	var session any
	if lookup != nil {
		session = lookup(user.ID)
	}
	return map[string]any{
		"id": user.ID, "username": user.Username, "type": user.Type,
		"session": session, "lastSeen": lastSeen, "createdAt": createdAt,
	}
}

func payloadAccessible(user store.User, payload any) bool {
	metadata, ok := payload.(map[string]any)
	if !ok {
		return true // global events without item/library metadata are unrestricted.
	}
	libraryID, hasLibrary := metadata["libraryId"].(string)
	media, hasMedia := metadata["media"]
	if !hasLibrary {
		if item, ok := metadata["item"].(map[string]any); ok {
			libraryID, hasLibrary = item["libraryId"].(string)
			media, hasMedia = item["media"]
		}
	}
	if !hasLibrary || libraryID == "" {
		return true
	}
	var permissions map[string]any
	if len(user.Permissions) > 0 {
		_ = json.Unmarshal(user.Permissions, &permissions)
	}
	if permissions == nil || !boolValue(permissions["accessAllLibraries"]) {
		if !contains(stringsValue(permissions["librariesAccessible"]), libraryID) {
			return false
		}
	}
	// Collection events carry a top-level libraryId but no media; upstream
	// broadcasts them after the library check and has no item/tag ACL to apply.
	if !hasMedia {
		return true
	}
	access, ok := itemapi.AccessMedia(media)
	if !ok {
		return false
	}
	if access.Explicit && !boolValue(permissions["accessExplicitContent"]) {
		return false
	}
	if boolValue(permissions["accessAllTags"]) {
		return true
	}
	userTags := stringsValue(permissions["itemTagsSelected"])
	itemTags := access.Tags
	if boolValue(permissions["selectedTagsNotAccessible"]) {
		if len(itemTags) == 0 {
			return true
		}
		for _, tag := range itemTags {
			if contains(userTags, tag) {
				return false
			}
		}
		return true
	}
	if len(itemTags) == 0 {
		return false
	}
	for _, tag := range itemTags {
		if contains(userTags, tag) {
			return true
		}
	}
	return false
}

func payloadForUser(user store.User, payload any) (any, bool) {
	switch values := payload.(type) {
	case []any:
		if !hasAccessMetadata(values) {
			return payload, true
		}
		filtered := make([]any, 0, len(values))
		for _, value := range values {
			if payloadAccessible(user, value) {
				filtered = append(filtered, value)
			}
		}
		return filtered, len(filtered) > 0
	case []map[string]any:
		items := make([]any, len(values))
		for i, value := range values {
			items[i] = value
		}
		if !hasAccessMetadata(items) {
			return payload, true
		}
		filtered := make([]map[string]any, 0, len(values))
		for _, value := range values {
			if payloadAccessible(user, value) {
				filtered = append(filtered, value)
			}
		}
		return filtered, len(filtered) > 0
	default:
		return payload, payloadAccessible(user, payload)
	}
}

func hasAccessMetadata(items []any) bool {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := item["libraryId"].(string); ok {
			return true
		}
		if nested, ok := item["item"].(map[string]any); ok {
			if _, ok := nested["libraryId"].(string); ok {
				return true
			}
		}
	}
	return false
}

func isAdmin(user store.User) bool { return user.Type == "admin" || user.Type == "root" }

func first(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func ackFrom(args []any) []socket.Ack {
	if len(args) == 0 {
		return nil
	}
	if ack, ok := args[len(args)-1].(socket.Ack); ok {
		return []socket.Ack{ack}
	}
	return nil
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

func truthy(value any) bool {
	if value == nil || value == false || value == "" {
		return false
	}
	if n, ok := value.(float64); ok && n == 0 {
		return false
	}
	return true
}

func boolValue(value any) bool {
	b, _ := value.(bool)
	return b
}

func stringsValue(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func tokenType(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Type
}

func authFailureMessage(token string, err error) string {
	tokenKind := tokenType(token)
	apiKey := tokenKind == "api" || tokenKind == "apiKey"
	if errors.Is(err, auth.ErrExpired) {
		if apiKey {
			return "API key expired"
		}
		return "Token expired"
	}
	if apiKey {
		if strings.Contains(strings.ToLower(err.Error()), "expired") {
			return "API key expired"
		}
		return "Invalid API key"
	}
	if strings.Contains(strings.ToLower(err.Error()), "inactive") {
		return "Invalid user"
	}
	return "Invalid token"
}

func int64Pointer(value int64) *int64 { return &value }
