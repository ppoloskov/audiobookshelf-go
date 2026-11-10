package userapi

import (
	"database/sql"
	"errors"
	"net/http"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

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
