package podcast_feed

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventTimeout = 15 * time.Second // a socket event the server sends during a request
	quietWindow  = 700 * time.Millisecond
	createWait   = 30 * time.Second // the asynchronous OPML import creates its items
)

// https://podcastindex.org/namespace/1.0 feed elements are written literally; the
// RSS builders below cover the subset server/utils/podcastUtils.js reads.

// wantStatus fails unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: status %d, want %d: %.400s", what, resp.Status, status, resp.Body)
	}
}

// decode unmarshals a JSON body into a new T.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %T: %v: %.400s", value, err, body)
	}
	return value
}

// feedStub is a local RSS server on 127.0.0.1 that serves the bodies set for a
// path and counts the requests per path, so a scenario can prove that a
// rejected or short-circuited request never fetched the feed.
type feedStub struct {
	server *httptest.Server

	mu     sync.Mutex
	bodies map[string]stubBody
	hits   map[string]int
}

type stubBody struct {
	body        string
	contentType string
	delay       time.Duration
}

func newFeedStub(t *testing.T) *feedStub {
	t.Helper()
	stub := &feedStub{bodies: map[string]stubBody{}, hits: map[string]int{}}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)
	return stub
}

// ServeHTTP answers the configured path; an unknown path is a 404.
func (s *feedStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	body, ok := s.bodies[r.URL.Path]
	s.hits[r.URL.Path]++
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if body.delay > 0 {
		time.Sleep(body.delay)
	}
	if body.contentType != "" {
		w.Header().Set("Content-Type", body.contentType)
	}
	_, _ = w.Write([]byte(body.body)) // a failed write fails the client side of the scenario
}

// setFeed serves an RSS document at a path.
func (s *feedStub) setFeed(path, body string) {
	s.set(stubBody{body: body, contentType: "application/rss+xml; charset=utf-8"}, path)
}

// setBytes serves raw bytes with a content type.
func (s *feedStub) setBytes(path, contentType string, body []byte) {
	s.set(stubBody{body: string(body), contentType: contentType}, path)
}

// setDelay makes the next requests to a path answer after a delay.
func (s *feedStub) setDelay(path string, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.bodies[path]
	entry.delay = delay
	s.bodies[path] = entry
}

func (s *feedStub) set(entry stubBody, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[path] = entry
}

// url is the absolute URL of a path on the stub.
func (s *feedStub) url(path string) string { return s.server.URL + path }

// hitsOf returns how often a path was requested.
func (s *feedStub) hitsOf(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// deadURL returns an absolute URL on a closed local port, so a fetch fails
// without leaving the machine.
func deadURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + address + "/feed.xml"
}

// jpegBytes returns a small valid JPEG.
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 99, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fixtureEpisodeMP3 returns the bytes of a fixture podcast episode, used as the
// enclosure payload of a feed refresh.
func fixtureEpisodeMP3(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Media, "podcasts", "Fixture Cast", "episode-1.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// feedOf fetches and decodes a feed through the API.
func feedOf(t *testing.T, e *harness.Env, token, feedURL string) rssPodcast {
	t.Helper()
	resp := e.Post(t, token, "/api/podcasts/feed", map[string]string{"rssFeed": feedURL})
	wantStatus(t, "get feed", resp, http.StatusOK)
	return decode[podcastPayload](t, resp.Body).Podcast
}

// itemOf reads a library item through the API.
func itemOf(t *testing.T, e *harness.Env, id string) libraryItemJSON {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id+"?expanded=1")
	wantStatus(t, "get item", resp, http.StatusOK)
	return decode[libraryItemJSON](t, resp.Body)
}

// createPodcast posts the create body and returns the decoded library item.
func createPodcast(t *testing.T, e *harness.Env, token string, body map[string]any) (harness.Response, libraryItemJSON) {
	t.Helper()
	resp := e.Post(t, token, "/api/podcasts", body)
	if resp.Status != http.StatusOK {
		return resp, libraryItemJSON{}
	}
	return resp, decode[libraryItemJSON](t, resp.Body)
}

// libraryFolder returns the id and full path of the first folder of a library.
func libraryFolder(t *testing.T, e *harness.Env, libraryID string) (folderID, fullPath string) {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID)
	wantStatus(t, "get library", resp, http.StatusOK)
	object := resp.Object(t)
	folderID, _ = harness.Dig(object, "folders", 0, "id").(string)
	fullPath, _ = harness.Dig(object, "folders", 0, "fullPath").(string)
	if folderID == "" || fullPath == "" {
		t.Fatalf("library %s has no folder: %.400s", libraryID, resp.Body)
	}
	return folderID, fullPath
}

// signalPodcastBody is the rich create payload of the normal create scenario.
func signalPodcastBody(e *harness.Env, folderID, folderPath, feedURL, imageURL string) map[string]any {
	return map[string]any{
		"libraryId": e.IDs["podcastLibrary"],
		"folderId":  folderID,
		"path":      filepath.Join(folderPath, "Signal Show"),
		"media": map[string]any{
			"metadata": map[string]any{
				"title":          "Signal Show",
				"author":         "Signal Author",
				"description":    "<p>Signal <b>description</b></p>",
				"releaseDate":    "2025-01-01",
				"genres":         []string{"Technology", "Science"},
				"feedUrl":        feedURL,
				"imageUrl":       imageURL,
				"itunesPageUrl":  "https://example.invalid/signal",
				"itunesId":       "123456789",
				"itunesArtistId": "987654321",
				"language":       "en-us",
				"type":           "episodic",
				"explicit":       false,
			},
			"autoDownloadEpisodes": true,
			"autoDownloadSchedule": "0 0 1 1 *",
			"tags":                 []string{"public"},
		},
	}
}

// createUser creates a local account and returns its token. The permissions
// decide which podcasts the user may see.
func createUser(t *testing.T, e *harness.Env, username string, permissions map[string]any) string {
	t.Helper()
	body := map[string]any{"username": username, "password": username + "-password", "type": "user", "isActive": true}
	for key, value := range permissions {
		body[key] = value
	}
	wantStatus(t, "create user", e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK)
	token, _ := e.Login(t, username, username+"-password")
	return token
}

// snapshot counts the rows a create must not add when a request is rejected.
type snapshot struct {
	libraryItems    int
	podcasts        int
	podcastEpisodes int
}

func takeSnapshot(t *testing.T, e *harness.Env) snapshot {
	t.Helper()
	return snapshot{
		libraryItems:    e.Count(t, "SELECT count(*) FROM libraryItems"),
		podcasts:        e.Count(t, "SELECT count(*) FROM podcasts"),
		podcastEpisodes: e.Count(t, "SELECT count(*) FROM podcastEpisodes"),
	}
}

// requireUnchanged fails unless the snapshot still matches.
func (s snapshot) requireUnchanged(t *testing.T, e *harness.Env, what string) {
	t.Helper()
	if after := takeSnapshot(t, e); after != s {
		t.Fatalf("%s changed rows: %+v, want %+v", what, after, s)
	}
}

// podcastRow reads the stored podcast row columns a client or the scanner reads back.
func podcastRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, `SELECT id, title, author, description, feedURL, imageURL,
		itunesId, itunesArtistId, language, podcastType, explicit, autoDownloadEpisodes,
		autoDownloadSchedule, lastEpisodeCheck, maxEpisodesToKeep, maxNewEpisodesToDownload,
		tags, genres, numEpisodes, coverPath FROM podcasts WHERE id = ?`, id)
	if len(rows) != 1 {
		t.Fatalf("podcast %s: %d rows", id, len(rows))
	}
	return rows[0]
}

// requireString asserts a column value.
func requireString(t *testing.T, row map[string]any, column, want string) {
	t.Helper()
	if got, _ := row[column].(string); got != want {
		t.Fatalf("%s = %q, want %q (row %+v)", column, got, want, row)
	}
}

// requireValue asserts a decoded string value, treating a nil pointer as "".
func requireValue(t *testing.T, what string, got any, want string) {
	t.Helper()
	var value string
	switch typed := got.(type) {
	case string:
		value = typed
	case *string:
		value = strPtr(typed)
	default:
		t.Fatalf("%s: unsupported value %T", what, got)
	}
	if value != want {
		t.Fatalf("%s = %q, want %q", what, value, want)
	}
}

// opmlOf reads the library OPML file.
func opmlOf(t *testing.T, e *harness.Env, token, libraryID string) (harness.Response, opmlDocument) {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+libraryID+"/opml")
	if resp.Status != http.StatusOK {
		return resp, opmlDocument{}
	}
	var doc opmlDocument
	if err := xml.Unmarshal(resp.Body, &doc); err != nil {
		t.Fatalf("decode OPML: %v: %.400s", err, resp.Body)
	}
	return resp, doc
}

// outlinesByURL indexes the outlines by their feed URL.
func outlinesByURL(doc opmlDocument) map[string]opmlOutline {
	outlines := make(map[string]opmlOutline, len(doc.Body.Outlines))
	for _, outline := range doc.Body.Outlines {
		outlines[outline.XMLURL] = outline
	}
	return outlines
}

// podcastTitles reads the library podcast titles.
func podcastTitles(t *testing.T, e *harness.Env, token, libraryID string) titlesResponse {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+libraryID+"/podcast-titles")
	wantStatus(t, "podcast titles", resp, http.StatusOK)
	return decode[titlesResponse](t, resp.Body)
}

// queryValues builds a query for a request.
func queryValues(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		values.Set(pairs[i], pairs[i+1])
	}
	return values
}
