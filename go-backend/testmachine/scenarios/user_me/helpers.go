package user_me

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install). The ids of
// the library items come from Env.IDs; these are values of the seeded rows.
const (
	listenerSessionID = "423a533f-1af9-4e00-80ef-62164eac86d7" // listener's seeded login session
	rootSessionID     = "614a3c57-edf0-4139-9f5d-c701e2dc212a" // root's seeded login session
	finisherID        = "7c538a8f-9711-47bd-89e4-8d0139473dcf" // finisher user id
	rootID            = "c2eedd44-2b7f-44cf-a18f-2687b798c386" // root user id
	finisherProgress  = "71830757-c50f-4e57-be06-17b8e8e8ab43" // finisher's progress of saga1
	chromeAgent       = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// progressJSON is the old media progress projection clients read.
type progressJSON struct {
	ID                        string   `json:"id"`
	UserID                    string   `json:"userId"`
	LibraryItemID             string   `json:"libraryItemId"`
	EpisodeID                 *string  `json:"episodeId"`
	MediaItemID               string   `json:"mediaItemId"`
	MediaItemType             string   `json:"mediaItemType"`
	Duration                  float64  `json:"duration"`
	Progress                  float64  `json:"progress"`
	CurrentTime               float64  `json:"currentTime"`
	IsFinished                bool     `json:"isFinished"`
	HideFromContinueListening bool     `json:"hideFromContinueListening"`
	EbookLocation             *string  `json:"ebookLocation"`
	EbookProgress             float64  `json:"ebookProgress"`
	LastUpdate                int64    `json:"lastUpdate"`
	StartedAt                 int64    `json:"startedAt"`
	FinishedAt                *float64 `json:"finishedAt"`
}

// bookmarkJSON is one user bookmark.
type bookmarkJSON struct {
	LibraryItemID string  `json:"libraryItemId"`
	Time          float64 `json:"time"`
	Title         string  `json:"title"`
	CreatedAt     int64   `json:"createdAt"`
}

// userJSON is the browser view of a user (GET /api/me).
type userJSON struct {
	ID                              string          `json:"id"`
	Username                        string          `json:"username"`
	Email                           *string         `json:"email"`
	Type                            string          `json:"type"`
	Token                           string          `json:"token"`
	MediaProgress                   []progressJSON  `json:"mediaProgress"`
	SeriesHideFromContinueListening []string        `json:"seriesHideFromContinueListening"`
	Bookmarks                       []bookmarkJSON  `json:"bookmarks"`
	IsActive                        bool            `json:"isActive"`
	IsLocked                        bool            `json:"isLocked"`
	LastSeen                        *int64          `json:"lastSeen"`
	CreatedAt                       int64           `json:"createdAt"`
	Permissions                     map[string]bool `json:"permissions"`
	LibrariesAccessible             []string        `json:"librariesAccessible"`
	ItemTagsSelected                []string        `json:"itemTagsSelected"`
	HasOpenIDLink                   bool            `json:"hasOpenIDLink"`
}

// sessionJSON is one auth session of GET /api/me/sessions.
type sessionJSON struct {
	ID         string         `json:"id"`
	IPAddress  string         `json:"ipAddress"`
	UserAgent  string         `json:"userAgent"`
	DeviceInfo map[string]any `json:"deviceInfo"`
	CreatedAt  int64          `json:"createdAt"`
	UpdatedAt  int64          `json:"updatedAt"`
	Current    bool           `json:"current"`
}

// sessionsPage is the GET /api/me/sessions response.
type sessionsPage struct {
	Total        int           `json:"total"`
	NumPages     int           `json:"numPages"`
	Page         int           `json:"page"`
	ItemsPerPage int           `json:"itemsPerPage"`
	Sessions     []sessionJSON `json:"sessions"`
}

// listeningSession is one playback session of the listening-sessions routes.
type listeningSession struct {
	ID            string         `json:"id"`
	UserID        string         `json:"userId"`
	LibraryID     string         `json:"libraryId"`
	LibraryItemID *string        `json:"libraryItemId"`
	BookID        *string        `json:"bookId"`
	EpisodeID     *string        `json:"episodeId"`
	MediaType     string         `json:"mediaType"`
	MediaMetadata map[string]any `json:"mediaMetadata"`
	Chapters      []any          `json:"chapters"`
	CoverPath     *string        `json:"coverPath"`
	ServerVersion *string        `json:"serverVersion"`
	DisplayTitle  string         `json:"displayTitle"`
	DisplayAuthor string         `json:"displayAuthor"`
	Duration      *float64       `json:"duration"`
	PlayMethod    *int           `json:"playMethod"`
	MediaPlayer   *string        `json:"mediaPlayer"`
	DeviceInfo    map[string]any `json:"deviceInfo"`
	Date          *string        `json:"date"`
	DayOfWeek     *string        `json:"dayOfWeek"`
	TimeListening *float64       `json:"timeListening"`
	StartTime     float64        `json:"startTime"`
	CurrentTime   float64        `json:"currentTime"`
	StartedAt     int64          `json:"startedAt"`
	UpdatedAt     int64          `json:"updatedAt"`
}

// listeningPage is the response of both listening-sessions routes.
type listeningPage struct {
	Total        int                `json:"total"`
	NumPages     int                `json:"numPages"`
	Page         int                `json:"page"`
	ItemsPerPage int                `json:"itemsPerPage"`
	Sessions     []listeningSession `json:"sessions"`
}

// sessionIDs returns the ids of the sessions in order.
func (p listeningPage) sessionIDs() []string {
	ids := make([]string, 0, len(p.Sessions))
	for _, session := range p.Sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

// fixtureSessionOrder is the seeded listener's playback sessions, newest update first.
var fixtureSessionOrder = []string{
	"193bf2e2-70c3-4b91-99e2-40bc7398812a",
	"f166755e-ad8c-4334-adb9-eea51bc07c18",
	"57d12836-1090-4347-b017-0c48eb482e11",
	"ab27584f-2573-49d1-99d9-5baf7ce82c80",
	"legacy-null-0003",
	"legacy-null-0002",
	"legacy-null-0001",
}

// getJSON performs a GET as token and decodes a 200 response into out.
func getJSON(t *testing.T, e *harness.Env, token, path string, out any) {
	t.Helper()
	resp := e.Get(t, token, path)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d %s", path, resp.Status, resp.Body)
	}
	resp.JSON(t, out)
}

// wantStatus requires the status of a request.
func wantStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %s", what, status, resp.Status, resp.Body)
	}
}

// equalJSON compares two values by their JSON encoding.
func equalJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: encode got: %v", what, err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: encode want: %v", what, err)
	}
	if string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// near reports whether two floats agree to a rounding error of sums.
func near(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

// requireUnauthenticated requires 401 for no token and for an unknown bearer.
func requireUnauthenticated(t *testing.T, e *harness.Env, method, path string) {
	t.Helper()
	for _, token := range []string{"", "not-a-token"} {
		resp := e.Do(t, harness.Request{Method: method, Path: path, Token: token})
		if resp.Status != http.StatusUnauthorized {
			t.Fatalf("%s %s with token %q: want 401, got %d", method, path, token, resp.Status)
		}
	}
}

// newUser creates a user through the admin API (setup owned by another packet)
// and returns its access token.
func newUser(t *testing.T, e *harness.Env, body map[string]any) string {
	t.Helper()
	body["password"] = "me-packet-password"
	body["isActive"] = true
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, resp, http.StatusOK, "create user")
	username, _ := body["username"].(string)
	token, _ := e.Login(t, username, "me-packet-password")
	return token
}

// restrictedUser returns the token of a user that can only access the podcast library.
func restrictedUser(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	return newUser(t, e, map[string]any{
		"username": username, "type": "user",
		"permissions":         map[string]any{"accessAllLibraries": false},
		"librariesAccessible": []string{e.IDs["podcastLibrary"]},
	})
}

// guestUser returns the token of a new guest user.
func guestUser(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	return newUser(t, e, map[string]any{"username": username, "type": "guest"})
}

// loginSession logs the listener in with the mobile-style refresh token
// return and a Chrome user agent; it returns the access token, the refresh
// token and the new auth session id.
func loginSession(t *testing.T, e *harness.Env, username, password string) (accessToken, refreshToken, sessionID string) {
	t.Helper()
	resp := e.Do(t, harness.Request{
		Method: http.MethodPost, Path: "/login",
		JSON:   map[string]string{"username": username, "password": password},
		Header: map[string]string{"X-Return-Tokens": "true", "User-Agent": chromeAgent},
	})
	wantStatus(t, resp, http.StatusOK, "login")
	var body struct {
		User struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	resp.JSON(t, &body)
	rows := e.Rows(t, "SELECT id FROM sessions WHERE refreshToken = ?", body.User.RefreshToken)
	if body.User.AccessToken == "" || body.User.RefreshToken == "" || len(rows) != 1 {
		t.Fatalf("login must return both tokens and store one session row: %s", resp.Body)
	}
	return body.User.AccessToken, body.User.RefreshToken, fmt.Sprint(rows[0]["id"])
}

// sessionCount counts the session rows of a user id.
func sessionCount(t *testing.T, e *harness.Env, userID string) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ?", userID)
}

// pageQuery builds a query string of page parameters.
func pageQuery(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		values.Set(pairs[i], pairs[i+1])
	}
	return values
}

// jsonOf encodes a value as a string for snapshot comparison.
func jsonOf(v any) (string, error) {
	encoded, err := json.Marshal(v)
	return string(encoded), err
}

const (
	farFuture = "2100-01-01 00:00:00.000 +00:00"
	longPast  = "2020-01-01 00:00:00.000 +00:00"
)

// editDatabase stops the server, runs the statements and restarts it: the way
// to prepare persisted state no API can produce (see README "Database").
func editDatabase(t *testing.T, e *harness.Env, statements ...[]any) {
	t.Helper()
	e.Stop(t)
	for _, statement := range statements {
		text, _ := statement[0].(string)
		e.Exec(t, text, statement[1:]...)
	}
	e.Restart(t)
}

// keepSeededSessionsValid moves the expiry of every seeded auth session far
// into the future so no scenario depends on the wall clock against the
// fixture dates (the seeded sessions expire in 2026).
func keepSeededSessionsValid(t *testing.T, e *harness.Env) {
	t.Helper()
	editDatabase(t, e, []any{"UPDATE sessions SET expiresAt = ? WHERE id IN (?, ?)", farFuture, listenerSessionID, rootSessionID})
}
