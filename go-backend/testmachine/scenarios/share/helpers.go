package share

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// fixture shares loaded from the fixture database on every server start.
	novelSlug = "trial-share-novel" // downloadable share of the novel book
	saga2Slug = "trial-share-saga2" // non-downloadable share of "The Middle"

	cookieName = "share_session_id"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// mediaItemShare is the client-facing media item share JSON of the management
// routes, the public routes and the share_open/share_closed events.
type mediaItemShare struct {
	ID              string           `json:"id"`
	MediaItemID     string           `json:"mediaItemId"`
	MediaItemType   string           `json:"mediaItemType"`
	Slug            string           `json:"slug"`
	ExpiresAt       *string          `json:"expiresAt"`
	CreatedAt       string           `json:"createdAt"`
	UpdatedAt       string           `json:"updatedAt"`
	IsDownloadable  *bool            `json:"isDownloadable"`
	PlaybackSession *playbackSession `json:"playbackSession"`
}

// playbackSession is the share playback session the public slug route returns.
type playbackSession struct {
	ID            string  `json:"id"`
	UserID        *string `json:"userId"`
	LibraryID     string  `json:"libraryId"`
	LibraryItemID string  `json:"libraryItemId"`
	BookID        *string `json:"bookId"`
	EpisodeID     *string `json:"episodeId"`
	MediaType     string  `json:"mediaType"`
	MediaMetadata struct {
		Title         string                                `json:"title"`
		Subtitle      string                                `json:"subtitle"`
		Authors       []struct{ ID, Name string }           `json:"authors"`
		Narrators     []string                              `json:"narrators"`
		Series        []struct{ ID, Name, Sequence string } `json:"series"`
		Genres        []string                              `json:"genres"`
		PublishedYear *string                               `json:"publishedYear"`
		Language      *string                               `json:"language"`
		Explicit      bool                                  `json:"explicit"`
		Abridged      bool                                  `json:"abridged"`
	} `json:"mediaMetadata"`
	Chapters []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Title string  `json:"title"`
	} `json:"chapters"`
	DisplayTitle  string  `json:"displayTitle"`
	DisplayAuthor string  `json:"displayAuthor"`
	CoverPath     *string `json:"coverPath"`
	Duration      float64 `json:"duration"`
	PlayMethod    int     `json:"playMethod"`
	MediaPlayer   string  `json:"mediaPlayer"`
	DeviceInfo    struct {
		ID         string  `json:"id"`
		UserID     *string `json:"userId"`
		DeviceID   string  `json:"deviceId"`
		ClientName string  `json:"clientName"`
	} `json:"deviceInfo"`
	ServerVersion    string       `json:"serverVersion"`
	Date             string       `json:"date"`
	DayOfWeek        string       `json:"dayOfWeek"`
	TimeListening    float64      `json:"timeListening"`
	StartTime        float64      `json:"startTime"`
	CurrentTime      float64      `json:"currentTime"`
	StartedAt        int64        `json:"startedAt"`
	UpdatedAt        int64        `json:"updatedAt"`
	AudioTracks      []audioTrack `json:"audioTracks"`
	LibraryItem      any          `json:"libraryItem"`
	CoverAspectRatio float64      `json:"coverAspectRatio"`
}

// audioTrack is one public share audio track.
type audioTrack struct {
	Index       int     `json:"index"`
	StartOffset float64 `json:"startOffset"`
	Duration    float64 `json:"duration"`
	Title       string  `json:"title"`
	ContentURL  string  `json:"contentUrl"`
	MimeType    string  `json:"mimeType"`
	Codec       *string `json:"codec"`
	Metadata    struct {
		Filename string `json:"filename"`
		Path     string `json:"path"`
	} `json:"metadata"`
}

// fixture names the fixture ids the share scenarios use.
type fixture struct{ library, saga1, saga2, novel, ebook, disposable, podcast, ep1, ep2 string }

func fixtureOf(e *harness.Env) fixture {
	return fixture{e.IDs["library"], e.IDs["saga1"], e.IDs["saga2"], e.IDs["novel"], e.IDs["ebook"], e.IDs["disposable"], e.IDs["podcast"], e.IDs["ep1"], e.IDs["ep2"]}
}

// wantStatus fails unless the response has the expected status.
func wantStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status = %d, want %d; body %.300s", what, resp.Status, want, resp.Body)
	}
}

// decodeShare decodes a 2xx media item share body.
func decodeShare(t *testing.T, resp harness.Response) mediaItemShare {
	t.Helper()
	var value mediaItemShare
	resp.JSON(t, &value)
	return value
}

// mediaIDForItem returns the books.id or podcasts.id behind a library item.
func mediaIDForItem(t *testing.T, e *harness.Env, libraryItemID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT mediaId FROM libraryItems WHERE id = ?", libraryItemID)
	if len(rows) != 1 {
		t.Fatalf("library item %s: %d rows", libraryItemID, len(rows))
	}
	media, _ := rows[0]["mediaId"].(string)
	if media == "" {
		t.Fatalf("library item %s has no media id", libraryItemID)
	}
	return media
}

// rootUserID returns the fixture root user id (the owner of created shares).
func rootUserID(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM users WHERE type = 'root'")
	if len(rows) != 1 {
		t.Fatalf("root users: %d rows", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// createShare sends POST /api/share/mediaitem with a complete body.
func createShare(t *testing.T, e *harness.Env, token, slug, mediaItemID string, expiresAt any, downloadable any) harness.Response {
	t.Helper()
	body := map[string]any{"slug": slug, "mediaItemType": "book", "mediaItemId": mediaItemID, "expiresAt": expiresAt}
	if downloadable != nil {
		body["isDownloadable"] = downloadable
	}
	return e.Post(t, token, "/api/share/mediaitem", body)
}

// mustCreateShare creates a share and requires 201.
func mustCreateShare(t *testing.T, e *harness.Env, slug, mediaItemID string, expiresAt any, downloadable bool) mediaItemShare {
	t.Helper()
	resp := createShare(t, e, e.Token(t, "root"), slug, mediaItemID, expiresAt, downloadable)
	wantStatus(t, resp, http.StatusCreated, "create share "+slug)
	return decodeShare(t, resp)
}

// removeShare deletes a share through the management route.
func removeShare(t *testing.T, e *harness.Env, id string) harness.Response {
	t.Helper()
	return e.Delete(t, e.Token(t, "root"), "/api/share/mediaitem/"+id)
}

// shareRows returns the persisted mediaItemShares rows for one id.
func shareRows(t *testing.T, e *harness.Env, id string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, mediaItemId, mediaItemType, slug, expiresAt, isDownloadable, userId, pash, extraData FROM mediaItemShares WHERE id = ?", id)
}

// wantShareRow asserts the persisted row agrees with the client share.
func wantShareRow(t *testing.T, e *harness.Env, value mediaItemShare, ownerID string) {
	t.Helper()
	rows := shareRows(t, e, value.ID)
	if len(rows) != 1 {
		t.Fatalf("mediaItemShares row %s: %d rows", value.ID, len(rows))
	}
	row := rows[0]
	if row["mediaItemId"] != value.MediaItemID || row["mediaItemType"] != value.MediaItemType || row["slug"] != value.Slug || row["userId"] != ownerID {
		t.Fatalf("share row identity = %v, want %+v owned by %s", row, value, ownerID)
	}
	if value.IsDownloadable == nil {
		if row["isDownloadable"] != nil {
			t.Fatalf("row isDownloadable = %v, want NULL", row["isDownloadable"])
		}
	} else if got := row["isDownloadable"]; fmt.Sprint(got) != fmt.Sprint(boolInt(*value.IsDownloadable)) {
		t.Fatalf("row isDownloadable = %v (%T), want %v", got, got, *value.IsDownloadable)
	}
	if value.ExpiresAt == nil {
		if row["expiresAt"] != nil {
			t.Fatalf("row expiresAt = %v, want NULL", row["expiresAt"])
		}
	} else {
		got, ok := row["expiresAt"].(string)
		if !ok {
			t.Fatalf("row expiresAt = %v (%T), want a date string", row["expiresAt"], row["expiresAt"])
		}
		if !sameInstant(t, *value.ExpiresAt, got) {
			t.Fatalf("row expiresAt %q differs from response %q", got, *value.ExpiresAt)
		}
	}
	if row["pash"] != nil || row["extraData"] != nil {
		t.Fatalf("share row must have NULL pash/extraData: %v", row)
	}
}

// boolInt renders a boolean the way SQLite stores it.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// sameInstant compares an ISO-8601 JSON date with a Sequelize SQLite date.
func sameInstant(t *testing.T, iso, stored string) bool {
	t.Helper()
	parsedISO, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		t.Fatalf("response date %q: %v", iso, err)
	}
	layouts := []string{"2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano}
	for _, layout := range layouts {
		parsedStored, err := time.Parse(layout, stored)
		if err == nil {
			return parsedISO.Equal(parsedStored)
		}
	}
	t.Fatalf("row date %q is not a parseable date", stored)
	return false
}

// noShareRow asserts no persisted row exists.
func noShareRow(t *testing.T, e *harness.Env, id string) {
	t.Helper()
	if rows := shareRows(t, e, id); len(rows) != 0 {
		t.Fatalf("mediaItemShares row %s must be gone: %v", id, rows)
	}
}

// shareCookie returns the share_session_id of a response, preferring the last
// non-empty value (a cleared cookie is followed by the new one).
func shareCookie(t *testing.T, resp harness.Response) string {
	t.Helper()
	value := ""
	for _, cookie := range resp.Cookies {
		if cookie.Name == cookieName && cookie.Value != "" {
			value = cookie.Value
		}
	}
	return value
}

// cookieHeader sends a share session cookie.
func cookieHeader(value string) map[string]string {
	return map[string]string{"Cookie": cookieName + "=" + value}
}

// publicSharePath is the unauthenticated capability path of a slug.
func publicSharePath(slug string) string { return "/public/share/" + slug }

// publicGet reads a public share route, optionally with a cookie and a query.
func publicGet(t *testing.T, e *harness.Env, slug string, query url.Values, cookie string, header map[string]string) harness.Response {
	t.Helper()
	merged := map[string]string{}
	for key, value := range header {
		merged[key] = value
	}
	if cookie != "" {
		merged["Cookie"] = cookieName + "=" + cookie
	}
	return e.Do(t, harness.Request{Path: publicSharePath(slug), Query: query, Header: merged})
}

// openPublicSession reads GET /public/share/:slug and returns the share and the
// session cookie, requiring a fresh session or an existing one.
func openPublicSession(t *testing.T, e *harness.Env, slug string, query url.Values, cookie string) (mediaItemShare, string) {
	t.Helper()
	resp := publicGet(t, e, slug, query, cookie, nil)
	wantStatus(t, resp, http.StatusOK, "public share "+slug)
	value := decodeShare(t, resp)
	if value.PlaybackSession == nil {
		t.Fatalf("public share %s returned no playbackSession", slug)
	}
	found := shareCookie(t, resp)
	if found == "" {
		found = cookie
	}
	return value, found
}

// mediaFile reads a file below the install media folder.
func mediaFile(t *testing.T, e *harness.Env, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Media, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// metadataFile reads a file below the install metadata folder.
func metadataFile(t *testing.T, e *harness.Env, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Metadata, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// itemShareFromDetail reads the mediaItemShare of GET /api/items/:id?expanded=1&include=share.
func itemShareFromDetail(t *testing.T, e *harness.Env, token, libraryItemID string) (map[string]any, bool) {
	t.Helper()
	var body map[string]any
	resp := e.Get(t, token, "/api/items/"+libraryItemID+"?expanded=1&include=share")
	wantStatus(t, resp, http.StatusOK, "item detail "+libraryItemID)
	resp.JSON(t, &body)
	raw, present := body["mediaItemShare"]
	if !present {
		return nil, false
	}
	value, ok := raw.(map[string]any)
	if !ok || value == nil {
		return nil, present
	}
	return value, true
}

// shareEvent decodes one share event payload.
func shareEvent(t *testing.T, args []json.RawMessage) mediaItemShare {
	t.Helper()
	if len(args) != 1 {
		t.Fatalf("event args = %d, want 1", len(args))
	}
	var value mediaItemShare
	if err := json.Unmarshal(args[0], &value); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	return value
}

// summaries keeps failure messages readable.
func summary(value mediaItemShare) string {
	return fmt.Sprintf("{%s %s %s %v}", value.ID, value.MediaItemID, value.Slug, value.IsDownloadable)
}

// libraryPath works around the long fixture folder names in assertions.
func libraryPath(parts ...string) string { return strings.Join(parts, "/") }
