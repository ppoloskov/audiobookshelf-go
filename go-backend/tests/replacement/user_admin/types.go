package user_admin

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install).
const (
	rootID       = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	finisherID   = "7c538a8f-9711-47bd-89e4-8d0139473dcf"
	unknownID    = "00000000-0000-4000-8000-000000000000"
	userPassword = "admin-packet-password"
)

// userView is the browser view of a user, as served by /api/users.
type userView struct {
	ID                              string          `json:"id"`
	Username                        string          `json:"username"`
	Email                           *string         `json:"email"`
	Type                            string          `json:"type"`
	Token                           string          `json:"token"`
	MediaProgress                   []progressView  `json:"mediaProgress"`
	SeriesHideFromContinueListening []string        `json:"seriesHideFromContinueListening"`
	Bookmarks                       []bookmarkView  `json:"bookmarks"`
	IsActive                        bool            `json:"isActive"`
	IsLocked                        bool            `json:"isLocked"`
	LastSeen                        *int64          `json:"lastSeen"`
	CreatedAt                       int64           `json:"createdAt"`
	Permissions                     map[string]bool `json:"permissions"`
	LibrariesAccessible             []string        `json:"librariesAccessible"`
	ItemTagsSelected                []string        `json:"itemTagsSelected"`
	HasOpenIDLink                   bool            `json:"hasOpenIDLink"`
}

// progressView is one media progress of GET /api/users/:id.
type progressView struct {
	ID             string  `json:"id"`
	LibraryItemID  string  `json:"libraryItemId"`
	EpisodeID      *string `json:"episodeId"`
	MediaItemType  string  `json:"mediaItemType"`
	Progress       float64 `json:"progress"`
	CurrentTime    float64 `json:"currentTime"`
	IsFinished     bool    `json:"isFinished"`
	DisplayTitle   *string `json:"displayTitle"`
	DisplaySubtitl *string `json:"displaySubtitle"`
	CoverPath      *string `json:"coverPath"`
	MediaUpdatedAt *string `json:"mediaUpdatedAt"`
}

type bookmarkView struct {
	LibraryItemID string  `json:"libraryItemId"`
	Time          float64 `json:"time"`
	Title         string  `json:"title"`
	CreatedAt     int64   `json:"createdAt"`
}

// wantStatus requires the status of a response.
func wantStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %s", what, status, resp.Status, resp.Body)
	}
}

// createUser creates an active user through POST /api/users as root (setup:
// the route itself is asserted by the create scenarios) and returns the view.
func createUser(t *testing.T, e *harness.Env, body map[string]any) userView {
	t.Helper()
	if _, ok := body["password"]; !ok {
		body["password"] = userPassword
	}
	if _, ok := body["isActive"]; !ok {
		body["isActive"] = true
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, resp, http.StatusOK, "create user")
	var created struct {
		User userView `json:"user"`
	}
	resp.JSON(t, &created)
	if created.User.ID == "" {
		t.Fatalf("create user: no id: %s", resp.Body)
	}
	return created.User
}

// loginToken logs a created user in with the packet password.
func loginToken(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	token, _ := e.Login(t, username, userPassword)
	return token
}

// userRow returns the users row of an id, or nil.
func userRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM users WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// jsonColumn decodes a JSON text column of a row into a generic value; the
// result is open-ended by nature (the persisted JSON documents).
func jsonColumn(t *testing.T, row map[string]any, column string) any {
	t.Helper()
	text, _ := row[column].(string)
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("column %s is not JSON: %q", column, text)
	}
	return value
}

// requireRowUnchanged requires the users row to be identical to before.
func requireRowUnchanged(t *testing.T, e *harness.Env, id string, before map[string]any) {
	t.Helper()
	if after := userRow(t, e, id); !reflect.DeepEqual(after, before) {
		t.Fatalf("a rejected request must not change the users row:\n before %v\n after  %v", before, after)
	}
}

// eventUser decodes the single user argument of a user_* event.
func eventUser(t *testing.T, event harness.Event) userView {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s must carry exactly one argument, got %d", event.Name, len(event.Args))
	}
	var user userView
	if err := json.Unmarshal(event.Args[0], &user); err != nil {
		t.Fatalf("%s payload: %v", event.Name, err)
	}
	return user
}

// matchUserID matches a user_* event about the user id.
func matchUserID(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var user struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &user) == nil && user.ID == id
	}
}

// matchUsername matches a user_* event about the username.
func matchUsername(name string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var user struct {
			Username string `json:"username"`
		}
		return json.Unmarshal(args[0], &user) == nil && user.Username == name
	}
}

// userPermissions returns the default permissions of an account type, with
// the two selection lists empty (the browser view keeps them outside).
func defaultPermissions(accountType string) map[string]bool {
	privileged := accountType == "admin" || accountType == "root"
	return map[string]bool{
		"download": true, "update": privileged, "delete": accountType == "root", "upload": privileged,
		"createEreader": privileged, "accessAllLibraries": true, "accessAllTags": true,
		"accessExplicitContent": privileged, "selectedTagsNotAccessible": false,
	}
}

// rowPermissions requires the stored permissions JSON of a user to hold the
// booleans, librariesAccessible and itemTagsSelected, and nothing else.
func requireRowPermissions(t *testing.T, e *harness.Env, id string, flags map[string]bool, libraries, tags []string) {
	t.Helper()
	row := userRow(t, e, id)
	if row == nil {
		t.Fatalf("user %s has no row", id)
	}
	want := map[string]any{}
	for key, value := range flags {
		want[key] = value
	}
	want["librariesAccessible"] = toAny(libraries)
	want["itemTagsSelected"] = toAny(tags)
	got := jsonColumn(t, row, "permissions")
	if !reflect.DeepEqual(got, any(want)) {
		t.Fatalf("stored permissions of %s:\n got  %v\n want %v", id, got, want)
	}
}

func toAny(list []string) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		out = append(out, item)
	}
	return out
}

// requireViewPermissions requires the browser view's permissions and lists.
func requireViewPermissions(t *testing.T, view userView, flags map[string]bool, libraries, tags []string) {
	t.Helper()
	if !reflect.DeepEqual(view.Permissions, flags) {
		t.Fatalf("view permissions of %s: got %v want %v", view.Username, view.Permissions, flags)
	}
	if view.LibrariesAccessible == nil || view.ItemTagsSelected == nil ||
		!reflect.DeepEqual(append([]string{}, view.LibrariesAccessible...), append([]string{}, libraries...)) ||
		!reflect.DeepEqual(append([]string{}, view.ItemTagsSelected...), append([]string{}, tags...)) {
		t.Fatalf("view lists of %s: libraries %v tags %v, want %v %v", view.Username, view.LibrariesAccessible, view.ItemTagsSelected, libraries, tags)
	}
}

// getUser reads GET /api/users/:id as token.
func getUser(t *testing.T, e *harness.Env, token, id string) userView {
	t.Helper()
	resp := e.Get(t, token, "/api/users/"+id)
	wantStatus(t, resp, http.StatusOK, "GET /api/users/"+id)
	var view userView
	resp.JSON(t, &view)
	return view
}

// looksLikeJWT reports the three-part shape of a JWT.
func looksLikeJWT(token string) bool {
	return len(strings.Split(token, ".")) == 3
}

// withCopy returns the map with extra keys set (a copy).
func withCopy(base map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// jsonString encodes a value for comparison (map keys are sorted by encoding/json).
func jsonString(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "<unencodable>"
	}
	return string(raw)
}

// isISODate reports whether the text is an RFC 3339 timestamp with
// milliseconds, as a JavaScript Date serialises (the mediaUpdatedAt of GET /api/users/:id).
func isISODate(text string) bool {
	_, err := time.Parse("2006-01-02T15:04:05.000Z", text)
	return err == nil
}

// flipped returns the flags with every value inverted.
func flipped(flags map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range flags {
		out[key] = !value
	}
	return out
}

// modalBody builds a request body shaped like the account modal sends it:
// the full permissions object of the nine flags plus top-level selection lists.
func modalBody(flags map[string]bool, libraries, tags []string) map[string]any {
	permissions := map[string]any{}
	for key, value := range flags {
		permissions[key] = value
	}
	return map[string]any{"permissions": permissions, "librariesAccessible": libraries, "itemTagsSelected": tags}
}
