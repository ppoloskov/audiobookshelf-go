package podcast_episodes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// feedChapter is one podlove simple chapter of a stub feed.
type feedChapter struct {
	Start string
	Title string
}

// feedItem is one RSS item of a stub feed.
type feedItem struct {
	Title         string
	Subtitle      string
	Description   string
	PubDate       string
	GUID          string
	EnclosureURL  string
	EnclosureType string
	EnclosureLen  string
	Episode       string
	Season        string
	EpisodeType   string
	Chapters      []feedChapter
}

// feed is a stub RSS channel.
type feed struct {
	Title string
	Items []feedItem
}

// document returns the RSS XML the upstream feed parser reads.
func (f feed) document() string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:podcast="https://podcastindex.org/namespace/1.0" xmlns:psc="http://podlove.org/simple-chapters/">` + "\n")
	b.WriteString("  <channel>\n")
	element(&b, "title", f.Title)
	for _, item := range f.Items {
		b.WriteString("    <item>\n")
		element(&b, "title", item.Title)
		element(&b, "itunes:subtitle", item.Subtitle)
		element(&b, "description", item.Description)
		element(&b, "pubDate", item.PubDate)
		element(&b, "itunes:episode", item.Episode)
		element(&b, "itunes:season", item.Season)
		element(&b, "itunes:episodeType", item.EpisodeType)
		if item.GUID != "" {
			fmt.Fprintf(&b, "    <guid isPermaLink=\"false\">%s</guid>\n", text(item.GUID))
		}
		if item.EnclosureURL != "" {
			fmt.Fprintf(&b, "    <enclosure url=%q type=%q length=%q/>\n", item.EnclosureURL, item.EnclosureType, item.EnclosureLen)
		}
		if len(item.Chapters) > 0 {
			b.WriteString("    <psc:chapters version=\"1.2\">\n")
			for _, chapter := range item.Chapters {
				fmt.Fprintf(&b, "      <psc:chapter start=%q title=%q/>\n", chapter.Start, chapter.Title)
			}
			b.WriteString("    </psc:chapters>\n")
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
	fmt.Fprintf(b, "    <%s>%s</%s>\n", name, text(value), name)
}

// text escapes XML text content.
func text(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(value)
}

// audioStub is a local HTTP server on 127.0.0.1 that serves episode audio and
// feeds, counts hits per path and can hold a response open so a scenario can
// observe the running download.
type audioStub struct {
	server *httptest.Server

	mu     sync.Mutex
	bodies map[string]stubResponse
	hits   map[string]int
}

type stubResponse struct {
	contentType string
	body        []byte
	delay       time.Duration // before the first byte
	holdAfter   int           // bytes to send before holding the connection
	hold        time.Duration // time the handler holds the connection open
}

func newAudioStub(t *testing.T) *audioStub {
	t.Helper()
	stub := &audioStub{bodies: map[string]stubResponse{}, hits: map[string]int{}}
	stub.server = httptest.NewServer(stub)
	t.Cleanup(stub.server.Close)
	return stub
}

// ServeHTTP answers a configured path; an unknown path is a 404.
func (s *audioStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Length", fmt.Sprint(len(body.body)))
	}
	if body.holdAfter > 0 && body.holdAfter < len(body.body) {
		if _, err := w.Write(body.body[:body.holdAfter]); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(body.hold)
		if _, err := w.Write(body.body[body.holdAfter:]); err != nil {
			return
		}
		return
	}
	_, _ = w.Write(body.body) // a failed write fails the client side of the scenario
}

// url is the absolute URL of a path on the stub.
func (s *audioStub) url(path string) string { return s.server.URL + path }

// hitsOf returns how often a path was requested.
func (s *audioStub) hitsOf(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// setAudio serves audio bytes at a path.
func (s *audioStub) setAudio(path string, body []byte) {
	s.set(path, stubResponse{contentType: "audio/mpeg", body: body})
}

// setBody serves raw bytes with a content type.
func (s *audioStub) setBody(path, contentType string, body []byte) {
	s.set(path, stubResponse{contentType: contentType, body: body})
}

// setHeldAudio serves audio but pauses after holdAfter bytes.
func (s *audioStub) setHeldAudio(path string, body []byte, holdAfter int, hold time.Duration) {
	s.set(path, stubResponse{contentType: "audio/mpeg", body: body, holdAfter: holdAfter, hold: hold})
}

func (s *audioStub) set(path string, response stubResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[path] = response
}

// setFeed serves an RSS document at a path.
func (s *audioStub) setFeed(path string, document string) {
	s.set(path, stubResponse{contentType: "application/rss+xml; charset=utf-8", body: []byte(document)})
}
