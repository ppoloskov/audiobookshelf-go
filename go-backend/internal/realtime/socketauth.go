package realtime

import (
	"audiobookshelf-compat/internal/events"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

func (h *Hub) handleAuth(s *Socket, raw any) {
	token, ok := raw.(string)
	if !ok || token == "" {
		logging.Error("Cannot validate socket - invalid token") // SocketAuthority.js:276
		s.Emit("auth_failed", map[string]any{"message": "Invalid token"})
		return
	}
	request := s.Request()
	if request == nil {
		logging.Error("Cannot validate socket - invalid token")
		s.Emit("auth_failed", map[string]any{"message": "Invalid token"})
		return
	}
	// The initial polling HTTP request may already have ended by the time the
	// Socket.IO auth event arrives. Give auth a connection-independent context.
	request = request.Clone(context.Background())
	request.Header = request.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+token)
	backend := h.backend()
	user, err := backend.Authenticate(request)
	if err != nil {
		message := authFailureMessage(token, err)
		// SocketAuthority.js:276-319 words each failure differently.
		switch message {
		case "Token expired":
			logging.Error("Cannot validate socket - token expired")
		case "Invalid API key":
			logging.Error("Cannot validate socket - API key not found or inactive")
		case "API key expired":
			logging.Info("[SocketAuthority] API key is expired - deactivated")
		default:
			logging.Errorf("Cannot validate socket - %s (%v)", strings.ToLower(message), err)
		}
		s.Emit("auth_failed", map[string]any{"message": message})
		return
	}
	if !user.IsActive {
		logging.Error("Cannot validate socket - user is not active") // SocketAuthority.js:325
		s.Emit("auth_failed", map[string]any{"message": "Invalid user"})
		return
	}
	if user.ID == "" {
		logging.Error("Cannot validate socket - invalid token")
		s.Emit("auth_failed", map[string]any{"message": "Invalid user"})
		return
	}
	lastSeen := time.Now().UnixMilli()
	dbTimestamp := dbtime.Format(time.UnixMilli(lastSeen))
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, writeErr := backend.DB().ExecContext(writeCtx, `UPDATE users SET lastSeen = ?, updatedAt = ? WHERE id = ?`, dbTimestamp, dbTimestamp, user.ID)
	cancel()
	if writeErr != nil {
		// The upstream save must complete before init is emitted. Preserve that
		// ordering and leave the socket unauthenticated when the write fails.
		logging.Errorf("[SocketAuthority] Failed to update lastSeen for user %q: %v", user.Username, writeErr)
		return
	}
	user.LastSeen = &lastSeen
	id := s.ID()
	var previous *store.User
	h.mu.Lock()
	if c := h.clients[id]; c != nil {
		previous = c.user
		copy := user
		c.user, c.grants = &copy, access.Of(copy)
	}
	h.mu.Unlock()
	// SocketAuthority.js:336-345
	if previous != nil {
		if previous.ID == user.ID {
			logging.Infof("[SocketAuthority] Authenticating socket already associated to user %q", previous.Username)
		} else {
			logging.Warnf("[SocketAuthority] Authenticating socket to user %q, but is already associated with a different user %q", user.Username, previous.Username)
		}
	}

	h.Emit(events.Admins, "user_online", users.OnlineUserDTO(user, h.backend().Session(user.ID)))
	initial := map[string]any{"userId": user.ID, "username": user.Username}
	if access.AdminUser(user) {
		initial["usersOnline"] = h.UsersOnline()
	}
	s.Emit("init", initial)
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
