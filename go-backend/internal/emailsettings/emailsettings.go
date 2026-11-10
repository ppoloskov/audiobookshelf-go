// Package emailsettings owns the email settings document and e-reader device policy.
package emailsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/settingsdoc"
	"audiobookshelf-compat/internal/store"
)

// Key is the settings-table key of the email settings document.
const Key = "email-settings"

// Device is one e-reader device. It stays a map because EmailSettings.update
// and getEReaderDevices copy the whole device object, including unknown fields.
type Device map[string]any

// Devices is the e-reader device array.
type Devices []Device

// Settings is the document EmailSettings.toJSON emits. Unknown top-level keys
// are not part of that document; device maps keep their unknown keys.
type Settings struct {
	ID                 string  `json:"id"`
	Host               *string `json:"host"`
	Port               Port    `json:"port"`
	Secure             Truthy  `json:"secure"`
	RejectUnauthorized Truthy  `json:"rejectUnauthorized"`
	User               *string `json:"user"`
	Pass               *string `json:"pass"`
	TestAddress        *string `json:"testAddress"`
	FromAddress        *string `json:"fromAddress"`
	EreaderDevices     Devices `json:"ereaderDevices"`
}

// Port is settings.port verbatim on load (EmailSettings.construct) and
// `isNullOrNaN(payload.port) ? 465 : Number(payload.port)` on update
// (EmailSettings.update). The update coercion is applied by UnmarshalJSON,
// which also sees already-coerced stored numbers and therefore preserves them.
type Port float64

func (p *Port) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n, ok := jsNumber(value)
	if !ok {
		n = 465
	}
	*p = Port(n)
	return nil
}

// MarshalJSON uses the existing jsFloat precedent. JavaScript renders values
// at or above 1e21 in exponent form; this fixed-point form does not.
func (p Port) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(p), 'f', -1, 64)), nil
}

// jsNumber is JavaScript Number(value) for decoded JSON values; ok is false
// when the result is NaN or non-finite. Binary/octal strings are not parsed;
// persisted port values only need the Number() forms produced by EmailSettings.
func jsNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	case string:
		return jsNumberString(value)
	case []any:
		return jsNumberString(jsonvalue.Text(value))
	}
	return 0, false
}

func jsNumberString(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, true
	}
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		n, err := strconv.ParseUint(value[2:], 16, 64)
		return float64(n), err == nil
	}
	n, err := strconv.ParseFloat(value, 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

// Truthy is the JavaScript truthiness EmailSettings.update applies to secure
// and rejectUnauthorized.
type Truthy bool

func (t *Truthy) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*t = Truthy(jsonvalue.Truthy(value))
	return nil
}

// Defaults is the document of a server that has stored no email settings.
func Defaults() Settings {
	return Settings{ID: Key, Port: 465, Secure: true, RejectUnauthorized: true, EreaderDevices: Devices{}}
}

// Load returns the stored settings, with defaults for absent fields.
func Load(ctx context.Context, db *sql.DB) (Settings, error) {
	settings := Defaults()
	if _, err := settingsdoc.LoadInto(ctx, db, Key, &settings); err != nil {
		return Settings{}, err
	}
	settings.ID = Key
	return settings, nil
}

// Save writes the email settings document.
func Save(ctx context.Context, db *sql.DB, settings Settings) error {
	return settingsdoc.Save(ctx, db, Key, settings)
}

// String redacts the SMTP password so %+v cannot log it.
func (s Settings) String() string {
	if s.Pass != nil {
		hidden := "redacted"
		s.Pass = &hidden
	}
	data, _ := json.Marshal(s) // Settings only contains JSON-compatible fields.
	return string(data)
}

// DeviceAllowed is whether user may send to device d by availabilityOption; adminOrUp when unset (EmailSettings.checkUserCanAccessDevice).
func DeviceAllowed(d Device, u store.User) bool {
	level := jsonvalue.String(d["availabilityOption"])
	switch level {
	case "", "adminOrUp":
		return access.AdminUser(u)
	case "userOrUp":
		return access.AdminUser(u) || u.Type == "user"
	case "guestOrUp":
		return true
	case "specificUsers":
		return slices.Contains(jsonvalue.Strings(d["users"]), u.ID)
	}
	return false
}

// VisibleTo is the devices of settings that user may send to (EmailSettings.getEReaderDevices).
func VisibleTo(settings Settings, u store.User) Devices {
	result := make(Devices, 0, len(settings.EreaderDevices))
	for _, device := range settings.EreaderDevices {
		if device["name"] != nil && device["email"] != nil && DeviceAllowed(device, u) {
			result = append(result, device)
		}
	}
	return result
}

// CleanDevices keeps devices with a name and email, resetting an unknown or unsatisfiable availabilityOption to adminOrUp; only specificUsers keeps users (EmailSettings.update).
func CleanDevices(a []any) Devices {
	out := make(Devices, 0, len(a))
	valid := map[string]bool{"adminOrUp": true, "userOrUp": true, "guestOrUp": true, "specificUsers": true}
	for _, raw := range a {
		d, ok := raw.(map[string]any)
		if !ok || d["name"] == nil || d["email"] == nil {
			continue
		}
		device := Device(d)
		level := jsonvalue.String(device["availabilityOption"])
		if !valid[level] || (level == "specificUsers" && !hasDeviceUsers(device["users"])) {
			device["availabilityOption"] = "adminOrUp"
		}
		if device["availabilityOption"] != "specificUsers" && hasDeviceUsers(device["users"]) {
			device["users"] = []any{}
		}
		out = append(out, device)
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
