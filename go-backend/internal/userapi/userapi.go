// Package userapi contains a narrow SQLite-backed implementation of the
// user-facing API routes that can be reproduced without the Node managers.
package userapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

// Handler serves the user-facing routes.
type Handler struct {
	db    *sql.DB
	ident Credentials
	emit  events.Emitter
	deps  Deps
}

// Deps are the collaborators of the user routes outside this package.
type Deps struct {
	// UserChanged (optional) runs after an admin updates or deletes a user, so
	// live connections stop using the old permissions.
	UserChanged func(ctx context.Context, userID string) error
	// UsersOnline lists the connected users; OpenSessions the open playback
	// sessions (GET /api/users/online). Both optional.
	UsersOnline  func() []map[string]any
	OpenSessions func() any
}

func (h *Handler) notifyUserChanged(ctx context.Context, id string) {
	if h.deps.UserChanged != nil {
		if err := h.deps.UserChanged(ctx, id); err != nil {
			logging.Errorf("[UserController] Failed to refresh sockets of user %s: %v", id, err)
		}
	}
}

// Credentials is what identity owns of tokens, passwords, sessions and API
// keys; the server passes the identity handler.
type Credentials interface {
	CreateUser(ctx context.Context, in users.NewUser) (string, error)
	// IssueLegacyToken re-issues the token of a renamed user.
	IssueLegacyToken(userID, username string) (string, error)
	HashPassword(password string) (string, error)
	ChangePassword(ctx context.Context, userID, password, newPassword string) error
	InvalidateUserSessions(ctx context.Context, userID, currentRefreshToken string) (accessToken, refreshToken string, err error)
	SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, refreshToken string)
	UserSessions(ctx context.Context, userID, currentRefreshToken string, page, perPage int) (int, []users.SessionInfo, error)
	DeleteUserSession(ctx context.Context, userID, sessionID string) (bool, error)
	APIKeys(ctx context.Context) ([]users.APIKey, error)
	APIKey(ctx context.Context, id string) (users.APIKey, error)
	CreateAPIKey(ctx context.Context, in users.NewAPIKey) (users.APIKey, string, error)
	UpdateAPIKey(ctx context.Context, id, ownerID string, active *bool) error
	DeleteAPIKey(ctx context.Context, id string) (bool, error)
}

// New constructs a handler for user-facing routes.
func New(db *sql.DB, ident Credentials, emit events.Emitter, deps Deps) (*Handler, error) {
	if db == nil {
		return nil, errors.New("userapi: database is nil")
	}
	return &Handler{db: db, ident: ident, emit: emit, deps: deps}, nil
}

// Routes registers user-facing and administrator routes.
func (h *Handler) Routes(mux *http.ServeMux) {
	for pattern, fn := range map[string]func(http.ResponseWriter, *http.Request, store.User) error{
		"GET /api/me":                                              h.getMe,
		"GET /api/me/sessions":                                     h.getSessions,
		"PATCH /api/me/password":                                   h.updatePassword,
		"DELETE /api/me/sessions/{id}":                             h.deleteSession,
		"GET /api/me/listening-sessions":                           h.ownListeningSessions,
		"GET /api/me/item/listening-sessions/{id}":                 h.getItemListeningSessions,
		"GET /api/me/item/listening-sessions/{id}/{episodeId}":     h.getItemListeningSessions,
		"GET /api/me/listening-stats":                              h.ownListeningStats,
		"GET /api/me/items-in-progress":                            h.getItemsInProgress,
		"GET /api/me/series/{id}/{action}":                         h.updateSeriesContinueListening,
		"GET /api/me/stats/year/{year}":                            h.getYearStats,
		"POST /api/me/ereader-devices":                             h.updateEReaderDevices,
		"GET /api/me/progress":                                     h.getAllProgress,
		"GET /api/me/progress/{id}":                                h.getProgress,
		"GET /api/me/progress/{id}/{episodeId}":                    h.getProgress,
		"GET /api/me/progress/{id}/remove-from-continue-listening": h.removeFromContinueListening,
		"DELETE /api/me/progress/{id}":                             h.deleteProgress,
		"PATCH /api/me/progress/batch/update":                      h.batchUpdateProgress,
		"PATCH /api/me/progress/{id}":                              h.updateOneProgress,
		"PATCH /api/me/progress/{id}/{episodeId}":                  h.updateOneProgress,
		"GET /api/me/bookmarks":                                    h.getBookmarks,
		"GET /api/me/bookmarks/{id}":                               h.getBookmarks,
		"POST /api/me/item/{id}/bookmark":                          h.saveBookmark,
		"PATCH /api/me/item/{id}/bookmark":                         h.saveBookmark,
		"DELETE /api/me/item/{id}/bookmark/{time}":                 h.deleteBookmark,
		"GET /api/users/{id}/{kind}":                               h.userListening,
	} {
		mux.Handle(pattern, withUser(fn))
	}
	for pattern, fn := range map[string]func(http.ResponseWriter, *http.Request, store.User) error{
		"GET /api/api-keys":                   h.listAPIKeys,
		"POST /api/api-keys":                  h.createAPIKey,
		"DELETE /api/api-keys/{id}":           h.deleteAPIKey,
		"PATCH /api/api-keys/{id}":            h.updateAPIKey,
		"GET /api/users":                      h.listUsers,
		"POST /api/users":                     h.createUser,
		"GET /api/users/online":               h.getOnlineUsers,
		"GET /api/users/{id}":                 h.getUser,
		"DELETE /api/users/{id}":              h.deleteUser,
		"PATCH /api/users/{id}":               h.updateUser,
		"PATCH /api/users/{id}/openid-unlink": h.openIDUnlink,
	} {
		mux.Handle(pattern, access.RequireAdmin(http.StatusForbidden, withUser(fn)))
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func withUser(fn func(http.ResponseWriter, *http.Request, store.User) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error { return fn(w, r, store.RequestUser(r)) }
}

func (h *Handler) emitUserUpdate(ctx context.Context, user store.User) {
	payload, err := h.userPayload(ctx, user)
	if err == nil {
		h.emit.ToUser(user.ID, "user_updated", payload)
	}
}
