package feeds

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// eventWait bounds one socket event wait; quietWindow is the period a
	// non-audience socket is observed for an event it must not receive.
	eventWait   = 5 * time.Second
	quietWindow = 400 * time.Millisecond

	// serverAddress is the address the web client sends with a feed open (the
	// browser origin). It is persisted but never used to build XML URLs.
	serverAddress = "https://abs.example:8443"

	// seriesID is the fixture "Long Saga" series (ids.json has no series entry).
	seriesID = "e987a322-9111-4f79-8cb9-c25ee714e0d6"

	// maxTestFileBytes bounds a test read of a fixture media file.
	maxTestFileBytes = 4 << 20
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// fixture names the fixture ids the feed scenarios use.
type fixture struct {
	library, podcastLibrary    string
	saga1, saga2, novel, ebook string
	disposable, podcast        string
	collection, series         string
}

func fixtureOf(e *harness.Env) fixture {
	return fixture{
		library:        e.IDs["library"],
		podcastLibrary: e.IDs["podcastLibrary"],
		saga1:          e.IDs["saga1"],
		saga2:          e.IDs["saga2"],
		novel:          e.IDs["novel"],
		ebook:          e.IDs["ebook"],
		disposable:     e.IDs["disposable"],
		podcast:        e.IDs["podcast"],
		collection:     e.IDs["collection"],
		series:         seriesID,
	}
}

// feedMetaMin is the minified feed metadata of the open route, the close event
// and the minified list.
type feedMetaMin struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	PreventIndexing *bool   `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// feedMeta is the full feed metadata of the management list.
type feedMeta struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	Author          *string `json:"author"`
	ImageURL        *string `json:"imageUrl"`
	FeedURL         *string `json:"feedUrl"`
	Link            *string `json:"link"`
	Explicit        *bool   `json:"explicit"`
	Type            *string `json:"type"`
	Language        *string `json:"language"`
	PreventIndexing *bool   `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// enclosure is a feed episode enclosure.
type enclosure struct {
	URL  string `json:"url"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// feedEpisode is one episode of the management list.
type feedEpisode struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Enclosure   enclosure `json:"enclosure"`
	PubDate     string    `json:"pubDate"`
	Link        string    `json:"link"`
	Author      string    `json:"author"`
	Explicit    bool      `json:"explicit"`
	Duration    float64   `json:"duration"`
	Season      *string   `json:"season"`
	Episode     *string   `json:"episode"`
	EpisodeType *string   `json:"episodeType"`
	FullPath    string    `json:"fullPath"`
}

// feedJSON is one feed of GET /api/feeds (feeds[]).
type feedJSON struct {
	ID              string        `json:"id"`
	Slug            string        `json:"slug"`
	UserID          string        `json:"userId"`
	EntityType      string        `json:"entityType"`
	EntityID        string        `json:"entityId"`
	EntityUpdatedAt int64         `json:"entityUpdatedAt"`
	CoverPath       *string       `json:"coverPath"`
	Meta            feedMeta      `json:"meta"`
	ServerAddress   string        `json:"serverAddress"`
	FeedURL         string        `json:"feedUrl"`
	Episodes        []feedEpisode `json:"episodes"`
	CreatedAt       int64         `json:"createdAt"`
	UpdatedAt       int64         `json:"updatedAt"`
}

// feedMinJSON is one feed of GET /api/feeds (minified[]), the open response
// and both socket event payloads.
type feedMinJSON struct {
	ID         string      `json:"id"`
	EntityType string      `json:"entityType"`
	EntityID   string      `json:"entityId"`
	FeedURL    string      `json:"feedUrl"`
	Meta       feedMetaMin `json:"meta"`
}

// feedList is the GET /api/feeds body.
type feedList struct {
	Feeds    []feedJSON    `json:"feeds"`
	Minified []feedMinJSON `json:"minified"`
}

// wantStatus fails unless the response has the expected status.
func wantStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status = %d, want %d; body %.300s", what, resp.Status, want, resp.Body)
	}
}

// openPath is the management path that opens a feed for one entity.
func openPath(entity, entityID string) string {
	switch entity {
	case "item":
		return "/api/feeds/item/" + entityID + "/open"
	case "collection":
		return "/api/feeds/collection/" + entityID + "/open"
	case "series":
		return "/api/feeds/series/" + entityID + "/open"
	}
	panic("unknown feed entity " + entity) // test bug, not data
}

// openBody builds an open request body. An empty details map means the request
// carries no metadataDetails key at all.
func openBody(slug string, details map[string]any) map[string]any {
	body := map[string]any{"serverAddress": serverAddress, "slug": slug}
	if details != nil {
		body["metadataDetails"] = details
	}
	return body
}

// openFeed sends one open request.
func openFeed(t *testing.T, e *harness.Env, entity, entityID, slug string, details map[string]any) harness.Response {
	t.Helper()
	return e.Post(t, e.Token(t, "root"), openPath(entity, entityID), openBody(slug, details))
}

// mustOpenFeed opens a feed and requires 200, returning the minified feed.
func mustOpenFeed(t *testing.T, e *harness.Env, entity, entityID, slug string, details map[string]any) feedMinJSON {
	t.Helper()
	resp := openFeed(t, e, entity, entityID, slug, details)
	wantStatus(t, resp, http.StatusOK, "open feed "+slug)
	var body struct {
		Feed feedMinJSON `json:"feed"`
	}
	resp.JSON(t, &body)
	if body.Feed.ID == "" || !uuidV4.MatchString(body.Feed.ID) {
		t.Fatalf("open feed %s returned no v4 id: %+v", slug, body.Feed)
	}
	if body.Feed.FeedURL != "/feed/"+slug {
		t.Fatalf("open feed %s feedUrl = %q", slug, body.Feed.FeedURL)
	}
	return body.Feed
}

// closeFeed sends POST /api/feeds/:id/close.
func closeFeed(t *testing.T, e *harness.Env, token, feedID string) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/feeds/"+feedID+"/close", nil)
}

// feedBody reads one feed's XML as an unauthenticated feed reader does.
func feedBody(t *testing.T, e *harness.Env, slug string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: "/feed/" + slug, Raw: false})
}

// mustFeedXML reads a feed XML and requires 200 text/xml.
func mustFeedXML(t *testing.T, e *harness.Env, slug string) parsedFeed {
	t.Helper()
	resp := feedBody(t, e, slug)
	wantStatus(t, resp, http.StatusOK, "feed "+slug)
	if got := resp.Header.Get("Content-Type"); got != "text/xml; charset=utf-8" {
		t.Fatalf("feed %s Content-Type = %q, want text/xml; charset=utf-8", slug, got)
	}
	return parseFeed(t, resp.Body)
}

// feedRowBySlug returns the single persisted feeds row of a slug.
func feedRowBySlug(t *testing.T, e *harness.Env, slug string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM feeds WHERE slug = ?", slug)
	if len(rows) != 1 {
		t.Fatalf("feeds rows for slug %s: %d, want 1", slug, len(rows))
	}
	return rows[0]
}

// noFeedRow asserts no persisted feed has this slug or id.
func noFeedRow(t *testing.T, e *harness.Env, slug, id string) {
	t.Helper()
	if rows := e.Rows(t, "SELECT id FROM feeds WHERE slug = ? OR id = ?", slug, id); len(rows) != 0 {
		t.Fatalf("feed %s/%s must be gone: %v", slug, id, rows)
	}
}

// feedEpisodeRows returns the persisted feed episodes of a feed in row order.
func feedEpisodeRows(t *testing.T, e *harness.Env, feedID string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT * FROM feedEpisodes WHERE feedId = ?", feedID)
}

// counts is a cheap snapshot for "nothing changed" assertions.
type counts struct{ feeds, episodes int }

func snapshotCounts(t *testing.T, e *harness.Env) counts {
	t.Helper()
	return counts{
		feeds:    e.Count(t, "SELECT COUNT(*) FROM feeds"),
		episodes: e.Count(t, "SELECT COUNT(*) FROM feedEpisodes"),
	}
}

// wantSameCounts asserts a rejected request persisted nothing.
func wantSameCounts(t *testing.T, e *harness.Env, before counts, what string) {
	t.Helper()
	if after := snapshotCounts(t, e); after != before {
		t.Fatalf("%s changed feed rows: before %+v, after %+v", what, before, after)
	}
}

// column asserts one SQLite column holds want (compared as text; nil = NULL).
func column(t *testing.T, row map[string]any, name string, want any) {
	t.Helper()
	got, ok := row[name]
	if !ok {
		t.Fatalf("column %s missing from row %v", name, row)
	}
	if want == nil {
		if got != nil {
			t.Fatalf("column %s = %v (%T), want NULL", name, got, got)
		}
		return
	}
	if got == nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("column %s = %v (%T), want %v", name, got, got, want)
	}
}

// readMediaFile reads a fixture file with a size bound.
func readMediaFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() > maxTestFileBytes {
		t.Fatalf("%s is %d bytes, above the %d byte test limit", path, info.Size(), maxTestFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// episodeFile returns the filePath column of an episode row.
func episodeFile(t *testing.T, row map[string]any) string {
	t.Helper()
	path, _ := row["filePath"].(string)
	if path == "" {
		t.Fatalf("episode row has no filePath: %v", row)
	}
	return path
}

// feedCoverPath returns the coverPath column of a feed row.
func feedCoverPath(t *testing.T, row map[string]any) string {
	t.Helper()
	path, ok := row["coverPath"].(string)
	if !ok || path == "" {
		t.Fatalf("feed row has no coverPath: %v", row)
	}
	return path
}

// rootUserID returns the fixture root user id (the owner of every feed).
func rootUserID(t *testing.T, e *harness.Env) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM users WHERE type = 'root'")
	if len(rows) != 1 {
		t.Fatalf("root users: %d rows", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// relativeToBase strips the server base URL from an absolute fixture URL.
func relativeToBase(t *testing.T, e *harness.Env, absolute string) string {
	t.Helper()
	if !strings.HasPrefix(absolute, e.Base+"/") {
		t.Fatalf("%q is not below the server base %q", absolute, e.Base)
	}
	return strings.TrimPrefix(absolute, e.Base)
}

// listFeeds reads GET /api/feeds and requires 200.
func listFeeds(t *testing.T, e *harness.Env, token string) feedList {
	t.Helper()
	resp := e.Get(t, token, "/api/feeds")
	wantStatus(t, resp, http.StatusOK, "list feeds")
	var value feedList
	resp.JSON(t, &value)
	return value
}

// feedByID finds one listed feed by id.
func feedByID(t *testing.T, list feedList, id string) feedJSON {
	t.Helper()
	for _, feed := range list.Feeds {
		if feed.ID == id {
			return feed
		}
	}
	t.Fatalf("feed %s is not in the list of %d feeds", id, len(list.Feeds))
	return feedJSON{}
}

// minifiedByID finds one minified listed feed by id.
func minifiedByID(t *testing.T, list feedList, id string) feedMinJSON {
	t.Helper()
	for _, feed := range list.Minified {
		if feed.ID == id {
			return feed
		}
	}
	t.Fatalf("feed %s is not in the %d minified feeds", id, len(list.Minified))
	return feedMinJSON{}
}

// feedIDs returns the ids of both list arrays, requiring the same order.
func feedIDs(t *testing.T, list feedList) []string {
	t.Helper()
	ids := make([]string, 0, len(list.Feeds))
	for i, feed := range list.Feeds {
		if i >= len(list.Minified) || list.Minified[i].ID != feed.ID {
			t.Fatalf("feeds and minified arrays disagree at index %d", i)
		}
		ids = append(ids, feed.ID)
	}
	if len(list.Minified) != len(list.Feeds) {
		t.Fatalf("feeds has %d entries, minified has %d", len(list.Feeds), len(list.Minified))
	}
	return ids
}

// hasString reports whether list contains value.
func hasString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// eventPayload decodes the single argument of a feed socket event.
func eventPayload(t *testing.T, args []json.RawMessage) feedMinJSON {
	t.Helper()
	if len(args) != 1 {
		t.Fatalf("feed event args = %d, want 1", len(args))
	}
	var value feedMinJSON
	if err := json.Unmarshal(args[0], &value); err != nil {
		t.Fatalf("feed event payload: %v", err)
	}
	return value
}
