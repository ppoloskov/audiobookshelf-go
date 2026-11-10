package userapi

import (
	"net/http"

	"audiobookshelf-compat/internal/emailsettings"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) updateEReaderDevices(w http.ResponseWriter, r *http.Request, user store.User) error {
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		return httputil.Status(400, "Invalid payload. ereaderDevices array required")
	}
	raw, ok := body["ereaderDevices"].([]any)
	if !ok {
		return httputil.Status(400, "Invalid payload. ereaderDevices array required")
	}
	provided := make([]emailsettings.Device, 0, len(raw))
	for _, entry := range raw {
		d, ok := entry.(map[string]any)
		if !ok || !jsonvalue.Truthy(d["name"]) || !jsonvalue.Truthy(d["email"]) {
			return httputil.Status(400, "Invalid payload. ereaderDevices array items must have name and email")
		}
		users, ok := d["users"].([]any)
		if d["availabilityOption"] != "specificUsers" || !ok || len(users) != 1 || users[0] != user.ID {
			return httputil.Status(400, `Invalid payload. ereaderDevices array items must have availabilityOption "specificUsers" and only the current user`)
		}
		provided = append(provided, emailsettings.Device(d))
	}
	// A database without an email-settings row starts from the defaults TS
	// EmailSettings (objects/settings/EmailSettings.js), and the write below
	// creates the row, as TS's updateSetting does.
	settings, err := emailsettings.Load(r.Context(), h.db)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	kept := make([]emailsettings.Device, 0, len(settings.EreaderDevices))
	for _, device := range settings.EreaderDevices {
		users, _ := device["users"].([]any)
		if !emailsettings.DeviceAllowed(device, user) || len(users) != 1 {
			kept = append(kept, device)
		}
	}
	combined := append(kept, provided...)
	names := map[string]bool{}
	for _, device := range combined {
		name := jsonvalue.String(device["name"])
		if names[name] {
			return httputil.Status(400, `Invalid payload. Duplicate "name" field found.`)
		}
		names[name] = true
	}
	settings.EreaderDevices = combined
	if err := emailsettings.Save(r.Context(), h.db, settings); err != nil {
		return httputil.Internal("database write failed", err)
	}
	visible := emailsettings.VisibleTo(settings, user)
	h.emit.ToUser(user.ID, "ereader-devices-updated", map[string]any{"ereaderDevices": visible})
	return httputil.JSON(w, r, map[string]any{"ereaderDevices": visible})
}
