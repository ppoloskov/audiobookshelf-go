package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/settings"
)

const backups = "/data/backups"

func newStore(t *testing.T, d *db.DB, env map[string]string) *settings.Store {
	t.Helper()
	store, err := settings.NewStore(context.Background(), d, func(k string) string { return env[k] }, backups)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func stored(t *testing.T, d *db.DB, key string) map[string]any {
	t.Helper()
	var row db.Setting
	if err := d.Get(context.Background(), &row, "SELECT * FROM settings WHERE `key` = ?", key); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(row.Value.V, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func put(t *testing.T, d *db.DB, key, value string) {
	t.Helper()
	_, err := d.Exec(context.Background(), "INSERT OR REPLACE INTO settings (`key`, `value`, createdAt, updatedAt) VALUES (?, ?, '2024-01-01 00:00:00.000 +00:00', '2024-01-01 00:00:00.000 +00:00')", key, value)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripFixtureServer(t *testing.T) {
	d := dbtest.Fixture(t)
	before := stored(t, d, settings.ServerKey)
	store := newStore(t, d, nil)
	if _, err := store.UpdateServer(context.Background(), func(*settings.Server) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after := stored(t, d, settings.ServerKey)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("server-settings changed by a no-op update:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRoundTripEmailAndNotifications(t *testing.T) {
	d := dbtest.Fixture(t)
	email := `{"id":"email-settings","host":"smtp.x","port":587,"secure":false,"rejectUnauthorized":true,"user":"u","pass":"p","testAddress":null,"fromAddress":"a@b","ereaderDevices":[{"name":"k","email":"k@x","availabilityOption":"specificUsers","users":["u1"]},{"name":"j","email":"j@x","availabilityOption":"adminOrUp","users":[]}]}`
	notes := `{"id":"notification-settings","appriseType":"api","appriseApiUrl":"http://a/notify","notifications":[{"id":"n1","libraryId":null,"eventName":"onPodcastEpisodeDownloaded","urls":["x://y"],"titleTemplate":"t","bodyTemplate":"b","enabled":true,"type":"info","lastFiredAt":1700000000000,"lastAttemptFailed":false,"numConsecutiveFailedAttempts":0,"numTimesFired":3,"createdAt":1690000000000}],"maxFailedAttempts":5,"maxNotificationQueue":20,"notificationDelay":1000}`
	put(t, d, settings.EmailKey, email)
	put(t, d, settings.NotificationsKey, notes)
	store := newStore(t, d, nil)
	ctx := context.Background()
	if _, err := store.UpdateEmail(ctx, func(*settings.Email) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateNotifications(ctx, func(*settings.Notifications) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{settings.EmailKey: email, settings.NotificationsKey: notes} {
		var wantDoc map[string]any
		if err := json.Unmarshal([]byte(want), &wantDoc); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, d, key); !reflect.DeepEqual(got, wantDoc) {
			t.Errorf("%s changed:\nwant %v\ngot  %v", key, wantDoc, got)
		}
	}
}

func TestMissingRowsReadAsDefaultsAndFirstUpdateCreatesThem(t *testing.T) {
	d := dbtest.Fixture(t)
	ctx := context.Background()
	store := newStore(t, d, nil)
	var count int
	if err := d.Get(ctx, &count, "SELECT count(*) FROM settings WHERE `key` != ?", settings.ServerKey); err != nil || count != 0 {
		t.Fatalf("fixture has other settings rows: %d %v", count, err)
	}
	email, err := store.Email(ctx)
	if err != nil || email.Port != 465 || !email.Secure || !email.RejectUnauthorized || email.EreaderDevices == nil {
		t.Fatalf("email defaults = %+v, %v", email, err)
	}
	notes, err := store.Notifications(ctx)
	if err != nil || notes.MaxFailedAttempts != 5 || notes.MaxNotificationQueue != 20 || notes.NotificationDelay != 1000 || notes.AppriseType != "api" {
		t.Fatalf("notification defaults = %+v, %v", notes, err)
	}
	if _, err := store.UpdateNotifications(ctx, func(n *settings.Notifications) error { n.MaxFailedAttempts = 7; return nil }); err != nil {
		t.Fatal(err)
	}
	doc := stored(t, d, settings.NotificationsKey)
	if doc["maxFailedAttempts"] != float64(7) || doc["id"] != settings.NotificationsKey {
		t.Fatalf("created row = %v", doc)
	}
	var row db.Setting
	if err := d.Get(ctx, &row, "SELECT * FROM settings WHERE `key` = ?", settings.NotificationsKey); err != nil || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatalf("row %v: %v", row, err)
	}
}

func TestDefaultsForMissingKeysAndUnknownKeyDropped(t *testing.T) {
	d := dbtest.New(t)
	put(t, d, settings.ServerKey, `{"id":"server-settings","language":"de","mystery":{"a":1},"port":"x","backupSchedule":"0 2 * * *","authActiveAuthMethods":["local","openid","saml"]}`)
	store := newStore(t, d, nil)
	server := store.Server()
	if server.Language != "de" || server.DateFormat != "MM/dd/yyyy" || server.BackupPath != backups || server.RateLimitLoginRequests != 10 ||
		!reflect.DeepEqual(server.SortingPrefixes, []string{"the", "a"}) || !reflect.DeepEqual(server.OpenIDMobileRedirectURIs, []string{"audiobookshelf://oauth"}) {
		t.Fatalf("defaults not applied: %+v", server.Public())
	}
	if string(server.BackupSchedule) != "0 2 * * *" {
		t.Fatalf("schedule %q", server.BackupSchedule)
	}
	// openid is dropped while its settings are incomplete, unknown values too
	if !reflect.DeepEqual(server.ActiveAuthMethods, []string{"local"}) {
		t.Fatalf("auth methods %v", server.ActiveAuthMethods)
	}
	if _, err := store.UpdateServer(context.Background(), func(*settings.Server) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored(t, d, settings.ServerKey)["mystery"]; ok {
		t.Fatal("unknown key survived a save")
	}
}

func TestLegacyEmailPortAndWrongTypeKeys(t *testing.T) {
	d := dbtest.New(t)
	put(t, d, settings.EmailKey, `{"id":"email-settings","port":"587","secure":"yes","host":"h"}`)
	store := newStore(t, d, nil)
	email, err := store.Email(context.Background())
	if err != nil || email.Port != 587 || !email.Secure || email.Host == nil || *email.Host != "h" {
		t.Fatalf("%+v %v", email, err)
	}
}

func TestSecretsAbsentFromViewsAndFormatting(t *testing.T) {
	d := dbtest.Fixture(t)
	put(t, d, settings.EmailKey, `{"id":"email-settings","pass":"hunter2","host":"h","port":465}`)
	store := newStore(t, d, nil)
	_, err := store.UpdateServer(context.Background(), func(s *settings.Server) error {
		s.OpenIDClientSecret, s.OpenIDClientID = "oidc-secret", "oidc-id"
		s.OpenIDGroupClaim = "groups"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := store.Server()
	if server.TokenSecret == "" {
		t.Fatal("fixture has a token secret")
	}
	public, err := json.Marshal(server.Public())
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(public, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"} {
		if _, ok := keys[key]; ok {
			t.Errorf("Public() has %s", key)
		}
	}
	if _, ok := keys["timeZone"]; !ok {
		t.Error("Public() has no timeZone")
	}
	email, err := store.Email(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{fmt.Sprintf("%+v", server), fmt.Sprintf("%v", server.Auth()), fmt.Sprintf("%+v", email), fmt.Sprintf("%#v", server.TokenSecret), fmt.Sprintf("%+v", &server)} {
		for _, secret := range []string{string(server.TokenSecret), "oidc-secret", "oidc-id", "hunter2"} {
			if strings.Contains(text, secret) {
				t.Errorf("%q leaks a secret", text)
			}
		}
	}
	if auth, _ := json.Marshal(server.Auth()); !strings.Contains(string(auth), "oidc-secret") {
		t.Error("Auth() must carry the secrets for admins")
	}
}

func TestEnvOverridesAreNotPersisted(t *testing.T) {
	d := dbtest.Fixture(t)
	env := map[string]string{"ALLOW_IFRAME": "1", "BACKUP_PATH": "/env/backups"}
	store := newStore(t, d, env)
	if s := store.Server(); !s.AllowIframe || s.BackupPath != "/env/backups" {
		t.Fatalf("env not applied: %v %v", s.AllowIframe, s.BackupPath)
	}
	updated, err := store.UpdateServer(context.Background(), func(s *settings.Server) error { s.Language = "fr"; return nil })
	if err != nil || !updated.AllowIframe || updated.BackupPath != "/env/backups" {
		t.Fatalf("update result %v %v", updated.BackupPath, err)
	}
	doc := stored(t, d, settings.ServerKey)
	if doc["allowIframe"] != false || doc["backupPath"] == "/env/backups" {
		t.Fatalf("env leaked into the row: %v %v", doc["allowIframe"], doc["backupPath"])
	}
	if newStore(t, d, map[string]string{"ALLOW_IFRAME": "0"}).Server().AllowIframe {
		t.Fatal("ALLOW_IFRAME=0 must not force iframe")
	}
}

func TestServerSnapshotsAreIndependent(t *testing.T) {
	store := newStore(t, dbtest.Fixture(t), nil)
	first := store.Server()
	first.SortingPrefixes[0] = "changed"
	first.AllowedOrigins = append(first.AllowedOrigins, "x")
	if got := store.Server().SortingPrefixes[0]; got != "the" {
		t.Fatalf("caller mutation reached the cache: %q", got)
	}
}

func TestUpdateServerErrorLeavesRowAndCache(t *testing.T) {
	d := dbtest.Fixture(t)
	store := newStore(t, d, nil)
	before := stored(t, d, settings.ServerKey)
	var calls atomic.Int32
	store.OnChange(func(settings.Server) { calls.Add(1) })
	boom := errors.New("boom")
	_, err := store.UpdateServer(context.Background(), func(s *settings.Server) error { s.Language = "xx"; return boom })
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if store.Server().Language == "xx" || !reflect.DeepEqual(before, stored(t, d, settings.ServerKey)) || calls.Load() != 0 {
		t.Fatal("a failed update changed state")
	}
	if _, err := store.UpdateServer(context.Background(), func(s *settings.Server) error { s.Language = "xx"; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || store.Server().Language != "xx" {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	d := dbtest.Fixture(t)
	store := newStore(t, d, nil)
	var fired atomic.Int32
	store.OnChange(func(settings.Server) { fired.Add(1) })
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.UpdateServer(context.Background(), func(s *settings.Server) error {
				s.AllowedOrigins = append(s.AllowedOrigins, fmt.Sprint(i))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := len(store.Server().AllowedOrigins); got != n {
		t.Fatalf("cache has %d origins, want %d", got, n)
	}
	if got := len(stored(t, d, settings.ServerKey)["allowedOrigins"].([]any)); got != n || fired.Load() != n {
		t.Fatalf("row has %d origins, OnChange fired %d times, want %d", got, fired.Load(), n)
	}
}

func TestReloadSeesForeignWrite(t *testing.T) {
	d := dbtest.Fixture(t)
	store := newStore(t, d, nil)
	_, err := d.Exec(context.Background(), "UPDATE settings SET `value` = json_set(`value`, '$.language', 'it') WHERE `key` = 'server-settings'")
	if err != nil {
		t.Fatal(err)
	}
	if store.Server().Language == "it" {
		t.Fatal("cache should be stale before Reload")
	}
	if err := store.Reload(context.Background()); err != nil || store.Server().Language != "it" {
		t.Fatalf("after Reload: %v", err)
	}
}

func TestEreaderDevicePolicy(t *testing.T) {
	devices := []settings.EreaderDevice{
		{Name: "a", Email: "a@x"},
		{Name: "b", Email: "b@x", AvailabilityOption: "userOrUp"},
		{Name: "c", Email: "c@x", AvailabilityOption: "guestOrUp"},
		{Name: "d", Email: "d@x", AvailabilityOption: "specificUsers", Users: []string{"u9"}},
		{Name: "", Email: "e@x", AvailabilityOption: "guestOrUp"},
		{Name: "f", Email: "f@x", AvailabilityOption: "bogus"},
	}
	email := settings.Email{EreaderDevices: devices}
	for _, tc := range []struct{ id, typ, want string }{
		{"u1", "root", "abc"}, {"u1", "admin", "abc"}, {"u1", "user", "bc"}, {"u1", "guest", "c"}, {"u9", "guest", "cd"},
	} {
		var got string
		for _, device := range email.DevicesFor(tc.id, tc.typ) {
			got += device.Name
		}
		if got != tc.want {
			t.Errorf("%s/%s sees %q, want %q", tc.id, tc.typ, got, tc.want)
		}
	}
}
