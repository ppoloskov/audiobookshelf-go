package auth

import (
	"context"
	"fmt"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/ids"
)

// APIKeys lists every API key.
func (s *Service) APIKeys(ctx context.Context) ([]db.ApiKey, error) {
	var keys []db.ApiKey
	err := s.db.Select(ctx, &keys, `SELECT * FROM apiKeys`)
	return keys, err
}

// APIKey loads one key; a missing one is sql.ErrNoRows.
func (s *Service) APIKey(ctx context.Context, id string) (db.ApiKey, error) {
	var key db.ApiKey
	err := s.db.Get(ctx, &key, `SELECT * FROM apiKeys WHERE id = ?`, id)
	return key, err
}

// CreateAPIKey stores a key owned by userID and returns it with its bearer
// token, which is not stored and cannot be read again. expiresIn 0 never
// expires.
func (s *Service) CreateAPIKey(ctx context.Context, name, userID, createdBy string, expiresIn time.Duration, active bool) (db.ApiKey, string, error) {
	id, err := ids.New()
	if err != nil {
		return db.ApiKey{}, "", fmt.Errorf("api key id: %w", err)
	}
	now := db.Now()
	key := db.ApiKey{ID: id, Name: name, IsActive: db.Bool(active), UserID: &userID, CreatedByUserID: &createdBy, CreatedAt: now, UpdatedAt: now}
	if expiresIn > 0 {
		key.ExpiresAt = db.Time{Time: now.Add(expiresIn)}
	}
	token, err := s.apiKeyToken(id, name, key.ExpiresAt.Time)
	if err != nil {
		return db.ApiKey{}, "", fmt.Errorf("api key token: %w", err)
	}
	// Written as SQL, not Insert: a new key has NULL permissions, which the
	// typed column cannot express.
	_, err = s.db.Exec(ctx, `INSERT INTO apiKeys (id, name, expiresAt, isActive, userId, createdByUserId, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, key.ExpiresAt, key.IsActive, userID, createdBy, now, now)
	return key, token, err
}

// UpdateAPIKey changes the owner of a key and, when active is not nil, whether
// it is usable.
func (s *Service) UpdateAPIKey(ctx context.Context, id, ownerID string, active *bool) error {
	_, err := s.db.Exec(ctx, `UPDATE apiKeys SET userId = ?, isActive = coalesce(?, isActive), updatedAt = ? WHERE id = ?`, ownerID, active, db.Now(), id)
	return err
}

// DeleteAPIKey deletes a key and reports whether it existed.
func (s *Service) DeleteAPIKey(ctx context.Context, id string) (bool, error) {
	result, err := s.db.Exec(ctx, `DELETE FROM apiKeys WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected() // sqlite always reports it
	return n > 0, nil
}
