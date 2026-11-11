// TEMPORARY: feeds old userapi; delete with it.

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/users"
)

// OldCredentials implements userapi.Credentials on top of Service.
type OldCredentials struct{ s *Service }

// OldCredentials returns the adapter the old userapi package takes.
func (s *Service) OldCredentials() *OldCredentials { return &OldCredentials{s} }

// CreateUser creates a user from the old request shape and returns its id.
func (c *OldCredentials) CreateUser(ctx context.Context, in users.NewUser) (string, error) {
	user := NewUser{Username: in.Username, Email: in.Email, PasswordHash: in.PasswordHash, Type: in.Type, Active: in.Active, OIDCSub: in.OIDCSub}
	if in.Permissions != nil {
		raw, err := json.Marshal(in.Permissions)
		if err != nil {
			return "", err
		}
		user.Permissions = &db.Permissions{}
		if err := json.Unmarshal(raw, user.Permissions); err != nil {
			return "", err
		}
	}
	created, err := c.s.CreateUser(ctx, user)
	return created.ID, err
}

// IssueLegacyToken re-issues the token of a renamed user.
func (c *OldCredentials) IssueLegacyToken(userID, username string) (string, error) {
	return c.s.legacyToken(userID, username)
}

// HashPassword hashes a password.
func (c *OldCredentials) HashPassword(password string) (string, error) { return HashPassword(password) }

// ChangePassword changes a password.
func (c *OldCredentials) ChangePassword(ctx context.Context, userID, password, newPassword string) error {
	return c.s.ChangePassword(ctx, userID, password, newPassword)
}

// InvalidateUserSessions drops the user's sessions but the current one.
func (c *OldCredentials) InvalidateUserSessions(ctx context.Context, userID, current string) (string, string, error) {
	return c.s.InvalidateUserSessions(ctx, userID, current)
}

// SetRefreshTokenCookie sets the refresh cookie.
func (c *OldCredentials) SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	c.s.setRefreshCookie(w, r, token)
}

// UserSessions lists sessions in the old shape.
func (c *OldCredentials) UserSessions(ctx context.Context, userID, current string, page, perPage int) (int, []users.SessionInfo, error) {
	total, rows, err := c.s.Sessions(ctx, userID, page, perPage)
	infos := make([]users.SessionInfo, 0, len(rows))
	for _, row := range rows {
		info := users.SessionInfo{ID: row.ID, IPAddress: row.IPAddress, UserAgent: row.UserAgent,
			Current: current != "" && (current == row.RefreshToken || current == db.Val(row.LastRefreshToken))}
		if !row.CreatedAt.IsZero() {
			info.CreatedAt = row.CreatedAt.Millis()
		}
		if !row.UpdatedAt.IsZero() {
			info.UpdatedAt = row.UpdatedAt.Millis()
		}
		infos = append(infos, info)
	}
	return total, infos, err
}

// DeleteUserSession deletes one session.
func (c *OldCredentials) DeleteUserSession(ctx context.Context, userID, sessionID string) (bool, error) {
	return c.s.DeleteSession(ctx, userID, sessionID)
}

// APIKeys lists keys in the old shape.
func (c *OldCredentials) APIKeys(ctx context.Context) ([]users.APIKey, error) {
	rows, err := c.s.APIKeys(ctx)
	keys := make([]users.APIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, c.keyView(ctx, row))
	}
	return keys, err
}

// APIKey returns one key; a missing key is sql.ErrNoRows.
func (c *OldCredentials) APIKey(ctx context.Context, id string) (users.APIKey, error) {
	row, err := c.s.APIKey(ctx, id)
	if err != nil {
		return users.APIKey{}, err
	}
	return c.keyView(ctx, row), nil
}

// CreateAPIKey stores a key and returns it with its token.
func (c *OldCredentials) CreateAPIKey(ctx context.Context, in users.NewAPIKey) (users.APIKey, string, error) {
	row, token, err := c.s.CreateAPIKey(ctx, in.Name, in.UserID, in.CreatedBy, time.Duration(in.ExpiresIn*float64(time.Second)), in.Active)
	if err != nil {
		return users.APIKey{}, "", err
	}
	return c.keyView(ctx, row), token, nil
}

// UpdateAPIKey changes a key's owner and state.
func (c *OldCredentials) UpdateAPIKey(ctx context.Context, id, ownerID string, active *bool) error {
	return c.s.UpdateAPIKey(ctx, id, ownerID, active)
}

// DeleteAPIKey deletes a key.
func (c *OldCredentials) DeleteAPIKey(ctx context.Context, id string) (bool, error) {
	return c.s.DeleteAPIKey(ctx, id)
}

func (c *OldCredentials) keyView(ctx context.Context, row db.ApiKey) users.APIKey {
	iso := func(t db.Time) *string {
		if t.IsZero() {
			return nil
		}
		text := t.UTC().Format("2006-01-02T15:04:05.000Z")
		return &text
	}
	var perms any
	if row.Permissions.V.Download || row.Permissions.V.Update || row.Permissions.V.Delete || row.Permissions.V.Upload || row.Permissions.V.AccessAllLibraries {
		perms = row.Permissions.V
	}
	return users.APIKey{
		ID: row.ID, Name: row.Name, Description: row.Description, ExpiresAt: iso(row.ExpiresAt), LastUsedAt: iso(row.LastUsedAt),
		IsActive: bool(row.IsActive), Permissions: perms, UserID: row.UserID, CreatedByUserID: row.CreatedByUserID,
		CreatedAt: iso(row.CreatedAt), UpdatedAt: iso(row.UpdatedAt),
		User: c.keyUser(ctx, row.UserID), CreatedByUser: c.keyUser(ctx, row.CreatedByUserID),
	}
}

// keyUser is the summary of the user id names; nil when unset or gone.
func (c *OldCredentials) keyUser(ctx context.Context, id *string) *users.KeyUser {
	if id == nil {
		return nil
	}
	user, err := c.s.userByID(ctx, *id)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil
	}
	return &users.KeyUser{ID: user.ID, Username: db.Val(user.Username), Type: db.Val(user.Type)}
}
