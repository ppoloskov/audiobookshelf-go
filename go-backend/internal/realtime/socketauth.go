package realtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
)

// OnlineUser is a connected user as administrators see it in init, user_online
// and user_offline (User.toJSONForPublic). It is not the full user view: that
// one carries the token and the permissions.
type OnlineUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Type        string `json:"type"`
	Session     any    `json:"session"`
	LastSeen    *int64 `json:"lastSeen"` // `lastSeen || null`: 0 is null
	CreatedAt   *int64 `json:"createdAt"`
	Connections int    `json:"connections,omitempty"` // only in the usersOnline list
}

func onlineUserOf(user *auth.Principal, session any) OnlineUser {
	online := OnlineUser{ID: user.ID, Username: db.Val(user.Username), Type: db.Val(user.Type), Session: session}
	if !user.LastSeen.IsZero() && user.LastSeen.Millis() != 0 {
		ms := user.LastSeen.Millis()
		online.LastSeen = &ms
	}
	if !user.CreatedAt.IsZero() {
		ms := user.CreatedAt.Millis()
		online.CreatedAt = &ms
	}
	return online
}

// initPayload is the answer to a successful auth event.
type initPayload struct {
	UserID      string       `json:"userId"`
	Username    string       `json:"username"`
	UsersOnline []OnlineUser `json:"usersOnline,omitempty"` // administrators only
}

// authFailed is the payload of auth_failed.
type authFailed struct {
	Message string `json:"message"`
}

func (h *Hub) handleAuth(s *Socket, raw any) {
	token, ok := raw.(string)
	request := s.Request()
	if !ok || token == "" || request == nil {
		slog.Error("Cannot validate socket - invalid token") // SocketAuthority.js:276
		s.Emit("auth_failed", authFailed{"Invalid token"})
		return
	}
	// The initial HTTP request may already have ended by the time the
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
			slog.Error("Cannot validate socket - token expired")
		case "Invalid API key":
			slog.Error("Cannot validate socket - API key not found or inactive")
		case "API key expired":
			slog.Info("[SocketAuthority] API key is expired - deactivated")
		default:
			slog.Error(fmt.Sprintf("Cannot validate socket - %s (%v)", strings.ToLower(message), err))
		}
		s.Emit("auth_failed", authFailed{message})
		return
	}
	if user == nil || user.ID == "" {
		slog.Error("Cannot validate socket - invalid token")
		s.Emit("auth_failed", authFailed{"Invalid user"})
		return
	}
	if !user.IsActive {
		slog.Error("Cannot validate socket - user is not active") // SocketAuthority.js:325
		s.Emit("auth_failed", authFailed{"Invalid user"})
		return
	}
	// The upstream save must complete before init is emitted. Preserve that
	// ordering and leave the socket unauthenticated when the write fails.
	now := db.Now()
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = backend.DB().Exec(writeCtx, `UPDATE users SET lastSeen = ?, updatedAt = ? WHERE id = ?`, now, now, user.ID)
	cancel()
	if err != nil {
		slog.Error(fmt.Sprintf("[SocketAuthority] Failed to update lastSeen for user %q: %v", db.Val(user.Username), err))
		return
	}
	user.LastSeen = now // the principal is ours until it is stored below
	var previous *auth.Principal
	h.mu.Lock()
	if c := h.clients[s.ID()]; c != nil {
		previous = c.user
		c.user, c.revoked = user, false
	}
	h.mu.Unlock()
	// SocketAuthority.js:336-345
	if previous != nil {
		if previous.ID == user.ID {
			slog.Info(fmt.Sprintf("[SocketAuthority] Authenticating socket already associated to user %q", db.Val(previous.Username)))
		} else {
			slog.Warn(fmt.Sprintf("[SocketAuthority] Authenticating socket to user %q, but is already associated with a different user %q", db.Val(user.Username), db.Val(previous.Username)))
		}
	}

	h.Emit(events.Admins, "user_online", onlineUserOf(user, backend.Session(user.ID)))
	initial := initPayload{UserID: user.ID, Username: db.Val(user.Username)}
	if user.IsAdmin() {
		initial.UsersOnline = h.UsersOnline()
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
