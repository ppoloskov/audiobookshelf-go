package podcasts

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The .golden.json files are pinned fixtures: the output of the earlier
// encoding/xml parser, captured before parseFeed replaced it, and reviewed by
// hand. They are never regenerated from the current code. Each feed covers
// one shape of real-world feed: Megaphone and Simplecast style iTunes tags,
// CDATA and escaped HTML descriptions, audioboom style categories and
// podcast:chapters, windows-1252 encoding, and a plain RSS 2.0 feed.
var goldenFeeds = []string{"megaphone", "simplecast", "audioboom", "plain-rss", "windows1252", "fixture-cast"}

func canonicalJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseFeedMatchesPinnedFixtures(t *testing.T) {
	for _, name := range goldenFeeds {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/feeds/" + name + ".xml")
			if err != nil {
				t.Fatal(err)
			}
			feed, err := parseFeed(raw, "https://feeds.example/feed.xml")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/feeds/" + name + ".golden.json")
			if err != nil {
				t.Fatal(err)
			}
			var wantValue any
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			if got := canonicalJSON(t, feed); !reflect.DeepEqual(got, wantValue) {
				gotBytes, _ := json.MarshalIndent(got, "", "  ")
				t.Errorf("parseFeed(%s) differs from the pinned fixture.\ngot:\n%s", name, gotBytes)
			}
		})
	}
}
