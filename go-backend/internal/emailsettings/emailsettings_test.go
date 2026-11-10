package emailsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"

	_ "modernc.org/sqlite"
)

// Expected values from Node:
// node -e 'for (const v of [null,"invalid","465","","  587 ","1e2","0x10",0,-1,587,99999,12.5,65536,true,[],[5],{}]) console.log(JSON.stringify(v), Number(v), Boolean(v))'
func TestPortAndTruthyMatchNode(t *testing.T) {
	cases := []struct {
		raw    string
		port   Port
		truthy bool
	}{
		{`null`, 465, false},
		{`"invalid"`, 465, true},
		{`"465"`, 465, true},
		{`""`, 0, false},
		{`"  587 "`, 587, true},
		{`"1e2"`, 100, true},
		{`"0x10"`, 16, true},
		{`0`, 0, false},
		{`-1`, -1, true},
		{`587`, 587, true},
		{`99999`, 99999, true},
		{`12.5`, 12.5, true},
		{`65536`, 65536, true},
		{`true`, 1, true},
		{`[]`, 0, true},
		{`[5]`, 5, true},
		{`{}`, 465, true},
	}
	for _, tc := range cases {
		var port Port
		if err := json.Unmarshal([]byte(tc.raw), &port); err != nil {
			t.Errorf("Port %s: %v", tc.raw, err)
			continue
		}
		if port != tc.port {
			t.Errorf("Port %s = %v, want %v", tc.raw, float64(port), float64(tc.port))
		}
		data, err := json.Marshal(port)
		if err != nil {
			t.Errorf("Marshal Port %s: %v", tc.raw, err)
			continue
		}
		var round Port
		if err := json.Unmarshal(data, &round); err != nil || round != port {
			t.Errorf("Port %s round trip = %s, %v", tc.raw, data, err)
		}
		var truthy Truthy
		if err := json.Unmarshal([]byte(tc.raw), &truthy); err != nil {
			t.Errorf("Truthy %s: %v", tc.raw, err)
		} else if bool(truthy) != tc.truthy {
			t.Errorf("Truthy %s = %t, want %t", tc.raw, bool(truthy), tc.truthy)
		}
	}
}

// EmailSettings.getEReaderDevices (server/objects/settings/EmailSettings.js).
func TestDeviceVisibilityByAvailability(t *testing.T) {
	admin := store.User{ID: "a", Type: "admin"}
	user := store.User{ID: "u", Type: "user"}
	guest := store.User{ID: "g", Type: "guest"}
	dev := func(level string, users ...any) Device {
		return Device{"name": "n", "email": "e@x", "availabilityOption": level, "users": users}
	}
	settings := Settings{EreaderDevices: []Device{
		dev(""), dev("userOrUp"), dev("guestOrUp"), dev("specificUsers", "g"), dev("bogus"),
		{"name": "no email", "availabilityOption": "guestOrUp"},
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
	for _, device := range got {
		levels = append(levels, device["availabilityOption"])
	}
	want := []any{"adminOrUp", "userOrUp", "specificUsers", "adminOrUp"}
	if len(levels) != len(want) {
		t.Fatalf("levels = %v, want %v", levels, want)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v", levels, want)
		}
	}
	if users := got[1]["users"].([]any); len(users) != 0 {
		t.Errorf("only specificUsers devices keep their users, got %v", users)
	}
}

func TestStoredPortRoundTripsVerbatim(t *testing.T) {
	db, err := sql.Open("sqlite", "file:email-settings-port-roundtrip?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, port := range []string{"99999", "-1", "12.5"} {
		raw := `{"id":"email-settings","port":` + port + `,"ereaderDevices":[]}`
		if _, err := db.Exec(`INSERT INTO settings(key,value,createdAt,updatedAt) VALUES (?,?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, Key, raw, "2000-01-01 00:00:00.000 +00:00", "2000-01-01 00:00:00.000 +00:00"); err != nil {
			t.Fatal(err)
		}
		settings, err := Load(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if got := strconv.FormatFloat(float64(settings.Port), 'f', -1, 64); got != port {
			t.Fatalf("stored port %s loaded as %s", port, got)
		}
		if err := Save(context.Background(), db, settings); err != nil {
			t.Fatal(err)
		}
		settings, err = Load(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		if got := strconv.FormatFloat(float64(settings.Port), 'f', -1, 64); got != port {
			t.Fatalf("stored port %s saved as %s", port, got)
		}
	}
}

func TestSettingsRoundTripKeepsDefaultsAndDeviceFields(t *testing.T) {
	db, err := sql.Open("sqlite", "file:email-settings-roundtrip?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	settings, err := Load(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Port != 465 || !settings.Secure || !settings.RejectUnauthorized || settings.EreaderDevices == nil {
		t.Fatalf("defaults = %#v", settings)
	}
	pass := "secret"
	settings.Pass = &pass
	settings.EreaderDevices = []Device{{"name": "kindle", "email": "reader@example.test", "extra": "kept"}}
	if err := Save(context.Background(), db, settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.EreaderDevices) != 1 || loaded.EreaderDevices[0]["extra"] != "kept" {
		t.Fatalf("device fields = %#v", loaded.EreaderDevices)
	}
	if loaded.Pass == nil || *loaded.Pass != pass {
		t.Fatalf("password round trip = %v", loaded.Pass)
	}
	if redacted := loaded.String(); strings.Contains(redacted, pass) || !strings.Contains(redacted, "redacted") {
		t.Fatalf("Settings.String did not redact the password: %s", redacted)
	}
}
