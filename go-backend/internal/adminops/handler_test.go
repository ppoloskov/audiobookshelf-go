package adminops

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

func adminopsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, e := sql.Open("sqlite", "file:adminops-test-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,updatedAt DATETIME);INSERT INTO settings VALUES('server-settings',?,NULL)`, `{"id":"server-settings","tokenSecret":"never-return","authActiveAuthMethods":["local"],"authOpenIDClientID":"id-secret","authOpenIDClientSecret":"client-secret","authOpenIDMobileRedirectURIs":["audiobookshelf://oauth"],"authOpenIDGroupClaim":"groups","authOpenIDAdvancedPermsClaim":"permissions"}`)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAdminStatsCachePurgeAndWatcherCallback(t *testing.T) {
	db := adminopsDB(t)
	_, err := db.Exec(`CREATE TABLE libraryItems(mediaType TEXT,size INTEGER); CREATE TABLE books(audioFiles JSON); CREATE TABLE podcastEpisodes(id TEXT); INSERT INTO libraryItems VALUES('book',100),('podcast',50); INSERT INTO books VALUES('[{},{}]'); INSERT INTO podcastEpisodes VALUES('p1')`)
	if err != nil {
		t.Fatal(err)
	}
	metadata := t.TempDir()
	cache := filepath.Join(metadata, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "items"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "items", "stale"), []byte("cached"), 0600); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{MetadataPath: metadata}, nil)
	admin := store.User{Type: "admin"}
	stats := httptest.NewRecorder()
	if !h.Handle(stats, httptest.NewRequest(http.MethodGet, "/api/stats/server", nil), admin) || stats.Code != 200 {
		t.Fatalf("stats status=%d %s", stats.Code, stats.Body.String())
	}
	var body map[string]any
	if json.Unmarshal(stats.Body.Bytes(), &body) != nil || body["total"].(map[string]any)["numAudioFiles"] != float64(3) {
		t.Fatalf("stats body %s", stats.Body.String())
	}
	purge := httptest.NewRecorder()
	h.Handle(purge, httptest.NewRequest(http.MethodPost, "/api/cache/items/purge", nil), admin)
	if purge.Code != 200 {
		t.Fatalf("purge status=%d", purge.Code)
	}
	if _, err := os.Stat(filepath.Join(cache, "items")); err != nil {
		t.Fatalf("item cache was not recreated: %v", err)
	}
	called := false
	h.SetWatcherUpdate(func(_ context.Context, libraryID, path, kind, oldPath string) error {
		called = true
		if libraryID != "lib" || path != "/new" || kind != "rename" || oldPath != "/old" {
			t.Fatalf("watch args %q %q %q %q", libraryID, path, kind, oldPath)
		}
		return nil
	})
	watch := httptest.NewRecorder()
	h.Handle(watch, httptest.NewRequest(http.MethodPost, "/api/watcher/update", strings.NewReader(`{"libraryId":"lib","path":"/new","type":"rename","oldPath":"/old"}`)), admin)
	if watch.Code != 200 || !called {
		t.Fatalf("watch status=%d called=%v", watch.Code, called)
	}
}
func TestSettingsPatchWhitelistAndSecretMasking(t *testing.T) {
	db := adminopsDB(t)
	h := New(db, config.Config{}, nil)
	u := store.User{Type: "admin"}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", strings.NewReader(`{"language":"fr","tokenSecret":"attacker","unlisted":true}`))
	w := httptest.NewRecorder()
	h.Handle(w, req, u)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid JSON")
	}
	settings := out["serverSettings"].(map[string]any)
	if settings["language"] != "fr" {
		t.Fatalf("language %#v", settings["language"])
	}
	for _, k := range []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"} {
		if _, ok := settings[k]; ok {
			t.Fatalf("secret setting %s leaked", k)
		}
	}
	var raw string
	if e := db.QueryRow(`SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "attacker") {
		t.Fatal("unpatchable token secret changed")
	}
}
func TestAuthSettingsAdminOnlyAndCronValidation(t *testing.T) {
	db := adminopsDB(t)
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	handled := h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/auth-settings", nil), store.User{Type: "user"})
	if !handled || w.Code != 403 {
		t.Fatalf("handled=%v status=%d", handled, w.Code)
	}
	ok := httptest.NewRecorder()
	h.Handle(ok, httptest.NewRequest(http.MethodPost, "/api/validate-cron", strings.NewReader(`{"expression":"0 * * * *"}`)), store.User{Type: "user"})
	if ok.Code != 200 {
		t.Fatalf("valid cron status %d", ok.Code)
	}
	bad := httptest.NewRecorder()
	h.Handle(bad, httptest.NewRequest(http.MethodPost, "/api/validate-cron", strings.NewReader(`{"expression":"never"}`)), store.User{Type: "user"})
	if bad.Code != 400 {
		t.Fatalf("invalid cron status %d", bad.Code)
	}
}
