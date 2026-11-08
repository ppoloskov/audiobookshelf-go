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
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
	"audiobookshelf-compat/internal/wire"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	db           *sql.DB
	creds        Credentials
	emit         events.Emitter
	usersOnline  func() []map[string]any
	openSessions func() []map[string]any
	userChanged  func(ctx context.Context, userID string) error
}

// Credentials is what identity owns of the token and session lifecycle:
// minting user and API-key tokens and changing a user's credentials.
type Credentials interface {
	IssueLegacyToken(userID, username string) (string, error)
	IssueAPIKeyToken(keyID, name string, expiresAtUnix int64) (string, error)
	ChangePassword(ctx context.Context, userID, password, newPassword string) error
	InvalidateUserSessions(ctx context.Context, userID, currentRefreshToken string) (accessToken, refreshToken string, err error)
	SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, refreshToken string)
}

func (h *Handler) notifyUserChanged(ctx context.Context, id string) {
	if h.userChanged != nil {
		if err := h.userChanged(ctx, id); err != nil {
			logging.Errorf("[UserController] Failed to refresh sockets of user %s: %v", id, err)
		}
	}
}

// New takes identity (a Credentials) as creds. userChanged (optional) runs
// after an admin updates or deletes a user, so live connections stop using the
// old permissions.
func New(db *sql.DB, creds Credentials, emit func(userID, event string, payload any), userChanged func(context.Context, string) error) (*Handler, error) {
	if db == nil {
		return nil, errors.New("userapi: database is nil")
	}
	return &Handler{db: db, creds: creds, emit: events.Emitter(emit), userChanged: userChanged}, nil
}

func (h *Handler) SetOnlineData(users func() []map[string]any, sessions func() []map[string]any) {
	h.usersOnline, h.openSessions = users, sessions
}

// Routes mounts the user-facing API (ApiRouter.js) on the runtime's mux.
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
		mux.Handle(pattern, adminOnly(fn))
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
	if h.usersOnline != nil {
		users = h.usersOnline()
	}
	if h.openSessions != nil {
		sessions = h.openSessions()
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
	if err := h.creds.ChangePassword(r.Context(), user.ID, oldPassword, newPassword); err != nil {
		logging.Errorf("[LocalAuth] User %q failed to change password: %v", user.Username, err) // LocalAuthStrategy.js:178
		return httputil.Status(400, err.Error())
	}
	logging.Infof("[LocalAuth] User %q changed password", user.Username) // LocalAuthStrategy.js:173
	accessToken, refreshToken, err := h.creds.InvalidateUserSessions(r.Context(), user.ID, refreshTokenFromRequest(r))
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if refreshToken != "" && r.Header.Get("x-refresh-token") != "" {
		return httputil.JSON(w, r, map[string]any{"success": true, "user": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken}})
	}
	if refreshToken != "" {
		h.creds.SetRefreshTokenCookie(w, r, refreshToken)
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
	access, err := h.canAccessItem(r.Context(), user, libraryItemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !access.exists {
		return httputil.Status(404, "")
	}
	if !access.allowed {
		return httputil.Status(403, "")
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
	var libraryID string
	var err error
	if p.EpisodeID != "" {
		err = h.db.QueryRowContext(ctx, `SELECT li.libraryId FROM podcastEpisodes pe JOIN libraryItems li ON li.mediaId = pe.podcastId WHERE pe.id = ?`, p.EpisodeID).Scan(&libraryID)
	} else {
		err = h.db.QueryRowContext(ctx, `SELECT libraryId FROM libraryItems WHERE id = ?`, p.LibraryItemID).Scan(&libraryID)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return wire.MediaProgress{}, err
	}
	if libraryID != "" && !access.For(ctx, user).Library(libraryID) {
		return wire.MediaProgress{}, &progressdto.StatusError{Status: http.StatusForbidden, Message: "Forbidden"}
	}
	return progressdto.Apply(ctx, h.db, user.ID, p)
}

// saveBookmark creates (POST) or retitles (PATCH) a bookmark of a library item.
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

// requireItemAccess answers 404 for a missing item and 403 for one the user
// cannot read.
func (h *Handler) requireItemAccess(r *http.Request, user store.User, itemID string) error {
	access, err := h.canAccessItem(r.Context(), user, itemID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !access.exists {
		return httputil.Status(404, "")
	}
	if !access.allowed {
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
	userIDs, err := h.userIDs(r.Context())
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	listed := make([]any, 0, len(userIDs))
	for _, id := range userIDs {
		u, err := h.userByID(r.Context(), id)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		listed = append(listed, users.ListedUserDTO(u, current.Type != "root"))
	}
	return httputil.JSON(w, r, map[string]any{"users": listed})
}

// userIDs lists every user id. The pool uses one connection, so each user is
// loaded after this cursor closes.
func (h *Handler) userIDs(ctx context.Context) ([]string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var userIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, id)
	}
	return userIDs, rows.Err()
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
	var exists int
	err = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if exists > 0 {
		return httputil.Status(400, "Username already taken")
	}
	permissions := access.Defaults(typ)
	patch, _ := body["permissions"].(map[string]any)
	access.Patch(permissions, typ, patch, body)
	encodedPerms, _ := json.Marshal(permissions)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
	if err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	userID, err := ids.New()
	if err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	email, _ := body["email"].(string)
	// Upstream user create via Sequelize omits undefined fields; an absent
	// email is stored as SQL NULL (models/User.js), and the browser DTO then
	// serializes it as null.
	var emailValue any = nil
	if email != "" {
		emailValue = email
	}
	active, _ := body["isActive"].(bool)
	now := dbtime.Now()
	token, err := h.creds.IssueLegacyToken(userID, username)
	if err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO users (id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, userID, username, emailValue, string(hash), typ, token, active, false, nil, string(encodedPerms), `[]`, users.NewUserExtraData(""), now, now); err != nil {
		return httputil.Internal("Failed to save new user", err)
	}
	newUser, err := h.userByID(r.Context(), userID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	serialized := users.UserDTO(newUser, []any{})
	if h.emit != nil {
		h.emit("admin", "user_added", serialized)
	}
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
	if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET extraData=?,updatedAt=? WHERE id=?`, string(encoded), dbtime.Now(), id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	u.ExtraData = encoded
	if h.emit != nil {
		h.emit(current.ID, "user_updated", users.UserDTO(u, []any{}))
	}
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
	if err := h.decorateAdminProgress(r.Context(), progress); err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, users.UserDTO(u, progress))
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
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id); err != nil {
		return httputil.Internal("database write failed", err)
	}
	if err = tx.Commit(); err != nil {
		return httputil.Internal("database write failed", err)
	}
	logging.Infof("[UserController] Updated %d playback sessions to remove user id", sessionsUpdated) // UserController.js:398
	h.notifyUserChanged(r.Context(), id)
	if h.emit != nil {
		h.emit("admin", "user_removed", users.UserDTO(u, []any{}))
	}
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
	emailChanged := false
	if name, ok := body["username"].(string); ok && name != "" && name != u.Username {
		var exists int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE username=? AND id<>?`, name, id).Scan(&exists); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if exists != 0 {
			return httputil.Status(400, "Username already taken")
		}
		u.Username = name
		token, err := h.creds.IssueLegacyToken(u.ID, u.Username)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		u.Token = token
		credentialsChanged = true
	}
	if password, ok := body["password"].(string); ok && password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		u.Password = string(hash)
		credentialsChanged = true
	}
	if email, ok := body["email"].(string); ok && email != "" {
		u.Email = email
		emailChanged = true
	} else if email, exists := body["email"]; exists && (email == nil || email == "") {
		// Upstream PATCH with null/empty email sets the column to SQL NULL
		// (models/User.js); the browser DTO then serializes null.
		u.Email = ""
		emailChanged = true
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
	now := dbtime.Now()
	var writeErr error
	// The users row email must not be rewritten when the patch does not
	// touch it: an absent-email row would otherwise flip SQL NULL to '':
	// Sequelize update() only writes mutated attributes.
	if emailChanged {
		_, writeErr = h.db.ExecContext(r.Context(), `UPDATE users SET username=?,email=?,pash=?,token=?,type=?,isActive=?,permissions=?,updatedAt=? WHERE id=?`, u.Username, store.NullIfEmpty(u.Email), u.Password, u.Token, u.Type, u.IsActive, string(encoded), now, u.ID)
	} else {
		_, writeErr = h.db.ExecContext(r.Context(), `UPDATE users SET username=?,pash=?,token=?,type=?,isActive=?,permissions=?,updatedAt=? WHERE id=?`, u.Username, u.Password, u.Token, u.Type, u.IsActive, string(encoded), now, u.ID)
	}
	if writeErr != nil {
		return httputil.Internal("database write failed", writeErr)
	}
	if credentialsChanged {
		_, refresh, err := h.creds.InvalidateUserSessions(r.Context(), u.ID, refreshTokenFromRequest(r))
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		if refresh != "" && r.Header.Get("x-refresh-token") == "" {
			h.creds.SetRefreshTokenCookie(w, r, refresh)
		}
	}
	h.notifyUserChanged(r.Context(), u.ID)
	u.Permissions = encoded
	serialized := users.UserDTO(u, []any{})
	if h.emit != nil {
		h.emit(current.ID, "user_updated", serialized)
	}
	return httputil.JSON(w, r, map[string]any{"success": true, "user": serialized})
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request, current store.User) error {
	keys, err := h.apiKeys(r.Context())
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"apiKeys": keys})
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
	if body["expiresIn"] != nil && (expiresIn <= 0) {
		return httputil.Status(400, "")
	}
	keyID, err := ids.New()
	if err != nil {
		return httputil.Internal("API key creation failed", err)
	}
	now := time.Now().UTC()
	var expires any
	if expiresIn > 0 {
		expires = dbtime.Format(now.Add(time.Duration(expiresIn * float64(time.Second))))
	}
	var expiresAtUnix int64
	if expiresIn > 0 {
		expiresAtUnix = now.Unix() + int64(expiresIn)
	}
	token, err := h.creds.IssueAPIKeyToken(keyID, name, expiresAtUnix)
	if err != nil {
		return httputil.Internal("API key creation failed", err)
	}
	active, _ := body["isActive"].(bool)
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO apiKeys (id,name,description,expiresAt,lastUsedAt,isActive,permissions,userId,createdByUserId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, keyID, name, nil, expires, nil, active, nil, userID, current.ID, dbtime.Format(now), dbtime.Format(now)); err != nil {
		return httputil.Internal("API key creation failed", err)
	}
	entry, err := h.apiKey(r.Context(), keyID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	// Upstream create spreads apiKey.toJSON() from a freshly built instance
	// (ApiKeyController.js:100): unpersisted attributes (description,
	// lastUsedAt, permissions) are undefined and dropped; createdByUserId
	// and user remain. The plain instance JSON has no createdByUser field.
	delete(entry, "description")
	delete(entry, "lastUsedAt")
	delete(entry, "permissions")
	delete(entry, "createdByUser")
	entry["apiKey"] = token
	return httputil.JSON(w, r, map[string]any{"apiKey": entry})
}

func (h *Handler) deleteAPIKey(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM apiKeys WHERE id=?`, id)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

func (h *Handler) updateAPIKey(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	var ownerID, ownerType string
	err := h.db.QueryRowContext(r.Context(), `SELECT u.id,u.type FROM apiKeys a JOIN users u ON u.id=a.userId WHERE a.id=?`, id).Scan(&ownerID, &ownerType)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if ownerType == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "")
	}
	var newOwner any = ownerID
	var active any
	setActive := false
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
		newOwner = newID
	}
	if val, ok := body["isActive"]; ok {
		b, ok := val.(bool)
		if !ok {
			return httputil.Status(400, "")
		}
		active = b
		setActive = true
	}
	if setActive {
		_, err = h.db.ExecContext(r.Context(), `UPDATE apiKeys SET userId=?,isActive=?,updatedAt=? WHERE id=?`, newOwner, active, dbtime.Now(), id)
	} else {
		_, err = h.db.ExecContext(r.Context(), `UPDATE apiKeys SET userId=?,updatedAt=? WHERE id=?`, newOwner, dbtime.Now(), id)
	}
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	key, err := h.apiKey(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	// Upstream update spreads apiKey.toJSON() from the instance it mutated
	// (ApiKeyController.js:~100): the plain instance JSON has no
	// createdByUser field, and lastUsedAt is the value as-loaded (the auth
	// usage that happened between operations does not refresh it).
	delete(key, "createdByUser")
	return httputil.JSON(w, r, map[string]any{"apiKey": key})
}

func (h *Handler) apiKeys(ctx context.Context) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id FROM apiKeys`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The pool uses one connection; nested queries must run only after the
	// outer cursor closes (see internal/scanner watchedLibraries comment).
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entry, err := h.apiKey(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}
func (h *Handler) apiKey(ctx context.Context, id string) (map[string]any, error) {
	var name, description sql.NullString
	var expires, lastUsed, created, updated any
	var active int
	var permissionsRaw []byte
	var userID, creatorID sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT name,description,expiresAt,lastUsedAt,isActive,permissions,userId,createdByUserId,createdAt,updatedAt FROM apiKeys WHERE id=?`, id).Scan(&name, &description, &expires, &lastUsed, &active, &permissionsRaw, &userID, &creatorID, &created, &updated)
	if err != nil {
		return nil, err
	}
	var perms any
	if len(permissionsRaw) > 0 {
		_ = json.Unmarshal(permissionsRaw, &perms)
	}
	user := map[string]any(nil)
	creator := map[string]any(nil)
	if userID.Valid {
		if u, e := h.userByID(ctx, userID.String); e == nil {
			user = map[string]any{"id": u.ID, "username": u.Username, "type": u.Type}
		}
	}
	if creatorID.Valid {
		if u, e := h.userByID(ctx, creatorID.String); e == nil {
			creator = map[string]any{"id": u.ID, "username": u.Username, "type": u.Type}
		}
	}
	return map[string]any{"id": id, "name": name.String, "description": store.NullString(description), "expiresAt": sequelizeISOString(expires), "lastUsedAt": sequelizeISOString(lastUsed), "isActive": active != 0, "permissions": perms, "userId": store.NullString(userID), "createdByUserId": store.NullString(creatorID), "createdAt": sequelizeISOString(created), "updatedAt": sequelizeISOString(updated), "user": user, "createdByUser": creator}, nil
}

// sequelizeISOString mirrors Sequelize model serialization of DATE attributes
// (apiKey.toJSON() in ApiKeyController.js): ISO-8601 UTC strings with
// millisecond precision, or null when the attribute is null.
func sequelizeISOString(v any) any {
	var t time.Time
	switch value := v.(type) {
	case nil:
		return nil
	case time.Time:
		t = value
	case int64:
		t = time.UnixMilli(value)
	case float64:
		t = time.UnixMilli(int64(value))
	case []byte:
		return sequelizeISOString(parseDateBytes(value))
	case string:
		return sequelizeISOString(parseDateString(value))
	default:
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// parseDateBytes/parseDateString decode the SQLite driver DATETIME formats the
// identity/userapi layer already handles elsewhere.
func parseDateBytes(raw []byte) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	return parseDateString(string(raw))
}

func parseDateString(value string) time.Time {
	if t, ok := dbtime.Parse(value); ok {
		return t
	}
	// A zone-less ISO spelling dbtime does not know.
	t, _ := time.Parse("2006-01-02T15:04:05.999999999", value)
	return t
}

type itemAccess struct{ exists, allowed bool }

// CanReadItem exposes the existing per-user item ACL decision to the list route owner.
func (h *Handler) CanReadItem(ctx context.Context, user store.User, itemID string) (bool, error) {
	ia, err := h.canAccessItem(ctx, user, itemID)
	return ia.allowed, err
}

func (h *Handler) canAccessItem(ctx context.Context, user store.User, itemID string) (itemAccess, error) {
	a, err := libitem.LoadAccess(ctx, h.db, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return itemAccess{}, nil
	}
	if err != nil {
		return itemAccess{}, err
	}
	return itemAccess{exists: true, allowed: access.For(ctx, user).Item(a)}, nil
}

// decorateAdminProgress adds the media fields UserController.findOne attaches
// to each progress (server/controllers/UserController.js:86-98): displayTitle,
// plus displaySubtitle for episodes, coverPath and mediaUpdatedAt (a JS Date,
// so an ISO string). Keys stay absent when the media row is missing.
func (h *Handler) decorateAdminProgress(ctx context.Context, progress []any) error {
	for _, raw := range progress {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		mediaItemID, _ := p["mediaItemId"].(string)
		var title, subtitle, cover sql.NullString
		var updated any
		var err error
		if p["mediaItemType"] == "podcastEpisode" {
			err = h.db.QueryRowContext(ctx, `SELECT pe.title, po.title, po.coverPath, po.updatedAt FROM podcastEpisodes pe LEFT JOIN podcasts po ON po.id = pe.podcastId WHERE pe.id = ?`, mediaItemID).Scan(&title, &subtitle, &cover, &updated)
		} else {
			err = h.db.QueryRowContext(ctx, `SELECT title, NULL, coverPath, updatedAt FROM books WHERE id = ?`, mediaItemID).Scan(&title, &subtitle, &cover, &updated)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		p["displayTitle"] = store.NullString(title)
		if p["mediaItemType"] == "podcastEpisode" {
			p["displaySubtitle"] = store.NullString(subtitle)
		}
		p["coverPath"] = store.NullString(cover)
		p["mediaUpdatedAt"] = nil
		switch v := updated.(type) {
		case time.Time:
			p["mediaUpdatedAt"] = dbtime.JSDate(v)
		case string:
			if t, ok := dbtime.Parse(v); ok {
				p["mediaUpdatedAt"] = dbtime.JSDate(t)
			}
		case []byte:
			if t, ok := dbtime.Parse(string(v)); ok {
				p["mediaUpdatedAt"] = dbtime.JSDate(t)
			}
		}
	}
	return nil
}

func (h *Handler) userPayload(ctx context.Context, user store.User) (map[string]any, error) {
	progress, err := progressdto.Read(ctx, h.db, user.ID)
	if err != nil {
		return nil, err
	}
	return users.UserDTO(user, progress), nil
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	row, err := sqlc.New(h.db).GetUserByID(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	return store.UserFromRow(row)
}

func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

func withUser(fn func(http.ResponseWriter, *http.Request, store.User) error) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error { return fn(w, r, requestUser(r)) }
}

// adminOnly is withUser for routes only root and admin users may call.
func adminOnly(fn func(http.ResponseWriter, *http.Request, store.User) error) httputil.Handler {
	return withUser(func(w http.ResponseWriter, r *http.Request, user store.User) error {
		if !access.AdminUser(user) {
			return httputil.Status(403, "")
		}
		return fn(w, r, user)
	})
}

func (h *Handler) emitUserUpdate(ctx context.Context, user store.User) {
	if h.emit == nil {
		return
	}
	payload, err := h.userPayload(ctx, user)
	if err == nil {
		h.emit(user.ID, "user_updated", payload)
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
