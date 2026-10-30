package userapi

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

func userFixture(t *testing.T) (*sql.DB, store.User) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, email TEXT, pash TEXT, type TEXT, token TEXT, isActive INTEGER, isLocked INTEGER, lastSeen DATETIME, permissions TEXT, bookmarks TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY, mediaItemId TEXT, mediaItemType TEXT, duration REAL, currentTime REAL, isFinished INTEGER, hideFromContinueListening INTEGER, ebookLocation TEXT, ebookProgress REAL, finishedAt DATETIME, extraData TEXT, podcastId TEXT, userId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE libraries (id TEXT PRIMARY KEY)`,
		`CREATE TABLE libraryItems (id TEXT PRIMARY KEY, mediaId TEXT, mediaType TEXT, libraryId TEXT, libraryFolderId TEXT, path TEXT, relPath TEXT, isFile INTEGER, mtime DATETIME, ctime DATETIME, birthtime DATETIME, createdAt DATETIME, updatedAt DATETIME, lastScan DATETIME, lastScanVersion INTEGER, isMissing INTEGER, isInvalid INTEGER, size INTEGER, extraData TEXT, libraryFiles TEXT, ino TEXT)`,
		`CREATE TABLE books (id TEXT PRIMARY KEY, explicit INTEGER, tags TEXT)`,
		`CREATE TABLE podcasts (id TEXT PRIMARY KEY, explicit INTEGER, tags TEXT)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY, podcastId TEXT)`,
		`CREATE TABLE apiKeys (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT, expiresAt DATETIME, lastUsedAt DATETIME, isActive INTEGER NOT NULL DEFAULT 0, permissions TEXT, userId TEXT, createdByUserId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE playlists (id TEXT PRIMARY KEY, name TEXT, description TEXT, libraryId TEXT, userId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE playlistMediaItems (id TEXT PRIMARY KEY, mediaItemId TEXT, mediaItemType TEXT, "order" INTEGER, playlistId TEXT, createdAt DATETIME)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	_, err = db.Exec(`INSERT INTO users(id,username,email,pash,type,token,isActive,isLocked,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES ('u1','reader','','','user','tok',1,0,?,?,?, ?,?)`, `{"accessAllLibraries":true,"accessAllTags":true}`, `[]`, `{}`, now, now)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	user := store.User{ID: "u1", Username: "reader", Type: "user", Token: "tok", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`), Bookmarks: json.RawMessage(`[]`), CreatedAt: msPtr(now.UnixMilli())}
	return db, user
}

func TestPlaylistCreateListAndOwnerRestriction(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO libraries(id) VALUES ('lib1')`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	create := httptest.NewRequest(http.MethodPost, "/api/playlists", strings.NewReader(`{"name":"<b>Queue</b>","libraryId":"lib1"}`))
	w := httptest.NewRecorder()
	if !h.Handle(w, create, user) || w.Code != http.StatusOK {
		t.Fatalf("POST /api/playlists status=%d body=%s", w.Code, w.Body.String())
	}
	var playlist map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &playlist); err != nil {
		t.Fatal(err)
	}
	if playlist["name"] != "Queue" || playlist["userId"] != user.ID || len(playlist["items"].([]any)) != 0 {
		t.Fatalf("unexpected playlist: %#v", playlist)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/playlists", nil)
	w = httptest.NewRecorder()
	if !h.Handle(w, list, user) || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"playlists":[{`) {
		t.Fatalf("GET /api/playlists status=%d body=%s", w.Code, w.Body.String())
	}
	other := user
	other.ID = "another-user"
	get := httptest.NewRequest(http.MethodGet, "/api/playlists/"+playlist["id"].(string), nil)
	w = httptest.NewRecorder()
	if !h.Handle(w, get, other) || w.Code != http.StatusForbidden {
		t.Fatalf("cross-owner playlist status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPlaylistBatchAddMissingLibraryItemUsesLegacyError(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO libraries(id) VALUES ('lib1')`)
	_, _ = db.Exec(`INSERT INTO playlists(id,name,libraryId,userId,createdAt,updatedAt) VALUES ('p1','Queue','lib1','u1',1,1)`)
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/playlists/p1/batch/add", strings.NewReader(`{"items":[{"libraryItemId":"missing"}]}`))
	r.Header.Set("Content-Type", "application/json")
	if !h.Handle(w, r, user) || w.Code != http.StatusBadRequest || w.Body.String() != "Invalid request body items" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("ETag") != expressUserETag([]byte("Invalid request body items")) {
		t.Fatalf("batch add missing item = %d %q", w.Code, w.Body.String())
	}
}

func TestBatchResponseTopLevelKeyOrder(t *testing.T) {
	for _, keys := range [][]string{{"id", "libraryId", "name", "userId", "description", "lastUpdate", "createdAt", "books"}, {"id", "name", "libraryId", "userId", "description", "lastUpdate", "createdAt", "items"}} {
		w := httptest.NewRecorder()
		writeJSONKeys(w, 200, map[string]any{"id": "c1", "libraryId": "lib1", "name": "Shelf", "userId": "u1", "description": nil, "lastUpdate": 1, "createdAt": 2, "books": []any{}, "items": []any{}}, keys)
		prefix := `{"id":"c1","libraryId":"lib1"`
		if keys[1] == "name" {
			prefix = `{"id":"c1","name":"Shelf","libraryId":"lib1"`
		}
		if !strings.HasPrefix(w.Body.String(), prefix) {
			t.Errorf("ordered response = %s", w.Body.String())
		}
	}
}

func TestCollectionMissingUsesExpressSend(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE collections (id TEXT PRIMARY KEY, libraryId TEXT, name TEXT, description TEXT, createdAt DATETIME, updatedAt DATETIME)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	for _, path := range []string{"/api/collections/missing", "/api/playlists/missing"} {
		w := httptest.NewRecorder()
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, path, nil), user) {
			t.Fatalf("%s route did not match", path)
		}
		want := "Collection not found"
		if strings.Contains(path, "playlists") {
			want = "Playlist not found"
		}
		if w.Code != http.StatusNotFound || w.Body.String() != want || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("ETag") != expressUserETag([]byte(want)) {
			t.Fatalf("%s status=%d headers=%v body=%q", path, w.Code, w.Header(), w.Body.String())
		}
	}
}

func TestDeleteMissingSessionUsesSendStatusNotFound(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT, userId TEXT)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodDelete, "/api/me/sessions/00000000-0000-0000-0000-000000000000", nil), user) {
		t.Fatal("route did not match")
	}
	if w.Code != http.StatusNotFound || w.Body.String() != "Not Found" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("ETag") != expressUserETag([]byte("Not Found")) {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestDeleteMalformedSessionIDMatchesUpstreamValidation(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT, userId TEXT)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodDelete, "/api/me/sessions/not-a-uuid", nil), user) {
		t.Fatal("route did not match")
	}
	if w.Code != http.StatusBadRequest || w.Body.String() != "Bad Request" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestEmptyCollectionNameUsesExpressSend(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	user.Permissions = json.RawMessage(`{"update":true,"accessAllLibraries":true}`)
	h := New(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(`{"name":"","libraryId":"lib1","books":["li1"]}`)), user) {
		t.Fatal("route did not match")
	}
	const body = "Invalid collection data"
	if w.Code != http.StatusBadRequest || w.Body.String() != body || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("ETag") != expressUserETag([]byte(body)) {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestCollectionDescriptionDBValueMatchesLegacyNullCoercion(t *testing.T) {
	if got := collectionDescriptionForDB(""); got != nil {
		t.Fatalf("empty description binding=%#v, want nil", got)
	}
	if got := collectionDescriptionForDB("notes"); got != "notes" {
		t.Fatalf("non-empty description binding=%#v", got)
	}
}

func expressUserETag(body []byte) string {
	// Express send ETag: W/"<hex byte length>-<base64 SHA-1 without padding>".
	sum := sha1.Sum(body)
	return fmt.Sprintf("W/\"%x-%s\"", len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "="))
}

func TestProgressReadAndUpdate(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId) VALUES ('li1','book1','book','lib1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookProgress,extraData,createdAt,updatedAt) VALUES ('p1','u1','book1','book',100,25,0,0,0,?,?,?)`, `{"libraryItemId":"li1","progress":0.25}`, now, now); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/me/progress", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != 200 {
		t.Fatalf("GET /me/progress: handled=%v status=%d body=%s", true, w.Code, w.Body.String())
	}
	var all map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all["mediaProgress"]) != 1 || all["mediaProgress"][0]["libraryItemId"] != "li1" || all["mediaProgress"][0]["progress"] != 0.25 {
		t.Fatalf("unexpected progress payload: %s", w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPatch, "/me/progress/li1", strings.NewReader(`{"currentTime":95,"duration":100}`))
	w = httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != 200 {
		t.Fatalf("PATCH /me/progress: status=%d body=%s", w.Code, w.Body.String())
	}
	var finished int
	var current float64
	var extra string
	if err := db.QueryRow(`SELECT isFinished,currentTime,extraData FROM mediaProgresses WHERE id='p1'`).Scan(&finished, &current, &extra); err != nil {
		t.Fatal(err)
	}
	if finished != 1 || current != 95 || !strings.Contains(extra, `"progress":1`) {
		t.Fatalf("updated progress finished=%d current=%v extra=%s", finished, current, extra)
	}
}

func TestProgressRowsParsesSequelizeTimestamp(t *testing.T) {
	db, _ := userFixture(t)
	defer db.Close()
	const timestamp = "2026-10-28 04:28:27.188 +00:00"
	if _, err := db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,mediaItemType,currentTime,isFinished,hideFromContinueListening,extraData,createdAt,updatedAt) VALUES ('p-ts','u1','book1','book',1,0,0,'{}',?,?)`, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	rows, err := ProgressRows(context.Background(), db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d progress rows, want 1", len(rows))
	}
	progress := rows[0].(map[string]any)
	want := time.Date(2026, time.October, 28, 4, 28, 27, 188_000_000, time.UTC).UnixMilli()
	if progress["lastUpdate"] != want || progress["startedAt"] != want {
		t.Fatalf("timestamps = lastUpdate:%v startedAt:%v, want %d", progress["lastUpdate"], progress["startedAt"], want)
	}
}

func TestMePreservesNullEmail(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET email=NULL WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != http.StatusOK {
		t.Fatalf("GET /api/me status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if value, present := body["email"]; !present || value != nil {
		t.Fatalf("email = %#v, want JSON null", value)
	}
}

func TestUserDTOUsesUpstreamNullAndArrayShapes(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET email=NULL, permissions='{}' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	user.Email = ""
	user.Permissions = json.RawMessage(`{}`)
	dto := minimalBrowserUser(user, false)
	if dto["email"] != nil {
		t.Fatalf("email=%#v, want null", dto["email"])
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		got, ok := dto[key].([]string)
		if !ok || got == nil || len(got) != 0 {
			t.Fatalf("%s=%#v, want []", key, dto[key])
		}
	}
	if _, ok := dto["isOldToken"]; ok {
		t.Fatalf("unset isOldToken must be omitted: %#v", dto)
	}
}

func TestBookmarkCreateAndLibraryPermissionDenial(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId) VALUES ('li1','book1','book','lib1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO books(id,explicit,tags) VALUES ('book1',0,'[]')`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	r := httptest.NewRequest(http.MethodPost, "/me/item/li1/bookmark", strings.NewReader(`{"time":42,"title":"Chapter"}`))
	w := httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"Chapter"`) {
		t.Fatalf("create bookmark: status=%d body=%s", w.Code, w.Body.String())
	}
	var stored string
	if err := db.QueryRow(`SELECT bookmarks FROM users WHERE id='u1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"time":42`) {
		t.Fatalf("bookmark not persisted: %s", stored)
	}
	denied := user
	denied.Permissions = json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":[],"accessAllTags":true}`)
	r = httptest.NewRequest(http.MethodPost, "/me/item/li1/bookmark", strings.NewReader(`{"time":43,"title":"No access"}`))
	w = httptest.NewRecorder()
	h.Handle(w, r, denied)
	if w.Code != http.StatusForbidden {
		t.Fatalf("restricted bookmark status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRouteMissRemainsUnhandled(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	h := New(db, config.Config{}, nil)
	if h.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/tags", nil), user) {
		t.Fatal("unsupported user admin route was claimed")
	}
}

func TestUserAdministrationAndAPIKeyReadCreate(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	user.Type = "admin"
	h := New(db, config.Config{JWTSecret: "test-secret"}, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != 200 {
		t.Fatalf("list users status=%d body=%s", w.Code, w.Body.String())
	}
	var listed map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed["users"]) != 1 {
		t.Fatalf("list users payload=%s err=%v", w.Body.String(), err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(`{"name":"automation","userId":"u1","isActive":true}`))
	w = httptest.NewRecorder()
	if !h.Handle(w, r, user) || w.Code != 200 {
		t.Fatalf("create API key status=%d body=%s", w.Code, w.Body.String())
	}
	var created map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created["apiKey"]["id"] == nil || created["apiKey"]["apiKey"] == nil {
		t.Fatalf("create API key payload=%s err=%v", w.Body.String(), err)
	}
	// Upstream create spreads apiKey.toJSON() from a freshly built instance
	// (ApiKeyController.js:100): unpersisted attributes (description,
	// lastUsedAt, permissions) are undefined and dropped, while the persisted
	// fields, createdByUserId and user remain.
	for _, field := range []string{"lastUsedAt", "description", "permissions", "createdByUser"} {
		if value, exists := created["apiKey"][field]; exists && value != nil {
			t.Fatalf("create response must omit unpersisted %s: %s", field, w.Body.String())
		}
	}
	if value := created["apiKey"]["expiresAt"]; value != nil {
		if _, ok := value.(float64); !ok {
			t.Fatalf("expiresAt type=%T, want numeric or null", value)
		}
	}
	for _, field := range []string{"createdAt", "createdByUserId", "user"} {
		if _, exists := created["apiKey"][field]; !exists {
			t.Fatalf("missing %s: %s", field, w.Body.String())
		}
	}
	if active, ok := created["apiKey"]["isActive"].(bool); !ok || !active {
		t.Fatalf("isActive=%#v, want true", created["apiKey"]["isActive"])
	}
	if perms := created["apiKey"]["permissions"]; perms != nil {
		if _, ok := perms.(map[string]any); !ok {
			t.Fatalf("permissions=%#v, want object or null", perms)
		}
	}
	var keys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM apiKeys WHERE name='automation' AND isActive=1`).Scan(&keys); err != nil || keys != 1 {
		t.Fatalf("API key persistence count=%d err=%v", keys, err)
	}
}

func msPtr(v int64) *int64 { return &v }

var _ = context.Background
