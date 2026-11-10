// Package emailsettings owns email settings defaults and e-reader device policy.
package emailsettings

import (
	"slices"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
)

// Key is the settings-table key of the email settings document.
const Key = "email-settings"

// Defaults is the document of a server that has stored no email settings.
func Defaults() map[string]any {
	return map[string]any{"id": Key, "host": nil, "port": 465, "secure": true, "rejectUnauthorized": true, "user": nil, "pass": nil, "testAddress": nil, "fromAddress": nil, "ereaderDevices": []any{}}
}

// DeviceAllowed is whether user may send to device d by availabilityOption; adminOrUp when unset (EmailSettings.getEReaderDevices).
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

// VisibleTo is the devices of settings that user may send to (EmailSettings.getEReaderDevices).
func VisibleTo(settings map[string]any, u store.User) []any {
	result := []any{}
	for _, raw := range jsonvalue.Array(settings["ereaderDevices"]) {
		if device, ok := raw.(map[string]any); ok && device["name"] != nil && device["email"] != nil && DeviceAllowed(device, u) {
			result = append(result, device)
		}
	}
	return result
}

// CleanDevices keeps devices with a name and email, resetting an unknown or unsatisfiable availabilityOption to adminOrUp; only specificUsers keeps users (EmailSettings.update).
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
	}
	return false
}
