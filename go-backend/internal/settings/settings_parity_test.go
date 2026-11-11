// TEMPORARY: parity with internal/serversettings/emailsettings; delete with them.

package settings_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/emailsettings"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/store"
)

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParityServerPublicAndAuth(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Fixture(t)
	old, err := serversettings.Get(ctx, d.SQL())
	if err != nil {
		t.Fatal(err)
	}
	newSettings := newStore(t, d, nil).Server()

	want := old.ForBrowser(backups)
	got := asMap(t, newSettings.Public())
	// the zone alias table is not ported (see timezone.go); compare it apart
	delete(want, "timeZone")
	delete(got, "timeZone")
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Public differs from ForBrowser:\nwant %v\ngot  %v", want, got)
	}

	auth := asMap(t, newSettings.Auth())
	for _, key := range serversettings.AuthKeys {
		if !reflect.DeepEqual(old.Doc()[key], auth[key]) {
			t.Errorf("auth key %s: old %v new %v", key, old.Doc()[key], auth[key])
		}
	}
	// authOpenIDSubfolderForRedirectURLs is undefined upstream until set, so absent in both
	if len(auth) != len(serversettings.AuthKeys)-1 {
		t.Errorf("auth has %d keys, old has %d", len(auth), len(serversettings.AuthKeys)-1)
	}
	if !reflect.DeepEqual(old.SortingPrefixes(), newSettings.SortingPrefixes) || old.IgnorePrefix() != newSettings.SortingIgnorePrefix {
		t.Error("sorting settings differ")
	}
	if old.Bool("storeCoverWithItem") != newSettings.StoreCoverWithItem || old.AllowIframe() != newSettings.AllowIframe {
		t.Error("flags differ")
	}
	// the stored document is the same by value
	if !reflect.DeepEqual(old.Doc(), asMap(t, newSettings)) {
		t.Errorf("stored document differs:\nold %v\nnew %v", old.Doc(), asMap(t, newSettings))
	}
}

func TestParityNewWriteIsReadByOldCode(t *testing.T) {
	ctx := context.Background()
	d := dbtest.Fixture(t)
	settingsStore := newStore(t, d, nil)
	if _, err := settingsStore.UpdateServer(ctx, func(s *settings.Server) error {
		s.Language, s.BackupSchedule, s.BackupsToKeep = "de", "0 3 * * *", 4
		s.SortingPrefixes = []string{"der", "die"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	serversettings.Invalidate(d.SQL())
	old, err := serversettings.Get(ctx, d.SQL())
	if err != nil {
		t.Fatal(err)
	}
	if old.String("language") != "de" || old.String("backupSchedule") != "0 3 * * *" || !reflect.DeepEqual(old.SortingPrefixes(), []string{"der", "die"}) {
		t.Fatalf("old code reads %v", old.Doc())
	}
	if !reflect.DeepEqual(old.Doc(), asMap(t, settingsStore.Server())) {
		t.Fatal("old and new read different documents")
	}

	if _, err := settingsStore.UpdateEmail(ctx, func(e *settings.Email) error {
		h, p := "smtp.example", "pw"
		e.Host, e.Pass, e.Port = &h, &p, 2525
		e.EreaderDevices = []settings.EreaderDevice{
			{Name: "a", Email: "a@x"},
			{Name: "b", Email: "b@x", AvailabilityOption: "specificUsers", Users: []string{"u1"}},
			{Name: "c", Email: "c@x", AvailabilityOption: "guestOrUp"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldEmail, err := emailsettings.Load(ctx, d.SQL())
	if err != nil {
		t.Fatal(err)
	}
	newEmail, err := settingsStore.Email(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asMap(t, oldEmail), asMap(t, newEmail)) && asMap(t, newEmail)["id"] != "email-settings" {
		t.Fatal("email documents differ")
	}
	oldJSON, newJSON := asMap(t, oldEmail), asMap(t, newEmail)
	if string(mustJSON(t, oldJSON["ereaderDevices"])) != string(mustJSON(t, newJSON["ereaderDevices"])) {
		t.Errorf("devices differ:\nold %s\nnew %s", mustJSON(t, oldJSON["ereaderDevices"]), mustJSON(t, newJSON["ereaderDevices"]))
	}
	for _, key := range []string{"host", "port", "pass", "secure", "rejectUnauthorized", "id"} {
		if !reflect.DeepEqual(oldJSON[key], newJSON[key]) {
			t.Errorf("email %s: old %v new %v", key, oldJSON[key], newJSON[key])
		}
	}
	for _, user := range []store.User{{ID: "u1", Type: "root"}, {ID: "u1", Type: "user"}, {ID: "u1", Type: "guest"}, {ID: "u2", Type: "guest"}} {
		var oldNames, newNames []string
		for _, device := range emailsettings.VisibleTo(oldEmail, user) {
			oldNames = append(oldNames, device["name"].(string))
		}
		for _, device := range newEmail.DevicesFor(user.ID, user.Type) {
			newNames = append(newNames, device.Name)
		}
		if !reflect.DeepEqual(oldNames, newNames) {
			t.Errorf("%s/%s: old sees %v, new sees %v", user.ID, user.Type, oldNames, newNames)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
