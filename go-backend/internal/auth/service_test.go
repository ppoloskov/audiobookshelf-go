package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/settings"

	"golang.org/x/crypto/bcrypt"
)

const (
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	testSecret = "service-test-secret"
)

// newService builds a Service over a fixture copy in which trial-listener has
// the password "shelf". tune may change the config and the database first.
func newService(t *testing.T, tune func(*config.Config, *db.DB)) (*Service, *db.DB, *http.ServeMux) {
	t.Helper()
	d := dbtest.Fixture(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("shelf"), 4)
	mustExec(t, d, `UPDATE users SET pash = ? WHERE id = ?`, string(hash), listenerID)
	mustExec(t, d, `DELETE FROM sessions`)
	cfg := config.Config{JWTSecret: testSecret, Source: "test", Auth: config.DefaultAuth()}
	if tune != nil {
		tune(&cfg, d)
	}
	st, err := settings.NewStore(t.Context(), d, func(string) string { return "" }, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(d, st, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux)
	return s, d, mux
}

func mustExec(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if _, err := d.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, d *db.DB, query string, args ...any) (n int) {
	t.Helper()
	if err := d.Get(context.Background(), &n, query, args...); err != nil {
		t.Fatal(err)
	}
	return n
}

func send(h http.Handler, method, path, body string, header map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host, r.RemoteAddr = "abs.example", "203.0.113.9:4444"
	for k, v := range header {
		r.Header.Set(k, v)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// loginTokens logs the listener in asking for the refresh token in the answer.
func loginTokens(t *testing.T, mux http.Handler) (access, refresh string) {
	t.Helper()
	w := send(mux, "POST", "/login", `{"username":"trial-listener","password":"shelf"}`, map[string]string{"x-return-tokens": "true"})
	if w.Code != 200 {
		t.Fatalf("login = %d %s", w.Code, w.Body)
	}
	var body struct {
		User struct{ AccessToken, RefreshToken string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.User.RefreshToken == "" {
		t.Fatalf("login answer %s: %v", w.Body, err)
	}
	return body.User.AccessToken, body.User.RefreshToken
}

func refreshWith(mux http.Handler, token string) *httptest.ResponseRecorder {
	return send(mux, "POST", "/auth/refresh", "", map[string]string{"x-refresh-token": token})
}

func TestLoginAnswerAndCookie(t *testing.T) {
	_, _, mux := newService(t, nil)
	w := send(mux, "POST", "/login", `{"username":"TRIAL-LISTENER","password":"shelf"}`, map[string]string{"X-Forwarded-Proto": "http, https"})
	var body struct {
		User struct {
			ID, AccessToken  string
			RefreshToken     *string
			MediaProgress    []json.RawMessage
			Bookmarks        []json.RawMessage
			ItemTagsSelected []string
		}
		UserDefaultLibraryID string
		Source               string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatalf("login = %d %s", w.Code, w.Body)
	}
	if body.User.ID != listenerID || body.User.AccessToken == "" || body.User.RefreshToken != nil || len(body.User.Bookmarks) != 2 ||
		body.User.ItemTagsSelected == nil || body.UserDefaultLibraryID == "" || body.Source != "test" {
		t.Fatalf("answer = %s", w.Body)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "refresh_token" || !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge <= 0 {
		t.Fatalf("cookie = %+v", cookie)
	}
	for _, bad := range []string{`{"username":"trial-listener","password":"nope"}`, `{"username":"nobody","password":"x"}`, `{"username":"","password":""}`, `{`} {
		if w := send(mux, "POST", "/login", bad, nil); w.Code != 401 {
			t.Fatalf("login %s = %d, want 401", bad, w.Code)
		}
	}
}

func TestLoginRefusedForInactiveAndPasswordlessUsers(t *testing.T) {
	_, d, mux := newService(t, nil)
	mustExec(t, d, `UPDATE users SET isActive = 0 WHERE id = ?`, listenerID)
	if w := send(mux, "POST", "/login", `{"username":"trial-listener","password":"shelf"}`, nil); w.Code != 401 {
		t.Fatalf("inactive = %d", w.Code)
	}
	mustExec(t, d, `UPDATE users SET isActive = 1, pash = NULL WHERE id = ?`, listenerID)
	if w := send(mux, "POST", "/login", `{"username":"trial-listener","password":""}`, nil); w.Code != 401 {
		t.Fatalf("passwordless = %d", w.Code)
	}
	mustExec(t, d, `UPDATE users SET pash = '' WHERE type = 'root'`)
	if w := send(mux, "POST", "/login", `{"username":"trial-root","password":""}`, nil); w.Code != 200 {
		t.Fatalf("root without a password = %d", w.Code)
	}
	if w := send(mux, "POST", "/login", `{"username":"trial-root","password":"x"}`, nil); w.Code != 401 {
		t.Fatalf("root with a password it does not have = %d", w.Code)
	}
}

func TestLocalLoginDisabled(t *testing.T) {
	_, _, mux := newService(t, func(_ *config.Config, d *db.DB) {
		mustExec(t, d, `UPDATE settings SET value = json_patch(value, '{"authActiveAuthMethods":["openid"],"authOpenIDIssuerURL":"http://i","authOpenIDAuthorizationURL":"http://i/a","authOpenIDTokenURL":"http://i/t","authOpenIDUserInfoURL":"http://i/u","authOpenIDJwksURL":"http://i/j","authOpenIDClientID":"c","authOpenIDClientSecret":"s"}') WHERE key = 'server-settings'`)
	})
	if w := send(mux, "POST", "/login", `{"username":"trial-listener","password":"shelf"}`, nil); w.Code != 401 {
		t.Fatalf("login with local auth off = %d", w.Code)
	}
}

func TestRefreshRotationConvergesAndGraceTokenWorks(t *testing.T) {
	_, d, mux := newService(t, nil)
	_, old := loginTokens(t, mux)
	var wg sync.WaitGroup
	var answers [2]*httptest.ResponseRecorder
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = refreshWith(mux, old)
		}()
	}
	wg.Wait()
	var tokens [2]string
	for i, w := range answers {
		var body struct{ User struct{ RefreshToken string } }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("refresh %d = %d %s", i, w.Code, w.Body)
		}
		tokens[i] = body.User.RefreshToken
	}
	if tokens[0] == "" || tokens[0] != tokens[1] || tokens[0] == old {
		t.Fatalf("rotation did not converge: %q %q (old %q)", tokens[0], tokens[1], old)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM sessions WHERE lastRefreshToken = ?`, old); n != 1 {
		t.Fatalf("old token is the grace token of %d sessions", n)
	}
	// The cookie of a refresh without the header carries the new token and the answer does not.
	w := send(mux, "POST", "/auth/refresh", "", nil, &http.Cookie{Name: "refresh_token", Value: tokens[0]})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refreshToken":null`) || w.Result().Cookies()[0].Name != "refresh_token" {
		t.Fatalf("cookie refresh = %d %s", w.Code, w.Body)
	}
	// After the grace period the old token is dead.
	mustExec(t, d, `UPDATE sessions SET lastRefreshTokenExpiresAt = '2020-01-01 00:00:00.000 +00:00'`)
	if w := refreshWith(mux, old); w.Code != 401 {
		t.Fatalf("expired grace token = %d", w.Code)
	}
}

func TestRefreshFailsClosed(t *testing.T) {
	s, d, mux := newService(t, nil)
	access, refresh := loginTokens(t, mux)
	expired, _ := s.sessionToken(typeRefresh, listenerID, "trial-listener", -time.Hour)
	for name, w := range map[string]*httptest.ResponseRecorder{
		"none":           send(mux, "POST", "/auth/refresh", "", nil),
		"garbage":        refreshWith(mux, "garbage"),
		"access token":   refreshWith(mux, access),
		"expired token":  refreshWith(mux, expired),
		"unknown (live)": refreshWith(mux, mustRefreshToken(t, s, "no-such-user")),
	} {
		if w.Code != 401 {
			t.Errorf("%s = %d, want 401", name, w.Code)
		}
	}
	mustExec(t, d, `UPDATE sessions SET expiresAt = '2020-01-01 00:00:00.000 +00:00'`)
	if w := refreshWith(mux, refresh); w.Code != 401 || count(t, d, `SELECT COUNT(*) FROM sessions`) != 0 {
		t.Fatalf("session expired in the database = %d, sessions left %d", w.Code, count(t, d, `SELECT COUNT(*) FROM sessions`))
	}
	_, refresh = loginTokens(t, mux)
	mustExec(t, d, `UPDATE users SET isActive = 0 WHERE id = ?`, listenerID)
	if w := refreshWith(mux, refresh); w.Code != 401 {
		t.Fatalf("inactive user = %d", w.Code)
	}
}

func mustRefreshToken(t *testing.T, s *Service, userID string) string {
	t.Helper()
	token, err := s.sessionToken(typeRefresh, userID, "x", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestLogoutRevokesTheSession(t *testing.T) {
	_, d, mux := newService(t, nil)
	_, first := loginTokens(t, mux)
	_, second := loginTokens(t, mux)
	w := send(mux, "POST", "/logout", "", map[string]string{"x-refresh-token": first})
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"redirect_url":null}` || count(t, d, `SELECT COUNT(*) FROM sessions`) != 1 {
		t.Fatalf("logout = %d %s", w.Code, w.Body)
	}
	if w := refreshWith(mux, first); w.Code != 401 {
		t.Fatalf("revoked token refreshed: %d", w.Code)
	}
	send(mux, "POST", "/logout?allDevices=1", "", nil, &http.Cookie{Name: "refresh_token", Value: second})
	if n := count(t, d, `SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("allDevices left %d sessions", n)
	}
	if w := send(mux, "POST", "/logout", "", nil); w.Code != 200 {
		t.Fatalf("logout without a session = %d", w.Code)
	}
}

func TestAuthenticate(t *testing.T) {
	s, d, mux := newService(t, nil)
	access, refresh := loginTokens(t, mux)
	user, _ := s.userByID(t.Context(), listenerID)
	legacy, _ := s.legacyToken(listenerID, db.Val(user.Username))
	check := func(label, token string, want bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		p, err := s.Authenticate(r)
		if (err == nil) != want || (err == nil && p.ID != listenerID) {
			t.Errorf("%s: principal %v, err %v, want ok=%v", label, p, err, want)
		}
	}
	check("access token", access, true)
	check("legacy users.token", legacy, true)
	check("refresh token", refresh, false)
	check("garbage", "garbage", false)
	r := httptest.NewRequest("GET", "/api/me?token="+access, nil)
	if p, err := s.Authenticate(r); err != nil || p.ID != listenerID {
		t.Fatalf("?token= = %v, %v", p, err)
	}
	// Tokens minted before the id migration carry the old id.
	mustExec(t, d, `UPDATE users SET extraData = '{"oldUserId":"old-1"}' WHERE id = ?`, listenerID)
	old, _ := s.legacyToken("old-1", "x")
	check("old user id", old, true)
	mustExec(t, d, `UPDATE users SET isActive = 0 WHERE id = ?`, listenerID)
	check("inactive user", access, false)
	// The middleware answers 401 without a principal and passes one with.
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if User(r.Context()) == nil {
			t.Error("no principal in the context")
		}
	}))
	if w := send(handler, "GET", "/x", "", nil); w.Code != 401 {
		t.Fatalf("middleware without a token = %d", w.Code)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	s, _, _ := newService(t, nil)
	ctx := t.Context()
	key, token, err := s.CreateAPIKey(ctx, "ci", listenerID, listenerID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func() error {
		r := httptest.NewRequest("GET", "/api/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		_, err := s.Authenticate(r)
		return err
	}
	if err := authenticate(); err != nil {
		t.Fatalf("new key: %v", err)
	}
	inactive := false
	if err := s.UpdateAPIKey(ctx, key.ID, listenerID, &inactive); err != nil {
		t.Fatal(err)
	}
	if authenticate() == nil {
		t.Fatal("deactivated key authenticates")
	}
	active := true
	_ = s.UpdateAPIKey(ctx, key.ID, listenerID, &active)
	mustExec(t, s.db, `UPDATE apiKeys SET expiresAt = '2020-01-01 00:00:00.000 +00:00' WHERE id = ?`, key.ID)
	if authenticate() == nil {
		t.Fatal("expired key authenticates")
	}
	mustExec(t, s.db, `UPDATE apiKeys SET expiresAt = NULL WHERE id = ?`, key.ID)
	mustExec(t, s.db, `UPDATE users SET isActive = 0 WHERE id = ?`, listenerID)
	if authenticate() == nil {
		t.Fatal("key of an inactive user authenticates")
	}
	mustExec(t, s.db, `UPDATE users SET isActive = 1 WHERE id = ?`, listenerID)
	if deleted, err := s.DeleteAPIKey(ctx, key.ID); err != nil || !deleted || authenticate() == nil {
		t.Fatalf("deleted key: deleted=%v err=%v", deleted, err)
	}
}

func TestInitCreatesOneRoot(t *testing.T) {
	_, d, mux := newService(t, func(_ *config.Config, d *db.DB) {
		mustExec(t, d, `UPDATE playbackSessions SET userId = NULL`)
		mustExec(t, d, `DELETE FROM mediaProgresses`)
		mustExec(t, d, `DELETE FROM sessions`)
		mustExec(t, d, `DELETE FROM users`)
	})
	body := `{"newRoot":{"username":"boss","password":"pw"}}`
	if w := send(mux, "POST", "/init", body, nil); w.Code != 200 || count(t, d, `SELECT COUNT(*) FROM users WHERE type = 'root' AND username = 'boss'`) != 1 {
		t.Fatalf("init = %d", w.Code)
	}
	if w := send(mux, "POST", "/login", `{"username":"boss","password":"pw"}`, nil); w.Code != 200 {
		t.Fatalf("login as the new root = %d", w.Code)
	}
	if w := send(mux, "POST", "/init", body, nil); w.Code != 500 {
		t.Fatalf("second init = %d, want 500", w.Code)
	}
}

func TestChangePasswordAndInvalidateSessions(t *testing.T) {
	s, d, mux := newService(t, nil)
	ctx := t.Context()
	_, current := loginTokens(t, mux)
	loginTokens(t, mux)
	if err := s.ChangePassword(ctx, listenerID, "wrong", "new"); err == nil || err.Error() != "Invalid password" {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := s.ChangePassword(ctx, listenerID, "shelf", ""); err == nil {
		t.Fatal("a non-root user may not have an empty password")
	}
	if err := s.ChangePassword(ctx, listenerID, "shelf", "new-shelf"); err != nil {
		t.Fatal(err)
	}
	if w := send(mux, "POST", "/login", `{"username":"trial-listener","password":"new-shelf"}`, nil); w.Code != 200 {
		t.Fatalf("login with the new password = %d", w.Code)
	}
	mustExec(t, d, `DELETE FROM sessions WHERE refreshToken NOT IN (?) AND userId = ?`, current, listenerID)
	access, refresh, err := s.InvalidateUserSessions(ctx, listenerID, current)
	if err != nil || access == "" || refresh == "" || refresh == current || count(t, d, `SELECT COUNT(*) FROM sessions`) != 1 {
		t.Fatalf("rotated %q %q %v", access, refresh, err)
	}
	if access, refresh, err = s.InvalidateUserSessions(ctx, listenerID, "unknown"); err != nil || access != "" || refresh != "" || count(t, d, `SELECT COUNT(*) FROM sessions`) != 0 {
		t.Fatalf("unknown token must drop every session: %q %q %v", access, refresh, err)
	}
}

func TestRateLimit(t *testing.T) {
	_, _, mux := newService(t, func(c *config.Config, _ *db.DB) { c.Auth.RateMax, c.Auth.RateWindow = 3, time.Hour })
	attempt := func(ip string) int {
		r := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"nobody","password":"x"}`))
		r.RemoteAddr = ip + ":1"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == 429 && !strings.Contains(w.Body.String(), config.DefaultAuth().RateMessage) {
			t.Fatalf("429 body = %s", w.Body)
		}
		return w.Code
	}
	for i := range 3 {
		if code := attempt("10.0.0.1"); code != 401 {
			t.Fatalf("attempt %d = %d", i, code)
		}
	}
	if code := attempt("10.0.0.1"); code != 429 {
		t.Fatalf("fourth attempt = %d, want 429", code)
	}
	if code := attempt("10.0.0.2"); code != 401 {
		t.Fatalf("another client = %d", code)
	}
	l := newLimiter(0, time.Minute)
	for range 100 {
		if !l.allow("x") {
			t.Fatal("disabled limiter refused")
		}
	}
}

func TestLimiterForgetsIdleClients(t *testing.T) {
	l := newLimiter(4, 50*time.Millisecond)
	for _, ip := range []string{"a", "b", "c"} {
		l.allow(ip)
	}
	time.Sleep(120 * time.Millisecond)
	l.allow("d")
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clients) != 1 {
		t.Fatalf("tracked %d clients, want 1", len(l.clients))
	}
}

func TestTokenSecretIsGeneratedAndStored(t *testing.T) {
	d := dbtest.Fixture(t)
	mustExec(t, d, `UPDATE settings SET value = json_remove(value, '$.tokenSecret') WHERE key = 'server-settings'`)
	st, err := settings.NewStore(t.Context(), d, func(string) string { return "" }, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(d, st, nil, config.Config{})
	if err != nil || len(s.secret) == 0 || string(st.Server().TokenSecret) != string(s.secret) {
		t.Fatalf("secret %q err %v stored %q", s.secret, err, st.Server().TokenSecret)
	}
	again, err := New(d, st, nil, config.Config{})
	if err != nil || string(again.secret) != string(s.secret) {
		t.Fatal("the stored secret was not reused")
	}
}

func TestSessionsListAndDelete(t *testing.T) {
	s, _, mux := newService(t, nil)
	loginTokens(t, mux)
	loginTokens(t, mux)
	total, sessions, err := s.Sessions(t.Context(), listenerID, 0, 1)
	if err != nil || total < 2 || len(sessions) != 1 {
		t.Fatalf("total %d page %d err %v", total, len(sessions), err)
	}
	if ok, err := s.DeleteSession(t.Context(), listenerID, sessions[0].ID); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := s.DeleteSession(t.Context(), "someone else", sessions[0].ID); ok {
		t.Fatal("deleted a session twice")
	}
}
