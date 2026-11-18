package notify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// episodePubDate is a publication date in the future of every run, so the
// scheduler's "episodes after the latest one" filter (server/managers/
// PodcastManager.js:396) always treats the stub episode as new.
const episodePubDate = "Sun, 01 Jun 2031 10:00:00 GMT"

// scheduleEveryTwoSeconds is a node-cron expression with a seconds field: the
// bundled scheduler validates six fields (server/libs/nodeCron/pattern-validation.js:112),
// so the podcast check runs without waiting for a wall-clock minute.
const scheduleEveryTwoSeconds = "*/2 * * * * *"

// stubItem is one RSS item of the stub feed.
type stubItem struct {
	Title           string
	Subtitle        string
	Description     string
	PubDate         string
	GUID            string
	EnclosureURL    string
	EnclosureType   string
	EnclosureLength string
}

// rssDocument renders the subset of RSS the upstream feed parser reads
// (server/utils/podcastUtils.js:120-210).
func rssDocument(channelTitle string, items ...stubItem) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/">` + "\n")
	b.WriteString("  <channel>\n")
	element(&b, "title", channelTitle)
	for _, item := range items {
		b.WriteString("    <item>\n")
		element(&b, "title", item.Title)
		element(&b, "itunes:subtitle", item.Subtitle)
		element(&b, "description", item.Description)
		element(&b, "pubDate", item.PubDate)
		if item.GUID != "" {
			fmt.Fprintf(&b, "    <guid isPermaLink=\"false\">%s</guid>\n", xmlText(item.GUID))
		}
		if item.EnclosureURL != "" {
			fmt.Fprintf(&b, "    <enclosure url=%q type=%q length=%q/>\n", item.EnclosureURL, item.EnclosureType, item.EnclosureLength)
		}
		b.WriteString("    </item>\n")
	}
	b.WriteString("  </channel>\n</rss>\n")
	return b.String()
}

// element writes one escaped element when the value is not empty.
func element(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "    <%s>%s</%s>\n", name, xmlText(value), name)
}

// xmlText escapes XML character data.
func xmlText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(value)
}

// feedStub serves a stub RSS feed and the episode audio on 127.0.0.1 and counts
// the requests per path, so a scenario can prove the scheduler fetched the feed.
type feedStub struct {
	server *httptest.Server

	mu     sync.Mutex
	bodies map[string]stubBody
	hits   map[string]int
}

type stubBody struct {
	body        []byte
	contentType string
}

// newFeedStub starts the stub and registers its shutdown.
func newFeedStub(t *testing.T) *feedStub {
	t.Helper()
	stub := &feedStub{bodies: map[string]stubBody{}, hits: map[string]int{}}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)
	return stub
}

// ServeHTTP answers a configured path; an unknown path is a 404.
func (s *feedStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	body, ok := s.bodies[r.URL.Path]
	s.hits[r.URL.Path]++
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if body.contentType != "" {
		w.Header().Set("Content-Type", body.contentType)
	}
	_, _ = w.Write(body.body) // a failed write fails the client side of the scenario
}

// url is the absolute URL of a path on the stub.
func (s *feedStub) url(path string) string { return s.server.URL + path }

// setFeed serves an RSS document at a path.
func (s *feedStub) setFeed(path, document string) {
	s.set(path, stubBody{body: []byte(document), contentType: "application/rss+xml; charset=utf-8"})
}

// setAudio serves episode audio at a path.
func (s *feedStub) setAudio(path string, body []byte) {
	s.set(path, stubBody{body: body, contentType: "audio/mpeg"})
}

func (s *feedStub) set(path string, body stubBody) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[path] = body
}

// hitsOf returns how often a path was requested.
func (s *feedStub) hitsOf(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// fixtureEpisodeAudio returns the bytes of a fixture podcast episode, served as
// the enclosure of the stub feed.
func fixtureEpisodeAudio(t *testing.T, e *harness.Env) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.Media, "podcasts", "Fixture Cast", "episode-1.mp3"))
	if err != nil {
		t.Fatalf("read fixture episode: %v", err)
	}
	return data
}

// podcastFolder returns the id and full path of the podcast library's folder.
func podcastFolder(t *testing.T, e *harness.Env) (folderID, fullPath string) {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["podcastLibrary"])
	wantStatus(t, "GET library", resp, http.StatusOK)
	object := resp.Object(t)
	folderID, _ = harness.Dig(object, "folders", 0, "id").(string)
	fullPath, _ = harness.Dig(object, "folders", 0, "fullPath").(string)
	if folderID == "" || fullPath == "" {
		t.Fatalf("podcast library has no folder: %.300s", resp.Body)
	}
	return folderID, fullPath
}

// createAutoDownloadPodcast creates a podcast whose feed the bundled scheduler
// polls every two seconds. It returns the library item id.
func createAutoDownloadPodcast(t *testing.T, e *harness.Env, title, feedURL string) string {
	t.Helper()
	folderID, folderPath := podcastFolder(t, e)
	resp := e.Post(t, e.Token(t, "root"), "/api/podcasts", map[string]any{
		"libraryId": e.IDs["podcastLibrary"],
		"folderId":  folderID,
		"path":      filepath.Join(folderPath, title),
		"media": map[string]any{
			"metadata": map[string]any{
				"title":       title,
				"author":      "Notify Author",
				"description": "<p>Notification fixture podcast</p>",
				"genres":      []string{"GenreX", "GenreY"},
				"feedUrl":     feedURL,
			},
			"autoDownloadEpisodes": true,
			"autoDownloadSchedule": scheduleEveryTwoSeconds,
			"tags":                 []string{"TagA", "TagB"},
		},
	})
	wantStatus(t, "POST /api/podcasts", resp, http.StatusOK)
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &item); err != nil || item.ID == "" {
		t.Fatalf("created podcast has no id: %v: %.300s", err, resp.Body)
	}
	return item.ID
}
