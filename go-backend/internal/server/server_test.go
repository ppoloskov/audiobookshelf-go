package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

type fakeRepo struct {
	user      store.User
	libraries []map[string]any
	db        *sql.DB
	settings  map[string]any
}

func (f *fakeRepo) UserByIDOrOldID(context.Context, string) (store.User, error) { return f.user, nil }
func (f *fakeRepo) ServerSettings(context.Context) (map[string]any, error) {
	if f.settings != nil {
		return f.settings, nil
	}
	return map[string]any{"tokenSecret": "test-secret"}, nil
}
func (f *fakeRepo) Libraries(context.Context) ([]map[string]any, error) { return f.libraries, nil }
func (f *fakeRepo) DB() *sql.DB                                         { return f.db }

func TestPingStatusAndPrefixedPing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE users(type TEXT); INSERT INTO users(type) VALUES ('user')`); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{db: db}
	cfg := config.Config{ConfigPath: "/abs/config", MetadataPath: "/abs/metadata", RouterBasePath: "/abs"}
	h, err := NewHandler(context.Background(), cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/ping", "/abs/ping"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != `{"success":true}` {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"app": "audiobookshelf", "serverVersion": "2.37.2", "isInit": false, "language": "en-us", "authMethods": []any{"local"}, "authFormData": map[string]any{"authLoginCustomMessage": ""}, "ConfigPath": "/abs/config", "MetadataPath": "/abs/metadata"}
	if w.Code != 200 || !jsonEqual(got, want) {
		t.Fatalf("status response mismatch: status=%d got=%#v want=%#v", w.Code, got, want)
	}
}

func TestLibraryRequiresAuthAndFiltersRestrictedLibraries(t *testing.T) {
	repo := &fakeRepo{user: store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"librariesAccessible":["allowed"]}`)}, libraries: []map[string]any{{"id": "allowed"}, {"id": "denied"}}}
	h := &Handler{Config: config.Config{}, Store: repo, Secret: []byte("test-secret")}
	unauth := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	uw := httptest.NewRecorder()
	h.ServeHTTP(uw, unauth)
	if uw.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d body=%s", uw.Code, uw.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"u1","username":"tester","type":"access","exp":4102444800}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var got map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(got["libraries"]) != 1 || got["libraries"][0]["id"] != "allowed" {
		t.Fatalf("restricted list: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUnsupportedAuthorizedRouteIsExplicit501(t *testing.T) {
	repo := &fakeRepo{user: store.User{ID: "u1", IsActive: true}}
	h := &Handler{Config: config.Config{}, Store: repo, Secret: []byte("test-secret")}
	r := httptest.NewRequest(http.MethodPost, "/api/authorize", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"u1","username":"tester","type":"access","exp":4102444800}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "experimental Go backend") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBearerRejectsInactiveRefreshAndAPIKey(t *testing.T) {
	tests := []struct {
		name, payload string
		active        bool
	}{
		{name: "inactive user", payload: `{"userId":"u1","username":"tester","type":"access","exp":4102444800}`},
		{name: "refresh token", payload: `{"userId":"u1","username":"tester","type":"refresh","exp":4102444800}`, active: true},
		{name: "api key", payload: `{"keyId":"k1","type":"api"}`, active: true},
		{name: "api key alias", payload: `{"keyId":"k1","userId":"u1","type":"apiKey"}`, active: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{user: store.User{ID: "u1", IsActive: tc.active}}
			h := &Handler{Store: repo, Secret: []byte("test-secret")}
			r := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
			r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", tc.payload))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || w.Body.String() != "Unauthorized" {
				t.Fatalf("status=%d body=%q, want 401 Unauthorized", w.Code, w.Body.String())
			}
		})
	}
}

func TestLibraryStatsAndCustomAuthMessageReturn501(t *testing.T) {
	repo := &fakeRepo{user: store.User{ID: "u1", IsActive: true}}
	h := &Handler{Store: repo, Secret: []byte("test-secret")}
	r := httptest.NewRequest(http.MethodGet, "/api/libraries?include=stats", nil)
	r.Header.Set("Authorization", "Bearer "+makeToken(t, "test-secret", `{"userId":"u1","username":"tester","type":"access"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("stats status=%d body=%s", w.Code, w.Body.String())
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users(type TEXT); INSERT INTO users(type) VALUES ('root')`); err != nil {
		t.Fatal(err)
	}
	repo.db = db
	repo.settings = map[string]any{"authLoginCustomMessage": `<script>alert(1)</script>`}
	h.Settings = repo.settings
	r = httptest.NewRequest(http.MethodGet, "/status", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("custom message status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHealthcheckGetAndHead(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct{ method, body string }{{http.MethodGet, "OK"}, {http.MethodHead, ""}} {
		r := httptest.NewRequest(tc.method, "/healthcheck", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != tc.body || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Content-Length") != "2" {
			t.Fatalf("%s: status=%d body=%q headers=%v", tc.method, w.Code, w.Body.String(), w.Header())
		}
	}
}

func makeToken(t *testing.T, secret, payload string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	unsigned := enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + enc(mac.Sum(nil))
}
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
