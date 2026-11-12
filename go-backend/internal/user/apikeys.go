package user

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

type keyUser struct {
	ID       string  `json:"id" db:"id"`
	Username *string `json:"username" db:"username"`
	Type     *string `json:"type" db:"type"`
}

type keyView struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     *string         `json:"description"`
	ExpiresAt       *string         `json:"expiresAt"`
	LastUsedAt      *string         `json:"lastUsedAt"`
	IsActive        bool            `json:"isActive"`
	Permissions     *db.Permissions `json:"permissions"` // null until a flag is set
	UserID          *string         `json:"userId"`
	CreatedByUserID *string         `json:"createdByUserId"`
	CreatedAt       *string         `json:"createdAt"`
	UpdatedAt       *string         `json:"updatedAt"`
	User            *keyUser        `json:"user"`
	CreatedByUser   *keyUser        `json:"createdByUser"`
}

type createdKey struct {
	keyView
	Token string `json:"apiKey"`
}

func (h *Handler) keyViews(ctx context.Context, rows []db.ApiKey) ([]keyView, error) {
	var users []keyUser
	if err := h.db.Select(ctx, &users, `SELECT id, username, type FROM users`); err != nil {
		return nil, err
	}
	byID := make(map[string]*keyUser, len(users))
	for i := range users {
		byID[users[i].ID] = &users[i]
	}
	views := make([]keyView, len(rows))
	for i, row := range rows {
		views[i] = keyView{ID: row.ID, Name: row.Name, Description: row.Description, ExpiresAt: iso(row.ExpiresAt), LastUsedAt: iso(row.LastUsedAt),
			IsActive: bool(row.IsActive), UserID: row.UserID, CreatedByUserID: row.CreatedByUserID,
			CreatedAt: iso(row.CreatedAt), UpdatedAt: iso(row.UpdatedAt), User: byID[db.Val(row.UserID)], CreatedByUser: byID[db.Val(row.CreatedByUserID)]}
		if p := row.Permissions.V; p.Download || p.Update || p.Delete || p.Upload || p.AccessAllLibraries {
			views[i].Permissions = &p
		}
	}
	return views, nil
}

func (h *Handler) keyView(ctx context.Context, key db.ApiKey) (keyView, error) {
	views, err := h.keyViews(ctx, []db.ApiKey{key})
	if err != nil {
		return keyView{}, err
	}
	return views[0], nil
}

func (h *Handler) checkOwner(ctx context.Context, me *auth.Principal, userID string) error {
	var owner db.User
	err := h.db.Get(ctx, &owner, `SELECT * FROM users WHERE id = ?`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusBadRequest, "")
	}
	if err != nil {
		return err
	}
	return onlyRootTouchesRoot(me, owner)
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	rows, err := h.auth.APIKeys(r.Context())
	if err != nil {
		return err
	}
	views, err := h.keyViews(r.Context(), rows)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, wrap("apiKeys", views))
}

type createKeyRequest struct {
	Name      string   `json:"name"`
	UserID    string   `json:"userId"`
	ExpiresIn *float64 `json:"expiresIn"`
	IsActive  bool     `json:"isActive"`
}

func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	req, err := httpx.Decode[createKeyRequest](w, r)
	if err != nil || req.Name == "" || req.UserID == "" || (req.ExpiresIn != nil && *req.ExpiresIn <= 0) {
		return httpx.Error(http.StatusBadRequest, "")
	}
	if err := h.checkOwner(r.Context(), me, req.UserID); err != nil {
		return err
	}
	var expiresIn time.Duration
	if req.ExpiresIn != nil {
		expiresIn = time.Duration(*req.ExpiresIn * float64(time.Second))
	}
	key, token, err := h.auth.CreateAPIKey(r.Context(), req.Name, req.UserID, me.ID, expiresIn, req.IsActive)
	if err != nil {
		return err
	}
	view, err := h.keyView(r.Context(), key)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, wrap("apiKey", createdKey{view, token}))
}

func (h *Handler) deleteAPIKey(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	found, err := h.auth.DeleteAPIKey(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound()
	}
	return ok(w)
}

type updateKeyRequest struct {
	UserID   *string `json:"userId"`
	IsActive *bool   `json:"isActive"`
}

// updateAPIKey is PATCH /api/api-keys/{id}. Only root touches a key of root.
func (h *Handler) updateAPIKey(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	ctx := r.Context()
	id := r.PathValue("id")
	key, err := h.auth.APIKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return err
	}
	current, err := h.userRow(ctx, db.Val(key.UserID)) // a key whose owner is gone is a 404
	if err != nil {
		return err
	}
	if err := onlyRootTouchesRoot(me, current); err != nil {
		return err
	}
	req, err := httpx.Decode[updateKeyRequest](w, r)
	if err != nil {
		return err
	}
	owner := current.ID
	if req.UserID != nil {
		if err := h.checkOwner(ctx, me, *req.UserID); err != nil {
			return err
		}
		owner = *req.UserID
	}
	if err := h.auth.UpdateAPIKey(ctx, id, owner, req.IsActive); err != nil {
		return err
	}
	if key, err = h.auth.APIKey(ctx, id); err != nil {
		return err
	}
	view, err := h.keyView(ctx, key)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, wrap("apiKey", view))
}
