// Package settingsdoc reads and writes the JSON document a settings-table row holds.
package settingsdoc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// LoadInto decodes the JSON document stored under key into value, which must be
// a non-nil pointer. It reports false without changing value when there is no
// row or the row holds JSON null.
func LoadInto(ctx context.Context, db *sql.DB, key string, value any) (bool, error) {
	raw, err := sqlc.New(db).GetSettingValue(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data := bytes.TrimSpace([]byte(raw.String))
	if bytes.Equal(data, []byte("null")) {
		return false, nil
	}
	if err := json.Unmarshal(data, value); err != nil {
		return false, err
	}
	return true, nil
}

// Load returns the document stored under key, or defaults when there is no row
// (or the row holds JSON null). A row that is not a JSON object is an error.
func Load(ctx context.Context, db *sql.DB, key string, defaults map[string]any) (map[string]any, error) {
	doc := map[string]any{}
	found, err := LoadInto(ctx, db, key, &doc)
	if err != nil {
		return nil, err
	}
	if !found || doc == nil {
		return defaults, nil
	}
	return doc, nil
}

func Save(ctx context.Context, db *sql.DB, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	return sqlc.New(db).UpsertSetting(ctx, sqlc.UpsertSettingParams{Key: key, Value: store.Text(string(data)), Createdat: now, Updatedat: now})
}
