package admin_settings

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// secretKeys are the settings fields that toJSONForBrowser removes
// (server/objects/settings/ServerSettings.js:299-308).
var secretKeys = []string{
	"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret",
	"authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim",
}

// storedSettings returns the decoded serverSettings JSON of the settings row.
func storedSettings(t *testing.T, e *harness.Env) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT value FROM settings WHERE key = 'server-settings'")
	if len(rows) != 1 {
		t.Fatalf("settings table must hold exactly one server-settings row, got %d", len(rows))
	}
	text, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatalf("settings.value is %T, want JSON text", rows[0]["value"])
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(text), &settings); err != nil {
		t.Fatalf("settings.value is not a JSON object: %v", err)
	}
	return settings
}

// responseSettings decodes the serverSettings object of a 200 response.
func responseSettings(t *testing.T, resp harness.Response) map[string]any {
	t.Helper()
	if resp.Status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", resp.Status, resp.Body)
	}
	settings, ok := resp.Object(t)["serverSettings"].(map[string]any)
	if !ok {
		t.Fatalf("response has no serverSettings object: %s", resp.Body)
	}
	return settings
}

// sameJSON reports whether two values are equal after a JSON round trip.
func sameJSON(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func normalize(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// wantStatus fails unless the response has exactly the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want status %d, got %d: %.300s", what, status, resp.Status, resp.Body)
	}
}

// wantKeys fails unless every key of want has the same JSON value in got.
func wantKeys(t *testing.T, what string, got, want map[string]any) {
	t.Helper()
	for key, value := range want {
		have, present := got[key]
		if !present || !sameJSON(have, value) {
			t.Errorf("%s: %s = %v (present %v), want %v", what, key, have, present, value)
		}
	}
}

// wantNoSecrets fails when the settings object carries a field browsers must not see.
func wantNoSecrets(t *testing.T, what string, settings map[string]any) {
	t.Helper()
	for _, key := range secretKeys {
		if _, present := settings[key]; present {
			t.Errorf("%s must not contain %s", what, key)
		}
	}
}

// onlyChanged fails unless every key of before has the same value in after,
// except the keys of changed, which must equal their new value.
func onlyChanged(t *testing.T, what string, before, after, changed map[string]any) {
	t.Helper()
	for key, old := range before {
		want := old
		if value, ok := changed[key]; ok {
			want = value
		}
		if !sameJSON(after[key], want) {
			t.Errorf("%s: %s = %v, want %v", what, key, after[key], want)
		}
	}
	for key, value := range changed {
		if !sameJSON(after[key], value) {
			t.Errorf("%s: %s = %v, want %v", what, key, after[key], value)
		}
	}
}

// adminOnly asserts 401 without a token and 403 for a non-admin user.
func adminOnly(t *testing.T, e *harness.Env, method, path string, body any) {
	t.Helper()
	anonymous := e.Do(t, harness.Request{Method: method, Path: path, JSON: body})
	wantStatus(t, method+" "+path+" without token", anonymous, http.StatusUnauthorized)
	listener := e.Do(t, harness.Request{Method: method, Path: path, JSON: body, Token: e.Token(t, "listener")})
	wantStatus(t, method+" "+path+" as non-admin", listener, http.StatusForbidden)
}

// settingsPatch sends PATCH /api/settings as root and returns the serverSettings of the 200 response.
func settingsPatch(t *testing.T, e *harness.Env, body any) map[string]any {
	t.Helper()
	return responseSettings(t, e.Patch(t, e.Token(t, "root"), "/api/settings", body))
}

// authorizeSettings reads serverSettings through POST /api/authorize.
func authorizeSettings(t *testing.T, e *harness.Env, token string) map[string]any {
	t.Helper()
	resp := e.Post(t, token, "/api/authorize", nil)
	wantStatus(t, "authorize", resp, http.StatusOK)
	settings, ok := resp.Object(t)["serverSettings"].(map[string]any)
	if !ok {
		t.Fatalf("authorize has no serverSettings: %s", resp.Body)
	}
	return settings
}

// readStatus returns language, authMethods and authFormData of GET /status.
func readStatus(t *testing.T, e *harness.Env) (language string, methods []string, form map[string]any) {
	t.Helper()
	resp := e.Get(t, "", "/status")
	wantStatus(t, "status", resp, http.StatusOK)
	var doc struct {
		Language    string         `json:"language"`
		AuthMethods []string       `json:"authMethods"`
		FormData    map[string]any `json:"authFormData"`
	}
	resp.JSON(t, &doc)
	return doc.Language, doc.AuthMethods, doc.FormData
}

// never fails if condition becomes true within window (a bounded absence check).
func never(t *testing.T, window time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sortedCopy returns a sorted copy of the strings.
func sortedCopy(values []string) []string {
	out := append([]string{}, values...)
	slices.Sort(out)
	return out
}

// stringList converts a decoded JSON array of strings; anything else fails the test.
func stringList(t *testing.T, value any) []string {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("want a JSON array, got %T (%v)", value, value)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("want strings in array, got %T", item)
		}
		out = append(out, text)
	}
	return out
}

// scenario builds a harness.Scenario with one claim and its upstream sources.
func scenario(requirement, invariant, caseName, state, assertion, claim string, sources []string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: caseName, State: state,
		Assertion: assertion, Sources: sources, Claim: claim, Run: run,
	}
}

// settingsUpdatedAt returns settings.updatedAt of the server-settings row; the
// stored format sorts like time, so strings compare chronologically.
func settingsUpdatedAt(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT updatedAt FROM settings WHERE key = 'server-settings'")
	text, _ := rows[0]["updatedAt"].(string)
	if text == "" {
		t.Fatalf("settings.updatedAt missing: %v", rows)
	}
	return text
}

// wantMovedForward fails unless after is strictly later than before.
func wantMovedForward(t *testing.T, what, before, after string) {
	t.Helper()
	if after <= before {
		t.Fatalf("%s: updatedAt %q must be later than %q", what, after, before)
	}
}

// wantSame fails unless the two timestamps are equal (a no-op writes nothing).
func wantSame(t *testing.T, what, before, after string) {
	t.Helper()
	if after != before {
		t.Fatalf("%s: updatedAt changed from %q to %q", what, before, after)
	}
}
