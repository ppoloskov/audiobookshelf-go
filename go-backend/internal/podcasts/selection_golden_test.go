package podcasts

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/libitem"
)

var updateSelectionGolden = flag.Bool("update-golden", false, "rewrite testdata/selection.golden.json from the current code")

var opmlDocs = map[string]string{
	"flat":     `<?xml version="1.0"?><opml version="2.0"><head><title>x</title></head><body><outline type="rss" text="One" xmlUrl="https://a.example/1.xml"/><outline type="rss" title="Two" text="ignored" xmlUrl=" https://a.example/2.xml "/></body></opml>`,
	"nested":   `<opml><body><outline text="Folder"><outline type="rss" text="In folder" xmlUrl="https://a.example/3.xml"/><outline text="Deeper"><outline type="RSS" text="Deep" xmlUrl="https://a.example/4.xml"/></outline></outline><outline type="rss" text="Top" xmlUrl="https://a.example/5.xml"/></body></opml>`,
	"skipped":  `<opml><body><outline type="link" text="Link" xmlUrl="https://a.example/6.xml"/><outline type="rss" text="No url"/><outline type="rss" xmlUrl="https://a.example/7.xml"/></body></opml>`,
	"empty":    `<opml><body></body></opml>`,
	"malform":  `<opml><body><outline type="rss" xmlUrl="https://a.example/8.xml"></body>`,
	"notxml":   `hello`,
	"latin1":   "<?xml version=\"1.0\" encoding=\"windows-1252\"?><opml><body><outline type=\"rss\" text=\"Caf\xe9\" xmlUrl=\"https://a.example/9.xml\"/></body></opml>",
	"entities": `<opml><body><outline type="rss" text="A &amp; B" xmlUrl="https://a.example/10.xml?a=1&amp;b=2"/></body></opml>`,
}

func TestEpisodeSelectionGolden(t *testing.T) {
	got := map[string]any{}
	opml := map[string]any{}
	for name, doc := range opmlDocs {
		opml[name] = canonicalJSON(t, parseOPML(doc))
	}
	got["opml"] = opml
	for _, name := range append(append([]string{}, goldenFeeds...), "many") {
		raw, err := os.ReadFile("testdata/feeds/" + name + ".xml")
		if err != nil {
			t.Fatal(err)
		}
		eps := feedEpisodeMaps(t, raw)
		res := map[string]any{}
		var titles []string
		var times []int64
		for _, e := range eps {
			title, _ := e["title"].(string)
			titles = append(titles, title)
			switch p := e["publishedAt"].(type) {
			case float64:
				times = append(times, int64(p))
			case int64:
				times = append(times, p)
			}
		}
		queries := []string{"", "zzzzqqq", "episode", "the"}
		for i, title := range titles {
			queries = append(queries, title)
			if len(title) > 6 && i < 3 {
				queries = append(queries, title[:len(title)/2])
			}
		}
		matches := map[string]any{}
		for _, q := range queries {
			matches[q] = canonicalJSON(t, matchEpisodesFor(t, raw, q))
		}
		res["match"] = matches
		var known []libitem.Episode
		if len(eps) > 0 {
			guid, _ := eps[0]["guid"].(string)
			known = append(known, libitem.Episode{GUID: guid})
		}
		if len(eps) > 2 {
			enc, _ := eps[len(eps)-1]["enclosure"].(map[string]any)
			url, _ := enc["url"].(string)
			known = append(known, libitem.Episode{Enclosure: &libitem.Enclosure{URL: url}})
		}
		item := libitem.Item{Podcast: &libitem.Podcast{Episodes: known}}
		fresh := map[string]any{}
		afters := []int64{0, 1}
		afters = append(afters, times...)
		for _, after := range afters {
			for _, limit := range []int{0, 1, 3} {
				key := json.Number(itoa(after)).String() + "/" + itoa(int64(limit))
				fresh[key] = canonicalJSON(t, filterNewFor(t, raw, item, after, limit))
			}
		}
		res["filterNew"] = fresh
		got[name] = res
	}
	const file = "testdata/selection.golden.json"
	gotValue := canonicalJSON(t, got)
	if *updateSelectionGolden {
		pretty, _ := json.MarshalIndent(gotValue, "", "  ")
		if err := os.WriteFile(file, append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		gotBytes, _ := json.MarshalIndent(gotValue, "", "  ")
		_ = os.WriteFile(os.TempDir()+"/selection.got.json", gotBytes, 0o644)
		t.Fatalf("episode selection differs from %s (got in %s/selection.got.json)", file, os.TempDir())
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func testFeed(t *testing.T, raw []byte) *Feed {
	t.Helper()
	feed, err := parseFeed(raw, "https://feeds.example/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func feedEpisodeMaps(t *testing.T, raw []byte) []map[string]any {
	var out []map[string]any
	b, _ := json.Marshal(testFeed(t, raw).Episodes)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func matchEpisodesFor(t *testing.T, raw []byte, q string) any {
	return matchEpisodes(testFeed(t, raw).Episodes, q)
}

func filterNewFor(t *testing.T, raw []byte, item libitem.Item, after int64, limit int) any {
	return filterNew(testFeed(t, raw).Episodes, item, after, limit)
}

func TestParseOPMLMatchesNamesCaseInsensitively(t *testing.T) {
	doc := `<OPML><BODY><Outline TYPE="rss" Text="Mixed" XMLURL="https://a.example/m.xml"/><outline type="rss" TITLE="T" text="x" xmlUrl="https://a.example/n.xml"/></BODY></OPML>`
	want := []opmlFeed{{"Mixed", "https://a.example/m.xml"}, {"T", "https://a.example/n.xml"}}
	if got := parseOPML(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
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
