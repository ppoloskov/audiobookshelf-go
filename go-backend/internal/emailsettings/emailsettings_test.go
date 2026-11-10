package emailsettings

import (
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/store"
)

// EmailSettings.getEReaderDevices (server/objects/settings/EmailSettings.js).
func TestDeviceVisibilityByAvailability(t *testing.T) {
	admin := store.User{ID: "a", Type: "admin"}
	user := store.User{ID: "u", Type: "user"}
	guest := store.User{ID: "g", Type: "guest"}
	dev := func(level string, users ...any) map[string]any {
		return map[string]any{"name": "n", "email": "e@x", "availabilityOption": level, "users": users}
	}
	settings := map[string]any{"ereaderDevices": []any{
		dev(""), dev("userOrUp"), dev("guestOrUp"), dev("specificUsers", "g"), dev("bogus"),
		map[string]any{"name": "no email", "availabilityOption": "guestOrUp"},
	}}
	for _, tc := range []struct {
		u    store.User
		want int
	}{{admin, 3}, {user, 2}, {guest, 2}} {
		if got := len(VisibleTo(settings, tc.u)); got != tc.want {
			t.Errorf("%s sees %d devices, want %d", tc.u.Type, got, tc.want)
		}
	}
	if !DeviceAllowed(dev("specificUsers", "g"), guest) || DeviceAllowed(dev("specificUsers", "g"), user) {
		t.Error("specificUsers lists the allowed user ids")
	}
}

func TestCleanDevices(t *testing.T) {
	got := CleanDevices([]any{
		map[string]any{"name": "a", "email": "e", "availabilityOption": "specificUsers", "users": []any{}},
		map[string]any{"name": "b", "email": "e", "availabilityOption": "userOrUp", "users": []any{"u1"}},
		map[string]any{"name": "c", "email": "e", "availabilityOption": "specificUsers", "users": []any{"u1"}},
		map[string]any{"name": "d", "email": "e", "availabilityOption": "weird"},
		map[string]any{"name": "missing email"},
		"not a device",
	})
	var levels []any
	for _, d := range got {
		levels = append(levels, d.(map[string]any)["availabilityOption"])
	}
	want := []any{"adminOrUp", "userOrUp", "specificUsers", "adminOrUp"}
	if !reflect.DeepEqual(levels, want) {
		t.Fatalf("levels = %v, want %v", levels, want)
	}
	if users := got[1].(map[string]any)["users"].([]any); len(users) != 0 {
		t.Errorf("only specificUsers devices keep their users, got %v", users)
	}
}
