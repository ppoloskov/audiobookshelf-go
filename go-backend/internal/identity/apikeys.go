package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

func (h *Handler) APIKeys(ctx context.Context) ([]users.APIKey, error) {
	rows, err := h.queries().ListApiKeys(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]users.APIKey, 0, len(rows))
	for _, row := range rows {
		key, err := h.apiKeyView(ctx, row)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// APIKey returns one key; a missing key is sql.ErrNoRows.
func (h *Handler) APIKey(ctx context.Context, id string) (users.APIKey, error) {
	row, err := h.queries().GetApiKey(ctx, id)
	if err != nil {
		return users.APIKey{}, err
	}
	return h.apiKeyView(ctx, row)
}

// CreateAPIKey stores a key and returns it with its bearer token, which is not
// stored and cannot be read again.
func (h *Handler) CreateAPIKey(ctx context.Context, in users.NewAPIKey) (users.APIKey, string, error) {
	id, err := ids.New()
	if err != nil {
		return users.APIKey{}, "", err
	}
	now := time.Now().UTC()
	var expires any
	var expiresAtUnix int64
	if in.ExpiresIn > 0 {
		expires = dbtime.Format(now.Add(time.Duration(in.ExpiresIn * float64(time.Second))))
		expiresAtUnix = now.Unix() + int64(in.ExpiresIn)
	}
	token, err := h.IssueAPIKeyToken(id, in.Name, expiresAtUnix)
	if err != nil {
		return users.APIKey{}, "", err
	}
	active := int64(0)
	if in.Active {
		active = 1
	}
	err = h.queries().InsertApiKey(ctx, sqlc.InsertApiKeyParams{
		ID: id, Name: in.Name, Expiresat: expires, Isactive: active, Userid: store.Text(in.UserID),
		Createdbyuserid: store.Text(in.CreatedBy), Createdat: dbtime.Format(now), Updatedat: dbtime.Format(now),
	})
	if err != nil {
		return users.APIKey{}, "", err
	}
	key, err := h.APIKey(ctx, id)
	return key, token, err
}

func (h *Handler) UpdateAPIKey(ctx context.Context, id, ownerID string, active *bool) error {
	var isActive sql.NullInt64
	if active != nil {
		isActive = sql.NullInt64{Valid: true}
		if *active {
			isActive.Int64 = 1
		}
	}
	return h.queries().UpdateApiKeyOwner(ctx, sqlc.UpdateApiKeyOwnerParams{UserID: store.Text(ownerID), IsActive: isActive, UpdatedAt: dbtime.Now(), ID: id})
}

func (h *Handler) DeleteAPIKey(ctx context.Context, id string) (bool, error) {
	res, err := h.queries().DeleteApiKey(ctx, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (h *Handler) apiKeyView(ctx context.Context, row sqlc.Apikey) (users.APIKey, error) {
	var perms any
	if row.Permissions.Valid {
		_ = json.Unmarshal([]byte(row.Permissions.String), &perms)
	}
	key := users.APIKey{
		ID: row.ID, Name: row.Name, Description: store.NullStringPtr(row.Description),
		ExpiresAt: isoDate(row.Expiresat), LastUsedAt: isoDate(row.Lastusedat), IsActive: row.Isactive != 0, Permissions: perms,
		UserID: store.NullStringPtr(row.Userid), CreatedByUserID: store.NullStringPtr(row.Createdbyuserid),
		CreatedAt: isoDate(row.Createdat), UpdatedAt: isoDate(row.Updatedat),
	}
	key.User = h.keyUser(ctx, row.Userid)
	key.CreatedByUser = h.keyUser(ctx, row.Createdbyuserid)
	return key, nil
}

// keyUser is the summary of the user id names; nil when unset or gone.
func (h *Handler) keyUser(ctx context.Context, id sql.NullString) *users.KeyUser {
	if !id.Valid {
		return nil
	}
	u, err := h.userByID(ctx, id.String)
	if err != nil {
		return nil
	}
	return &users.KeyUser{ID: u.ID, Username: u.Username, Type: u.Type}
}

// isoDate is a DATETIME column as Sequelize serializes a DATE (Date.toJSON);
// NULL and unsupported values are nil.
func isoDate(v any) *string {
	var t time.Time
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		t = time.UnixMilli(x)
	case float64:
		t = time.UnixMilli(int64(x))
	case time.Time:
		t = x
	case string:
		t = parseDate(x)
	case []byte:
		t = parseDate(string(x))
	default:
		return nil
	}
	s := dbtime.JSDate(t)
	return &s
}

func parseDate(text string) time.Time {
	if t, ok := dbtime.Parse(text); ok {
		return t
	}
	// A zone-less ISO spelling dbtime does not know.
	t, _ := time.Parse("2006-01-02T15:04:05.999999999", text)
	return t
}
