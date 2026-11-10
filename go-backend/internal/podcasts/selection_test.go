package podcasts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/podcasts/read"
)

// Expected feeds are read off the OPML text by hand: rss outlines with an
// xmlUrl count, nested outlines are walked depth first, the title is "title"
// before "text", and values are trimmed and entity-decoded.
func TestParseOPMLSelectsRSSOutlines(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		want      []opmlFeed
	}{
		{"flat", `<?xml version="1.0"?><opml version="2.0"><head><title>x</title></head><body><outline type="rss" text="One" xmlUrl="https://a.example/1.xml"/><outline type="rss" title="Two" text="ignored" xmlUrl=" https://a.example/2.xml "/></body></opml>`,
			[]opmlFeed{{"One", "https://a.example/1.xml"}, {"Two", "https://a.example/2.xml"}}},
		{"nested", `<opml><body><outline text="Folder"><outline type="rss" text="In folder" xmlUrl="https://a.example/3.xml"/><outline text="Deeper"><outline type="RSS" text="Deep" xmlUrl="https://a.example/4.xml"/></outline></outline><outline type="rss" text="Top" xmlUrl="https://a.example/5.xml"/></body></opml>`,
			[]opmlFeed{{"In folder", "https://a.example/3.xml"}, {"Deep", "https://a.example/4.xml"}, {"Top", "https://a.example/5.xml"}}},
		{"skips non-rss and urlless outlines", `<opml><body><outline type="link" text="Link" xmlUrl="https://a.example/6.xml"/><outline type="rss" text="No url"/><outline type="rss" xmlUrl="https://a.example/7.xml"/></body></opml>`,
			[]opmlFeed{{"", "https://a.example/7.xml"}}},
		{"windows-1252", "<?xml version=\"1.0\" encoding=\"windows-1252\"?><opml><body><outline type=\"rss\" text=\"Caf\xe9\" xmlUrl=\"https://a.example/9.xml\"/></body></opml>",
			[]opmlFeed{{"Café", "https://a.example/9.xml"}}},
		{"entities", `<opml><body><outline type="rss" text="A &amp; B" xmlUrl="https://a.example/10.xml?a=1&amp;b=2"/></body></opml>`,
			[]opmlFeed{{"A & B", "https://a.example/10.xml?a=1&b=2"}}},
		{"attribute names ignore case", `<OPML><BODY><Outline TYPE="rss" Text="Mixed" XMLURL="https://a.example/m.xml"/><outline type="rss" TITLE="T" text="x" xmlUrl="https://a.example/n.xml"/></BODY></OPML>`,
			[]opmlFeed{{"Mixed", "https://a.example/m.xml"}, {"T", "https://a.example/n.xml"}}},
		{"empty body", `<opml><body></body></opml>`, nil},
		{"malformed xml", `<opml><body><outline type="rss" xmlUrl="https://a.example/8.xml"></body>`, nil},
		{"not xml", `hello`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parseOPML(c.doc)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func publishedAt(ms int64) *int64 { return &ms }

func ptr(s string) *string { return &s }

func enclosureOf(url string) enclosure { return enclosure{URL: url} }

// PodcastManager.checkPodcastForNewEpisodes (PodcastManager.js:402-407): an
// episode is new when it is published after the cutoff and the item has no
// episode with its guid or enclosure URL; at most `limit` are returned when
// the limit is positive. The TS also needs a publishedAt (undefined > n is
// false), so an undated episode is never new.
func TestFilterNewSelectsUnseenEpisodesAfterCutoff(t *testing.T) {
	feed := []Episode{
		{Title: "undated", GUID: ptr("g0"), Enclosure: enclosureOf("u0")},
		{Title: "old", GUID: ptr("g1"), PublishedAt: publishedAt(100), Enclosure: enclosureOf("u1")},
		{Title: "known guid", GUID: ptr("known"), PublishedAt: publishedAt(300), Enclosure: enclosureOf("u2")},
		{Title: "known url", GUID: ptr("g3"), PublishedAt: publishedAt(310), Enclosure: enclosureOf("known-url")},
		{Title: "new a", GUID: ptr("g4"), PublishedAt: publishedAt(400), Enclosure: enclosureOf("u4")},
		{Title: "at cutoff", GUID: ptr("g5"), PublishedAt: publishedAt(200), Enclosure: enclosureOf("u5")},
		{Title: "new b", PublishedAt: publishedAt(500), Enclosure: enclosureOf("u6")},
		{Title: "new c", GUID: ptr("g7"), PublishedAt: publishedAt(600), Enclosure: enclosureOf("u7")},
	}
	item := items.Item{Podcast: &podcastread.Podcast{Episodes: []podcastread.Episode{
		{GUID: "known"}, {Enclosure: &podcastread.Enclosure{URL: "known-url"}},
	}}}
	titles := func(limit int, after int64) string {
		var out []string
		for _, e := range filterNew(feed, item, after, limit) {
			out = append(out, e.Title)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		after int64
		limit int
		want  string
	}{
		{200, 0, "new a,new b,new c"},
		{200, 2, "new a,new b"},
		{200, 1, "new a"},
		{200, 99, "new a,new b,new c"},
		{0, 0, "old,new a,at cutoff,new b,new c"},
		{600, 0, ""},
	} {
		if got := titles(c.limit, c.after); got != c.want {
			t.Errorf("after=%d limit=%d: got %q, want %q", c.after, c.limit, got, c.want)
		}
	}
}

// Episodes are found by fuzzy title/subtitle similarity: an exact title ranks
// first, an unrelated or empty query finds nothing.
func TestMatchEpisodesRanksByTitleSimilarity(t *testing.T) {
	eps := []Episode{
		{Title: "The Long Road Home", Subtitle: "part one"},
		{Title: "Cooking with Gas", Subtitle: "kitchen tips"},
		{Title: "The Long Road Home (remastered)"},
	}
	titles := func(q string) []string {
		var out []string
		for _, m := range matchEpisodes(eps, q) {
			out = append(out, m.Episode.Title)
		}
		return out
	}
	if got := titles("the long road home"); len(got) == 0 || got[0] != "The Long Road Home" {
		t.Errorf("exact title should rank first: %v", got)
	}
	if got := titles("Cooking with Gas"); len(got) != 1 || got[0] != "Cooking with Gas" {
		t.Errorf("only the cooking episode matches: %v", got)
	}
	for _, q := range []string{"", "   ", "zzzzqqq"} {
		if got := titles(q); len(got) != 0 {
			t.Errorf("query %q matched %v", q, got)
		}
	}
}

func TestEnclosureLengthAcceptsNumberOrString(t *testing.T) {
	var eps []Episode
	if err := json.Unmarshal([]byte(`[{"enclosure":{"url":"u","length":12}},{"enclosure":{"url":"u","length":"34"}}]`), &eps); err != nil {
		t.Fatal(err)
	}
	if eps[0].Enclosure.Length != "12" || eps[1].Enclosure.Length != "34" {
		t.Fatalf("%#v", eps)
	}
}
