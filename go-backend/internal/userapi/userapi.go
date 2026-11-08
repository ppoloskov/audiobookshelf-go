// Package userapi contains a narrow SQLite-backed implementation of the
// user-facing API routes that can be reproduced without the Node managers.
package userapi

import (
	"audiobookshelf-compat/internal/access"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
	"audiobookshelf-compat/internal/wire"
)

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
	OpenSessions func() []map[string]any
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

func New(db *sql.DB, ident Credentials, emit events.Emitter, deps Deps) (*Handler, error) {
	if db == nil {
		return nil, errors.New("userapi: database is nil")
	}
	return &Handler{db: db, ident: ident, emit: emit, deps: deps}, nil
}

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

func (h *Handler) ownListeningSessions(w http.ResponseWriter, r *http.Request, user store.User) error {
	return h.writeListeningSessions(w, r, user.ID, "", false)
}

func (h *Handler) ownListeningStats(w http.ResponseWriter, r *http.Request, user store.User) error {
	return h.writeListeningStats(w, r, user.ID)
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request, user store.User) error {
	progress, err := progressdto.Read(r.Context(), h.db, user.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, users.UserDTO(user, progress))
}

func (h *Handler) getOnlineUsers(w http.ResponseWriter, r *http.Request, _ store.User) error {
	users, sessions := []map[string]any{}, []map[string]any{}
	if h.deps.UsersOnline != nil {
		users = h.deps.UsersOnline()
	}
	if h.deps.OpenSessions != nil {
		sessions = h.deps.OpenSessions()
	}
	return httputil.JSON(w, r, map[string]any{"usersOnline": users, "openSessions": sessions})
}

func (h *Handler) updatePassword(w http.ResponseWriter, r *http.Request, user store.User) error {
	if user.Type == "guest" {
		return httputil.Status(403, "")
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Missing or invalid password or new password")
	}
	oldRaw, hasOld := body["password"]
	newRaw, hasNew := body["newPassword"]
	if !hasOld || !hasNew {
		return httputil.Status(400, "Missing or invalid password or new password")
	}
	oldPassword, oldOK := oldRaw.(string)
	newPassword, newOK := newRaw.(string)
	if oldRaw == nil {
		oldOK = true
	}
	if newRaw == nil {
		newOK = true
	}
	if !oldOK || !newOK {
		return httputil.Status(400, "Missing or invalid password or new password")
	}
	if err := h.ident.ChangePassword(r.Context(), user.ID, oldPassword, newPassword); err != nil {
		logging.Errorf("[LocalAuth] User %q failed to change password: %v", user.Username, err) // LocalAuthStrategy.js:178
		return httputil.Status(400, err.Error())
	}
	logging.Infof("[LocalAuth] User %q changed password", user.Username) // LocalAuthStrategy.js:173
	accessToken, refreshToken, err := h.ident.InvalidateUserSessions(r.Context(), user.ID, refreshTokenFromRequest(r))
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if refreshToken != "" && r.Header.Get("x-refresh-token") != "" {
		return httputil.JSON(w, r, map[string]any{"success": true, "user": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken}})
	}
	if refreshToken != "" {
		h.ident.SetRefreshTokenCookie(w, r, refreshToken)
	}
	return httputil.OK(w, r)
}

func (h *Handler) getAllProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	progress, err := progressdto.Read(r.Context(), h.db, user.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"mediaProgress": progress})
}

func (h *Handler) getBookmarks(w http.ResponseWriter, r *http.Request, user store.User) error {
	libraryItemID := r.PathValue("id")
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	if libraryItemID == "" {
		return httputil.JSON(w, r, map[string]any{"bookmarks": bookmarks})
	}
	if err := h.requireItemAccess(r, user, libraryItemID); err != nil {
		return err
	}
	filtered := make([]any, 0)
	for _, entry := range bookmarks {
		if bm, ok := entry.(map[string]any); ok && bm["libraryItemId"] == libraryItemID {
			filtered = append(filtered, bm)
		}
	}
	return httputil.JSON(w, r, map[string]any{"bookmarks": filtered})
}

func (h *Handler) removeFromContinueListening(w http.ResponseWriter, r *http.Request, user store.User) error {
	res, err := sqlc.New(h.db).HideMediaProgressFromContinueListening(r.Context(), sqlc.HideMediaProgressFromContinueListeningParams{UpdatedAt: dbtime.Now(), ID: r.PathValue("id"), UserID: store.Text(user.ID)})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httputil.Status(404, "")
	}
	h.emitUserUpdate(r.Context(), user)
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) getProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	p, err := progressdto.ReadOne(r.Context(), h.db, user.ID, r.PathValue("id"), r.PathValue("episodeId"))
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if p == nil {
		return httputil.Status(404, "")
	}
	return httputil.JSON(w, r, p)
}

func (h *Handler) deleteProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	res, err := sqlc.New(h.db).DeleteMediaProgressForUser(r.Context(), sqlc.DeleteMediaProgressForUserParams{ID: r.PathValue("id"), UserID: store.Text(user.ID)})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

func (h *Handler) batchUpdateProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	var batch []progressdto.Payload
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch) == 0 {
		return httputil.Status(400, "Missing request payload")
	}
	updated := false
	for _, payload := range batch {
		if _, err := h.updateProgress(r.Context(), user, payload); err == nil {
			updated = true
		}
	}
	if updated {
		h.emitUserUpdate(r.Context(), user)
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateOneProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	// MeController.createUpdateMediaProgress (MeController.js:277): the
	// body is spread first, so the URL's libraryItemId and (possibly
	// absent) episodeId always win.
	var payload progressdto.Payload
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	payload.LibraryItemID, payload.EpisodeID = r.PathValue("id"), r.PathValue("episodeId")
	if _, err := h.updateProgress(r.Context(), user, payload); err != nil {
		var refused *progressdto.StatusError
		if errors.As(err, &refused) {
			// Express res.status(n).send(string): text/html, no trailing newline.
			return httputil.Status(refused.Status, refused.Message)
		}
		return httputil.Internal("database write failed", err)
	}
	h.emitUserUpdate(r.Context(), user)
	return httputil.OK(w, r)
}

// updateProgress applies one progress payload for the user
// (User.createUpdateMediaProgressFromPayload, server/models/User.js:728). The
// library access check is stricter than upstream, which has none here.
func (h *Handler) updateProgress(ctx context.Context, user store.User, p progressdto.Payload) (wire.MediaProgress, error) {
	perms := access.For(ctx, user)
	return progressdto.ApplyChecked(ctx, h.db, user.ID, p, perms.Library)
}

func (h *Handler) saveBookmark(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID := r.PathValue("id")
	if err := h.requireItemAccess(r, user, itemID); err != nil {
		return err
	}
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid request body")
	}
	timeValue, ok := jsonvalue.Number(body["time"])
	if !ok {
		return httputil.Status(400, "Invalid time")
	}
	title, ok := body["title"].(string)
	if !ok || title == "" {
		return httputil.Status(400, "Invalid title")
	}
	index := bookmarkIndex(bookmarks, itemID, timeValue)
	if r.Method == http.MethodPatch && index < 0 {
		return httputil.Status(404, "")
	}
	var entry map[string]any
	if index < 0 {
		entry = map[string]any{"libraryItemId": itemID, "time": timeValue, "title": title, "createdAt": time.Now().UnixMilli()}
		bookmarks = append(bookmarks, entry)
	} else {
		entry, _ = bookmarks[index].(map[string]any)
		entry["title"] = title
	}
	if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
		return httputil.Internal("database write failed", err)
	}
	user.Bookmarks, _ = json.Marshal(bookmarks)
	h.emitUserUpdate(r.Context(), user)
	return httputil.JSON(w, r, entry)
}

func (h *Handler) deleteBookmark(w http.ResponseWriter, r *http.Request, user store.User) error {
	itemID := r.PathValue("id")
	if err := h.requireItemAccess(r, user, itemID); err != nil {
		return err
	}
	bookmarks := store.DecodeJSONSlice[any](user.Bookmarks)
	timeValue, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil || math.IsNaN(timeValue) || math.IsInf(timeValue, 0) {
		return httputil.Status(400, "Invalid time")
	}
	index := bookmarkIndex(bookmarks, itemID, timeValue)
	if index < 0 {
		return httputil.Status(404, "")
	}
	bookmarks = append(bookmarks[:index], bookmarks[index+1:]...)
	if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
		return httputil.Internal("database write failed", err)
	}
	user.Bookmarks, _ = json.Marshal(bookmarks)
	h.emitUserUpdate(r.Context(), user)
	return httputil.OK(w, r)
}

func (h *Handler) requireItemAccess(r *http.Request, user store.User, itemID string) error {
	exists, allowed, err := access.ItemAccess(r.Context(), h.db, user, itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !exists {
		return httputil.Status(404, "")
	}
	if !allowed {
		return httputil.Status(403, "")
	}
	return nil
}

func (h *Handler) saveBookmarks(ctx context.Context, userID string, bookmarks []any) error {
	encoded, err := json.Marshal(bookmarks)
	if err != nil {
		return err
	}
	err = sqlc.New(h.db).UpdateUserBookmarks(ctx, sqlc.UpdateUserBookmarksParams{Bookmarks: store.Text(string(encoded)), Updatedat: dbtime.Now(), ID: userID})
	return err
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request, current store.User) error {
	rows, err := sqlc.New(h.db).ListUsers(r.Context())
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	listed := make([]any, 0, len(rows))
	for _, row := range rows {
		u, err := store.UserFromRow(row)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		listed = append(listed, users.ListedUserDTO(u, current.Type != "root"))
	}
	return httputil.JSON(w, r, map[string]any{"users": listed})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Username and password are required")
	}
	username, _ := body["username"].(string)
	password, _ := body["password"].(string)
	if username == "" || password == "" {
		return httputil.Status(400, "Username and password are required")
	}
	typ, _ := body["type"].(string)
	if typ == "" {
		typ = "user"
	}
	if typ != "user" && typ != "admin" && typ != "guest" {
		return httputil.Status(400, "Invalid account type")
	}
	taken, err := sqlc.New(h.db).CountOtherUsersNamed(r.Context(), sqlc.CountOtherUsersNamedParams{Username: store.Text(username), ID: ""})
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if taken > 0 {
		return httputil.Status(400, "Username already taken")
	}
	permissions := access.Defaults(typ)
	patch, _ := body["permissions"].(map[string]any)
	access.Patch(permissions, typ, patch, body)
	hash, err := h.ident.HashPassword(password)
	if err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	// UserController.js:174: a string email is stored as given, anything else as NULL.
	var email *string
	if address, ok := body["email"].(string); ok {
		email = &address
	}
	userID, err := h.ident.CreateUser(r.Context(), users.NewUser{
		Username: username, Email: email, PasswordHash: &hash, Type: typ, Active: jsonvalue.Truthy(body["isActive"]), Permissions: permissions,
	})
	if err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	newUser, err := h.userByID(r.Context(), userID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	serialized := users.UserDTO(newUser, nil)
	h.emit.ToAdmins("user_added", serialized)
	return httputil.JSON(w, r, map[string]any{"user": serialized})
}

func (h *Handler) userListening(w http.ResponseWriter, r *http.Request, current store.User) error {
	id, kind := r.PathValue("id"), r.PathValue("kind")
	if kind != "listening-sessions" && kind != "listening-stats" {
		return httputil.Status(404, "")
	}
	if !access.AdminUser(current) && current.ID != id {
		return httputil.Status(403, "")
	}
	if _, err := h.targetUser(r.Context(), id); err != nil {
		return err
	}
	if kind == "listening-stats" {
		return h.writeListeningStats(w, r, id)
	}
	return h.writeListeningSessions(w, r, id, "", true)
}

// targetUser loads the user an admin route addresses; a missing user is a 404.
func (h *Handler) targetUser(ctx context.Context, id string) (store.User, error) {
	u, err := h.userByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return u, httputil.Status(404, "")
	}
	if err != nil {
		return u, httputil.Internal("database read failed", err)
	}
	return u, nil
}

func (h *Handler) openIDUnlink(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	u, err := h.targetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if u.Type == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	var extra map[string]any
	_ = json.Unmarshal(u.ExtraData, &extra)
	if extra == nil || extra["authOpenIDSub"] == nil {
		return httputil.OK(w, r)
	}
	extra["authOpenIDSub"] = nil
	encoded, _ := json.Marshal(extra)
	if err := sqlc.New(h.db).UpdateUserExtraData(r.Context(), sqlc.UpdateUserExtraDataParams{Extradata: store.Text(string(encoded)), Updatedat: dbtime.Now(), ID: id}); err != nil {
		return httputil.Internal("database write failed", err)
	}
	u.ExtraData = encoded
	h.emit.ToUser(current.ID, "user_updated", users.UserDTO(u, nil))
	return httputil.OK(w, r)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	u, err := h.targetUser(r.Context(), id)
	if err != nil {
		return err
	}
	progress, err := progressdto.Read(r.Context(), h.db, id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	decorated, err := h.decorateAdminProgress(r.Context(), progress)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	dto := users.UserDTO(u, nil)
	dto["mediaProgress"] = decorated
	return httputil.JSON(w, r, dto)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	u, err := h.targetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if current.ID == id {
		logging.Errorf("[UserController] User %s is attempting to delete self", current.Username) // UserController.js:367
		return httputil.Status(400, "")
	}
	if u.Type == "root" {
		logging.Errorf("[UserController] Admin user %q attempted to delete root user", current.Username) // UserController.js:371
		return httputil.Status(403, "")
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	defer tx.Rollback()
	if err = sqlc.New(tx).DeleteUserPlaylists(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	sessionsUpdated := int64(0)
	if res, execErr := sqlc.New(tx).DetachPlaybackSessionsFromUser(r.Context(), store.Text(id)); execErr != nil {
		return httputil.Internal("database write failed", execErr)
	} else if n, rowsErr := res.RowsAffected(); rowsErr == nil {
		sessionsUpdated = n
	}
	if err = sqlc.New(tx).DeleteUser(r.Context(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err = tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	logging.Infof("[UserController] Updated %d playback sessions to remove user id", sessionsUpdated) // UserController.js:398
	h.notifyUserChanged(r.Context(), id)
	h.emit.ToAdmins("user_removed", users.UserDTO(u, nil))
	return httputil.JSON(w, r, map[string]any{"success": true})
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	u, err := h.targetUser(r.Context(), id)
	if err != nil {
		return err
	}
	if u.Type == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid request")
	}
	for _, key := range []string{"id", "pash", "token", "extraData", "bookmarks"} {
		if _, ok := body[key]; ok {
			return httputil.Status(400, `Key "`+key+`" cannot be updated`)
		}
	}
	if value, exists := body["email"]; exists && value != nil && value != "" {
		if _, ok := value.(string); !ok {
			return httputil.Status(400, "Invalid email")
		}
	}
	if value, exists := body["username"]; exists && value != nil && value != "" {
		if _, ok := value.(string); !ok {
			return httputil.Status(400, "Invalid username")
		}
	}
	if value, exists := body["type"]; exists && value != nil && value != "" {
		if _, ok := value.(string); !ok {
			return httputil.Status(400, "Invalid account type")
		}
	}
	if value, exists := body["permissions"]; exists && value != nil {
		if _, ok := value.(map[string]any); !ok {
			return httputil.Status(400, "Invalid permissions")
		}
	}
	credentialsChanged := false
	if name, ok := body["username"].(string); ok && name != "" && name != u.Username {
		taken, err := sqlc.New(h.db).CountOtherUsersNamed(r.Context(), sqlc.CountOtherUsersNamedParams{Username: store.Text(name), ID: id})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if taken != 0 {
			return httputil.Status(400, "Username already taken")
		}
		u.Username = name
		token, err := h.ident.IssueLegacyToken(u.ID, u.Username)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		u.Token = token
		credentialsChanged = true
	}
	if password, ok := body["password"].(string); ok && password != "" {
		hash, err := h.ident.HashPassword(password)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		u.Password = hash
		credentialsChanged = true
	}
	// UserController.js:312: only a non-empty email is applied; null and ""
	// leave the stored value (NULL or otherwise) alone.
	if email, ok := body["email"].(string); ok && email != "" {
		u.Email = &email
	}
	if typ, ok := body["type"].(string); ok && typ != "" && u.Type != "root" {
		if typ != "user" && typ != "admin" && typ != "guest" {
			return httputil.Status(400, "Invalid account type")
		}
		u.Type = typ
	}
	if active, ok := body["isActive"].(bool); ok {
		u.IsActive = active
	}
	perms := store.DecodeJSONObject(u.Permissions)
	patch, _ := body["permissions"].(map[string]any)
	access.Patch(perms, u.Type, patch, body)
	encoded, _ := json.Marshal(perms)
	active := int64(0)
	if u.IsActive {
		active = 1
	}
	err = sqlc.New(h.db).UpdateUserProfile(r.Context(), sqlc.UpdateUserProfileParams{
		Username: store.Text(u.Username), Email: sql.NullString{String: derefString(u.Email), Valid: u.Email != nil}, Pash: store.Text(u.Password),
		Token: store.Text(u.Token), Type: store.Text(u.Type), Isactive: sql.NullInt64{Int64: active, Valid: true},
		Permissions: store.Text(string(encoded)), Updatedat: dbtime.Now(), ID: u.ID,
	})
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if credentialsChanged {
		_, refresh, err := h.ident.InvalidateUserSessions(r.Context(), u.ID, refreshTokenFromRequest(r))
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		if refresh != "" && r.Header.Get("x-refresh-token") == "" {
			h.ident.SetRefreshTokenCookie(w, r, refresh)
		}
	}
	h.notifyUserChanged(r.Context(), u.ID)
	u.Permissions = encoded
	serialized := users.UserDTO(u, nil)
	h.emit.ToUser(current.ID, "user_updated", serialized)
	return httputil.JSON(w, r, map[string]any{"success": true, "user": serialized})
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request, _ store.User) error {
	keys, err := h.ident.APIKeys(r.Context())
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"apiKeys": keys})
}

// createdAPIKey is the create response: ApiKeyController.js:100 spreads
// apiKey.toJSON() of a freshly built instance, which has no description,
// lastUsedAt, permissions or createdByUser; the nil fields shadow them.
type createdAPIKey struct {
	users.APIKey
	Description   *struct{} `json:"description,omitempty"`
	LastUsedAt    *struct{} `json:"lastUsedAt,omitempty"`
	Permissions   *struct{} `json:"permissions,omitempty"`
	CreatedByUser *struct{} `json:"createdByUser,omitempty"`
	Token         string    `json:"apiKey"`
}

// updatedAPIKey is the update response: the instance JSON has no createdByUser.
type updatedAPIKey struct {
	users.APIKey
	CreatedByUser *struct{} `json:"createdByUser,omitempty"`
}

func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request, current store.User) error {
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "")
	}
	name, _ := body["name"].(string)
	userID, _ := body["userId"].(string)
	if name == "" || userID == "" {
		return httputil.Status(400, "")
	}
	target, err := h.userByID(r.Context(), userID)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(400, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if target.Type == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	expiresIn := jsonvalue.Float(body["expiresIn"])
	if body["expiresIn"] != nil && expiresIn <= 0 {
		return httputil.Status(400, "")
	}
	active, _ := body["isActive"].(bool)
	key, token, err := h.ident.CreateAPIKey(r.Context(), users.NewAPIKey{Name: name, UserID: userID, CreatedBy: current.ID, ExpiresIn: expiresIn, Active: active})
	if err != nil {
		return httputil.Internal("API key creation failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"apiKey": createdAPIKey{APIKey: key, Token: token}})
}

func (h *Handler) deleteAPIKey(w http.ResponseWriter, r *http.Request, _ store.User) error {
	found, err := h.ident.DeleteAPIKey(r.Context(), r.PathValue("id"))
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if !found {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateAPIKey(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	key, err := h.ident.APIKey(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && key.User == nil) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if key.User.Type == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "")
	}
	owner := key.User.ID
	if val, ok := body["userId"]; ok {
		newID, ok := val.(string)
		if !ok {
			return httputil.Status(400, "")
		}
		target, e := h.userByID(r.Context(), newID)
		if e != nil {
			return httputil.Status(400, "")
		}
		if target.Type == "root" && current.Type != "root" {
			return httputil.Status(403, "")
		}
		owner = newID
	}
	var active *bool
	if val, ok := body["isActive"]; ok {
		b, ok := val.(bool)
		if !ok {
			return httputil.Status(400, "")
		}
		active = &b
	}
	if err = h.ident.UpdateAPIKey(r.Context(), id, owner, active); err != nil {
		return httputil.Internal("database write failed", err)
	}
	// The response is the instance as loaded plus the update; lastUsedAt is
	// the value as-loaded (ApiKeyController.js update).
	key, err = h.ident.APIKey(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"apiKey": updatedAPIKey{APIKey: key}})
}

// adminProgress is a progress with the media fields UserController.findOne
// attaches (server/controllers/UserController.js:86-98): displayTitle, plus
// displaySubtitle for episodes, coverPath and mediaUpdatedAt (a JS Date, so an
// ISO string). A nil RawMessage leaves the key out (no media row); the JSON
// text null keeps it as null.
type adminProgress struct {
	wire.MediaProgress
	DisplayTitle    json.RawMessage `json:"displayTitle,omitempty"`
	DisplaySubtitle json.RawMessage `json:"displaySubtitle,omitempty"`
	CoverPath       json.RawMessage `json:"coverPath,omitempty"`
	MediaUpdatedAt  json.RawMessage `json:"mediaUpdatedAt,omitempty"`
}

func jsonText(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func (h *Handler) decorateAdminProgress(ctx context.Context, progress []wire.MediaProgress) ([]adminProgress, error) {
	out := make([]adminProgress, 0, len(progress))
	for _, p := range progress {
		d := adminProgress{MediaProgress: p}
		var title, subtitle, cover sql.NullString
		var updated any
		var err error
		episode := p.MediaItemType == "podcastEpisode"
		if episode {
			err = h.db.QueryRowContext(ctx, `SELECT pe.title, po.title, po.coverPath, po.updatedAt FROM podcastEpisodes pe LEFT JOIN podcasts po ON po.id = pe.podcastId WHERE pe.id = ?`, p.MediaItemID).Scan(&title, &subtitle, &cover, &updated)
		} else {
			err = h.db.QueryRowContext(ctx, `SELECT title, NULL, coverPath, updatedAt FROM books WHERE id = ?`, p.MediaItemID).Scan(&title, &subtitle, &cover, &updated)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			d.DisplayTitle, d.CoverPath, d.MediaUpdatedAt = jsonText(store.NullString(title)), jsonText(store.NullString(cover)), jsonText(nil)
			if episode {
				d.DisplaySubtitle = jsonText(store.NullString(subtitle))
			}
			if t := dbtime.Time(updated); !t.IsZero() {
				d.MediaUpdatedAt = jsonText(dbtime.JSDate(t))
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (h *Handler) userPayload(ctx context.Context, user store.User) (map[string]any, error) {
	progress, err := progressdto.Read(ctx, h.db, user.ID)
	if err != nil {
		return nil, err
	}
	return users.UserDTO(user, progress), nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	row, err := sqlc.New(h.db).GetUserByID(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	return store.UserFromRow(row)
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

func bookmarkIndex(bookmarks []any, item string, t float64) int {
	for i, v := range bookmarks {
		if bm, ok := v.(map[string]any); ok && bm["libraryItemId"] == item {
			if n, ok := jsonvalue.Number(bm["time"]); ok && n == t {
				return i
			}
		}
	}
	return -1
}
