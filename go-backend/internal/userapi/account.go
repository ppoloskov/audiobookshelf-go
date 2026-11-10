package userapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request, user store.User) error {
	payload, err := h.userPayload(r.Context(), user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, payload)
}

func (h *Handler) getOnlineUsers(w http.ResponseWriter, r *http.Request, _ store.User) error {
	users := []map[string]any{}
	var sessions any = []map[string]any{}
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
	for _, field := range []struct{ key, message string }{
		{"email", "Invalid email"},
		{"username", "Invalid username"},
		{"type", "Invalid account type"},
	} {
		if value, exists := body[field.key]; exists && value != nil && value != "" {
			if _, ok := value.(string); !ok {
				return httputil.Status(400, field.message)
			}
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
