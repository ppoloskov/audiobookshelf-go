// Package settingsdoc reads and writes the JSON document a settings-table row holds.
package settingsdoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// Load returns the document stored under key, or defaults when there is no row
// (or the row holds JSON null). A row that is not a JSON object is an error.
func Load(ctx context.Context, db *sql.DB, key string, defaults map[string]any) (map[string]any, error) {
	raw, err := sqlc.New(db).GetSettingValue(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return defaults, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw.String), &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return defaults, nil
	}
	return doc, nil
}

func Save(ctx context.Context, db *sql.DB, key string, doc map[string]any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	return sqlc.New(db).UpsertSetting(ctx, sqlc.UpsertSettingParams{Key: key, Value: store.Text(string(data)), Createdat: now, Updatedat: now})
}
