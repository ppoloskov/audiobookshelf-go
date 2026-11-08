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

const EmailKey = "email-settings"

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

// VisibleTo is the e-reader devices of an email-settings document that user
// may send to (EmailSettings.getEReaderDevices).
func VisibleTo(settings map[string]any, u store.User) []any {
	result := []any{}
	for _, raw := range jsonvalue.Array(settings["ereaderDevices"]) {
		if device, ok := raw.(map[string]any); ok && device["name"] != nil && device["email"] != nil && DeviceAllowed(device, u) {
			result = append(result, device)
		}
	}
	return result
}

// CleanDevices keeps the e-reader devices with a name and an email and resets
// an unknown or unsatisfiable availabilityOption to adminOrUp; only
// specificUsers devices keep their users (EmailSettings.update).
func CleanDevices(a []any) []any {
	out := []any{}
	valid := map[string]bool{"adminOrUp": true, "userOrUp": true, "guestOrUp": true, "specificUsers": true}
	for _, raw := range a {
		d, ok := raw.(map[string]any)
		if !ok || d["name"] == nil || d["email"] == nil {
			continue
		}
		level, _ := d["availabilityOption"].(string)
		if !valid[level] || (level == "specificUsers" && !hasDeviceUsers(d["users"])) {
			d["availabilityOption"] = "adminOrUp"
		}
		if d["availabilityOption"] != "specificUsers" && hasDeviceUsers(d["users"]) {
			d["users"] = []any{}
		}
		out = append(out, d)
	}
	return out
}
func hasDeviceUsers(value any) bool {
	switch users := value.(type) {
	case []any:
		return len(users) > 0
	case string:
		return users != ""
	case map[string]any:
		return jsonvalue.Truthy(users["length"])
	default:
		return false
	}
}
