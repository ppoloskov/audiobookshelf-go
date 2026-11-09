package identity

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

// tokensFor logs reader in with x-return-tokens and returns the access and
// refresh token of the new session.
func tokensFor(t *testing.T, h *Handler) (access, refresh string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"reader","password":"shelf"}`))
	r.Header.Set("x-return-tokens", "true")
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	user := decodeResponse(t, w)["user"].(map[string]any)
	return user["accessToken"].(string), user["refreshToken"].(string)
}

func refreshWith(h *Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/auth/refresh", nil)
	if token != "" {
		r.Header.Set("x-refresh-token", token)
	}
	w := httptest.NewRecorder()
	h.Handle(w, r)
	return w
}

func bearer(h *Handler, token string) error {
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	_, err := h.Authenticate(r)
	return err
}

func sessionCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Logging out ends the session: its refresh token stops working (TokenManager
// deletes the session row; Auth.js logout).
func TestLogoutRevokesTheRefreshToken(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	_, refresh := tokensFor(t, h)
	if w := refreshWith(h, refresh); w.Code != 200 {
		t.Fatalf("refresh before logout: %d", w.Code)
	}
	var current string // the refresh above rotated the token; the stored one is live
	if err := db.QueryRow(`SELECT refreshToken FROM sessions`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/logout", nil)
	r.Header.Set("x-refresh-token", current)
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != 200 || sessionCount(t, db) != 0 {
		t.Fatalf("logout: %d, %d sessions left", w.Code, sessionCount(t, db))
	}
	for _, token := range []string{current, refresh} {
		if w := refreshWith(h, token); w.Code != http.StatusUnauthorized {
			t.Errorf("refresh with a logged-out token: %d, want 401", w.Code)
		}
	}
}

// allDevices=1 ends every session of the refresh token's user and leaves other
// users' sessions alone.
func TestLogoutAllDevicesRevokesEverySessionOfTheUser(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	_, first := tokensFor(t, h)
	_, second := tokensFor(t, h)
	now := dbtime.Format(time.Now())
	if _, err := db.Exec(`INSERT INTO sessions(id,refreshToken,expiresAt,createdAt,updatedAt,userId) VALUES('other','other-token',?,?,?,'someone-else')`, dbtime.Format(time.Now().Add(time.Hour)), now, now); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/logout?allDevices=1", nil)
	r.Header.Set("x-refresh-token", first)
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	var left []string
	rows, err := db.Query(`SELECT userId FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		rows.Scan(&u)
		left = append(left, u)
	}
	if len(left) != 1 || left[0] != "someone-else" {
		t.Fatalf("sessions left after allDevices logout: %v, want only the other user's", left)
	}
	if w := refreshWith(h, second); w.Code != http.StatusUnauthorized {
		t.Fatalf("the user's other session still refreshes: %d", w.Code)
	}
}

// Every refresh failure answers 401 and never hands out tokens.
func TestRefreshFailsClosed(t *testing.T) {
	type setup func(t *testing.T, db *sql.DB, h *Handler) string
	for _, tc := range []struct {
		name  string
		setup setup
		want  string
	}{
		{"no token", func(*testing.T, *sql.DB, *Handler) string { return "" }, "No refresh token provided"},
		{"not a JWT", func(*testing.T, *sql.DB, *Handler) string { return "garbage" }, "Invalid refresh token"},
		{"access token in place of a refresh token", func(t *testing.T, _ *sql.DB, h *Handler) string {
			access, _ := tokensFor(t, h)
			return access
		}, "Invalid token type"},
		{"session deleted", func(t *testing.T, db *sql.DB, h *Handler) string {
			_, refresh := tokensFor(t, h)
			if _, err := db.Exec(`DELETE FROM sessions`); err != nil {
				t.Fatal(err)
			}
			return refresh
		}, "Invalid refresh token"},
		{"session expired in the database", func(t *testing.T, db *sql.DB, h *Handler) string {
			_, refresh := tokensFor(t, h)
			if _, err := db.Exec(`UPDATE sessions SET expiresAt=?`, dbtime.Format(time.Now().Add(-time.Minute))); err != nil {
				t.Fatal(err)
			}
			return refresh
		}, "Refresh token expired"},
		{"user deactivated", func(t *testing.T, db *sql.DB, h *Handler) string {
			_, refresh := tokensFor(t, h)
			if _, err := db.Exec(`UPDATE users SET isActive=0`); err != nil {
				t.Fatal(err)
			}
			return refresh
		}, "User not found or inactive"},
		{"rotated token after the grace window", func(t *testing.T, db *sql.DB, h *Handler) string {
			_, old := tokensFor(t, h)
			if w := refreshWith(h, old); w.Code != 200 {
				t.Fatalf("rotation: %d", w.Code)
			}
			if _, err := db.Exec(`UPDATE sessions SET lastRefreshTokenExpiresAt=?`, dbtime.Format(time.Now().Add(-time.Second))); err != nil {
				t.Fatal(err)
			}
			return old
		}, "Invalid refresh token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, h := identityFixture(t)
			defer db.Close()
			w := refreshWith(h, tc.setup(t, db, h))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401: %s", w.Code, w.Body.String())
			}
			body := decodeResponse(t, w)
			if body["error"] != tc.want || body["user"] != nil {
				t.Fatalf("body %v, want only error %q", body, tc.want)
			}
		})
	}
}

// An expired session row is removed when its token is presented, so it cannot
// be replayed or listed afterwards (TokenManager.js:391).
func TestRefreshOfExpiredSessionDeletesIt(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	_, refresh := tokensFor(t, h)
	if _, err := db.Exec(`UPDATE sessions SET expiresAt=?`, dbtime.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if w := refreshWith(h, refresh); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
	if n := sessionCount(t, db); n != 0 {
		t.Fatalf("%d sessions left, want the expired one deleted", n)
	}
}

// A bearer token authenticates only an existing, active user, and only when it
// is an access (or legacy/API) token: a refresh token never does
// (TokenManager.js:81-84).
func TestBearerAuthenticationFailsClosed(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	access, refresh := tokensFor(t, h)
	if err := bearer(h, access); err != nil {
		t.Fatalf("fresh access token refused: %v", err)
	}
	if err := bearer(h, refresh); err == nil {
		t.Error("a refresh token authenticated as a bearer token")
	}
	if err := bearer(h, access+"x"); err == nil {
		t.Error("a token with a damaged signature authenticated")
	}
	if err := bearer(h, ""); err == nil {
		t.Error("a request without a token authenticated")
	}
	other, err := h.IssueLegacyToken("user-1", "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := bearer(h, other); err != nil {
		t.Fatalf("legacy token refused: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET isActive=0`); err != nil {
		t.Fatal(err)
	}
	if err := bearer(h, access); err == nil {
		t.Error("deactivated user's access token still authenticates")
	}
	if err := bearer(h, other); err == nil {
		t.Error("deactivated user's legacy token still authenticates")
	}
	if _, err := db.Exec(`UPDATE users SET isActive=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	if err := bearer(h, access); err == nil {
		t.Error("deleted user's access token still authenticates")
	}
}

// An API key stops working when the key is deactivated, expired, deleted, or
// its user deactivated, even though the signed token itself is still valid.
func TestAPIKeyRevocationAndExpiry(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	now := time.Now().UTC()
	insert := func(id string, active int, expires time.Time) string {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO apiKeys (id, name, isActive, expiresAt, userId, createdAt, updatedAt) VALUES (?, 'k', ?, ?, 'user-1', ?, ?)`, id, active, dbtime.Format(expires), dbtime.Format(now), dbtime.Format(now)); err != nil {
			t.Fatal(err)
		}
		token, err := h.IssueAPIKeyToken(id, "k", 0)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	live := insert("live", 1, now.Add(time.Hour))
	inactive := insert("inactive", 0, now.Add(time.Hour))
	expired := insert("expired", 1, now.Add(-time.Minute))
	deleted := insert("deleted", 1, now.Add(time.Hour))
	if err := bearer(h, live); err != nil {
		t.Fatalf("live key refused: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM apiKeys WHERE id='deleted'`); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"inactive": inactive, "expired": expired, "deleted": deleted} {
		if err := bearer(h, token); err == nil {
			t.Errorf("%s API key authenticated", name)
		}
	}
	if _, err := db.Exec(`UPDATE apiKeys SET isActive=0 WHERE id='live'`); err != nil {
		t.Fatal(err)
	}
	if err := bearer(h, live); err == nil {
		t.Error("deactivating a key did not revoke its token")
	}
	if _, err := db.Exec(`UPDATE apiKeys SET isActive=1 WHERE id='live'; UPDATE users SET isActive=0`); err != nil {
		t.Fatal(err)
	}
	if err := bearer(h, live); err == nil {
		t.Error("a key of a deactivated user authenticated")
	}
}

// Login is refused, with the same wire answer as an unknown user, for a
// deactivated user even with the right password, and for a user without a
// password hash.
func TestLoginRefusedForInactiveOrPasswordlessUsers(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	post := func(body string) int {
		r := httptest.NewRequest("POST", "/login", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.Handle(w, r)
		return w.Code
	}
	if got := post(`{"username":"reader","password":"shelf"}`); got != 200 {
		t.Fatalf("baseline login %d", got)
	}
	if _, err := db.Exec(`UPDATE users SET isActive=0`); err != nil {
		t.Fatal(err)
	}
	if got := post(`{"username":"reader","password":"shelf"}`); got != 401 {
		t.Errorf("inactive user login: %d, want 401", got)
	}
	if _, err := db.Exec(`UPDATE users SET isActive=1, pash=NULL`); err != nil {
		t.Fatal(err)
	}
	if got := post(`{"username":"reader","password":""}`); got != 401 {
		t.Errorf("passwordless non-root login: %d, want 401", got)
	}
	for _, body := range []string{`{"username":"","password":"shelf"}`, `{`, `{"password":"shelf"}`} {
		if got := post(body); got != 401 {
			t.Errorf("login with body %q: %d, want 401", body, got)
		}
	}
}
