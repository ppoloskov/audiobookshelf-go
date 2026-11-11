package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/settings"
)

// env is a Handler over an empty database; a non-empty apprise URL seeds the settings.
type env struct {
	t       *testing.T
	h       *Handler
	mux     *http.ServeMux
	store   *settings.Store
	updates int // notifications_updated events
}

func newEnv(t *testing.T, apprise string) *env {
	t.Helper()
	store, err := settings.NewStore(context.Background(), dbtest.New(t), func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: store, mux: http.NewServeMux()}
	if apprise != "" {
		_, err := store.UpdateNotifications(context.Background(), func(n *settings.Notifications) error {
			n.AppriseAPIURL, n.MaxFailedAttempts = &apprise, 2
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	e.h = New(store, func(_, event string, _ any) {
		if event == "notifications_updated" {
			e.updates++
		}
	})
	e.h.Routes(e.mux)
	return e
}

func (e *env) serve(userType, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	user := auth.NewPrincipal(db.User{ID: "user-1", Type: db.Ptr(userType), IsActive: true})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
	return w
}

func (e *env) admin(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.serve("admin", method, path, body)
}

func (e *env) create(body string) settings.Notification {
	e.t.Helper()
	if w := e.admin("POST", "/api/notifications", body); w.Code != 200 {
		e.t.Fatalf("create %s: %d %s", body, w.Code, w.Body)
	}
	all := e.current().Notifications
	return all[len(all)-1]
}

func (e *env) current() settings.Notifications {
	e.t.Helper()
	doc, err := e.store.Notifications(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return doc
}

// apprise is a fake Apprise API that records the payloads it receives.
type apprise struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	payloads []map[string]any
}

func newApprise(t *testing.T) *apprise {
	a := &apprise{status: 200}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		a.mu.Lock()
		a.payloads = append(a.payloads, payload)
		status := a.status
		a.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *apprise) setStatus(status int) { a.mu.Lock(); a.status = status; a.mu.Unlock() }

func (a *apprise) received() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.payloads...)
}

func TestSettingsDefaultsAndPatch(t *testing.T) {
	e := newEnv(t, "")
	var got struct {
		Data     struct{ Events []Event }
		Settings settings.Notifications
	}
	w := e.admin("GET", "/api/notifications", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatalf("GET: %d %v %s", w.Code, err, w.Body)
	}
	if got.Settings.MaxFailedAttempts != 5 || got.Settings.MaxNotificationQueue != 20 || got.Settings.AppriseAPIURL != nil || len(got.Data.Events) != 6 {
		t.Fatalf("defaults: %+v", got)
	}
	if data := e.admin("GET", "/api/notificationdata", ""); !strings.Contains(data.Body.String(), `"onBackupFailed"`) {
		t.Fatalf("notificationdata: %s", data.Body)
	}
	w = e.admin("PATCH", "/api/notifications", `{"appriseApiUrl":"http://apprise.example.invalid","maxFailedAttempts":3,"maxNotificationQueue":8}`)
	if w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("PATCH: %d %q", w.Code, w.Body)
	}
	if c := e.current(); *c.AppriseAPIURL != "http://apprise.example.invalid" || c.MaxFailedAttempts != 3 || c.MaxNotificationQueue != 8 {
		t.Fatalf("stored: %+v", c)
	}
	// Absent keys reset to their defaults (NotificationSettings.update).
	e.admin("PATCH", "/api/notifications", `{}`)
	if c := e.current(); c.AppriseAPIURL != nil || c.MaxFailedAttempts != 5 || c.MaxNotificationQueue != 20 {
		t.Fatalf("after empty PATCH: %+v", c)
	}
	if w := e.admin("PATCH", "/api/notifications", `{"maxFailedAttempts":"many"}`); w.Code != http.StatusBadRequest {
		t.Errorf("wrong type: %d, want 400", w.Code)
	}
}

func TestCreateUpdateDelete(t *testing.T) {
	e := newEnv(t, "http://apprise.invalid")
	created := e.create(`{"eventName":"onTest","urls":["discord://test"],"titleTemplate":"Original","enabled":false,"libraryId":"","type":""}`)
	if created.LibraryID != nil || created.Type != nil || created.Enabled || created.TitleTemplate != "Original" || created.ID == "" || created.CreatedAt == 0 {
		t.Fatalf("created: %+v", created)
	}
	// No event or no URLs adds nothing and answers the current settings.
	for _, body := range []string{`{"eventName":"onTest","urls":[]}`, `{"urls":["a://b"]}`} {
		if w := e.admin("POST", "/api/notifications", body); w.Code != 200 || len(e.current().Notifications) != 1 {
			t.Fatalf("create %s: %d, %d notifications", body, w.Code, len(e.current().Notifications))
		}
	}
	// PATCH uses the route id and changes only the present keys; null clears.
	if w := e.admin("PATCH", "/api/notifications/"+created.ID, `{"bodyTemplate":"B","libraryId":"lib-1","enabled":true}`); w.Code != 200 {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	if n := e.current().Notifications[0]; n.TitleTemplate != "Original" || n.BodyTemplate != "B" || n.LibraryID == nil || *n.LibraryID != "lib-1" || !n.Enabled {
		t.Fatalf("updated: %+v", n)
	}
	e.admin("PATCH", "/api/notifications/"+created.ID, `{"libraryId":null}`)
	if e.current().Notifications[0].LibraryID != nil {
		t.Error("libraryId null did not clear it")
	}
	if w := e.admin("PATCH", "/api/notifications/missing", `{}`); w.Code != 404 {
		t.Errorf("PATCH missing: %d", w.Code)
	}
	if w := e.admin("DELETE", "/api/notifications/missing", ""); w.Code != 404 || w.Body.String() != "Not Found" {
		t.Fatalf("DELETE missing: %d %q", w.Code, w.Body)
	}
	if w := e.admin("DELETE", "/api/notifications/"+created.ID, ""); w.Code != 200 || len(e.current().Notifications) != 0 {
		t.Fatalf("DELETE: %d", w.Code)
	}
}

func TestEnablingResetsFailureRecord(t *testing.T) {
	e := newEnv(t, "http://apprise.invalid")
	n := e.create(`{"eventName":"onTest","urls":["a://b"],"enabled":false}`)
	_, err := e.store.UpdateNotifications(context.Background(), func(s *settings.Notifications) error {
		failing := s.Find(n.ID)
		failing.LastAttemptFailed, failing.NumConsecutiveFailedAttempts, failing.LastFiredAt = true, 4, db.Ptr(int64(1))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.admin("PATCH", "/api/notifications/"+n.ID, `{"enabled":true}`)
	if got := e.current().Notifications[0]; !got.Enabled || got.LastAttemptFailed || got.NumConsecutiveFailedAttempts != 0 || got.LastFiredAt != nil {
		t.Fatalf("failure record not reset: %+v", got)
	}
}

func TestRoutesRejectNonAdmin(t *testing.T) {
	e := newEnv(t, "")
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/notifications"}, {"PATCH", "/api/notifications"}, {"POST", "/api/notifications"},
		{"GET", "/api/notificationdata"}, {"GET", "/api/notifications/test"}, {"GET", "/api/notifications/x/test"},
		{"DELETE", "/api/notifications/x"}, {"PATCH", "/api/notifications/x"},
	} {
		if w := e.serve("user", route.method, route.path, `{}`); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as user: %d, want 403", route.method, route.path, w.Code)
		}
	}
}

func TestTriggerEventDeliversRenderedPayload(t *testing.T) {
	a := newApprise(t)
	e := newEnv(t, a.URL)
	e.create(`{"eventName":"onBackupFailed","urls":["discord://t"],"titleTemplate":"Backup failed","bodyTemplate":"{{errorMsg}} / {{ errorMsg }} / {{missing}}","enabled":true}`)
	if err := e.h.TriggerEvent(context.Background(), "onBackupFailed", map[string]any{"errorMsg": "disk full"}); err != nil {
		t.Fatal(err)
	}
	got := a.received()
	if len(got) != 1 || got[0]["title"] != "Backup failed" || got[0]["body"] != "disk full / disk full / {{missing}}" {
		t.Fatalf("payload: %#v", got)
	}
	if urls, _ := got[0]["urls"].([]any); len(urls) != 1 || urls[0] != "discord://t" {
		t.Fatalf("urls: %#v", got[0]["urls"])
	}
}

func TestTriggerEventOnlyEnabledMatching(t *testing.T) {
	a := newApprise(t)
	e := newEnv(t, a.URL)
	e.create(`{"eventName":"onBackupFailed","urls":["d://t"],"titleTemplate":"enabled","enabled":true}`)
	e.create(`{"eventName":"onBackupFailed","urls":["d://t"],"titleTemplate":"disabled","enabled":false}`)
	e.create(`{"eventName":"onBackupCompleted","urls":["d://t"],"titleTemplate":"other event","enabled":true}`)
	for _, event := range []string{"onBackupFailed", "onNoSuchEvent"} {
		if err := e.h.TriggerEvent(context.Background(), event, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.received(); len(got) != 1 || got[0]["title"] != "enabled" {
		t.Fatalf("delivered %#v, want only the enabled match", got)
	}
	for _, n := range e.current().Notifications {
		if fired := n.NumTimesFired == 1; fired != (n.TitleTemplate == "enabled") {
			t.Errorf("%s: numTimesFired %d", n.TitleTemplate, n.NumTimesFired)
		}
	}
}

func TestTriggerEventWithoutAppriseDoesNothing(t *testing.T) {
	e := newEnv(t, "")
	e.create(`{"eventName":"onBackupFailed","urls":["d://t"],"enabled":true}`)
	if err := e.h.TriggerEvent(context.Background(), "onBackupFailed", nil); err != nil {
		t.Fatal(err)
	}
	if n := e.current().Notifications[0]; n.NumTimesFired != 0 {
		t.Fatalf("fired without Apprise configured: %+v", n)
	}
}

// maxFailedAttempts (2) consecutive failures disable a notification; a success
// in between resets the count; a disabled one is not delivered again.
func TestConsecutiveFailuresDisable(t *testing.T) {
	a := newApprise(t)
	a.setStatus(500)
	e := newEnv(t, a.URL)
	e.create(`{"eventName":"onBackupFailed","urls":["d://t"],"enabled":true}`)
	trigger := func() { _ = e.h.TriggerEvent(context.Background(), "onBackupFailed", nil) }

	trigger()
	if n := e.current().Notifications[0]; !n.Enabled || n.NumConsecutiveFailedAttempts != 1 || !n.LastAttemptFailed || n.LastFiredAt == nil {
		t.Fatalf("after one failure: %+v", n)
	}
	a.setStatus(200)
	trigger()
	if n := e.current().Notifications[0]; !n.Enabled || n.NumConsecutiveFailedAttempts != 0 || n.LastAttemptFailed || n.NumTimesFired != 2 {
		t.Fatalf("a success must reset the count: %+v", n)
	}
	a.setStatus(500)
	trigger()
	trigger()
	if n := e.current().Notifications[0]; n.Enabled || n.NumConsecutiveFailedAttempts != 2 {
		t.Fatalf("two failures in a row must disable it: %+v", n)
	}
	before := len(a.received())
	trigger()
	if len(a.received()) != before {
		t.Error("a disabled notification was delivered again")
	}
	if e.updates != 4 {
		t.Errorf("notifications_updated emitted %d times, want 4", e.updates)
	}
}

func TestTestNotificationRoute(t *testing.T) {
	a := newApprise(t)
	e := newEnv(t, a.URL)
	n := e.create(`{"eventName":"onTest","urls":["d://t"],"titleTemplate":"Version {{version}}","bodyTemplate":"Hi {{version}}","enabled":true}`)
	if w := e.admin("GET", "/api/notifications/"+n.ID+"/test", ""); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("test: %d %q", w.Code, w.Body)
	}
	if got := a.received(); len(got) != 1 || got[0]["title"] != "Version v2.37.2" || got[0]["body"] != "Hi v2.37.2" {
		t.Fatalf("payload: %#v", got)
	}
	a.setStatus(503)
	if w := e.admin("GET", "/api/notifications/"+n.ID+"/test", ""); w.Code != 500 || w.Body.String() != "Internal Server Error" {
		t.Fatalf("Apprise failure: %d %q", w.Code, w.Body)
	}
}

func TestTestNotificationPreconditions(t *testing.T) {
	e := newEnv(t, "")
	n := e.create(`{"eventName":"onTest","urls":["d://t"]}`)
	if w := e.admin("GET", "/api/notifications/missing/test", ""); w.Code != 404 {
		t.Errorf("missing id: %d", w.Code)
	}
	if w := e.admin("GET", "/api/notifications/"+n.ID+"/test", ""); w.Code != 400 || w.Body.String() != "Apprise is not configured" {
		t.Errorf("unconfigured: %d %q", w.Code, w.Body)
	}
}

func TestTestEventRoute(t *testing.T) {
	a := newApprise(t)
	e := newEnv(t, a.URL)
	n := e.create(`{"eventName":"onTest","urls":["d://t"],"titleTemplate":"{{version}}","enabled":true}`)
	if w := e.admin("GET", "/api/notifications/test", ""); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("test event: %d %q", w.Code, w.Body)
	}
	if got := a.received(); len(got) != 1 || got[0]["title"] != "v2.37.2" {
		t.Fatalf("payload: %#v", got)
	}
	// ?fail=1 records a failed attempt without sending.
	e.admin("GET", "/api/notifications/test?fail=1", "")
	current := e.current()
	if got := current.Find(n.ID); len(a.received()) != 1 || !got.LastAttemptFailed || got.NumConsecutiveFailedAttempts != 1 {
		t.Fatalf("fail=1: %+v, %d posts", got, len(a.received()))
	}
}

func TestRender(t *testing.T) {
	data := map[string]any{"s": "text", "empty": "", "zero": 0, "zeroF": 0.0, "n": 3, "f": 2.5, "no": false, "yes": true, "nil": nil}
	for template, want := range map[string]string{
		"{{s}}": "text", "{{ s }}": "text", "{{  s  }}": "{{  s  }}", "{{empty}}": "{{empty}}", "{{zero}}": "{{zero}}",
		"{{zeroF}}": "{{zeroF}}", "{{n}} {{f}}": "3 2.5", "{{no}}{{yes}}": "{{no}}true", "{{nil}}{{other}}": "{{nil}}{{other}}",
		"{{s}} and {{s}}": "text and text", "plain": "plain",
	} {
		if got := render(template, data); got != want {
			t.Errorf("render(%q) = %q, want %q", template, got, want)
		}
	}
}
