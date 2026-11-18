package playback_sessions

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// minifiedUser is the user sub-object of a stored or open session.
type minifiedUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// historyDoc is one stored playback session of GET /api/sessions
// (server/models/PlaybackSession.js:74-106 getOldPlaybackSession).
type historyDoc struct {
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
	User          *minifiedUser  `json:"user"`
}

// historyPage is the GET /api/sessions envelope.
type historyPage struct {
	Total        int          `json:"total"`
	NumPages     int          `json:"numPages"`
	Page         int          `json:"page"`
	ItemsPerPage int          `json:"itemsPerPage"`
	Sessions     []historyDoc `json:"sessions"`
	UserID       *string      `json:"userId"`
}

// openSessionDoc is one entry of GET /api/sessions/open: the session as
// toJSON plus the minified owner.
type openSessionDoc struct {
	sessionDoc
	User *minifiedUser `json:"user"`
}

// openPage is the GET /api/sessions/open envelope.
type openPage struct {
	Sessions      []openSessionDoc `json:"sessions"`
	ShareSessions []any            `json:"shareSessions"`
}

// localResult is one entry of POST /api/session/local-all results.
type localResult struct {
	ID             string  `json:"id"`
	Success        bool    `json:"success"`
	ProgressSynced *bool   `json:"progressSynced"`
	Error          *string `json:"error"`
}

// history reads GET /api/sessions with a raw query string.
func history(t *testing.T, e *harness.Env, token, query string) historyPage {
	t.Helper()
	resp := e.Get(t, token, "/api/sessions"+query)
	requireStatus(t, resp, http.StatusOK, "GET /api/sessions"+query)
	var page historyPage
	resp.JSON(t, &page)
	return page
}

// userSessionTotal reads the admin history total of one user.
func userSessionTotal(t *testing.T, e *harness.Env, token, userID string) int {
	t.Helper()
	return history(t, e, token, "?user="+userID+"&itemsPerPage=1&page=0").Total
}

// findHistory returns the stored session with the id, or nil.
func findHistory(page historyPage, id string) *historyDoc {
	for i := range page.Sessions {
		if page.Sessions[i].ID == id {
			return &page.Sessions[i]
		}
	}
	return nil
}

// requireHistoryIDs requires the exact id sequence of a page.
func requireHistoryIDs(t *testing.T, page historyPage, ids ...string) {
	t.Helper()
	got := make([]string, 0, len(page.Sessions))
	for _, s := range page.Sessions {
		got = append(got, s.ID)
	}
	requireEqual(t, "session ids", got, ids)
}

// requireStoredSession compares a stored session document with the play
// response it came from, its listening time and position, and its row's
// updatedAt in epoch milliseconds.
func requireStoredSession(t *testing.T, doc historyDoc, play sessionDoc, listening, currentTime float64, updatedAt int64) {
	t.Helper()
	if doc.ID != play.ID || doc.UserID != play.UserID || doc.LibraryID != play.LibraryID || doc.MediaType != play.MediaType {
		t.Fatalf("stored session ids: %+v vs play %+v", doc, play)
	}
	requireEqual(t, "stored libraryItemId", doc.LibraryItemID, play.LibraryItemID)
	requireEqual(t, "stored bookId", doc.BookID, play.BookID)
	requireEqual(t, "stored episodeId", doc.EpisodeID, play.EpisodeID)
	requireEqual(t, "stored mediaMetadata", doc.MediaMetadata, play.MediaMetadata)
	// The stored row has no chapters column: the model always answers [].
	requireEqual(t, "stored chapters", doc.Chapters, []chapterDoc{})
	requireEqual(t, "stored display fields", []string{doc.DisplayTitle, doc.DisplayAuthor}, []string{play.DisplayTitle, play.DisplayAuthor})
	requireEqual(t, "stored coverPath", doc.CoverPath, play.CoverPath)
	requireEqual(t, "stored duration", doc.Duration, play.Duration)
	requireEqual(t, "stored playMethod", doc.PlayMethod, play.PlayMethod)
	requireEqual(t, "stored mediaPlayer", doc.MediaPlayer, play.MediaPlayer)
	requireEqual(t, "stored serverVersion", doc.ServerVersion, play.ServerVersion)
	requireEqual(t, "stored date", []any{doc.Date, doc.DayOfWeek}, []any{play.Date, play.DayOfWeek})
	requireEqual(t, "stored timeListening", doc.TimeListening, &listening)
	if doc.StartTime != play.StartTime || doc.CurrentTime != currentTime || doc.StartedAt != play.StartedAt || doc.UpdatedAt != updatedAt {
		t.Fatalf("stored times: start %v current %v startedAt %d updatedAt %d (play start %v, startedAt %d, row %d)",
			doc.StartTime, doc.CurrentTime, doc.StartedAt, doc.UpdatedAt, play.StartTime, play.StartedAt, updatedAt)
	}
}

// createStoredSession plays an item, listens for a whole number of seconds and
// closes it, so the session row is stored with that listening time.
func createStoredSession(t *testing.T, e *harness.Env, who actor, itemID, episodeID, deviceID string, listening float64) sessionDoc {
	t.Helper()
	doc := play(t, e, who.Token, itemID, episodeID, androidBody(deviceID, "audio/mpeg"))
	syncSession(t, e, who.Token, doc.ID, map[string]any{"currentTime": listening + 20, "timeListened": listening})
	closeQuietly(t, e, who.Token, doc.ID)
	return doc
}

// ptrOf returns a pointer to its argument.
func ptrOf[T any](v T) *T { return &v }

// utcMillis returns an epoch-millisecond timestamp of a UTC calendar moment.
func utcMillis(year int, month time.Month, day, hour, minute, second int) int64 {
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC).UnixMilli()
}

// containsToken reports whether text contains token, for refusal leak checks.
func containsToken(text, token string) bool {
	return token != "" && strings.Contains(text, token)
}

// localBody is an offline playback session as the mobile clients send it
// (contracts/clients/official-app.md:79, absorb.md:56, audiobooth.md:66). The
// userId and libraryId are deliberately legacy values: the server overrides
// them.
func localBody(id, itemID, episodeID string, startedAt, updatedAt int64, currentTime, timeListening float64) map[string]any {
	body := map[string]any{
		"id":            id,
		"userId":        "00000000-0000-4000-8000-000000000000",
		"libraryId":     "lib_legacy",
		"libraryItemId": itemID,
		"mediaType":     "book",
		"mediaMetadata": map[string]any{},
		"chapters":      []any{},
		"displayTitle":  "Offline Title",
		"displayAuthor": "Offline Author",
		"duration":      1234.5,
		"playMethod":    3,
		"mediaPlayer":   "exo-player",
		"serverVersion": "0.9.9",
		"date":          "2026-05-01",
		"dayOfWeek":     "Friday",
		"timeListening": timeListening,
		"startTime":     0,
		"currentTime":   currentTime,
		"startedAt":     startedAt,
		"updatedAt":     updatedAt,
		"deviceInfo": map[string]any{
			"deviceId": "ps-offline-android", "clientVersion": "0.10.0", "manufacturer": "Google",
			"model": "Pixel 8", "sdkVersion": 34,
		},
	}
	if episodeID != "" {
		body["episodeId"] = episodeID
		body["mediaType"] = "podcast"
	}
	return body
}

// postLocal uploads one offline session.
func postLocal(t *testing.T, e *harness.Env, token string, body map[string]any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/session/local", body)
}

// postLocalAll uploads a batch of offline sessions.
func postLocalAll(t *testing.T, e *harness.Env, token string, sessions []map[string]any, deviceID string) []localResult {
	t.Helper()
	body := map[string]any{
		"sessions":   sessions,
		"deviceInfo": map[string]any{"deviceId": deviceID, "clientVersion": "0.10.0", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": 34},
	}
	resp := e.Post(t, token, "/api/session/local-all", body)
	requireStatus(t, resp, http.StatusOK, "POST /api/session/local-all")
	var page struct {
		Results []localResult `json:"results"`
	}
	resp.JSON(t, &page)
	return page.Results
}

// localSessionRow returns the stored row of an offline session.
func localSessionRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM playbackSessions WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// requireLocalSession checks the persisted values of a new offline session of
// saga1, whose library duration replaces the client's duration.
func requireLocalSession(t *testing.T, row map[string]any, id, userID, itemID string, startedAt, updatedAt int64, currentTime, timeListening float64) {
	t.Helper()
	if row == nil {
		t.Fatalf("offline session %s is not stored", id)
	}
	if row["id"] != id || row["userId"] != userID || row["mediaItemId"] != saga1BookID || row["mediaItemType"] != "book" || row["libraryId"] != fixtureLibraryID {
		t.Fatalf("offline session identity: %v", row)
	}
	if row["displayTitle"] != "Offline Title" || row["displayAuthor"] != "Offline Author" {
		t.Fatalf("the client's initial metadata must be preserved: %v", row)
	}
	if !near(number(t, row["duration"]), saga1Duration) || number(t, row["playMethod"]) != 3 || row["mediaPlayer"] != "exo-player" || row["serverVersion"] != serverVersion {
		t.Fatalf("offline session playback values: %v", row)
	}
	if dbTime(t, row["createdAt"]) != startedAt || dbTime(t, row["updatedAt"]) != updatedAt {
		t.Fatalf("offline session times: createdAt %v updatedAt %v, want %d and %d", row["createdAt"], row["updatedAt"], startedAt, updatedAt)
	}
	if !near(number(t, row["currentTime"]), currentTime) || !near(number(t, row["timeListening"]), timeListening) || !near(number(t, row["startTime"]), 0) {
		t.Fatalf("offline session progress values: %v", row)
	}
	if row["extraData"] != `{"libraryItemId":"`+itemID+`"}` {
		t.Fatalf("offline session extraData: %v", row["extraData"])
	}
	var metadata struct {
		Title   string `json:"title"`
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
	}
	text, _ := row["mediaMetadata"].(string)
	if err := json.Unmarshal([]byte(text), &metadata); err != nil {
		t.Fatalf("stored mediaMetadata %q: %v", text, err)
	}
	if metadata.Title != "The Beginning" || len(metadata.Authors) != 1 || metadata.Authors[0].Name != "Terry Fixture" {
		t.Fatalf("stored mediaMetadata must be the library item's: %q", text)
	}
}

// itemLibraryID is gone: all fixture audiobooks live in fixtureLibraryID.
// requireLocalProgressValues checks the progress row an offline session moved:
// identity, position, duration and ratio.
func requireLocalProgressValues(t *testing.T, row map[string]any, userID, bookID string, currentTime, duration float64) {
	t.Helper()
	if row == nil {
		t.Fatalf("offline session created no progress for %s", bookID)
	}
	if row["userId"] != userID || row["mediaItemId"] != bookID || row["mediaItemType"] != "book" {
		t.Fatalf("progress identity: %v", row)
	}
	requireNear(t, "progress currentTime", number(t, row["currentTime"]), currentTime)
	requireNear(t, "progress duration", number(t, row["duration"]), duration)
	requireNear(t, "progress ratio", progressFraction(t, row), currentTime/duration)
}

// requireLocalProgress also requires the progress updatedAt the client
// timestamp sets when the progress row already existed (MediaProgress.js:253-272).
func requireLocalProgress(t *testing.T, row map[string]any, userID, bookID string, currentTime, duration float64, updatedAt int64) {
	t.Helper()
	requireLocalProgressValues(t, row, userID, bookID, currentTime, duration)
	if dbTime(t, row["updatedAt"]) != updatedAt {
		t.Fatalf("progress updatedAt %v must be the client's %d", row["updatedAt"], updatedAt)
	}
}
