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
	"reflect"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	absdb "audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

type userAPIForTest struct {
	*Handler
	routes *http.ServeMux
}

// Handle serves r on the route mux as the runtime does and reports whether a
// route matched; an unmatched request is the runtime's 404.
func (h *userAPIForTest) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	if _, pattern := h.routes.Handler(r); pattern == "" {
		return false
	}
	h.routes.ServeHTTP(w, r.WithContext(store.ContextWithUser(r.Context(), user)))
	return true
}

func newUserAPIForTest(db *sql.DB, cfg config.Config, emit func(string, string, any)) *userAPIForTest {
	ident, err := auth.New(absdb.New(db), nil, nil, config.Config{JWTSecret: "userapi-test-secret"})
	if err != nil {
		panic(err)
	}
	h, err := New(db, ident.OldCredentials(), emit, Deps{})
	if err != nil {
		panic(err)
	}
	routes := http.NewServeMux()
	h.Routes(routes)
	return &userAPIForTest{Handler: h, routes: routes}
}

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
		`CREATE TABLE libraries (id TEXT PRIMARY KEY, displayOrder INTEGER)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, ipAddress TEXT, userAgent TEXT, refreshToken TEXT, expiresAt DATETIME, lastRefreshToken TEXT, lastRefreshTokenExpiresAt DATETIME, createdAt DATETIME, updatedAt DATETIME, userId TEXT)`,
		`CREATE TABLE libraryItems (id TEXT PRIMARY KEY, mediaId TEXT, mediaType TEXT, libraryId TEXT, libraryFolderId TEXT, path TEXT, relPath TEXT, isFile INTEGER, mtime DATETIME, ctime DATETIME, birthtime DATETIME, createdAt DATETIME, updatedAt DATETIME, lastScan DATETIME, lastScanVersion INTEGER, isMissing INTEGER, isInvalid INTEGER, size INTEGER, extraData TEXT, libraryFiles TEXT, ino TEXT)`,
		`CREATE TABLE books (id TEXT PRIMARY KEY, title TEXT, subtitle TEXT, publishedYear TEXT, publishedDate TEXT, publisher TEXT, description TEXT, isbn TEXT, asin TEXT, language TEXT, explicit INTEGER, abridged INTEGER, coverPath TEXT, duration REAL, narrators TEXT, audioFiles TEXT, ebookFile TEXT, chapters TEXT, tags TEXT, genres TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE podcasts (id TEXT PRIMARY KEY, title TEXT, author TEXT, releaseDate TEXT, feedURL TEXT, imageURL TEXT, description TEXT, itunesPageURL TEXT, itunesId TEXT, itunesArtistId TEXT, language TEXT, podcastType TEXT, explicit INTEGER, autoDownloadEpisodes INTEGER, autoDownloadSchedule TEXT, lastEpisodeCheck DATETIME, maxEpisodesToKeep INTEGER, maxNewEpisodesToDownload INTEGER, coverPath TEXT, tags TEXT, genres TEXT, numEpisodes INTEGER, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY, "index" INTEGER, season TEXT, episode TEXT, episodeType TEXT, title TEXT, subtitle TEXT, description TEXT, pubDate TEXT, enclosureURL TEXT, enclosureSize INTEGER, enclosureType TEXT, publishedAt DATETIME, audioFile TEXT, chapters TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME, podcastId TEXT)`,
		`CREATE TABLE authors (id TEXT PRIMARY KEY, name TEXT, lastFirst TEXT, libraryId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE series (id TEXT PRIMARY KEY, name TEXT, nameIgnorePrefix TEXT, libraryId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE bookAuthors (id TEXT PRIMARY KEY, bookId TEXT, authorId TEXT, createdAt DATETIME)`,
		`CREATE TABLE bookSeries (id TEXT PRIMARY KEY, sequence TEXT, bookId TEXT, seriesId TEXT, createdAt DATETIME)`,
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

func TestDeleteMissingSessionUsesSendStatusNotFound(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	h := newUserAPIForTest(db, config.Config{}, nil)
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
	h := newUserAPIForTest(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodDelete, "/api/me/sessions/not-a-uuid", nil), user) {
		t.Fatal("route did not match")
	}
	if w.Code != http.StatusBadRequest || w.Body.String() != "Bad Request" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestCollectionDescriptionDBValueMatchesLegacyNullCoercion(t *testing.T) {
	if got := store.TextOrNull(""); got.Valid {
		t.Fatalf("empty description binding=%#v, want NULL", got)
	}
	if got := store.TextOrNull("notes"); got != (sql.NullString{String: "notes", Valid: true}) {
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
	h := newUserAPIForTest(db, config.Config{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/me/progress", nil)
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
	r = httptest.NewRequest(http.MethodPatch, "/api/me/progress/li1", strings.NewReader(`{"currentTime":95,"duration":100}`))
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

// A progress row stored with Sequelize's timestamp spelling is served with
// millisecond lastUpdate and startedAt on /api/me.
func TestProgressReadParsesSequelizeTimestamp(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	const timestamp = "2026-10-28 04:28:27.188 +00:00"
	if _, err := db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,mediaItemType,currentTime,isFinished,hideFromContinueListening,extraData,createdAt,updatedAt) VALUES ('p-ts','u1','book1','book',1,0,0,'{}',?,?)`, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	h := newUserAPIForTest(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/me", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("GET /api/me status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		MediaProgress []map[string]any `json:"mediaProgress"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.MediaProgress) != 1 {
		t.Fatalf("mediaProgress = %v err=%v in %s", body.MediaProgress, err, w.Body.String())
	}
	want := float64(time.Date(2026, time.October, 28, 4, 28, 27, 188_000_000, time.UTC).UnixMilli())
	if got := body.MediaProgress[0]; got["lastUpdate"] != want || got["startedAt"] != want {
		t.Fatalf("timestamps = lastUpdate:%v startedAt:%v, want %v", got["lastUpdate"], got["startedAt"], want)
	}
}

func TestMePreservesNullEmail(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET email=NULL WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	h := newUserAPIForTest(db, config.Config{}, nil)
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

func TestBookmarkCreateAndLibraryPermissionDenial(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId) VALUES ('li1','book1','book','lib1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO books(id,explicit,tags) VALUES ('book1',0,'[]')`); err != nil {
		t.Fatal(err)
	}
	h := newUserAPIForTest(db, config.Config{}, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/me/item/li1/bookmark", strings.NewReader(`{"time":42,"title":"Chapter"}`))
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
	r = httptest.NewRequest(http.MethodPost, "/api/me/item/li1/bookmark", strings.NewReader(`{"time":43,"title":"No access"}`))
	w = httptest.NewRecorder()
	h.Handle(w, r, denied)
	if w.Code != http.StatusForbidden {
		t.Fatalf("restricted bookmark status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUserAdministrationAndAPIKeyReadCreate(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	user.Type = "admin"
	h := newUserAPIForTest(db, config.Config{JWTSecret: "test-secret"}, nil)
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

// Two progress records for one item remain two results; deduplication is by
// progress row, whose ID is unique, rather than by library item.
func TestItemsInProgressRetainsDistinctRowsForSameItem(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO libraryItems(id,mediaId,mediaType,libraryId,extraData,libraryFiles) VALUES ('li1','book1','book','lib1','{}','[]')`,
		`INSERT INTO books(id,title,explicit,tags,genres,narrators,audioFiles,chapters) VALUES ('book1','Book',0,'[]','[]','[]','[]','[]')`,
		`INSERT INTO mediaProgresses(id,userId,mediaItemId,mediaItemType,currentTime,isFinished,extraData,updatedAt) VALUES ('p1','u1','book1','book',1,0,'{"libraryItemId":"li1"}','2024-01-01 00:00:00')`,
		`INSERT INTO mediaProgresses(id,userId,mediaItemId,mediaItemType,currentTime,isFinished,extraData,updatedAt) VALUES ('p2','u1','book1','book',2,0,'{"libraryItemId":"li1"}','2024-01-02 00:00:00')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	h := newUserAPIForTest(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/me/items-in-progress", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		LibraryItems []struct {
			ID string `json:"id"`
		} `json:"libraryItems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.LibraryItems) != 2 || body.LibraryItems[0].ID != "li1" || body.LibraryItems[1].ID != "li1" {
		t.Fatalf("items=%+v", body.LibraryItems)
	}
}

func TestYearStatsRankingRoundsBeforeOrdering(t *testing.T) {
	got := topEntries(map[string]float64{"Zoe": 1.49, "Ada": 1.4, "Bea": 1.5, "": 9}, "name", 3)
	want := []any{map[string]any{"name": "Bea", "time": 2}, map[string]any{"name": "Ada", "time": 1}, map[string]any{"name": "Zoe", "time": 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranking=%#v, want %#v", got, want)
	}
	if got := topOne(map[string]float64{}, "name"); got != nil {
		t.Fatalf("empty top=%#v", got)
	}
	if got := topMonth(map[int]float64{1: 0.4, 2: 0.6}); !reflect.DeepEqual(got, map[string]any{"month": 2, "time": 1}) {
		t.Fatalf("month=%#v", got)
	}
}

func TestListeningStatsLegacyZeroAndPrefix(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE devices (id TEXT PRIMARY KEY, deviceId TEXT, clientName TEXT, clientVersion TEXT, ipAddress TEXT, deviceName TEXT, deviceVersion TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME, userId TEXT)`,
		`CREATE TABLE playbackSessions (id TEXT PRIMARY KEY, mediaItemId TEXT, mediaItemType TEXT, displayTitle TEXT, displayAuthor TEXT, duration REAL, playMethod REAL, mediaPlayer TEXT, startTime REAL, currentTime REAL, serverVersion TEXT, coverPath TEXT, timeListening REAL, mediaMetadata TEXT, date TEXT, dayOfWeek TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME, userId TEXT, deviceId TEXT, libraryId TEXT)`,
		`INSERT INTO playbackSessions(id,userId,timeListening,mediaMetadata,date,dayOfWeek,extraData,createdAt,updatedAt) VALUES ('one','u1',0,'{"title":"First"}','2024-01-01','Monday','{"libraryItemId":"li_old"}','2024-01-01','2024-01-03')`,
		`INSERT INTO playbackSessions(id,userId,timeListening,mediaMetadata,date,dayOfWeek,extraData,createdAt,updatedAt) VALUES ('two','u1',3,'{"title":"Second"}','2024-01-01','Monday','{"libraryItemId":"li_old"}','2024-01-01','2024-01-02')`,
		`INSERT INTO playbackSessions(id,userId,timeListening,mediaMetadata,date,dayOfWeek,extraData,createdAt,updatedAt) VALUES ('three','u1',-2,'null','2024-01-02','Tuesday','{"libraryItemId":"item1"}','2024-01-01','2024-01-01')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h := newUserAPIForTest(db, config.Config{}, nil)
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/me/listening-stats", nil), user) || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"totalTime": float64(1), "today": float64(0),
		"days":      map[string]any{"2024-01-01": float64(3)},
		"dayOfWeek": map[string]any{"Monday": float64(3), "Tuesday": float64(-2)},
		"items":     map[string]any{"null": map[string]any{"id": nil, "timeListening": float64(3), "mediaMetadata": map[string]any{"title": "First"}}, "item1": map[string]any{"id": "item1", "timeListening": float64(-2), "mediaMetadata": nil}},
	} {
		if !reflect.DeepEqual(body[key], want) {
			t.Errorf("%s=%#v, want %#v", key, body[key], want)
		}
	}
}

func TestSeriesVisibilityTransitions(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO series(id) VALUES ('s1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET extraData='{"unknown":{"keep":true},"seriesHideFromContinueListening":["other"]}' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	events := 0
	h := newUserAPIForTest(db, config.Config{}, func(audience, event string, payload any) {
		if audience != user.ID || event != "user_updated" {
			t.Errorf("event=%q/%q", audience, event)
		}
		events++
	})
	steps := []struct {
		action string
		want   []any
		events int
	}{
		{"remove-from-continue-listening", []any{"other", "s1"}, 1},
		{"remove-from-continue-listening", []any{"other", "s1"}, 1},
		{"readd-to-continue-listening", []any{"other"}, 2},
		{"readd-to-continue-listening", []any{"other"}, 2},
	}
	const sentinel = "2001-01-01 00:00:00"
	for i, step := range steps {
		if i%2 == 1 {
			if _, err := db.Exec(`UPDATE users SET updatedAt=? WHERE id='u1'`, sentinel); err != nil {
				t.Fatal(err)
			}
		}
		var extra string
		if err := db.QueryRow(`SELECT extraData FROM users WHERE id='u1'`).Scan(&extra); err != nil {
			t.Fatal(err)
		}
		user.ExtraData = json.RawMessage(extra)
		w := httptest.NewRecorder()
		path := "/api/me/series/s1/" + step.action
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, path, nil), user) || w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		var sentinelPreserved bool
		if err := db.QueryRow(`SELECT extraData, updatedAt = ? FROM users WHERE id='u1'`, sentinel).Scan(&extra, &sentinelPreserved); err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal([]byte(extra), &stored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored["seriesHideFromContinueListening"], step.want) || !reflect.DeepEqual(stored["unknown"], map[string]any{"keep": true}) || events != step.events {
			t.Fatalf("step %d stored=%#v events=%d", i, stored, events)
		}
		if i%2 == 1 && !sentinelPreserved {
			t.Fatal("repeat changed updatedAt")
		}
	}
	// Readd removes every duplicate; hiding an already present series retains them.
	user.ExtraData = json.RawMessage(`{"unknown":7,"seriesHideFromContinueListening":["s1","other","s1"]}`)
	if _, err := db.Exec(`UPDATE users SET extraData=? WHERE id='u1'`, string(user.ExtraData)); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		action string
		want   []any
		events int
	}{{"remove-from-continue-listening", []any{"s1", "other", "s1"}, 2}, {"readd-to-continue-listening", []any{"other"}, 3}} {
		if step.action == "remove-from-continue-listening" {
			if _, err := db.Exec(`UPDATE users SET updatedAt=? WHERE id='u1'`, sentinel); err != nil {
				t.Fatal(err)
			}
		}
		w := httptest.NewRecorder()
		h.Handle(w, httptest.NewRequest(http.MethodGet, "/api/me/series/s1/"+step.action, nil), user)
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		var extra string
		if err := db.QueryRow(`SELECT extraData FROM users WHERE id='u1'`).Scan(&extra); err != nil {
			t.Fatal(err)
		}
		user.ExtraData = json.RawMessage(extra)
		var stored map[string]any
		if err := json.Unmarshal(user.ExtraData, &stored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored["seriesHideFromContinueListening"], step.want) || stored["unknown"] != float64(7) || events != step.events {
			t.Fatalf("stored=%#v events=%d", stored, events)
		}
		if step.action == "remove-from-continue-listening" {
			var preserved bool
			if err := db.QueryRow(`SELECT updatedAt = ? FROM users WHERE id='u1'`, sentinel).Scan(&preserved); err != nil {
				t.Fatal(err)
			}
			if !preserved {
				t.Fatal("repeat with duplicates changed updatedAt")
			}
		}
	}
}

func TestUpdateUserValidationOrderAndNulls(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"email first", `{"email":1,"username":1,"type":1,"permissions":1}`, "Invalid email", 400},
		{"username second", `{"email":null,"username":false,"type":1}`, "Invalid username", 400},
		{"type third", `{"email":"","username":null,"type":[]}`, "Invalid account type", 400},
		{"permissions after strings", `{"email":null,"username":"","type":null,"permissions":[]}`, "Invalid permissions", 400},
		{"nulls ignored", `{"email":null,"username":null,"type":null,"permissions":null}`, "", 200},
		{"empty ignored", `{"email":"","username":"","type":""}`, "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, user := userFixture(t)
			defer db.Close()
			user.Type = "admin"
			h := newUserAPIForTest(db, config.Config{}, nil)
			w := httptest.NewRecorder()
			h.Handle(w, httptest.NewRequest(http.MethodPatch, "/api/users/u1", strings.NewReader(tc.body)), user)
			if w.Code != tc.status || (tc.want != "" && !strings.Contains(w.Body.String(), tc.want)) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	t.Run("admin cannot patch root before body validation", func(t *testing.T) {
		db, user := userFixture(t)
		defer db.Close()
		user.Type = "admin"
		if _, err := db.Exec(`UPDATE users SET type='root' WHERE id='u1'`); err != nil {
			t.Fatal(err)
		}
		h := newUserAPIForTest(db, config.Config{}, nil)
		w := httptest.NewRecorder()
		h.Handle(w, httptest.NewRequest(http.MethodPatch, "/api/users/u1", strings.NewReader(`{"email":1}`)), user)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}
