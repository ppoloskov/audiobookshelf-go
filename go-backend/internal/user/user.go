// Package user serves the account routes (/api/me), the user administration
// (/api/users), API keys, and the per-user progress, bookmark and listening
// statistics routes.
package user

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/settings"
)

// Handler owns the user routes.
type Handler struct {
	db       *db.DB
	auth     *auth.Service
	settings *settings.Store
	emit     events.Emitter
	playback *playback.Handler
	deps     Deps
}

// Deps are the hooks into the realtime hub: UserChanged runs after an admin
// updates or deletes a user, so live connections drop the old permissions;
// UsersOnline lists the connected users.
type Deps struct {
	UserChanged func(ctx context.Context, userID string) error
	UsersOnline func() []map[string]any
}

// New returns a Handler. playback supplies the progress rules, the stored
// session views and the open sessions.
func New(d *db.DB, a *auth.Service, st *settings.Store, emit events.Emitter, pb *playback.Handler, deps Deps) *Handler {
	return &Handler{db: d, auth: a, settings: st, emit: emit, playback: pb, deps: deps}
}

type userHandler func(w http.ResponseWriter, r *http.Request, me *auth.Principal) error

func member(next userHandler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		me := auth.User(r.Context())
		if me == nil {
			return httpx.Error(http.StatusUnauthorized, "Unauthorized")
		}
		return next(w, r, me)
	}
}

func admin(next userHandler) http.Handler { return auth.Admin(member(next)) }

// Routes registers the routes. Account routes need any authenticated user and
// state their own further checks; user and API key administration is admin only.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/me", member(h.getMe))
	mux.Handle("GET /api/me/sessions", member(h.listSessions))
	mux.Handle("PATCH /api/me/password", member(h.changePassword))
	mux.Handle("DELETE /api/me/sessions/{id}", member(h.deleteSession))
	mux.Handle("POST /api/me/ereader-devices", member(h.updateEreaderDevices))
	mux.Handle("GET /api/me/listening-sessions", member(own(h.listeningSessions)))
	mux.Handle("GET /api/me/item/listening-sessions/{id}", member(h.itemListeningSessions))
	mux.Handle("GET /api/me/item/listening-sessions/{id}/{episodeId}", member(h.itemListeningSessions))
	mux.Handle("GET /api/me/listening-stats", member(own(h.listeningStats)))
	mux.Handle("GET /api/me/stats/year/{year}", member(h.yearStats))
	mux.Handle("GET /api/me/items-in-progress", member(h.itemsInProgress))
	mux.Handle("GET /api/me/series/{id}/{action}", member(h.seriesContinueListening))
	mux.Handle("GET /api/me/progress", member(h.allProgress))
	mux.Handle("GET /api/me/progress/{id}", member(h.getProgress))
	mux.Handle("GET /api/me/progress/{id}/{episodeId}", member(h.getProgress))
	mux.Handle("GET /api/me/progress/{id}/remove-from-continue-listening", member(h.hideProgress))
	mux.Handle("DELETE /api/me/progress/{id}", member(h.deleteProgress))
	mux.Handle("PATCH /api/me/progress/batch/update", member(h.batchUpdateProgress))
	mux.Handle("PATCH /api/me/progress/{id}", member(h.updateProgress))
	mux.Handle("PATCH /api/me/progress/{id}/{episodeId}", member(h.updateProgress))
	mux.Handle("GET /api/me/bookmarks", member(h.getBookmarks))
	mux.Handle("GET /api/me/bookmarks/{id}", member(h.getBookmarks))
	mux.Handle("POST /api/me/item/{id}/bookmark", member(h.createBookmark))
	mux.Handle("PATCH /api/me/item/{id}/bookmark", member(h.updateBookmark))
	mux.Handle("DELETE /api/me/item/{id}/bookmark/{time}", member(h.deleteBookmark))
	// A user reads their own statistics, an admin anyone's.
	mux.Handle("GET /api/users/{id}/listening-sessions", member(h.ofUser(h.listeningSessions)))
	mux.Handle("GET /api/users/{id}/listening-stats", member(h.ofUser(h.listeningStats)))

	mux.Handle("GET /api/api-keys", admin(h.listAPIKeys))
	mux.Handle("POST /api/api-keys", admin(h.createAPIKey))
	mux.Handle("DELETE /api/api-keys/{id}", admin(h.deleteAPIKey))
	mux.Handle("PATCH /api/api-keys/{id}", admin(h.updateAPIKey))
	mux.Handle("GET /api/users", admin(h.listUsers))
	mux.Handle("POST /api/users", admin(h.createUser))
	mux.Handle("GET /api/users/online", admin(h.onlineUsers))
	mux.Handle("GET /api/users/{id}", admin(h.getUser))
	mux.Handle("DELETE /api/users/{id}", admin(h.deleteUser))
	mux.Handle("PATCH /api/users/{id}", admin(h.updateUser))
	mux.Handle("PATCH /api/users/{id}/openid-unlink", admin(h.unlinkOpenID))
}

// body is a JSON object with the one key name, for answers that wrap a value.
type body[T any] struct {
	name  string
	value T
}

func wrap[T any](name string, value T) body[T] { return body[T]{name, value} }

func (b body[T]) MarshalJSON() ([]byte, error) { return json.Marshal(map[string]T{b.name: b.value}) }

func ok(w http.ResponseWriter) error { return httpx.Text(w, http.StatusOK, "OK") }

// emitUserUpdate tells the user's own connections about their changed account.
func (h *Handler) emitUserUpdate(ctx context.Context, user db.User) (auth.UserView, error) {
	view, err := h.auth.ViewWithProgress(ctx, user)
	if err == nil {
		h.emit.ToUser(user.ID, "user_updated", view)
	}
	return view, err
}

func (h *Handler) userChanged(ctx context.Context, id string) {
	if err := h.deps.UserChanged(ctx, id); err != nil {
		slog.Error("refresh sockets of changed user", "user", id, "error", err)
	}
}

var errUnchanged = errors.New("unchanged")

// editUser applies edit to the stored user in one transaction and saves the.
func (h *Handler) editUser(ctx context.Context, id string, edit func(context.Context, *db.User) error, columns ...string) (user db.User, saved bool, err error) {
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var err error
		if user, err = h.userRow(ctx, id); err != nil {
			return err
		}
		if err = edit(ctx, &user); err != nil {
			return err
		}
		user.UpdatedAt, saved = db.Now(), true
		return h.db.Update(ctx, "users", &user, append(columns, "updatedAt")...)
	})
	if errors.Is(err, errUnchanged) {
		err = nil
	}
	return user, saved, err
}

func pageOf(r *http.Request) (page, perPage int) {
	return max(httpx.QueryInt(r, "page", 0), 0), httpx.QueryInt(r, "itemsPerPage", 10)
}
