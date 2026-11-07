package podcasts

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// These tests pin the deliberate differences from the encoding/xml parser
// that gofeed replaced. Each one names the old behavior it changes.

func parseTestFeed(t *testing.T, raw string) (*Feed, error) {
	t.Helper()
	return parseFeed([]byte(raw), "https://feeds.example/feed.xml")
}

// TestParseFeedFallbackDifferences: the old parser left these fields empty or
// dropped the episode. gofeed fills them from the element the feed really has.
func TestParseFeedFallbackDifferences(t *testing.T) {
	raw, err := os.ReadFile("testdata/feeds/diff-fallbacks.xml")
	if err != nil {
		t.Fatal(err)
	}
	feed, err := parseFeed(raw, "https://feeds.example/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	meta := feed.Metadata
	// Old: channel description was empty when only itunes:summary was set.
	if meta.Description != "Only the summary is set." || meta.DescriptionPlain != "Only the summary is set." {
		t.Errorf("channel description from itunes:summary: %#v", meta)
	}
	// Old: categories came from the nested itunes categories, as now.
	if !reflect.DeepEqual(meta.Categories, []string{"Society & Culture:Philosophy", "Arts:Books"}) {
		t.Errorf("categories: %#v", meta.Categories)
	}
	// Old: an unprefixed <author> was the episode author when itunes:author was
	// missing. Now only itunes:author counts, so the field is empty.
	episodes := feed.Episodes
	if len(episodes) != 4 {
		t.Fatalf("episodes: want 4 (old 3, media:content fallback added), got %d", len(episodes))
	}
	summary, media, psc := episodes[0], episodes[1], episodes[2]
	// Old: description was empty when only itunes:summary was set.
	if summary.Description != "Summary used as description." {
		t.Errorf("episode description from itunes:summary: %#v", summary.Description)
	}
	// Old: an episode with only a media:content audio file was dropped. The old
	// namespace match looked for "media" inside the URL http://search.yahoo.com/mrss/.
	if !reflect.DeepEqual(media.Enclosure, enclosure{URL: "https://fallback.example/b.mp3", Type: "audio/mpeg", Length: "2"}) {
		t.Errorf("media:content enclosure: %#v", media.Enclosure)
	}
	// Old: psc:chapters were never read (same namespace-URL mismatch).
	wantChapters := []chapter{
		{ID: 0, Title: "Intro", Start: 0, End: 30},
		{ID: 1, Title: "Middle", Start: 30, End: 60},
	}
	if !reflect.DeepEqual(psc.Chapters, wantChapters) {
		t.Errorf("psc chapters: %#v", psc.Chapters)
	}
	plainEp := episodes[3]
	// Old: pubDate was empty and publishedAt nil when only dc:date was set.
	if plainEp.PubDate != "2024-02-01T10:00:00Z" || plainEp.PublishedAt == nil || *plainEp.PublishedAt != 1706781600000 {
		t.Errorf("dc:date fallback: pubDate=%#v publishedAt=%#v", plainEp.PubDate, plainEp.PublishedAt)
	}
	// Old: the unprefixed <author> was the episode author; now it is empty.
	if plainEp.Author != "" {
		t.Errorf("episode author from unprefixed <author>: %#v", plainEp.Author)
	}
}

// TestParseFeedChannelImageIsRSSFirst: the old lookup of <image> matched any
// namespace, so an itunes:image that came first hid the RSS <image><url>. The
// intended order is <image><url>, then itunes:image, which is kept here.
func TestParseFeedChannelImageIsRSSFirst(t *testing.T) {
	feed, err := parseTestFeed(t, `<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>T</title><itunes:image href="https://img.example/itunes.png"/><image><url>https://img.example/rss.png</url></image><item><title>E</title><enclosure url="https://e.example/1.mp3"/></item></channel></rss>`)
	if err != nil {
		t.Fatal(err)
	}
	if got := feed.Metadata.Image; got != "https://img.example/rss.png" {
		t.Errorf("image: %#v", got)
	}
}

// TestParseFeedRejectsWhatTheOldParserRejected keeps the errors the old parser
// returned: Atom and non-feed documents, a feed with no episodes, and a feed
// whose items have no playable enclosure.
func TestParseFeedRejectsWhatTheOldParserRejected(t *testing.T) {
	cases := map[string]string{
		"atom":         `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>A</title><entry><title>E</title></entry></feed>`,
		"not feed":     `<html><body>nope</body></html>`,
		"no items":     `<rss version="2.0"><channel><title>Empty</title></channel></rss>`,
		"no enclosure": `<rss version="2.0"><channel><title>T</title><item><title>E</title><description>No audio here.</description></item></channel></rss>`,
	}
	for name, doc := range cases {
		if _, err := parseTestFeed(t, doc); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := parseTestFeed(t, cases["no items"]); err == nil || !strings.Contains(err.Error(), "no episodes") {
		t.Errorf("no items error: %v", err)
	}
}

// TestParseFeedEmptyTextIsNil keeps the nil values the golden records for an
// absent category list and absent chapters, so JSON stays null as before.
func TestParseFeedEmptyTextIsNil(t *testing.T) {
	feed, err := parseTestFeed(t, `<rss version="2.0"><channel><title>T</title><item><title>E</title><enclosure url="https://e.example/1.mp3"/></item></channel></rss>`)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Metadata.Categories != nil {
		t.Errorf("categories should be a nil slice, got %#v", feed.Metadata.Categories)
	}
	if ep := feed.Episodes[0]; ep.Chapters == nil || len(ep.Chapters) != 0 {
		t.Errorf("chapters should be an empty slice, got %#v", ep.Chapters)
	}
}
