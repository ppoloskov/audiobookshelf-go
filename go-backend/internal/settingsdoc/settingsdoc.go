// Package settingsdoc reads and writes the JSON document a settings-table row
// holds (email-settings, notification-settings), so each feature does not carry
// its own copy of the row access.
package settingsdoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
)

// EmailKey is the row of the SMTP settings and e-reader devices.
const EmailKey = "email-settings"

// EmailDefaults is the email settings document of a server that never saved one.
func EmailDefaults() map[string]any {
	return map[string]any{
		"id": EmailKey, "host": nil, "port": 465, "secure": true,
		"rejectUnauthorized": true, "user": nil, "pass": nil,
		"testAddress": nil, "fromAddress": nil, "ereaderDevices": []any{},
	}
}

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

// Save stores doc under key, creating the row when needed.
func Save(ctx context.Context, db *sql.DB, key string, doc map[string]any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	return sqlc.New(db).UpsertSetting(ctx, sqlc.UpsertSettingParams{Key: key, Value: store.Text(string(data)), Createdat: now, Updatedat: now})
}

// DeviceAllowed is whether user may send to the e-reader device d
// (EmailSettings.getEReaderDevices): by availabilityOption, adminOrUp when
// unset, or by being listed for specificUsers.
func DeviceAllowed(d map[string]any, u store.User) bool {
	level, _ := d["availabilityOption"].(string)
	switch level {
	case "", "adminOrUp":
		return access.AdminUser(u)
	case "userOrUp":
		return access.AdminUser(u) || u.Type == "user"
	case "guestOrUp":
		return true
	case "specificUsers":
		return slices.Contains(jsonvalue.Array(d["users"]), any(u.ID))
	}
	return false
}
