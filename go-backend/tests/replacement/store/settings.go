package store

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func settingsReopen(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	for _, payload := range []map[string]any{{"language": "fr"}, {"chromecastEnabled": true}} {
		r := e.Patch(t, token, "/api/settings", payload)
		if r.Status != http.StatusOK {
			t.Fatalf("settings PATCH status %d", r.Status)
		}
	}
	query := "SELECT value,updatedAt FROM settings WHERE key='server-settings'"
	before := e.Rows(t, query)
	assertSettings(t, before)
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	surfaced := auth.Object(t)["serverSettings"]
	assertSurfaced(t, surfaced)

	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	after := reopened.Rows(t, query)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("settings row or timestamp changed on reopen")
	}
	assertSettings(t, after)
	token = reopened.Token(t, "root")
	auth = reopened.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK || !reflect.DeepEqual(auth.Object(t)["serverSettings"], surfaced) {
		t.Fatal("reopened settings differ")
	}
}
func assertSettings(t *testing.T, rows []map[string]any) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatal("missing settings row")
	}
	raw, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatal("settings value is not text")
	}
	var value struct {
		Language          string `json:"language"`
		ChromecastEnabled bool   `json:"chromecastEnabled"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal("invalid settings JSON")
	}
	if value.Language != "fr" || !value.ChromecastEnabled {
		t.Fatalf("settings merge: language=%s chromecast=%v", value.Language, value.ChromecastEnabled)
	}
}

func assertSurfaced(t *testing.T, value any) {
	t.Helper()
	settings, ok := value.(map[string]any)
	if !ok || settings["language"] != "fr" || settings["chromecastEnabled"] != true {
		t.Fatal("surfaced settings do not contain both changes")
	}
}
func settingsRejection(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := "SELECT value,updatedAt FROM settings WHERE key='server-settings'"
	before := e.Rows(t, query)
	if len(before) != 1 {
		t.Fatal("missing settings row")
	}
	raw, ok := before[0]["value"].(string)
	if !ok || raw == "" {
		t.Fatal("missing stored settings JSON")
	}
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	surfaced := auth.Object(t)["serverSettings"]
	visible, ok := surfaced.(map[string]any)
	if !ok || visible["language"] != "en-us" {
		t.Fatal("fixture surfaced language differs")
	}
	r := e.Patch(t, token, "/api/settings", map[string]any{"allowedOrigins": "invalid", "language": "de"})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("invalid settings status %d", r.Status)
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, before) {
		t.Fatal("rejection changed settings row or timestamp")
	}
	auth = e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK || !reflect.DeepEqual(auth.Object(t)["serverSettings"], surfaced) {
		t.Fatal("rejection changed surfaced settings")
	}
}
