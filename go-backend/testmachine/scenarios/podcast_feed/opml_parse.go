package podcast_feed

import (
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	opmlParseReq     = "route-r151"
	opmlParseInv     = "INV-R151-PARSE-001"
	opmlParseAuthInv = "INV-R151-SAMPLED"
	opmlParseSrc     = "server/controllers/PodcastController.js:226-243"
	opmlParserSrc    = "server/utils/parsers/parseOPML.js:1-27"
)

// opmlFeed is one feed of the OPML preview response.
type opmlFeed struct {
	Title   string `json:"title"`
	FeedURL string `json:"feedUrl"`
}

// opmlFeedsPayload is the body of POST /api/podcasts/opml/parse.
type opmlFeedsPayload struct {
	Feeds []opmlFeed `json:"feeds"`
}

// parseOPML posts an OPML document and returns the preview feeds.
func parseOPML(t *testing.T, e *harness.Env, token, text string) []opmlFeed {
	t.Helper()
	resp := e.Post(t, token, "/api/podcasts/opml/parse", map[string]string{"opmlText": text})
	wantStatus(t, "parse opml", resp, http.StatusOK)
	return decode[opmlFeedsPayload](t, resp.Body).Feeds
}

func opmlParseScenarios() []harness.Scenario {
	const simple = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Subscriptions</title></head>
  <body>
    <outline type="rss" text="Text Feed" xmlUrl="http://127.0.0.1:9/text.xml"/>
    <outline type="rss" title="Title Feed" xmlUrl="http://127.0.0.1:9/title.xml"/>
  </body>
</opml>`
	const nested = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Subscriptions</title></head>
  <body>
    <outline text="Group">
      <outline type="rss" text="Child" xmlUrl="http://127.0.0.1:9/child.xml"/>
      <outline type="link" title="Not a feed" url="http://127.0.0.1:9/page"/>
      <outline type="rss" title="Missing url"/>
      <outline type="rss" xmlUrl="http://127.0.0.1:9/untitled.xml"/>
      <outline type="RSS" title="Upper case" xmlUrl="http://127.0.0.1:9/upper.xml"/>
    </outline>
  </body>
</opml>`
	return []harness.Scenario{
		{
			Requirement: opmlParseReq, Invariant: opmlParseInv, Case: "normal", State: "initial", Assertion: "opml-parse-returns-rss-outlines",
			Sources: []string{opmlParseSrc, opmlParserSrc},
			Claim:   "POST /api/podcasts/opml/parse answers every RSS outline of the uploaded document as {title, feedUrl}, taking the title from the outline title attribute and falling back to its text attribute.",
			Run: func(t *testing.T, e *harness.Env) {
				want := []opmlFeed{
					{Title: "Text Feed", FeedURL: "http://127.0.0.1:9/text.xml"},
					{Title: "Title Feed", FeedURL: "http://127.0.0.1:9/title.xml"},
				}
				if got := parseOPML(t, e, e.Token(t, "root"), simple); !reflect.DeepEqual(got, want) {
					t.Fatalf("feeds = %+v, want %+v", got, want)
				}
			},
		},
		{
			Requirement: opmlParseReq, Invariant: opmlParseInv, Case: "edge", State: "changed", Assertion: "opml-parse-nested-and-invalid-outlines",
			Sources: []string{opmlParserSrc},
			Claim:   "Nested outlines are reported in document order, an outline whose type attribute is not lower-case rss, a non-RSS outline and an RSS outline without an xmlUrl are skipped, and an RSS outline without title or text reports an empty title.",
			Run: func(t *testing.T, e *harness.Env) {
				want := []opmlFeed{{Title: "Child", FeedURL: "http://127.0.0.1:9/child.xml"}, {Title: "", FeedURL: "http://127.0.0.1:9/untitled.xml"}}
				if got := parseOPML(t, e, e.Token(t, "root"), nested); !reflect.DeepEqual(got, want) {
					t.Fatalf("feeds = %+v, want %+v", got, want)
				}
				if got := parseOPML(t, e, e.Token(t, "root"), `<opml><body></body></opml>`); len(got) != 0 {
					t.Fatalf("empty document feeds = %+v", got)
				}
			},
		},
		{
			Requirement: opmlParseReq, Invariant: opmlParseInv, Case: "edge", State: "repeat", Assertion: "opml-parse-repeat-is-identical",
			Sources: []string{opmlParserSrc},
			Claim:   "Parsing the same OPML text twice answers the same feeds and creates no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				first := parseOPML(t, e, e.Token(t, "root"), simple)
				second := parseOPML(t, e, e.Token(t, "root"), simple)
				if !reflect.DeepEqual(first, second) {
					t.Fatalf("second parse = %+v, want %+v", second, first)
				}
				before.requireUnchanged(t, e, "previewing an OPML document")
			},
		},
		{
			Requirement: opmlParseReq, Invariant: opmlParseAuthInv, Case: "error", State: "failure", Assertion: "opml-parse-rejections-change-nothing",
			Sources: []string{opmlParseSrc},
			Claim:   "POST /api/podcasts/opml/parse answers 401 without a token, 403 for a non-admin and 400 without opmlText or with an empty one, and a rejected request creates no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/podcasts/opml/parse", JSON: map[string]string{"opmlText": simple}}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/podcasts/opml/parse", map[string]string{"opmlText": simple}), http.StatusForbidden)
				wantStatus(t, "no body", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/parse", map[string]any{}), http.StatusBadRequest)
				wantStatus(t, "empty text", e.Post(t, e.Token(t, "root"), "/api/podcasts/opml/parse", map[string]string{"opmlText": ""}), http.StatusBadRequest)
				before.requireUnchanged(t, e, "a rejected OPML preview")
			},
		},
	}
}
