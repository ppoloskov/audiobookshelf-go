// Package userapi contains a narrow SQLite-backed implementation of the
// user-facing API routes that can be reproduced without the Node managers.
package userapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
	"golang.org/x/crypto/bcrypt"
)

// queries returns the generated query set on the handler's pool; sqlc.New only
// stores the handle.
func (h *Handler) queries() *sqlc.Queries { return sqlc.New(h.db) }

type Handler struct {
	db                 *sql.DB
	tokenSecret        []byte
	emit               events.Emitter
	items              items.Reader
	invalidateSessions func(context.Context, string, string) (string, string, error)
	changePassword     func(context.Context, string, string, string) error
	setRefreshCookie   func(http.ResponseWriter, *http.Request, string)
	usersOnline        func() []map[string]any
	openSessions       func() []map[string]any
}

// New takes the token secret identity resolved at startup, so both packages
// sign with the same key.
func New(db *sql.DB, tokenSecret []byte, emit func(userID, event string, payload any), itemReader items.Reader) (*Handler, error) {
	if db == nil {
		return nil, errors.New("userapi: database is nil")
	}
	if itemReader == nil {
		return nil, errors.New("userapi: item reader is nil")
	}
	return &Handler{db: db, tokenSecret: tokenSecret, emit: events.Emitter(emit), items: itemReader}, nil
}

func (h *Handler) SetCredentialHooks(invalidate func(context.Context, string, string) (string, string, error), changePassword func(context.Context, string, string, string) error, setCookie func(http.ResponseWriter, *http.Request, string)) {
	h.invalidateSessions, h.changePassword, h.setRefreshCookie = invalidate, changePassword, setCookie
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
	progress, err := h.progressRows(r.Context(), user.ID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	serialized, err := h.browserUserDB(r.Context(), user, progress, jsonArray(user.Bookmarks))
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, serialized)
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
		return plainError(w, "Missing or invalid password or new password", 400)
	}
	oldRaw, hasOld := body["password"]
	newRaw, hasNew := body["newPassword"]
	if !hasOld || !hasNew {
		return plainError(w, "Missing or invalid password or new password", 400)
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
		return plainError(w, "Missing or invalid password or new password", 400)
	}
	if h.changePassword == nil || h.invalidateSessions == nil {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err := h.changePassword(r.Context(), user.ID, oldPassword, newPassword); err != nil {
		logging.Errorf("[LocalAuth] User %q failed to change password: %v", user.Username, err) // LocalAuthStrategy.js:178
		return plainError(w, err.Error(), 400)
	}
	logging.Infof("[LocalAuth] User %q changed password", user.Username) // LocalAuthStrategy.js:173
	accessToken, refreshToken, err := h.invalidateSessions(r.Context(), user.ID, refreshTokenFromRequest(r))
	if err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	if refreshToken != "" && r.Header.Get("x-refresh-token") != "" {
		return httputil.JSON(w, r, map[string]any{"success": true, "user": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken}})
	}
	if refreshToken != "" && h.setRefreshCookie != nil {
		h.setRefreshCookie(w, r, refreshToken)
	}
	return httputil.OK(w, r)
}

func (h *Handler) getAllProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	progress, err := h.progressRows(r.Context(), user.ID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"mediaProgress": progress})
}

func (h *Handler) getBookmarks(w http.ResponseWriter, r *http.Request, user store.User) error {
	libraryItemID := r.PathValue("id")
	bookmarks := jsonArray(user.Bookmarks)
	if libraryItemID == "" {
		return httputil.JSON(w, r, map[string]any{"bookmarks": bookmarks})
	}
	access, err := h.canAccessItem(r.Context(), user, libraryItemID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database write failed", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httputil.Status(404, "")
	}
	h.emitUserUpdate(r.Context(), user)
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) getProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	p, err := progressdto.ReadOne(r.Context(), h.db, user.ID, r.PathValue("id"), r.PathValue("episodeId"))
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if p == nil {
		return httputil.Status(404, "")
	}
	return httputil.JSON(w, r, p)
}

func (h *Handler) deleteProgress(w http.ResponseWriter, r *http.Request, user store.User) error {
	res, err := sqlc.New(h.db).DeleteMediaProgressForUser(r.Context(), sqlc.DeleteMediaProgressForUserParams{ID: r.PathValue("id"), UserID: store.Text(user.ID)})
	if err != nil {
		return writeInternalError(w, r, "database write failed", err)
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
		return plainError(w, "Missing request payload", 400)
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
		return plainError(w, "Invalid request body", 400)
	}
	payload.LibraryItemID, payload.EpisodeID = r.PathValue("id"), r.PathValue("episodeId")
	if _, err := h.updateProgress(r.Context(), user, payload); err != nil {
		var refused *progressdto.StatusError
		if errors.As(err, &refused) {
			// Express res.status(n).send(string): text/html, no trailing newline.
			return httputil.Status(refused.Status, refused.Message)
		}
		logging.RequestError(r, "request failed", err)
		return plainError(w, "database write failed", 500)
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
	if libraryID != "" && !canAccessLibrary(store.DecodeJSONObject(user.Permissions), libraryID) {
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
	bookmarks := jsonArray(user.Bookmarks)
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return plainError(w, "Invalid request body", 400)
	}
	timeValue, ok := finiteNumber(body["time"])
	if !ok {
		return plainError(w, "Invalid time", 400)
	}
	title, ok := body["title"].(string)
	if !ok || title == "" {
		return plainError(w, "Invalid title", 400)
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
		return writeInternalError(w, r, "database write failed", err)
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
	bookmarks := jsonArray(user.Bookmarks)
	timeValue, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil || math.IsNaN(timeValue) || math.IsInf(timeValue, 0) {
		return plainError(w, "Invalid time", 400)
	}
	index := bookmarkIndex(bookmarks, itemID, timeValue)
	if index < 0 {
		return httputil.Status(404, "")
	}
	bookmarks = append(bookmarks[:index], bookmarks[index+1:]...)
	if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
		return writeInternalError(w, r, "database write failed", err)
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
	if !isAdmin(current) {
		return httputil.Status(403, "")
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt FROM users`)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	defer rows.Close()
	users := make([]any, 0)
	hideRootToken := current.Type != "root"
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return writeInternalError(w, r, "database read failed", err)
		}
		users = append(users, minimalBrowserUser(u, hideRootToken))
	}
	if err := rows.Err(); err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"users": users})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	if !isAdmin(current) {
		return httputil.Status(403, "")
	}
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
		return writeInternalError(w, r, "database read failed", err)
	}
	if exists > 0 {
		return httputil.Status(400, "Username already taken")
	}
	permissions := defaultPermissions(typ)
	if reqPerms, ok := body["permissions"].(map[string]any); ok {
		for k, v := range reqPerms {
			if _, exists := permissions[k]; exists {
				if b, ok := v.(bool); ok {
					permissions[k] = b
				}
			}
		}
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		if value, ok := body[key]; ok {
			if validStringArray(value) {
				permissions[key] = value
			}
		} else if reqPerms, ok := body["permissions"].(map[string]any); ok {
			if value, ok := reqPerms[key]; ok && validStringArray(value) {
				permissions[key] = value
			}
		}
	}
	encodedPerms, _ := json.Marshal(permissions)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
	if err != nil {
		return writeInternalError(w, r, "Failed to save new user", err)
	}
	userID, err := ids.New()
	if err != nil {
		return writeInternalError(w, r, "Failed to save new user", err)
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
	token, err := h.signToken(map[string]any{"userId": userID, "username": username})
	if err != nil {
		return writeInternalError(w, r, "Failed to save new user", err)
	}
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO users (id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, userID, username, emailValue, string(hash), typ, token, active, false, nil, string(encodedPerms), `[]`, `{"seriesHideFromContinueListening":[]}`, now, now); err != nil {
		return writeInternalError(w, r, "Failed to save new user", err)
	}
	newUser, err := h.userByID(r.Context(), userID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	serialized, err := h.browserUserDB(r.Context(), newUser, []any{}, []any{})
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
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
	if !isAdmin(current) && current.ID != id {
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
		return writeInternalError(w, r, "database write failed", err)
	}
	u.ExtraData = encoded
	serialized, err := h.browserUserDB(r.Context(), u, []any{}, jsonArray(u.Bookmarks))
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if h.emit != nil {
		h.emit(current.ID, "user_updated", serialized)
	}
	return httputil.OK(w, r)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request, current store.User) error {
	id := r.PathValue("id")
	u, err := h.targetUser(r.Context(), id)
	if err != nil {
		return err
	}
	progress, err := h.progressRows(r.Context(), id)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if err := h.decorateAdminProgress(r.Context(), progress); err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	serialized, err := h.browserUserDB(r.Context(), u, progress, jsonArray(u.Bookmarks))
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	return httputil.JSON(w, r, serialized)
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
		return writeInternalError(w, r, "database write failed", err)
	}
	defer tx.Rollback()
	if err = sqlc.New(tx).DeleteUserPlaylists(r.Context(), id); err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	sessionsUpdated := int64(0)
	if res, execErr := sqlc.New(tx).DetachPlaybackSessionsFromUser(r.Context(), store.Text(id)); execErr != nil {
		return writeInternalError(w, r, "database write failed", execErr)
	} else if n, rowsErr := res.RowsAffected(); rowsErr == nil {
		sessionsUpdated = n
	}
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id); err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	if err = tx.Commit(); err != nil {
		return writeInternalError(w, r, "database write failed", err)
	}
	logging.Infof("[UserController] Updated %d playback sessions to remove user id", sessionsUpdated) // UserController.js:398
	if h.emit != nil {
		h.emit("admin", "user_removed", browserUser(u, []any{}, jsonArray(u.Bookmarks)))
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
		return plainError(w, "Invalid request", 400)
	}
	for _, key := range []string{"id", "pash", "token", "extraData", "bookmarks"} {
		if _, ok := body[key]; ok {
			return plainError(w, `Key "`+key+`" cannot be updated`, 400)
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
			return writeInternalError(w, r, "database read failed", err)
		}
		if exists != 0 {
			return httputil.Status(400, "Username already taken")
		}
		u.Username = name
		token, err := h.signToken(map[string]any{"userId": u.ID, "username": u.Username})
		if err != nil {
			return writeInternalError(w, r, "database write failed", err)
		}
		u.Token = token
		credentialsChanged = true
	}
	if password, ok := body["password"].(string); ok && password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
		if err != nil {
			return writeInternalError(w, r, "database write failed", err)
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
	if patch, ok := body["permissions"].(map[string]any); ok {
		defaults := defaultPermissions(u.Type)
		for key, value := range patch {
			if _, allowed := defaults[key]; allowed {
				if b, ok := value.(bool); ok {
					perms[key] = b
				}
			}
		}
		for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
			if val, ok := patch[key]; ok && validStringArray(val) {
				perms[key] = val
			}
		}
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		if val, ok := body[key]; ok && validStringArray(val) {
			perms[key] = val
		}
	}
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
		return writeInternalError(w, r, "database write failed", writeErr)
	}
	if credentialsChanged && h.invalidateSessions != nil {
		_, refresh, err := h.invalidateSessions(r.Context(), u.ID, refreshTokenFromRequest(r))
		if err != nil {
			return writeInternalError(w, r, "database write failed", err)
		}
		if refresh != "" && r.Header.Get("x-refresh-token") == "" && h.setRefreshCookie != nil {
			h.setRefreshCookie(w, r, refresh)
		}
	}
	u.Permissions = encoded
	serialized, err := h.browserUserDB(r.Context(), u, []any{}, jsonArray(u.Bookmarks))
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
	}
	if h.emit != nil {
		h.emit(current.ID, "user_updated", serialized)
	}
	return httputil.JSON(w, r, map[string]any{"success": true, "user": serialized})
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request, current store.User) error {
	keys, err := h.apiKeys(r.Context())
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database read failed", err)
	}
	if target.Type == "root" && current.Type != "root" {
		return httputil.Status(403, "")
	}
	expiresIn := number(body["expiresIn"], 0)
	if body["expiresIn"] != nil && (expiresIn <= 0) {
		return httputil.Status(400, "")
	}
	keyID, err := ids.New()
	if err != nil {
		return writeInternalError(w, r, "API key creation failed", err)
	}
	now := time.Now().UTC()
	var expires any
	if expiresIn > 0 {
		expires = dbtime.Format(now.Add(time.Duration(expiresIn * float64(time.Second))))
	}
	tokenClaims := map[string]any{"keyId": keyID, "name": name, "type": "api"}
	if expiresIn > 0 {
		tokenClaims["exp"] = now.Unix() + int64(expiresIn)
	}
	token, err := h.signToken(tokenClaims)
	if err != nil {
		return writeInternalError(w, r, "API key creation failed", err)
	}
	active, _ := body["isActive"].(bool)
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO apiKeys (id,name,description,expiresAt,lastUsedAt,isActive,permissions,userId,createdByUserId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, keyID, name, nil, expires, nil, active, nil, userID, current.ID, dbtime.Format(now), dbtime.Format(now)); err != nil {
		return writeInternalError(w, r, "API key creation failed", err)
	}
	entry, err := h.apiKey(r.Context(), keyID)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database write failed", err)
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
		return writeInternalError(w, r, "database read failed", err)
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
		return writeInternalError(w, r, "database write failed", err)
	}
	key, err := h.apiKey(r.Context(), id)
	if err != nil {
		return writeInternalError(w, r, "database read failed", err)
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

func (h *Handler) signToken(payload map[string]any) (string, error) {
	// jsonwebtoken's default iat is the current Unix time in seconds.
	payload["iat"] = time.Now().Unix()
	return auth.Sign(h.tokenSecret, payload)
}

type itemAccess struct{ exists, allowed bool }

// CanReadItem exposes the existing per-user item ACL decision to the list route owner.
func (h *Handler) CanReadItem(ctx context.Context, user store.User, itemID string) (bool, error) {
	access, err := h.canAccessItem(ctx, user, itemID)
	return access.allowed, err
}

// AllowsLibrary exposes the existing library ACL decision to the list route owner.
func (h *Handler) AllowsLibrary(user store.User, libraryID string) bool {
	return canAccessLibrary(store.DecodeJSONObject(user.Permissions), libraryID)
}

func (h *Handler) canAccessItem(ctx context.Context, user store.User, itemID string) (itemAccess, error) {
	var libraryID, mediaType string
	var explicit int
	var rawTags []byte
	err := h.db.QueryRowContext(ctx, `SELECT li.libraryId, li.mediaType, COALESCE(b.explicit,p.explicit,0), COALESCE(b.tags,p.tags,'[]') FROM libraryItems li LEFT JOIN books b ON li.mediaType='book' AND b.id=li.mediaId LEFT JOIN podcasts p ON li.mediaType='podcast' AND p.id=li.mediaId WHERE li.id=?`, itemID).Scan(&libraryID, &mediaType, &explicit, &rawTags)
	if errors.Is(err, sql.ErrNoRows) {
		return itemAccess{}, nil
	}
	if err != nil {
		return itemAccess{}, err
	}
	perms := store.DecodeJSONObject(user.Permissions)
	if !canAccessLibrary(perms, libraryID) {
		return itemAccess{exists: true}, nil
	}
	if explicit != 0 && !boolValue(perms["accessExplicitContent"]) {
		return itemAccess{exists: true}, nil
	}
	var tags []string
	_ = json.Unmarshal(rawTags, &tags)
	return itemAccess{exists: true, allowed: canAccessTags(perms, tags)}, nil
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

func (h *Handler) progressRows(ctx context.Context, userID string) ([]any, error) {
	return ProgressRows(ctx, h.db, userID)
}

// ProgressRows returns the legacy DTOs for a user's media progress.
func ProgressRows(ctx context.Context, db *sql.DB, userID string) ([]any, error) {
	return progressdto.Read(ctx, db, userID)
}

// BrowserUser serializes a user using the legacy browser shape with progress
// and bookmarks supplied by the caller.
func BrowserUser(user store.User, progress []any, bookmarks []any) map[string]any {
	return browserUser(user, progress, bookmarks)
}

func (h *Handler) userPayload(ctx context.Context, user store.User) (map[string]any, error) {
	progress, err := h.progressRows(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return h.browserUserDB(ctx, user, progress, jsonArray(user.Bookmarks))
}

func (h *Handler) browserUserDB(ctx context.Context, user store.User, progress, bookmarks []any) (map[string]any, error) {
	result := browserUser(user, progress, bookmarks)
	var email sql.NullString
	if err := h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id=?`, user.ID).Scan(&email); err != nil {
		return nil, err
	}
	result["email"] = store.NullString(email)
	return result, nil
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	return scanUser(h.db.QueryRowContext(ctx, `SELECT id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt FROM users WHERE id=?`, id))
}

func scanUser(row rowScanner) (store.User, error) {
	var u store.User
	var email, pash, typ, token sql.NullString
	var active, locked int
	var lastSeen, createdAt, updatedAt any
	var permissionsRaw, bookmarksRaw, extraRaw []byte
	if err := row.Scan(&u.ID, &u.Username, &email, &pash, &typ, &token, &active, &locked, &lastSeen, &permissionsRaw, &bookmarksRaw, &extraRaw, &createdAt, &updatedAt); err != nil {
		return store.User{}, err
	}
	u.Email, u.Password, u.Type, u.Token = email.String, pash.String, typ.String, token.String
	u.IsActive, u.IsLocked = active != 0, locked != 0
	u.LastSeen, u.CreatedAt, u.UpdatedAt = int64Pointer(dbtime.Millis(lastSeen)), int64Pointer(dbtime.Millis(createdAt)), int64Pointer(dbtime.Millis(updatedAt))
	u.Permissions, u.Bookmarks, u.ExtraData = permissionsRaw, bookmarksRaw, extraRaw
	return u, nil
}

func int64Pointer(value any) *int64 {
	n, ok := value.(int64)
	if !ok {
		return nil
	}
	return &n
}
func isAdmin(user store.User) bool { return user.Type == "root" || user.Type == "admin" }

func minimalBrowserUser(user store.User, hideRootToken bool) map[string]any {
	permissionsMap := store.DecodeJSONObject(user.Permissions)
	libraries, tags := jsonvalue.Strings(permissionsMap["librariesAccessible"]), jsonvalue.Strings(permissionsMap["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissionsMap, "librariesAccessible")
	delete(permissionsMap, "itemTagsSelected")
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	series := any([]any{})
	if extra != nil && extra["seriesHideFromContinueListening"] != nil {
		series = extra["seriesHideFromContinueListening"]
	}
	token := user.Token
	if user.Type == "root" && hideRootToken {
		token = ""
	}
	var lastSeen, createdAt any
	if user.LastSeen != nil {
		lastSeen = *user.LastSeen
	}
	if user.CreatedAt != nil {
		createdAt = *user.CreatedAt
	}
	return map[string]any{"id": user.ID, "username": user.Username, "email": store.NullIfEmpty(user.Email), "type": user.Type, "token": token, "seriesHideFromContinueListening": series, "isActive": user.IsActive, "isLocked": user.IsLocked, "lastSeen": lastSeen, "createdAt": createdAt, "permissions": permissionsMap, "librariesAccessible": libraries, "itemTagsSelected": tags, "hasOpenIDLink": extra != nil && extra["authOpenIDSub"] != nil}
}

func defaultPermissions(userType string) map[string]any {
	return map[string]any{"download": true, "update": userType == "root" || userType == "admin", "delete": userType == "root", "upload": userType == "root" || userType == "admin", "createEreader": userType == "root" || userType == "admin", "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": userType == "root" || userType == "admin", "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
}

func validStringArray(value any) bool {
	switch a := value.(type) {
	case []any:
		for _, v := range a {
			if _, ok := v.(string); !ok {
				return false
			}
		}
		return true
	case []string:
		return true
	default:
		return false
	}
}

// writeInternalError answers a failed database operation with net/http's
// plain-text 500 (the framing clients already see) and logs the cause. It
// returns nil because the response is written.
func writeInternalError(w http.ResponseWriter, r *http.Request, message string, err error) error {
	logging.RequestError(r, message, err)
	return plainError(w, message, http.StatusInternalServerError)
}

// plainError answers with net/http's text/plain framing, which the validation
// messages of these routes have always used. It returns nil: the response is written.
func plainError(w http.ResponseWriter, message string, status int) error {
	http.Error(w, message, status)
	return nil
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
		if !isAdmin(user) {
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

type rowScanner interface{ Scan(...any) error }

func browserUser(user store.User, progress []any, bookmarks []any) map[string]any {
	var permissionsMap map[string]any
	_ = json.Unmarshal(user.Permissions, &permissionsMap)
	if permissionsMap == nil {
		permissionsMap = map[string]any{}
	}
	// User.js:604-606/629-630: librariesAccessible and itemTagsSelected are
	// always serialized as arrays.
	libraries := jsonvalue.Strings(permissionsMap["librariesAccessible"])
	tags := jsonvalue.Strings(permissionsMap["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissionsMap, "librariesAccessible")
	delete(permissionsMap, "itemTagsSelected")
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	series := any([]any{})
	if extra != nil && extra["seriesHideFromContinueListening"] != nil {
		series = extra["seriesHideFromContinueListening"]
	}
	return map[string]any{"id": user.ID, "username": user.Username, "email": store.NullIfEmpty(user.Email), "type": user.Type, "token": user.Token, "mediaProgress": progress, "seriesHideFromContinueListening": series, "bookmarks": bookmarks, "isActive": user.IsActive, "isLocked": user.IsLocked, "lastSeen": user.LastSeen, "createdAt": user.CreatedAt, "permissions": permissionsMap, "librariesAccessible": libraries, "itemTagsSelected": tags, "hasOpenIDLink": extra != nil && extra["authOpenIDSub"] != nil && extra["authOpenIDSub"] != ""}
}

func jsonArray(raw json.RawMessage) []any {
	var out []any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		return []any{}
	}
	return out
}
func canAccessLibrary(p map[string]any, id string) bool {
	if boolValue(p["accessAllLibraries"]) {
		return true
	}
	for _, lib := range jsonvalue.Strings(p["librariesAccessible"]) {
		if lib == id {
			return true
		}
	}
	return false
}
func canAccessTags(p map[string]any, tags []string) bool {
	if boolValue(p["accessAllTags"]) {
		return true
	}
	selected := jsonvalue.Strings(p["itemTagsSelected"])
	inverted := boolValue(p["selectedTagsNotAccessible"])
	if len(tags) == 0 {
		return inverted
	}
	if inverted {
		for _, tag := range tags {
			for _, sel := range selected {
				if tag == sel {
					return false
				}
			}
		}
		return true
	}
	for _, tag := range tags {
		for _, sel := range selected {
			if tag == sel {
				return true
			}
		}
	}
	return false
}
func boolValue(v any) bool { b, _ := v.(bool); return b }
func jsonNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
func number(v any, fallback float64) float64 {
	if n, ok := jsonNumber(v); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
		return n
	}
	return fallback
}
func finiteNumber(v any) (float64, bool) {
	n, ok := jsonNumber(v)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func bookmarkIndex(bookmarks []any, item string, t float64) int {
	for i, v := range bookmarks {
		if bm, ok := v.(map[string]any); ok && bm["libraryItemId"] == item {
			if n, ok := jsonNumber(bm["time"]); ok && n == t {
				return i
			}
		}
	}
	return -1
}
