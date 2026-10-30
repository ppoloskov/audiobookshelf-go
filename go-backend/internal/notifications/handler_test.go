package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

func notificationDB(t *testing.T, apprise string) *sql.DB {
	t.Helper()
	db, e := sql.Open("sqlite", "file:notifications-test-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME,updatedAt DATETIME); INSERT INTO settings(key,value,createdAt,updatedAt) VALUES('notification-settings',?,NULL,NULL)`, `{"id":"notification-settings","appriseType":"api","appriseApiUrl":"`+apprise+`","notifications":[],"maxFailedAttempts":2,"maxNotificationQueue":20,"notificationDelay":1000}`); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSettingsDefaultsAndPartialPatchMatchUpstream(t *testing.T) {
	db, err := sql.Open("sqlite", "file:notification-settings-defaults?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	admin := store.User{Type: "admin"}
	get := httptest.NewRecorder()
	h.Handle(get, httptest.NewRequest(http.MethodGet, "/api/notifications", nil), admin)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", get.Code, get.Body)
	}
	var response struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Settings["maxFailedAttempts"] != float64(5) || response.Settings["maxNotificationQueue"] != float64(20) || response.Settings["appriseApiUrl"] != nil {
		t.Fatalf("upstream defaults not returned: %#v", response.Settings)
	}
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/notifications", strings.NewReader(`{"appriseApiUrl":"http://apprise.example.invalid","maxFailedAttempts":3,"maxNotificationQueue":8}`)), admin)
	if patch.Code != http.StatusOK || patch.Body.String() != "OK" || patch.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("PATCH response: status=%d content-type=%q body=%q", patch.Code, patch.Header().Get("Content-Type"), patch.Body.String())
	}
	defaults := httptest.NewRecorder()
	h.Handle(defaults, httptest.NewRequest(http.MethodPatch, "/api/notifications", strings.NewReader(`{}`)), admin)
	if defaults.Code != http.StatusOK || defaults.Body.String() != "OK" {
		t.Fatalf("default PATCH response: status=%d body=%q", defaults.Code, defaults.Body.String())
	}
	var persisted string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='notification-settings'`).Scan(&persisted); err != nil {
		t.Fatalf("first setting update was not persisted: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(persisted), &saved); err != nil {
		t.Fatal(err)
	}
	if saved["appriseApiUrl"] != nil || saved["maxFailedAttempts"] != float64(5) || saved["maxNotificationQueue"] != float64(20) {
		t.Fatalf("partial PATCH did not restore upstream defaults: %#v", saved)
	}
}

func TestNotificationMutationLookupAndMissingRouteResponse(t *testing.T) {
	db := notificationDB(t, "")
	h := New(db, config.Config{}, nil)
	admin := store.User{Type: "admin"}
	create := httptest.NewRecorder()
	h.Handle(create, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onTest","urls":["discord://test"],"titleTemplate":"Original"}`)), admin)
	if create.Code != http.StatusOK {
		t.Fatalf("create status %d: %s", create.Code, create.Body)
	}
	var settings map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	items := settings["notifications"].([]any)
	created := items[0].(map[string]any)
	id := created["id"].(string)
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/notifications/"+id, strings.NewReader(`{"titleTemplate":"Ignored without body ID"}`)), admin)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status %d: %s", patch.Code, patch.Body)
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	items = settings["notifications"].([]any)
	created = items[0].(map[string]any)
	if created["titleTemplate"] != "Original" {
		t.Fatalf("route ID must not substitute for missing PATCH body ID: %#v", created)
	}
	minimal := httptest.NewRecorder()
	h.Handle(minimal, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onTest","urls":["discord://minimal"],"enabled":"false","libraryId":"","type":""}`)), admin)
	if minimal.Code != http.StatusOK {
		t.Fatalf("minimal create status %d: %s", minimal.Code, minimal.Body)
	}
	if err := json.Unmarshal(minimal.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	items = settings["notifications"].([]any)
	created = items[len(items)-1].(map[string]any)
	if created["enabled"] != true || created["libraryId"] != nil || created["type"] != nil {
		t.Fatalf("create defaults/coercion do not match upstream: %#v", created)
	}
	if _, ok := created["titleTemplate"]; ok {
		t.Fatalf("omitted titleTemplate must not serialize as null: %#v", created)
	}
	if _, ok := created["bodyTemplate"]; ok {
		t.Fatalf("omitted bodyTemplate must not serialize as null: %#v", created)
	}
	missing := httptest.NewRecorder()
	h.Handle(missing, httptest.NewRequest(http.MethodDelete, "/api/notifications/00000000-0000-4000-8000-000000000000", nil), admin)
	if missing.Code != http.StatusNotFound || missing.Body.String() != "Not Found" || missing.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("missing DELETE response status=%d content-type=%q body=%q", missing.Code, missing.Header().Get("Content-Type"), missing.Body.String())
	}
}

func TestTriggerEventDeliversRuntimeLifecyclePayload(t *testing.T) {
	got := make(chan map[string]any, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		got <- payload
		w.WriteHeader(200)
	}))
	defer mock.Close()
	db := notificationDB(t, mock.URL)
	h := New(db, config.Config{}, nil)
	admin := store.User{Type: "admin"}
	create := httptest.NewRecorder()
	h.Handle(create, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onBackupFailed","urls":["discord://test"],"titleTemplate":"Backup failed","bodyTemplate":"{{errorMsg}}","enabled":true}`)), admin)
	if create.Code != 200 {
		t.Fatalf("create notification status %d", create.Code)
	}
	if err := h.TriggerEvent(context.Background(), "onBackupFailed", map[string]any{"errorMsg": "disk full"}); err != nil {
		t.Fatal(err)
	}
	if payload := <-got; payload["body"] != "disk full" {
		t.Fatalf("runtime event payload %#v", payload)
	}
}
func TestNotificationTestPostsRenderedPayloadToLocalApprise(t *testing.T) {
	got := make(chan map[string]any, 1)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		got <- p
		w.WriteHeader(200)
	}))
	defer mock.Close()
	db := notificationDB(t, mock.URL)
	h := New(db, config.Config{}, nil)
	admin := store.User{Type: "admin"}
	create := httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onTest","urls":["discord://test"],"titleTemplate":"Version {{version}}","bodyTemplate":"Hi {{version}}","enabled":true}`))
	cw := httptest.NewRecorder()
	h.Handle(cw, create, admin)
	if cw.Code != 200 {
		t.Fatalf("create status %d", cw.Code)
	}
	var state map[string]any
	if json.Unmarshal(cw.Body.Bytes(), &state) != nil {
		t.Fatal("invalid settings response")
	}
	list := state["notifications"].([]any)
	id := list[0].(map[string]any)["id"].(string)
	testReq := httptest.NewRequest(http.MethodGet, "/api/notifications/"+id+"/test", nil)
	tw := httptest.NewRecorder()
	h.Handle(tw, testReq, admin)
	if tw.Code != 200 || tw.Body.String() != "OK" || tw.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("test status %d: %s", tw.Code, tw.Body.String())
	}
	p := <-got
	if p["title"] != "Version v2.37.2" || p["body"] != "Hi v2.37.2" {
		t.Fatalf("payload %#v", p)
	}
}

func TestNotificationTestAppriseFailureUsesSendStatus(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer mock.Close()
	db := notificationDB(t, mock.URL)
	h := New(db, config.Config{}, nil)
	create := httptest.NewRecorder()
	h.Handle(create, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onTest","urls":["discord://test"]}`)), store.User{Type: "admin"})
	var settings map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	id := settings["notifications"].([]any)[0].(map[string]any)["id"].(string)
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/notifications/"+id+"/test", nil), store.User{Type: "admin"})
	if w.Code != http.StatusInternalServerError || w.Body.String() != "Internal Server Error" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status/header/body = %d/%q/%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
func TestNotificationRejectsNonAdmin(t *testing.T) {
	db := notificationDB(t, "")
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/notifications", nil), store.User{Type: "user"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
}

func TestNotificationTestMissingIDPrecedesAppriseConfiguration(t *testing.T) {
	db := notificationDB(t, "")
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/notifications/missing/test", nil), store.User{Type: "admin"})
	if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != http.StatusText(http.StatusNotFound) {
		t.Fatalf("status/body = %d/%q, want 404 %q", w.Code, w.Body.String(), http.StatusText(http.StatusNotFound))
	}
}

func TestNotificationTestUnavailableUsesExpressSendResponse(t *testing.T) {
	db := notificationDB(t, "")
	h := New(db, config.Config{}, nil)
	create := httptest.NewRecorder()
	h.Handle(create, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(`{"eventName":"onTest","urls":["discord://test"]}`)), store.User{Type: "admin"})
	var settings map[string]any
	if err := json.Unmarshal(create.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	id := settings["notifications"].([]any)[0].(map[string]any)["id"].(string)
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/notifications/"+id+"/test", nil), store.User{Type: "admin"})
	if w.Code != http.StatusBadRequest || w.Body.String() != "Apprise is not configured" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status/header/body = %d/%q/%q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w.Header().Get("Content-Length") != "25" || w.Header().Get("ETag") == "" {
		t.Fatalf("missing Express response headers: %#v", w.Header())
	}
}
