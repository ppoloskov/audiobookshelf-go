package playback_sessions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install). Library
// item ids come from Env.IDs; these are values of the seeded rows.
const (
	serverVersion = "2.37.2"
	saga1BookID   = "5e33de11-3e94-4c51-87d8-d06d2928ac38" // book of "The Beginning" (saga1)
	saga2BookID   = "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af" // book of "The Middle" (saga2)
	saga1Duration = 60.186123
	saga2Duration = 30.0
	ep1Duration   = 24.058776
	ep3Duration   = 12.068571
	listenerProg  = "b95b3028-3461-4306-803b-6e310140cd22" // listener's progress row of saga1
	listenerEp3   = "3dc1ddba-4b1a-45fc-8b91-53e355fc52fc" // listener's progress row of episode 3
	chromeAgent   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	userPassword  = "ps-packet-password"
)

// chapterDoc is one chapter of a session.
type chapterDoc struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// trackDoc is one audio track of a session as a player reads it.
type trackDoc struct {
	Index    int     `json:"index"`
	Ino      string  `json:"ino"`
	Duration float64 `json:"duration"`
	MimeType string  `json:"mimeType"`
	Codec    *string `json:"codec"`
	Title    string  `json:"title"`
	Offset   float64 `json:"startOffset"`
	URL      string  `json:"contentUrl"`
	Metadata *struct {
		Filename string `json:"filename"`
		Path     string `json:"path"`
		RelPath  string `json:"relPath"`
	} `json:"metadata"`
}

// itemDoc is the expanded library item that GET /api/session/:id attaches.
type itemDoc struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	MediaType string `json:"mediaType"`
	Media     struct {
		ID       string `json:"id"`
		Metadata struct {
			Title string `json:"title"`
		} `json:"metadata"`
		Duration  float64 `json:"duration"`
		NumTracks int     `json:"numTracks"`
	} `json:"media"`
}

// sessionDoc is a playback session as clients read it.
type sessionDoc struct {
	ID            string         `json:"id"`
	UserID        string         `json:"userId"`
	LibraryID     string         `json:"libraryId"`
	LibraryItemID *string        `json:"libraryItemId"`
	BookID        *string        `json:"bookId"`
	EpisodeID     *string        `json:"episodeId"`
	MediaType     string         `json:"mediaType"`
	MediaMetadata map[string]any `json:"mediaMetadata"`
	Chapters      []chapterDoc   `json:"chapters"`
	DisplayTitle  string         `json:"displayTitle"`
	DisplayAuthor string         `json:"displayAuthor"`
	CoverPath     *string        `json:"coverPath"`
	Duration      *float64       `json:"duration"`
	PlayMethod    *int           `json:"playMethod"`
	MediaPlayer   *string        `json:"mediaPlayer"`
	DeviceInfo    map[string]any `json:"deviceInfo"`
	ServerVersion *string        `json:"serverVersion"`
	Date          *string        `json:"date"`
	DayOfWeek     *string        `json:"dayOfWeek"`
	TimeListening *float64       `json:"timeListening"`
	StartTime     float64        `json:"startTime"`
	CurrentTime   float64        `json:"currentTime"`
	StartedAt     int64          `json:"startedAt"`
	UpdatedAt     int64          `json:"updatedAt"`
	AudioTracks   []trackDoc     `json:"audioTracks"`
	LibraryItem   *itemDoc       `json:"libraryItem"`
}

// progressDoc is the old media progress shape of user_item_progress_updated.
type progressDoc struct {
	ID            string   `json:"id"`
	UserID        string   `json:"userId"`
	LibraryItemID string   `json:"libraryItemId"`
	EpisodeID     *string  `json:"episodeId"`
	MediaItemID   string   `json:"mediaItemId"`
	MediaItemType string   `json:"mediaItemType"`
	Duration      float64  `json:"duration"`
	Progress      float64  `json:"progress"`
	CurrentTime   float64  `json:"currentTime"`
	IsFinished    bool     `json:"isFinished"`
	LastUpdate    int64    `json:"lastUpdate"`
	StartedAt     int64    `json:"startedAt"`
	FinishedAt    *float64 `json:"finishedAt"`
}

// progressEvent is the user_item_progress_updated payload.
type progressEvent struct {
	ID                string      `json:"id"`
	SessionID         string      `json:"sessionId"`
	DeviceDescription string      `json:"deviceDescription"`
	Data              progressDoc `json:"data"`
}

// actor is a user with its token, id and live sockets.
type actor struct {
	Name  string
	Token string
	ID    string
}

// near reports whether two floats agree to a rounding error of sums.
func near(a, b float64) bool {
	d := a - b
	return d < 1e-6 && d > -1e-6
}

// requireStatus fails unless the response has the status.
func requireStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %.300s", what, status, resp.Status, resp.Body)
	}
}

// requireEqual compares two values by their JSON encoding.
func requireEqual(t *testing.T, what string, got, want any) {
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

// requireNear fails unless two numbers agree to a rounding error.
func requireNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if !near(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// createUser creates a user through the admin API (setup owned by another
// packet) and returns it logged in.
func createUser(t *testing.T, e *harness.Env, username string, body map[string]any) actor {
	t.Helper()
	body["username"] = username
	body["password"] = userPassword
	body["isActive"] = true
	if _, ok := body["type"]; !ok {
		body["type"] = "user"
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	requireStatus(t, resp, http.StatusOK, "create user "+username)
	token, user := e.Login(t, username, userPassword)
	id, _ := user["id"].(string)
	if token == "" || id == "" {
		t.Fatalf("login of %s returned no token or id", username)
	}
	return actor{Name: username, Token: token, ID: id}
}

// fixtureActor returns a fixture user (root, listener, finisher) as an actor.
func fixtureActor(t *testing.T, e *harness.Env, name string) actor {
	t.Helper()
	token := e.Token(t, name)
	resp := e.Get(t, token, "/api/me")
	requireStatus(t, resp, http.StatusOK, "GET /api/me")
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	resp.JSON(t, &me)
	return actor{Name: me.Username, Token: token, ID: me.ID}
}

// playBody is the body of a play request.
type playBody map[string]any

// androidBody is a play body as the official Android app sends it.
func androidBody(deviceID string, mimeTypes ...string) playBody {
	return playBody{
		"deviceInfo":         map[string]any{"deviceId": deviceID, "clientVersion": "0.10.0", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": 34},
		"mediaPlayer":        "exo-player",
		"supportedMimeTypes": mimeTypes,
	}
}

// play starts a playback session and decodes the answer.
func play(t *testing.T, e *harness.Env, token, itemID, episodeID string, body playBody) sessionDoc {
	t.Helper()
	path := "/api/items/" + itemID + "/play"
	if episodeID != "" {
		path += "/" + episodeID
	}
	resp := e.Post(t, token, path, body)
	requireStatus(t, resp, http.StatusOK, "POST "+path)
	var doc sessionDoc
	resp.JSON(t, &doc)
	if doc.ID == "" {
		t.Fatalf("play returned no session id: %.300s", resp.Body)
	}
	return doc
}

// getSession reads an open session.
func getSession(t *testing.T, e *harness.Env, token, id string) (sessionDoc, int) {
	t.Helper()
	resp := e.Get(t, token, "/api/session/"+id)
	var doc sessionDoc
	if resp.Status == http.StatusOK {
		resp.JSON(t, &doc)
	}
	return doc, resp.Status
}

// mustGetSession reads an open session and fails unless it is open.
func mustGetSession(t *testing.T, e *harness.Env, token, id string) sessionDoc {
	t.Helper()
	doc, status := getSession(t, e, token, id)
	if status != http.StatusOK {
		t.Fatalf("GET /api/session/%s: want 200, got %d", id, status)
	}
	return doc
}

// syncBody is the body of a sync request.
func syncBody(currentTime, timeListened float64) map[string]any {
	return map[string]any{"currentTime": currentTime, "timeListened": timeListened, "duration": saga1Duration}
}

// syncSession posts a sync and requires 200.
func syncSession(t *testing.T, e *harness.Env, token, id string, body map[string]any) {
	t.Helper()
	requireStatus(t, e.Post(t, token, "/api/session/"+id+"/sync", body), http.StatusOK, "sync "+id)
}

// closeSession posts a close and requires 200.
func closeSession(t *testing.T, e *harness.Env, token, id string, body map[string]any) {
	t.Helper()
	requireStatus(t, e.Post(t, token, "/api/session/"+id+"/close", body), http.StatusOK, "close "+id)
}

// closeQuietly closes a session at the end of a scenario.
func closeQuietly(t *testing.T, e *harness.Env, token, id string) {
	t.Helper()
	closeSession(t, e, token, id, map[string]any{})
}

// openSessionIDs returns the ids of the open sessions as root sees them.
func openSessionIDs(t *testing.T, e *harness.Env) []string {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/sessions/open")
	requireStatus(t, resp, http.StatusOK, "GET /api/sessions/open")
	var body struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	resp.JSON(t, &body)
	ids := make([]string, 0, len(body.Sessions))
	for _, s := range body.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

// closeAllOpen closes every open session through the admin API so a scenario
// starts without leftovers (closing is the client-offered admin action).
func closeAllOpen(t *testing.T, e *harness.Env) {
	t.Helper()
	root := e.Token(t, "root")
	for _, id := range openSessionIDs(t, e) {
		requireStatus(t, e.Post(t, root, "/api/session/"+id+"/close", map[string]any{}), http.StatusOK, "close leftover "+id)
	}
}

// contains reports whether ids has id.
func contains(ids []string, id string) bool {
	for _, have := range ids {
		if have == id {
			return true
		}
	}
	return false
}

// dbTime parses a stored "2006-01-02 15:04:05.000 +00:00" column to epoch ms.
func dbTime(t *testing.T, value any) int64 {
	t.Helper()
	text, _ := value.(string)
	parsed, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("stored time %v: %v", value, err)
	}
	return parsed.UnixMilli()
}

// number converts a numeric column to float64.
func number(t *testing.T, value any) float64 {
	t.Helper()
	switch v := value.(type) {
	case int64:
		return float64(v)
	case float64:
		return v
	}
	t.Fatalf("column %v (%T) is not a number", value, value)
	return 0
}

// sessionRow returns the stored playback session row, or nil.
func sessionRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM playbackSessions WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// waitListening waits until the stored row has the listening time.
func waitListening(t *testing.T, e *harness.Env, id string, want float64) map[string]any {
	t.Helper()
	var row map[string]any
	harness.Eventually(t, 5*time.Second, fmt.Sprintf("row %s timeListening %v", id, want), func() bool {
		row = sessionRow(t, e, id)
		return row != nil && row["timeListening"] != nil && near(number(t, row["timeListening"]), want)
	})
	return row
}

// progressRow returns the stored media progress row of a user and media item.
func progressRow(t *testing.T, e *harness.Env, userID, mediaItemID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?", userID, mediaItemID)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// progressFraction reads the progress ratio stored in the row's extraData.
func progressFraction(t *testing.T, row map[string]any) float64 {
	t.Helper()
	var extra struct {
		Progress float64 `json:"progress"`
	}
	text, _ := row["extraData"].(string)
	if err := json.Unmarshal([]byte(text), &extra); err != nil {
		t.Fatalf("progress extraData %q: %v", text, err)
	}
	return extra.Progress
}

// sessionEvents decodes the user_item_progress_updated events of a socket
// since mark.
func progressEvents(t *testing.T, s *harness.Socket, mark int) []progressEvent {
	t.Helper()
	var out []progressEvent
	for _, ev := range s.Events()[mark:] {
		if ev.Name != "user_item_progress_updated" {
			continue
		}
		if len(ev.Args) != 1 {
			t.Fatalf("user_item_progress_updated has %d arguments", len(ev.Args))
		}
		var payload progressEvent
		if err := json.Unmarshal(ev.Args[0], &payload); err != nil {
			t.Fatalf("decode progress event: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

// waitProgressEvent waits for a progress event of a session and decodes it.
func waitProgressEvent(t *testing.T, s *harness.Socket, mark int, sessionID string) progressEvent {
	t.Helper()
	ev := s.Wait(t, mark, "user_item_progress_updated", 5*time.Second, func(args []json.RawMessage) bool {
		var payload progressEvent
		return len(args) == 1 && json.Unmarshal(args[0], &payload) == nil && payload.SessionID == sessionID
	})
	var payload progressEvent
	if err := json.Unmarshal(ev.Args[0], &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// closedEvents returns the session ids of the user_session_closed events since mark.
func closedEvents(t *testing.T, s *harness.Socket, mark int) []string {
	t.Helper()
	var ids []string
	for _, ev := range s.Events()[mark:] {
		if ev.Name != "user_session_closed" {
			continue
		}
		var id string
		if len(ev.Args) != 1 || json.Unmarshal(ev.Args[0], &id) != nil {
			t.Fatalf("user_session_closed arguments are not one string: %s", ev.Args)
		}
		ids = append(ids, id)
	}
	return ids
}

// streamUpdate is the user_stream_update payload.
type streamUpdate struct {
	ID        string      `json:"id"`
	Username  string      `json:"username"`
	Type      string      `json:"type"`
	Session   *sessionDoc `json:"session"`
	LastSeen  *int64      `json:"lastSeen"`
	CreatedAt int64       `json:"createdAt"`
}

// streamUpdates returns the user_stream_update payloads since mark.
func streamUpdates(t *testing.T, s *harness.Socket, mark int) []streamUpdate {
	t.Helper()
	var out []streamUpdate
	for _, ev := range s.Events()[mark:] {
		if ev.Name != "user_stream_update" {
			continue
		}
		var payload streamUpdate
		if len(ev.Args) != 1 || json.Unmarshal(ev.Args[0], &payload) != nil {
			t.Fatalf("user_stream_update arguments are not one user object: %s", ev.Args)
		}
		out = append(out, payload)
	}
	return out
}

// waitStreamUpdates waits until the socket has n user_stream_update events since mark.
func waitStreamUpdates(t *testing.T, s *harness.Socket, mark, n int) []streamUpdate {
	t.Helper()
	var got []streamUpdate
	harness.Eventually(t, 5*time.Second, fmt.Sprintf("%d user_stream_update events", n), func() bool {
		got = streamUpdates(t, s, mark)
		return len(got) >= n
	})
	return got
}

// settle makes sure events sent before this call have reached the socket: the
// server answers ping after it has written everything emitted earlier.
func settle(t *testing.T, s *harness.Socket) {
	t.Helper()
	mark := s.Mark()
	s.Emit(t, "ping")
	s.Wait(t, mark, "pong", 5*time.Second, nil)
}

// itemPath is the folder of a fixture item below the install.
func mediaPath(e *harness.Env, rel string) string {
	return strings.TrimRight(e.Media, "/") + "/" + rel
}
