package adminops

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

// serve dispatches r through Routes the way the server does: the user travels
// in the request context.
func serve(h *Handler, w http.ResponseWriter, r *http.Request, u store.User) {
	mux := http.NewServeMux()
	h.Routes(mux)
	mux.ServeHTTP(w, r.WithContext(store.ContextWithUser(r.Context(), u)))
}

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
	called := false
	h := New(db, config.Config{MetadataPath: metadata}, nil, Deps{WatcherUpdate: func(_ context.Context, libraryID, path, kind string) error {
		called = true
		if libraryID != "lib" || path != "/new" || kind != "rename" {
			t.Fatalf("watch args %q %q %q", libraryID, path, kind)
		}
		return nil
	}})
	admin := store.User{Type: "admin"}
	stats := httptest.NewRecorder()
	serve(h, stats, httptest.NewRequest(http.MethodGet, "/api/stats/server", nil), admin)
	if stats.Code != 200 {
		t.Fatalf("stats status=%d %s", stats.Code, stats.Body.String())
	}
	var body map[string]any
	if json.Unmarshal(stats.Body.Bytes(), &body) != nil || body["total"].(map[string]any)["numAudioFiles"] != float64(3) {
		t.Fatalf("stats body %s", stats.Body.String())
	}
	purge := httptest.NewRecorder()
	serve(h, purge, httptest.NewRequest(http.MethodPost, "/api/cache/items/purge", nil), admin)
	if purge.Code != 200 {
		t.Fatalf("purge status=%d", purge.Code)
	}
	if _, err := os.Stat(filepath.Join(cache, "items")); err != nil {
		t.Fatalf("item cache was not recreated: %v", err)
	}
	watch := httptest.NewRecorder()
	serve(h, watch, httptest.NewRequest(http.MethodPost, "/api/watcher/update", strings.NewReader(`{"libraryId":"lib","path":"/new","type":"rename","oldPath":"/old"}`)), admin)
	if watch.Code != 200 || !called {
		t.Fatalf("watch status=%d called=%v", watch.Code, called)
	}
}
func TestSettingsPatchWhitelistAndSecretMasking(t *testing.T) {
	db := adminopsDB(t)
	h := New(db, config.Config{}, nil, Deps{})
	u := store.User{Type: "admin"}
	if _, err := serversettings.Get(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", strings.NewReader(`{"language":"fr","tokenSecret":"attacker","unlisted":true}`))
	w := httptest.NewRecorder()
	serve(h, w, req, u)
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
	if cached, err := serversettings.Get(req.Context(), db); err != nil || cached.Doc()["language"] != "fr" {
		t.Fatalf("cached settings stale after PATCH: %v %v", cached.Doc()["language"], err)
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
	h := New(db, config.Config{}, nil, Deps{})
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodGet, "/api/auth-settings", nil), store.User{Type: "user"})
	if w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
	ok := httptest.NewRecorder()
	serve(h, ok, httptest.NewRequest(http.MethodPost, "/api/validate-cron", strings.NewReader(`{"expression":"0 * * * *"}`)), store.User{Type: "user"})
	if ok.Code != 200 {
		t.Fatalf("valid cron status %d", ok.Code)
	}
	bad := httptest.NewRecorder()
	serve(h, bad, httptest.NewRequest(http.MethodPost, "/api/validate-cron", strings.NewReader(`{"expression":"never"}`)), store.User{Type: "user"})
	if bad.Code != 400 {
		t.Fatalf("invalid cron status %d", bad.Code)
	}
}

// FileSystemController.checkPathExists answers an invalid body with
// res.status(400).json({error: 'Invalid request body'}) (FileSystemController.js:94-97),
// so the response carries a JSON Content-Type.
func TestPathExistsInvalidBodyIsJSON(t *testing.T) {
	h := New(adminopsDB(t), config.Config{}, nil, Deps{})
	u := store.User{Type: "admin", IsActive: true, Permissions: json.RawMessage(`{"upload":true}`)}
	for _, body := range []string{`{}`, `{"directory":"a"}`, `{"folderPath":"/x"}`, `not json`} {
		w := httptest.NewRecorder()
		serve(h, w, httptest.NewRequest(http.MethodPost, "/api/filesystem/pathexists", strings.NewReader(body)), u)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", body, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Fatalf("%s: Content-Type %q, want application/json; charset=utf-8", body, ct)
		}
		if got := strings.TrimSpace(w.Body.String()); got != `{"error":"Invalid request body"}` {
			t.Fatalf("%s: body %q", body, got)
		}
	}
}

// The sorting-prefix rewrite reads each table and then updates it; every query
// runs on the one-connection pool, so none may start while a cursor is open.
func TestSortingPrefixesRewritesAllTablesOnOneConnection(t *testing.T) {
	db := adminopsDB(t)
	_, err := db.Exec(`CREATE TABLE books(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT);
CREATE TABLE podcasts(id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT);
CREATE TABLE series(id TEXT PRIMARY KEY,name TEXT,nameIgnorePrefix TEXT);
INSERT INTO books VALUES('b1','Der Hobbit','Der Hobbit');
INSERT INTO podcasts VALUES('p1','Der Podcast','Der Podcast');
INSERT INTO series VALUES('s1','Der Ring','Der Ring')`)
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil, Deps{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPatch, "/api/sorting-prefixes", strings.NewReader(`{"sortingPrefixes":["der"]}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	serve(h, w, req, store.User{Type: "admin"})
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		RowsUpdated int `json:"rowsUpdated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.RowsUpdated != 3 {
		t.Fatalf("rowsUpdated = %d (%v), body %s", out.RowsUpdated, err, w.Body.String())
	}
	for query, want := range map[string]string{
		`SELECT titleIgnorePrefix FROM books`:    "Hobbit",
		`SELECT titleIgnorePrefix FROM podcasts`: "Podcast",
		`SELECT nameIgnorePrefix FROM series`:    "Ring",
	} {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %q (%v), want %q", query, got, err, want)
		}
	}
}
