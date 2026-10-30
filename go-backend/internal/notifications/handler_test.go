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
	if tw.Code != 200 {
		t.Fatalf("test status %d: %s", tw.Code, tw.Body.String())
	}
	p := <-got
	if p["title"] != "Version v2.37.2" || p["body"] != "Hi v2.37.2" {
		t.Fatalf("payload %#v", p)
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
