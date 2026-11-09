package identity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/users"
	"golang.org/x/crypto/bcrypt"
)

// A created user's permanent token (users.token) is a JWT whose payload has a
// numeric iat (jsonwebtoken.sign adds it), and it authenticates that user as a
// bearer token.
func TestLegacyAccessTokenIncludesJWTIssuedAt(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	id, err := handler.CreateUser(t.Context(), users.NewUser{Username: "newcomer", Type: "user", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if err := db.QueryRow(`SELECT token FROM users WHERE id=?`, id).Scan(&token); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("users.token has %d segments, want JWT: %q", len(parts), token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if _, ok := claims["iat"].(float64); !ok || claims["userId"] != id || claims["username"] != "newcomer" {
		t.Fatalf("legacy token claims %#v lack numeric iat, userId or username", claims)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if u, err := handler.Authenticate(r); err != nil || u.ID != id {
		t.Fatalf("legacy token did not authenticate its user: %v %v", u.ID, err)
	}
}

func TestLocalLoginAndRefreshRotationGrace(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	progressAt := dbtime.Format(time.Now().Add(-time.Minute))
	if _, err := db.Exec(`INSERT INTO mediaProgresses (id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookProgress,extraData,createdAt,updatedAt) VALUES ('progress-1','user-1','book-1','book',100,25,0,0,0,?,?,?)`, `{"libraryItemId":"library-item-1","progress":0.25}`, progressAt, progressAt); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"reader","password":"shelf"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("x-return-tokens", "true")
	loginResponse := httptest.NewRecorder()
	if !handler.Handle(loginResponse, login) {
		t.Fatal("login route was not handled")
	}
	if loginResponse.Code != 200 {
		t.Fatalf("login returned %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	loginBody := decodeResponse(t, loginResponse)
	user := loginBody["user"].(map[string]any)
	if user["email"] != nil {
		t.Fatalf("NULL email was serialized as %#v", user["email"])
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		if list, ok := user[key].([]any); !ok || list == nil {
			t.Fatalf("%s=%#v, want array", key, user[key])
		}
	}
	if _, ok := user["isOldToken"]; ok {
		t.Fatalf("unset isOldToken must be omitted: %#v", user)
	}
	mediaProgress := user["mediaProgress"].([]any)
	if len(mediaProgress) != 1 {
		t.Fatalf("login returned %d progress entries", len(mediaProgress))
	}
	progress := mediaProgress[0].(map[string]any)
	if progress["lastUpdate"] == nil || progress["startedAt"] == nil {
		t.Fatalf("Sequelize progress timestamps were lost: %#v", progress)
	}
	if _, ok := progress["lastUpdate"].(float64); !ok {
		t.Fatalf("progress lastUpdate was not epoch milliseconds: %#v", progress["lastUpdate"])
	}
	oldRefresh, _ := user["refreshToken"].(string)
	access, _ := user["accessToken"].(string)
	if oldRefresh == "" || access == "" {
		t.Fatalf("login did not return tokens: %#v", user)
	}
	request := httptest.NewRequest("GET", "/api/me", nil)
	request.Header.Set("Authorization", "Bearer "+access)
	authenticated, err := handler.Authenticate(request)
	if err != nil || authenticated.ID != "user-1" {
		t.Fatalf("access token did not authenticate: user=%#v err=%v", authenticated, err)
	}

	const concurrentRequests = 2
	var wait sync.WaitGroup
	responses := make([]map[string]any, concurrentRequests)
	codes := make([]int, concurrentRequests)
	for i := range concurrentRequests {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			r := httptest.NewRequest("POST", "/auth/refresh", nil)
			r.Header.Set("x-refresh-token", oldRefresh)
			w := httptest.NewRecorder()
			handler.Handle(w, r)
			codes[i] = w.Code
			if w.Code == 200 {
				responses[i] = decodeResponse(t, w)
			}
		}(i)
	}
	wait.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("concurrent refresh %d returned %d", i, code)
		}
	}
	firstUser := responses[0]["user"].(map[string]any)
	secondUser := responses[1]["user"].(map[string]any)
	firstRefresh := firstUser["refreshToken"].(string)
	secondRefresh := secondUser["refreshToken"].(string)
	if firstRefresh == "" || firstRefresh != secondRefresh || firstRefresh == oldRefresh {
		t.Fatalf("rotation did not converge on one token: %q / %q", firstRefresh, secondRefresh)
	}

	grace := httptest.NewRequest("POST", "/auth/refresh", nil)
	grace.Header.Set("x-refresh-token", oldRefresh)
	graceResponse := httptest.NewRecorder()
	handler.Handle(graceResponse, grace)
	if graceResponse.Code != 200 {
		t.Fatalf("grace refresh returned %d: %s", graceResponse.Code, graceResponse.Body.String())
	}
	graceUser := decodeResponse(t, graceResponse)["user"].(map[string]any)
	if graceUser["refreshToken"] != firstRefresh {
		t.Fatalf("grace response returned %v, want current token %v", graceUser["refreshToken"], firstRefresh)
	}
}

func TestLocalLoginRejectsWrongPasswordAndSetsSecureCookie(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	wrong := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"reader","password":"wrong"}`))
	wrongResponse := httptest.NewRecorder()
	handler.Handle(wrongResponse, wrong)
	if wrongResponse.Code != 401 {
		t.Fatalf("wrong password returned %d", wrongResponse.Code)
	}
	good := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"READER","password":"shelf"}`))
	good.Header.Set("X-Forwarded-Proto", "http, https")
	goodResponse := httptest.NewRecorder()
	handler.Handle(goodResponse, good)
	if goodResponse.Code != 200 {
		t.Fatalf("login returned %d: %s", goodResponse.Code, goodResponse.Body.String())
	}
	for _, cookie := range goodResponse.Result().Cookies() {
		if cookie.Name == "refresh_token" {
			if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != 2 {
				t.Fatalf("unexpected refresh cookie: %#v", cookie)
			}
			return
		}
	}
	t.Fatal("login did not set refresh token cookie")
}

func TestDeniedLoginMatchesPassportWireHeaders(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.Handle(w, r)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/login", "application/json", strings.NewReader(`{"username":"reader","password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Passport's default failure ends the response with these exact bytes.
	if response.StatusCode != 401 || string(body) != "Unauthorized" {
		t.Fatalf("denial = %d %q", response.StatusCode, body)
	}
	if _, present := response.Header["Content-Type"]; present {
		t.Fatalf("passport denial must omit Content-Type: %v", response.Header)
	}
	if response.Header.Get("Content-Length") != "12" || len(response.TransferEncoding) != 0 {
		t.Fatalf("denial must have Content-Length: 12 without chunking: %v, transfer=%v", response.Header, response.TransferEncoding)
	}
}

func TestAPIKeyAuthenticationAndInitOneTime(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO apiKeys (id, name, isActive, expiresAt, lastUsedAt, userId, createdAt, updatedAt) VALUES (?, 'test', 1, ?, NULL, ?, ?, ?)`, "key-1", now.Add(time.Hour).Format(time.RFC3339Nano), "user-1", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	keyToken, err := handler.IssueAPIKeyToken("key-1", "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/libraries", nil)
	request.Header.Set("Authorization", "Bearer "+keyToken)
	user, err := handler.Authenticate(request)
	if err != nil || user.ID != "user-1" {
		t.Fatalf("API key did not resolve to user: user=%#v err=%v", user, err)
	}

	initRequest := httptest.NewRequest("POST", "/init", strings.NewReader(`{"newRoot":{"username":"root","password":""}}`))
	initResponse := httptest.NewRecorder()
	handler.Handle(initResponse, initRequest)
	if initResponse.Code != 200 {
		t.Fatalf("initial root creation returned %d", initResponse.Code)
	}
	var roots int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE type = 'root'`).Scan(&roots); err != nil || roots != 1 {
		t.Fatalf("root row count=%d err=%v", roots, err)
	}
	duplicate := httptest.NewRecorder()
	handler.Handle(duplicate, initRequest)
	if duplicate.Code != 500 {
		t.Fatalf("duplicate init returned %d, want 500", duplicate.Code)
	}
}

func identityFixture(t *testing.T) (*sql.DB, *Handler) {
	t.Helper()
	return identityFixtureWith(t, config.Config{})
}

// identityFixtureWith builds the fixture handler from production config; cfg's
// router base path and source are fixed, its Auth and secret are kept.
func identityFixtureWith(t *testing.T, cfg config.Config) (*sql.DB, *Handler) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:identity-test-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open); exposes nested-query deadlocks
	schema := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, email TEXT, pash TEXT, type TEXT, token TEXT, isActive INTEGER, isLocked INTEGER, lastSeen DATETIME, permissions JSON, bookmarks JSON, extraData JSON, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value JSON)`,
		`CREATE TABLE libraries (id TEXT PRIMARY KEY, displayOrder INTEGER)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, ipAddress TEXT, userAgent TEXT, refreshToken TEXT NOT NULL, expiresAt DATETIME NOT NULL, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, userId TEXT NOT NULL, lastRefreshToken TEXT, lastRefreshTokenExpiresAt DATETIME)`,
		`CREATE TABLE apiKeys (id TEXT PRIMARY KEY, name TEXT, description TEXT, expiresAt DATETIME, lastUsedAt DATETIME, isActive INTEGER, permissions TEXT, userId TEXT, createdByUserId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY, userId TEXT, mediaItemId TEXT, mediaItemType TEXT, duration REAL, currentTime REAL, isFinished INTEGER, hideFromContinueListening INTEGER, ebookLocation TEXT, ebookProgress REAL, finishedAt DATETIME, extraData TEXT, podcastId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
	}
	for _, query := range schema {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("shelf"), 8)
	if err != nil {
		t.Fatal(err)
	}
	now := dbtime.Format(time.Now())
	permissions := `{"accessAllLibraries":true,"accessAllTags":true,"librariesAccessible":[],"itemTagsSelected":[]}`
	if _, err := db.Exec(`INSERT INTO users VALUES (?, ?, NULL, ?, ?, ?, ?, ?, NULL, ?, '[]', '{}', ?, ?)`, "user-1", "reader", string(hash), "user", "legacy", 1, 0, permissions, now, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"server-settings": `{"id":"server-settings","tokenSecret":"test-secret","authActiveAuthMethods":["local"]}`,
		"email-settings":  `{"id":"email-settings","ereaderDevices":[]}`,
	} {
		if _, err := db.Exec(`INSERT INTO settings(key,value) VALUES (?, ?)`, key, value); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	cfg.RouterBasePath, cfg.Source = "/audiobookshelf", "test"
	handler, err := New(db, cfg, nil)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, handler
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestChangePasswordAndInvalidateSessionsPreservingCurrent(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	user, err := h.userByID(t.Context(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	_, current, err := h.createSession(t.Context(), user, httptest.NewRequest("POST", "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.createSession(t.Context(), user, httptest.NewRequest("POST", "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.ChangePassword(t.Context(), "user-1", "shelf", "new-shelf"); err != nil {
		t.Fatal(err)
	}
	if err = h.VerifyPassword(t.Context(), "user-1", "new-shelf"); err != nil {
		t.Fatal(err)
	}
	if err = h.VerifyPassword(t.Context(), "user-1", "shelf"); err == nil {
		t.Fatal("old password accepted")
	}
	access, rotated, err := h.InvalidateUserSessions(t.Context(), "user-1", current)
	if err != nil || access == "" || rotated == "" || rotated == current {
		t.Fatalf("rotated access=%q refresh=%q err=%v", access, rotated, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE userId='user-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("session count=%d err=%v", count, err)
	}
	if _, err = h.sessionByToken(t.Context(), rotated); err != nil {
		t.Fatalf("new refresh token not in session: %v", err)
	}
}

func TestInvalidateSessionsWithoutValidCurrentDeletesAll(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	user, err := h.userByID(t.Context(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.createSession(t.Context(), user, httptest.NewRequest("POST", "/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	access, refresh, err := h.InvalidateUserSessions(t.Context(), "user-1", "invalid-refresh")
	if err != nil || access != "" || refresh != "" {
		t.Fatalf("tokens=%q/%q err=%v", access, refresh, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE userId='user-1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("session count=%d err=%v", count, err)
	}
}

// A token minted before the 2.3 user-id migration carries the old id. Looking
// it up scans users.extraData and then loads the match; on the production
// one-connection pool the second query used to wait for the open cursor until
// the request was cancelled.
func TestUserByIDOrOldIDDoesNotHoldCursorOnOneConnectionPool(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET extraData = '{"oldUserId":"old-user-1"}' WHERE id = 'user-1'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	user, err := h.userByIDOrOldID(ctx, "old-user-1")
	if err != nil {
		t.Fatalf("userByIDOrOldID: %v", err)
	}
	if user.ID != "user-1" {
		t.Fatalf("user id = %q, want user-1", user.ID)
	}
	if _, err := h.userByIDOrOldID(ctx, "unknown"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown id error = %v, want sql.ErrNoRows", err)
	}
}
