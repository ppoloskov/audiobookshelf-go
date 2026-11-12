package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/playback"
)

// userRequest is the body of POST /api/users and PATCH /api/users/{id}; see parseUser.
type userRequest struct {
	Username            *string        `json:"username"`
	Password            *string        `json:"password"`
	Email               *string        `json:"email"`
	Type                *string        `json:"type"`
	IsActive            *bool          `json:"isActive"`
	Permissions         db.Permissions `json:"permissions"`
	LibrariesAccessible []string       `json:"librariesAccessible"`
	ItemTagsSelected    []string       `json:"itemTagsSelected"`
}

// readOnlyKeys are never updatable (UserController.js update).
var readOnlyKeys = []string{"id", "pash", "token", "extraData", "bookmarks"}

// parseUser reads a request onto base, the permissions it patches (a wrong type or a read-only key is a 400).
func parseUser(raw json.RawMessage, base db.Permissions) (userRequest, error) {
	var keys map[string]json.RawMessage
	req := userRequest{Permissions: base}
	if json.Unmarshal(raw, &keys) != nil || json.Unmarshal(raw, &req) != nil {
		return req, httpx.BadRequest("Invalid request")
	}
	for _, key := range readOnlyKeys {
		if _, sent := keys[key]; sent {
			return req, httpx.BadRequest(`Key "` + key + `" cannot be updated`)
		}
	}
	for _, f := range []struct {
		top []string
		to  *[]string
	}{
		{req.LibrariesAccessible, &req.Permissions.LibrariesAccessible}, {req.ItemTagsSelected, &req.Permissions.ItemTagsSelected},
	} {
		if f.top != nil {
			*f.to = f.top
		}
		if *f.to == nil {
			*f.to = []string{} // stored as [], never null
		}
	}
	return req, nil
}

var accountTypes = []string{"user", "admin", "guest"}

func (h *Handler) userRow(ctx context.Context, id string) (db.User, error) {
	var u db.User
	err := h.db.Get(ctx, &u, `SELECT * FROM users WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return u, httpx.NotFound()
	}
	return u, err
}

func onlyRootTouchesRoot(me *auth.Principal, target db.User) error {
	if db.Val(target.Type) == "root" && !me.IsRoot() {
		return httpx.Forbidden()
	}
	return nil
}

// listedUser is a user of the list: no progress or bookmarks.
type listedUser struct {
	auth.UserView
	MediaProgress *struct{} `json:"mediaProgress,omitempty"`
	Bookmarks     *struct{} `json:"bookmarks,omitempty"`
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	var rows []db.User
	if err := h.db.Select(r.Context(), &rows, `SELECT * FROM users`); err != nil {
		return err
	}
	users := make([]listedUser, len(rows))
	for i, row := range rows {
		users[i].UserView = auth.ViewOf(row)
		if db.Val(row.Type) == "root" && !me.IsRoot() {
			users[i].Token = db.Ptr("")
		}
	}
	return httpx.JSON(w, r, wrap("users", users))
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	raw, err := httpx.Decode[json.RawMessage](w, r)
	if err != nil {
		return err
	}
	req, err := parseUser(raw, db.Permissions{})
	if err != nil {
		return err
	}
	username, password, kind := db.Val(req.Username), db.Val(req.Password), db.Val(req.Type)
	if username == "" || password == "" {
		return httpx.BadRequest("Username and password are required")
	}
	if kind == "" {
		kind = "user"
	}
	if !slices.Contains(accountTypes, kind) {
		return httpx.BadRequest("Invalid account type")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if req, err = parseUser(raw, auth.DefaultPermissions(kind)); err != nil { // the defaults depend on the type
		return err
	}
	var created db.User
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		if err := h.checkUsername(ctx, username, ""); err != nil {
			return err
		}
		var err error
		created, err = h.auth.CreateUser(ctx, auth.NewUser{Username: username, Email: req.Email, PasswordHash: &hash, Type: kind,
			Active: db.Val(req.IsActive), Permissions: &req.Permissions})
		return err
	})
	if err != nil {
		return err
	}
	view := auth.ViewOf(created)
	h.emit.ToAdmins("user_added", view)
	return httpx.JSON(w, r, wrap("user", view))
}

func (h *Handler) checkUsername(ctx context.Context, name, exceptID string) error {
	var n int
	if err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM users WHERE username = ? AND id <> ?`, name, exceptID); err != nil {
		return err
	}
	if n > 0 {
		return httpx.BadRequest("Username already taken")
	}
	return nil
}

func (h *Handler) onlineUsers(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	return httpx.JSON(w, r, struct {
		UsersOnline  []map[string]any `json:"usersOnline"`
		OpenSessions any              `json:"openSessions"` // the playback sessions' own view
	}{h.deps.UsersOnline(), h.playback.OpenSessions()})
}

type mediaInfo struct {
	DisplayTitle    *string `json:"displayTitle"`
	DisplaySubtitle *string `json:"displaySubtitle,omitempty"`
	CoverPath       *string `json:"coverPath"`
	MediaUpdatedAt  *string `json:"mediaUpdatedAt"`
}

type adminProgress struct {
	playback.ProgressView
	*mediaInfo
}

func (h *Handler) withMedia(ctx context.Context, progress []playback.ProgressView) ([]adminProgress, error) {
	out := make([]adminProgress, len(progress))
	for i, p := range progress {
		out[i].ProgressView = p
		var row struct {
			Title     *string `db:"title"`
			Subtitle  *string `db:"subtitle"`
			CoverPath *string `db:"coverPath"`
			UpdatedAt db.Time `db:"updatedAt"`
		}
		query := `SELECT title, NULL AS subtitle, coverPath, updatedAt FROM books WHERE id = ?`
		if p.MediaItemType == "podcastEpisode" {
			query = `SELECT pe.title AS title, po.title AS subtitle, po.coverPath AS coverPath, po.updatedAt AS updatedAt
FROM podcastEpisodes pe LEFT JOIN podcasts po ON po.id = pe.podcastId WHERE pe.id = ?`
		}
		err := h.db.Get(ctx, &row, query, p.MediaItemID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[i].mediaInfo = &mediaInfo{row.Title, row.Subtitle, row.CoverPath, iso(row.UpdatedAt)}
	}
	return out, nil
}

func iso(t db.Time) *string {
	if t.IsZero() {
		return nil
	}
	return db.Ptr(t.UTC().Format("2006-01-02T15:04:05.000Z"))
}

type userDetail struct {
	auth.UserView
	MediaProgress []adminProgress `json:"mediaProgress"`
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	ctx := r.Context()
	target, err := h.userRow(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	progress, err := h.playback.Progress().Read(ctx, target.ID)
	if err != nil {
		return err
	}
	decorated, err := h.withMedia(ctx, progress)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, userDetail{auth.ViewOf(target), decorated})
}

// deleteUser is DELETE /api/users/{id}: not the acting admin, not root; playlists go, sessions stay unowned.
func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	target, err := h.userRow(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if me.ID == target.ID {
		slog.Error("admin attempted to delete self", "username", db.Val(me.Username))
		return httpx.Error(http.StatusBadRequest, "")
	}
	if db.Val(target.Type) == "root" {
		slog.Error("admin attempted to delete root user", "username", db.Val(me.Username))
		return httpx.Forbidden()
	}
	var detached int64
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if _, err := h.db.Exec(ctx, `DELETE FROM playlists WHERE userId = ?`, target.ID); err != nil {
			return err
		}
		result, err := h.db.Exec(ctx, `UPDATE playbackSessions SET userId = NULL WHERE userId = ?`, target.ID)
		if err != nil {
			return err
		}
		detached, _ = result.RowsAffected() // only logged
		_, err = h.db.Exec(ctx, `DELETE FROM users WHERE id = ?`, target.ID)
		return err
	})
	if err != nil {
		return err
	}
	slog.Info("removed user from playback sessions", "sessions", detached, "user", target.ID)
	h.userChanged(ctx, target.ID)
	h.emit.ToAdmins("user_removed", auth.ViewOf(target))
	return httpx.JSON(w, r, map[string]bool{"success": true})
}

// updateUser is PATCH /api/users/{id}: only root edits root; user_updated goes to the acting admin.
func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	raw, err := httpx.Decode[json.RawMessage](w, r)
	if err != nil {
		return err
	}
	credentialsChanged := false
	updated, _, err := h.editUser(ctx, r.PathValue("id"), func(ctx context.Context, u *db.User) (err error) {
		if err := onlyRootTouchesRoot(me, *u); err != nil {
			return err
		}
		credentialsChanged, err = h.apply(ctx, u, raw)
		return err
	}, "username", "email", "pash", "token", "type", "isActive", "permissions")
	if err != nil {
		return err
	}
	if credentialsChanged {
		_, refresh, err := h.auth.InvalidateUserSessions(ctx, updated.ID, refreshToken(r))
		if err != nil {
			return err
		}
		if refresh != "" && r.Header.Get("x-refresh-token") == "" {
			h.auth.SetRefreshTokenCookie(w, r, refresh)
		}
	}
	h.userChanged(ctx, updated.ID)
	view := auth.ViewOf(updated)
	h.emit.ToUser(me.ID, "user_updated", view)
	return httpx.JSON(w, r, struct {
		Success bool          `json:"success"`
		User    auth.UserView `json:"user"`
	}{true, view})
}

func (h *Handler) apply(ctx context.Context, u *db.User, raw json.RawMessage) (credentialsChanged bool, err error) {
	req, err := parseUser(raw, u.Permissions.V)
	if err != nil {
		return false, err
	}
	if name := db.Val(req.Username); name != "" && name != db.Val(u.Username) {
		if err := h.checkUsername(ctx, name, u.ID); err != nil {
			return false, err
		}
		token, err := h.auth.LegacyToken(u.ID, name)
		if err != nil {
			return false, err
		}
		u.Username, u.Token, credentialsChanged = &name, &token, true
	}
	if password := db.Val(req.Password); password != "" {
		hash, err := auth.HashPassword(password)
		if err != nil {
			return false, err
		}
		u.Pash, credentialsChanged = &hash, true
	}
	// UserController.js:312: only a non-empty email is applied; null and ""
	// leave the stored value alone.
	if email := db.Val(req.Email); email != "" {
		u.Email = &email
	}
	if kind := db.Val(req.Type); kind != "" && db.Val(u.Type) != "root" {
		if !slices.Contains(accountTypes, kind) {
			return false, httpx.BadRequest("Invalid account type")
		}
		u.Type = &kind
	}
	if req.IsActive != nil {
		u.IsActive = db.Bool(*req.IsActive)
	}
	u.Permissions.V = req.Permissions
	return credentialsChanged, nil
}

func (h *Handler) unlinkOpenID(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	updated, saved, err := h.editUser(r.Context(), r.PathValue("id"), func(_ context.Context, u *db.User) error {
		if err := onlyRootTouchesRoot(me, *u); err != nil {
			return err
		}
		if u.ExtraData.V == nil || u.ExtraData.V.AuthOpenIDSub == nil {
			return errUnchanged
		}
		u.ExtraData.V.AuthOpenIDSub = nil
		return nil
	}, "extraData")
	if err != nil {
		return err
	}
	if saved {
		h.emit.ToUser(me.ID, "user_updated", auth.ViewOf(updated))
	}
	return ok(w)
}
