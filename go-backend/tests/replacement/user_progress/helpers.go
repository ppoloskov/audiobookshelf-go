package user_progress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install): the ids
// of the book and podcast rows behind the library items of Env.IDs.
const (
	saga1Book  = "5e33de11-3e94-4c51-87d8-d06d2928ac38" // book row of saga1, 60.186123 s
	novelBook  = "85ab0f32-b17f-4c9a-883e-a61e260f1e46" // book row of novel, 45 s
	ebookBook  = "83013005-1614-4732-8f4b-07858cfb5de0" // book row of the ebook-only item
	saga2Book  = "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af" // book row of saga2, tagged "x" by accessUsers
	explicitBk = "54374ca2-e3c2-4121-93eb-4b0d806b241f" // book row of the disposable item, made explicit by accessUsers
	podcastRow = "a477b353-4058-4b9e-8399-49781256fc24" // podcast row of the podcast item
	password   = "progress-packet-password"
	unknownID  = "00000000-0000-4000-8000-000000000000"
)

const (
	patchSource  = "server/controllers/MeController.js:275-287"
	createSource = "server/models/User.js:730-831"
	applySource  = "server/models/MediaProgress.js:190-266"
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

// userJSON is the part of the browser view of a user that user_updated carries.
type userJSON struct {
	ID            string         `json:"id"`
	Username      string         `json:"username"`
	MediaProgress []progressJSON `json:"mediaProgress"`
	Bookmarks     []bookmarkJSON `json:"bookmarks"`
}

// actor is a user created for one scenario, so its progress starts empty.
type actor struct {
	name, id, token string
}

var actorCount atomic.Int64

// newActor creates a plain user through the admin API (setup owned by another
// packet) and logs it in.
func newActor(t *testing.T, e *harness.Env) actor {
	t.Helper()
	return newActorWith(t, e, map[string]any{"type": "user"})
}

// newActorWith creates a user from the admin API body (username, password and
// isActive are filled in) and returns it.
func newActorWith(t *testing.T, e *harness.Env, body map[string]any) actor {
	t.Helper()
	name := fmt.Sprintf("prog-%d", actorCount.Add(1))
	body["username"], body["password"], body["isActive"] = name, password, true
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, resp, http.StatusOK, "create user")
	var created struct {
		User struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	if created.User.ID == "" || created.User.Token == "" {
		t.Fatalf("create user returned no id or token: %s", resp.Body)
	}
	// The token of the creation response is a working bearer; it avoids one
	// login per scenario user, which the login rate limit would refuse.
	token, id := created.User.Token, created.User.ID
	return actor{name: name, id: id, token: token}
}

// wantStatus requires the status of a response.
func wantStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %s", what, status, resp.Status, resp.Body)
	}
}

// requireUnauthenticated requires 401 for no token and for an unknown bearer.
func requireUnauthenticated(t *testing.T, e *harness.Env, method, path string, body any) {
	t.Helper()
	for _, token := range []string{"", "not-a-token"} {
		resp := e.Do(t, harness.Request{Method: method, Path: path, Token: token, JSON: body})
		if resp.Status != http.StatusUnauthorized {
			t.Fatalf("%s %s with token %q: want 401, got %d", method, path, token, resp.Status)
		}
	}
}

// progressPath is the path of the progress routes of an item and optional episode.
func progressPath(item, episode string) string {
	if episode == "" {
		return "/api/me/progress/" + item
	}
	return "/api/me/progress/" + item + "/" + episode
}

// patchOK writes progress and requires 200.
func patchOK(t *testing.T, e *harness.Env, who actor, item, episode string, body any) {
	t.Helper()
	wantStatus(t, e.Patch(t, who.token, progressPath(item, episode), body), http.StatusOK, "PATCH "+progressPath(item, episode))
}

// apiProgress reads GET /api/me/progress/... and decodes a 200.
func apiProgress(t *testing.T, e *harness.Env, who actor, item, episode string) progressJSON {
	t.Helper()
	resp := e.Get(t, who.token, progressPath(item, episode))
	wantStatus(t, resp, http.StatusOK, "GET "+progressPath(item, episode))
	var progress progressJSON
	resp.JSON(t, &progress)
	return progress
}

// progressRows returns the stored progress rows of a user.
func progressRows(t *testing.T, e *harness.Env, userID string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT * FROM mediaProgresses WHERE userId = ? ORDER BY createdAt, id", userID)
}

// progressRow returns the one stored progress row of a user and media item.
func progressRow(t *testing.T, e *harness.Env, userID, mediaItemID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?", userID, mediaItemID)
	if len(rows) != 1 {
		t.Fatalf("want one progress row of user %s on %s, got %d", userID, mediaItemID, len(rows))
	}
	return rows[0]
}

// num reads a numeric column.
func num(t *testing.T, row map[string]any, column string) float64 {
	t.Helper()
	switch v := row[column].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	t.Fatalf("column %s is not numeric: %#v", column, row[column])
	return 0
}

// text reads a text column; ok is false for NULL.
func text(row map[string]any, column string) (value string, ok bool) {
	value, ok = row[column].(string)
	return value, ok
}

var timestampFormat = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} \+00:00$`)

// stamp parses a stored timestamp column and requires the stored format.
func stamp(t *testing.T, row map[string]any, column string) time.Time {
	t.Helper()
	value, ok := text(row, column)
	if !ok || !timestampFormat.MatchString(value) {
		t.Fatalf("column %s must be 'YYYY-MM-DD HH:MM:SS.mmm +00:00', got %#v", column, row[column])
	}
	parsed, err := time.Parse("2006-01-02 15:04:05.000 -07:00", value)
	if err != nil {
		t.Fatalf("column %s: %v", column, err)
	}
	return parsed
}

// extra decodes the extraData JSON column.
func extra(t *testing.T, row map[string]any) (libraryItemID string, progress float64) {
	t.Helper()
	raw, _ := text(row, "extraData")
	var data struct {
		LibraryItemID string  `json:"libraryItemId"`
		Progress      float64 `json:"progress"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("extraData %q: %v", raw, err)
	}
	return data.LibraryItemID, data.Progress
}

// wantRow is the expected content of a stored progress row.
type wantRow struct {
	item              string // library item id kept in extraData
	mediaType         string
	podcastID         string // "" = NULL
	duration, current float64
	progress          float64 // extraData.progress
	finished, hide    bool
	ebookLocation     string // "" = NULL
	ebookProgress     float64
	finishedAt        string // "" = NULL; "now" = between before and after; else the exact stored text
	before, after     time.Time
}

// requireRow compares every column of a stored progress row with want.
func requireRow(t *testing.T, row map[string]any, userID, mediaItemID string, want wantRow) {
	t.Helper()
	if got, _ := text(row, "userId"); got != userID {
		t.Fatalf("userId = %q, want %q", got, userID)
	}
	if got, _ := text(row, "mediaItemId"); got != mediaItemID {
		t.Fatalf("mediaItemId = %q, want %q", got, mediaItemID)
	}
	if got, _ := text(row, "mediaItemType"); got != want.mediaType {
		t.Fatalf("mediaItemType = %q, want %q", got, want.mediaType)
	}
	if id, _ := text(row, "id"); len(id) != 36 {
		t.Fatalf("id must be a uuid, got %q", id)
	}
	podcast, hasPodcast := text(row, "podcastId")
	if hasPodcast != (want.podcastID != "") || podcast != want.podcastID {
		t.Fatalf("podcastId = %#v, want %q", row["podcastId"], want.podcastID)
	}
	if num(t, row, "duration") != want.duration || num(t, row, "currentTime") != want.current {
		t.Fatalf("duration/currentTime = %v/%v, want %v/%v", row["duration"], row["currentTime"], want.duration, want.current)
	}
	item, progress := extra(t, row)
	if item != want.item || !near(progress, want.progress) {
		t.Fatalf("extraData libraryItemId/progress = %s/%v, want %s/%v", item, progress, want.item, want.progress)
	}
	if (num(t, row, "isFinished") == 1) != want.finished || (num(t, row, "hideFromContinueListening") == 1) != want.hide {
		t.Fatalf("isFinished/hide = %v/%v, want %v/%v", row["isFinished"], row["hideFromContinueListening"], want.finished, want.hide)
	}
	location, hasLocation := text(row, "ebookLocation")
	if hasLocation != (want.ebookLocation != "") || location != want.ebookLocation {
		t.Fatalf("ebookLocation = %#v, want %q", row["ebookLocation"], want.ebookLocation)
	}
	if !near(num(t, row, "ebookProgress"), want.ebookProgress) {
		t.Fatalf("ebookProgress = %v, want %v", row["ebookProgress"], want.ebookProgress)
	}
	stamp(t, row, "createdAt")
	stamp(t, row, "updatedAt")
	finishedAt, hasFinished := text(row, "finishedAt")
	switch want.finishedAt {
	case "":
		if hasFinished {
			t.Fatalf("finishedAt must be NULL, got %q", finishedAt)
		}
	case "now":
		at := stamp(t, row, "finishedAt")
		requireBetween(t, "finishedAt", at, want.before, want.after)
	default:
		if finishedAt != want.finishedAt {
			t.Fatalf("finishedAt = %q, want %q", finishedAt, want.finishedAt)
		}
	}
}

// requireBetween requires before <= at <= after at the millisecond precision of the database.
func requireBetween(t *testing.T, what string, at, before, after time.Time) {
	t.Helper()
	if at.Before(before.Truncate(time.Millisecond)) || at.After(after) {
		t.Fatalf("%s = %s, want between %s and %s", what, at.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}
}

// near reports whether two floats agree to a rounding error.
func near(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

// requireAPIMatchesRow requires the old progress projection to be the stored
// row: lastUpdate is updatedAt, startedAt is createdAt, finishedAt is finishedAt
// (milliseconds), ids and type as stored.
func requireAPIMatchesRow(t *testing.T, got progressJSON, row map[string]any, userID, libraryItemID, episodeID string) {
	t.Helper()
	id, _ := text(row, "id")
	itemType, _ := text(row, "mediaItemType")
	mediaItemID, _ := text(row, "mediaItemId")
	if got.ID != id || got.UserID != userID || got.LibraryItemID != libraryItemID || got.MediaItemID != mediaItemID || got.MediaItemType != itemType {
		t.Fatalf("progress identity wrong: %+v", got)
	}
	if (episodeID == "") != (got.EpisodeID == nil) || (got.EpisodeID != nil && *got.EpisodeID != episodeID) {
		t.Fatalf("episodeId = %v, want %q", got.EpisodeID, episodeID)
	}
	_, progress := extra(t, row)
	location, hasLocation := text(row, "ebookLocation")
	if got.Duration != num(t, row, "duration") || got.CurrentTime != num(t, row, "currentTime") || !near(got.Progress, progress) ||
		got.IsFinished != (num(t, row, "isFinished") == 1) || got.HideFromContinueListening != (num(t, row, "hideFromContinueListening") == 1) ||
		(got.EbookLocation != nil) != hasLocation || (hasLocation && *got.EbookLocation != location) || !near(got.EbookProgress, num(t, row, "ebookProgress")) {
		t.Fatalf("progress values differ from the row: %+v vs %v", got, row)
	}
	if got.LastUpdate != stamp(t, row, "updatedAt").UnixMilli() || got.StartedAt != stamp(t, row, "createdAt").UnixMilli() {
		t.Fatalf("lastUpdate/startedAt = %d/%d, want updatedAt/createdAt of the row %v/%v", got.LastUpdate, got.StartedAt, row["updatedAt"], row["createdAt"])
	}
	if _, hasFinished := text(row, "finishedAt"); hasFinished != (got.FinishedAt != nil) || (hasFinished && int64(*got.FinishedAt) != stamp(t, row, "finishedAt").UnixMilli()) {
		t.Fatalf("finishedAt = %v, want the row's %v", got.FinishedAt, row["finishedAt"])
	}
}

// requireNoRows requires that a user has no stored progress.
func requireNoRows(t *testing.T, e *harness.Env, userID string) {
	t.Helper()
	if n := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", userID); n != 0 {
		t.Fatalf("user %s must have no progress rows, has %d", userID, n)
	}
}

// socketsFor connects the acting user, another user and the admin.
type socketsFor struct {
	owner, other, admin *harness.Socket
}

// connectAudiences connects the sockets of the three audiences of a user event.
func connectAudiences(t *testing.T, e *harness.Env, who actor) socketsFor {
	t.Helper()
	bystander := newActor(t, e)
	return socketsFor{
		owner: e.Socket(t, who.token),
		other: e.Socket(t, bystander.token),
		admin: e.Socket(t, e.Token(t, "root")),
	}
}

// mark takes the event marks of all three sockets.
type marks struct{ owner, other, admin int }

func (s socketsFor) mark() marks {
	return marks{s.owner.Mark(), s.other.Mark(), s.admin.Mark()}
}

// ownerOnly requires that only the owner receives name (matching match) and
// the other audiences get nothing within the window; it returns the owner's event.
func (s socketsFor) ownerOnly(t *testing.T, m marks, name string, match func([]json.RawMessage) bool) harness.Event {
	t.Helper()
	ev := s.owner.Wait(t, m.owner, name, 5*time.Second, match)
	s.other.None(t, m.other, name, 400*time.Millisecond, nil)
	s.admin.None(t, m.admin, name, 100*time.Millisecond, nil)
	return ev
}

// nobody requires that no audience receives name.
func (s socketsFor) nobody(t *testing.T, m marks, name string) {
	t.Helper()
	s.owner.None(t, m.owner, name, 600*time.Millisecond, nil)
	s.other.None(t, m.other, name, 100*time.Millisecond, nil)
	s.admin.None(t, m.admin, name, 100*time.Millisecond, nil)
}

// userUpdatedOf matches a user_updated event of one user.
func userUpdatedOf(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var user userJSON
		return len(args) == 1 && json.Unmarshal(args[0], &user) == nil && user.ID == id
	}
}

// decodeUserUpdated decodes the user of a user_updated event.
func decodeUserUpdated(t *testing.T, ev harness.Event) userJSON {
	t.Helper()
	var user userJSON
	if len(ev.Args) != 1 {
		t.Fatalf("user_updated takes one argument, got %d", len(ev.Args))
	}
	if err := json.Unmarshal(ev.Args[0], &user); err != nil {
		t.Fatalf("user_updated payload: %v", err)
	}
	return user
}

// progressByID indexes progress objects by media item id.
func progressByItem(list []progressJSON) map[string]progressJSON {
	out := make(map[string]progressJSON, len(list))
	for _, p := range list {
		out[p.MediaItemID] = p
	}
	return out
}

// nextMillisecond returns once the wall clock is in a later millisecond than
// when it was called, so a following write has a strictly later stamp
// (timestamps are stored with millisecond precision).
func nextMillisecond() {
	start := time.Now().UnixMilli()
	for time.Now().UnixMilli() <= start {
	}
}
