package podcast_feed

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	feedReq      = "route-r150"
	feedParseInv = "INV-R150-PARSE-001"
	feedAuthInv  = "INV-R150-SAMPLED"
	feedIdemInv  = "INV-R150-REPEAT-001"
	feedSrc      = "server/controllers/PodcastController.js:200-218"
	feedUtilSrc  = "server/utils/podcastUtils.js:106-240,296-344"
	feedFetchSrc = "server/utils/podcastUtils.js:346-410"
	feedFetchInv = "INV-R150-FETCH-001"
)

// dailyFeed is the ordinary feed of the parse scenario.
func dailyFeed(stub *feedStub) feed {
	return feed{
		Title:       "Daily Wire",
		Link:        "https://example.invalid/daily",
		Language:    "en",
		Description: "<p>Daily <em>wire</em> report</p>",
		Author:      "Daily Author",
		Explicit:    "no",
		Type:        "episodic",
		PubDate:     "Wed, 01 Jan 2025 03:04:05 +0200",
		Image:       stub.url("/cover.jpg"),
		Categories:  []string{"News"},
		Items: []feedItem{
			{
				Title:         "Morning Brief",
				Subtitle:      "The start",
				Description:   "Plain morning description",
				PubDate:       "Mon, 06 Jan 2025 12:00:00 -0500",
				GUID:          "daily-guid-1",
				EnclosureURL:  stub.url("/one.mp3"),
				EnclosureType: "audio/mpeg",
				EnclosureLen:  "96673",
				Duration:      "10:05",
				Episode:       "1",
				Season:        "2",
			},
			{
				Title:     "Evening Brief",
				PubDate:   "Tue, 07 Jan 2025 08:30:00 GMT",
				MediaURL:  stub.url("/two.m4a"),
				MediaType: "audio/x-m4a",
				MediaLen:  "1234",
				Duration:  "95",
			},
		},
	}
}

// edgeFeed exercises CDATA, timezones, chapters, item skipping and URL trimming.
func edgeFeed(stub *feedStub) feed {
	return feed{
		Title:       "Wire Signal",
		Link:        "https://example.invalid/wire",
		Language:    "en-us",
		Description: "<p>Wire <b>signal</b> show</p>",
		Author:      "Wire Author",
		Explicit:    "yes",
		Type:        "serial",
		PubDate:     "Wed, 01 Jan 2025 03:04:05 +0200",
		Image:       stub.url("/itunes-cover.jpg"),
		Categories:  []string{"Technology/Software", "Society"},
		AtomLink:    stub.url("/self.rss"),
		Items: []feedItem{
			{
				Title:          "First Signal",
				Subtitle:       "Opening <b>shot</b>",
				ContentEncoded: "<p>Ep <b>one</b> body</p>",
				Description:    "Plain <i>desc</i>",
				PubDate:        "Mon, 06 Jan 2025 12:00:00 -0500",
				GUID:           "wire-guid-1",
				EnclosureURL:   stub.url("/one.mp3"),
				EnclosureType:  "audio/mpeg",
				EnclosureLen:   "96673",
				Duration:       "00:24:27",
				Episode:        "1",
				Season:         "1",
				EpisodeType:    "full",
				Author:         "Wire Author",
				Explicit:       "no",
				ChaptersURL:    "http://chapters.invalid/one.json",
				ChaptersType:   "application/json+chapters",
				Chapters: []feedChapter{
					{Start: "00:00", Title: "Intro"},
					{Start: "00:10:30", Title: "Body"},
				},
			},
			{
				Title:     "Second Signal",
				PubDate:   "Tue, 07 Jan 2025 08:30:00 GMT",
				MediaURL:  stub.url("/two.m4a"),
				MediaType: "audio/x-m4a",
				MediaLen:  "1234",
				Duration:  "95",
			},
			{
				Title:       "Skipped Signal",
				Description: "No enclosure here",
			},
			{
				Title:         "Third Signal",
				PubDate:       "not-a-date",
				EnclosureURL:  " " + stub.url("/three.mp3") + " ",
				EnclosureType: "audio/mpeg",
			},
		},
	}
}

func feedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: feedReq, Invariant: feedParseInv, Case: "normal", State: "initial", Assertion: "feed-route-parses-metadata-and-episodes",
			Sources: []string{feedSrc, feedUtilSrc, feedFetchSrc},
			Claim:   "POST /api/podcasts/feed fetches an RSS feed and answers its metadata (title, author, description and sanitized plain text, language, explicit, type, pubDate, link, image, categories and the requested feed URL) plus every playable item as an episode with its enclosure, guid, duration in seconds and parsed publication time.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/daily.xml", dailyFeed(stub).document())
				podcast := feedOf(t, e, e.Token(t, "root"), stub.url("/daily.xml"))
				requireValue(t, "metadata.title", podcast.Metadata.Title, "Daily Wire")
				requireValue(t, "metadata.author", podcast.Metadata.Author, "Daily Author")
				requireValue(t, "metadata.language", podcast.Metadata.Language, "en")
				requireValue(t, "metadata.explicit", podcast.Metadata.Explicit, "no")
				requireValue(t, "metadata.type", podcast.Metadata.Type, "episodic")
				requireValue(t, "metadata.pubDate", podcast.Metadata.PubDate, "Wed, 01 Jan 2025 03:04:05 +0200")
				requireValue(t, "metadata.link", podcast.Metadata.Link, "https://example.invalid/daily")
				requireValue(t, "metadata.image", podcast.Metadata.Image, stub.url("/cover.jpg"))
				requireValue(t, "metadata.description", podcast.Metadata.Description, "<p>Daily <em>wire</em> report</p>")
				requireValue(t, "metadata.descriptionPlain", podcast.Metadata.DescriptionPlain, "Daily wire report")
				requireValue(t, "metadata.feedUrl", podcast.Metadata.FeedURL, stub.url("/daily.xml"))
				if !reflect.DeepEqual(podcast.Metadata.Categories, []string{"News"}) {
					t.Fatalf("categories = %v", podcast.Metadata.Categories)
				}
				if len(podcast.Episodes) != 2 {
					t.Fatalf("%d episodes, want 2: %+v", len(podcast.Episodes), podcast.Episodes)
				}
				first := podcast.Episodes[0]
				requireValue(t, "episode title", first.Title, "Morning Brief")
				requireValue(t, "episode subtitle", first.Subtitle, "The start")
				requireValue(t, "episode description", first.Description, "Plain morning description")
				requireValue(t, "episode descriptionPlain", first.DescriptionPlain, "Plain morning description")
				requireValue(t, "episode pubDate", first.PubDate, "Mon, 06 Jan 2025 12:00:00 -0500")
				requireValue(t, "episode guid", first.GUID, "daily-guid-1")
				requireValue(t, "episode duration", first.Duration, "10:05")
				requireValue(t, "episode episode", first.Episode, "1")
				requireValue(t, "episode season", first.Season, "2")
				requireValue(t, "episode enclosure url", first.Enclosure.URL, stub.url("/one.mp3"))
				requireValue(t, "episode enclosure type", first.Enclosure.Type, "audio/mpeg")
				requireValue(t, "episode enclosure length", first.Enclosure.Length, "96673")
				if first.DurationSeconds == nil || *first.DurationSeconds != 605 {
					t.Fatalf("durationSeconds = %v, want 605", first.DurationSeconds)
				}
				if first.PublishedAt == nil || *first.PublishedAt != 1736182800000 {
					t.Fatalf("publishedAt = %v, want 1736182800000", first.PublishedAt)
				}
				second := podcast.Episodes[1]
				requireValue(t, "media:content enclosure", second.Enclosure.URL, stub.url("/two.m4a"))
				requireValue(t, "media:content type", second.Enclosure.Type, "audio/x-m4a")
				requireValue(t, "media:content length", second.Enclosure.Length, "1234")
				if second.GUID != nil {
					t.Fatalf("second guid = %v, want null", *second.GUID)
				}
				if second.PublishedAt == nil || *second.PublishedAt != 1736238600000 {
					t.Fatalf("second publishedAt = %v, want 1736238600000", second.PublishedAt)
				}
				if second.DurationSeconds == nil || *second.DurationSeconds != 95 {
					t.Fatalf("second durationSeconds = %v, want 95", second.DurationSeconds)
				}
			},
		},
		{
			Requirement: feedReq, Invariant: feedParseInv, Case: "edge", State: "changed", Assertion: "feed-route-cdata-timezones-and-item-edges",
			Sources: []string{feedUtilSrc, "server/utils/podcastUtils.js:257-294"},
			Claim:   "A feed served with CDATA descriptions, nested itunes categories, a timezone offset, podlove chapters, a media:content-only item, an item without an enclosure, an unparsable pubDate and a space-padded enclosure URL yields sanitized HTML, plain text, chapter starts and ends, the trimmed URL, a null publishedAt and only the playable items; a feed whose cover lives in the RSS image element reports no image, attributed text and bare guids are read, invalid chapters are dropped, a category whose only child has no text keeps the parent name, and an ISO-8859-1 feed decodes its accented title.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/edge.xml", edgeFeed(stub).document())
				podcast := feedOf(t, e, e.Token(t, "root"), stub.url("/edge.xml"))
				if !reflect.DeepEqual(podcast.Metadata.Categories, []string{"Technology:Software", "Society"}) {
					t.Fatalf("categories = %v", podcast.Metadata.Categories)
				}
				requireValue(t, "image", podcast.Metadata.Image, stub.url("/itunes-cover.jpg"))
				requireValue(t, "description", podcast.Metadata.Description, "<p>Wire <b>signal</b> show</p>")
				requireValue(t, "descriptionPlain", podcast.Metadata.DescriptionPlain, "Wire signal show")
				requireValue(t, "explicit", podcast.Metadata.Explicit, "yes")
				requireValue(t, "type", podcast.Metadata.Type, "serial")
				if len(podcast.Episodes) != 3 {
					t.Fatalf("%d episodes, want 3 (the item without an enclosure is skipped): %+v", len(podcast.Episodes), podcast.Episodes)
				}
				first := podcast.Episodes[0]
				requireValue(t, "subtitle", first.Subtitle, "Opening <b>shot</b>")
				requireValue(t, "description", first.Description, "<p>Ep <b>one</b> body</p>")
				requireValue(t, "descriptionPlain", first.DescriptionPlain, "Plain desc")
				requireValue(t, "chaptersUrl", first.ChaptersURL, "http://chapters.invalid/one.json")
				requireValue(t, "chaptersType", first.ChaptersType, "application/json+chapters")
				wantChapters := []rssChapter{{ID: 0, Title: "Intro", Start: 0, End: 630}, {ID: 1, Title: "Body", Start: 630, End: 1467}}
				if !reflect.DeepEqual(first.Chapters, wantChapters) {
					t.Fatalf("chapters = %+v, want %+v", first.Chapters, wantChapters)
				}
				if first.DurationSeconds == nil || *first.DurationSeconds != 1467 {
					t.Fatalf("durationSeconds = %v, want 1467", first.DurationSeconds)
				}
				last := podcast.Episodes[2]
				requireValue(t, "trimmed enclosure", last.Enclosure.URL, stub.url("/three.mp3"))
				requireValue(t, "pubDate kept verbatim", last.PubDate, "not-a-date")
				if last.PublishedAt != nil {
					t.Fatalf("publishedAt = %d, want null", *last.PublishedAt)
				}
				stub.setFeed("/plain-image.xml", feed{
					Title: "RSS Image Only",
					Items: []feedItem{{Title: "Only", EnclosureURL: stub.url("/only.mp3"), EnclosureType: "audio/mpeg"}},
				}.document())
				plain := feedOf(t, e, e.Token(t, "root"), stub.url("/plain-image.xml"))
				if plain.Metadata.Image != nil {
					t.Fatalf("the RSS image element is not read, image = %q", *plain.Metadata.Image)
				}
				stub.setFeed("/variants.xml", feed{
					Title:      "Variant Wire",
					Categories: []string{"Empty/"},
					Items: []feedItem{
						{
							Title: "Attributed Text", Subtitle: "Attributed subtitle", SubtitleType: "text",
							Description: "Attributed description", DescriptionType: "text/plain",
							GUID: "bare-guid", GUIDBare: true, PubDateType: "text",
							PubDate:      "Fri, 03 Jan 2025 10:00:00 GMT",
							EnclosureURL: stub.url("/variant-1.mp3"), EnclosureType: "audio/mpeg",
						},
						{
							Title: "Bad Chapters", EnclosureURL: stub.url("/variant-2.mp3"), EnclosureType: "audio/mpeg",
							Duration: "10:00",
							Chapters: []feedChapter{{Start: "abc", Title: "Broken"}, {Start: "00:01", Title: ""}},
						},
					},
				}.document())
				variants := feedOf(t, e, e.Token(t, "root"), stub.url("/variants.xml"))
				if !reflect.DeepEqual(variants.Metadata.Categories, []string{"Empty"}) {
					t.Fatalf("variant categories = %v", variants.Metadata.Categories)
				}
				requireValue(t, "attributed subtitle", variants.Episodes[0].Subtitle, "Attributed subtitle")
				requireValue(t, "attributed description", variants.Episodes[0].Description, "Attributed description")
				requireValue(t, "bare guid", variants.Episodes[0].GUID, "bare-guid")
				requireValue(t, "attributed pubDate", variants.Episodes[0].PubDate, "Fri, 03 Jan 2025 10:00:00 GMT")
				if variants.Episodes[0].PublishedAt == nil || *variants.Episodes[0].PublishedAt != 1735898400000 {
					t.Fatalf("attributed pubDate publishedAt = %v", variants.Episodes[0].PublishedAt)
				}
				if len(variants.Episodes[1].Chapters) != 0 {
					t.Fatalf("invalid chapters were accepted: %+v", variants.Episodes[1].Chapters)
				}
				if variants.Episodes[1].DurationSeconds == nil || *variants.Episodes[1].DurationSeconds != 600 {
					t.Fatalf("invalid-chapter durationSeconds = %v", variants.Episodes[1].DurationSeconds)
				}
				latin1 := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<rss version=\"2.0\"><channel><title>Caf\xe9 Wire</title><item><title>Caf\xe9 One</title><enclosure url=\"http://127.0.0.1:9/latin.mp3\" type=\"audio/mpeg\"/></item></channel></rss>")
				stub.setBytes("/latin1.xml", "text/xml; charset=ISO-8859-1", latin1)
				decoded := feedOf(t, e, e.Token(t, "root"), stub.url("/latin1.xml"))
				requireValue(t, "latin1 title", decoded.Metadata.Title, "Café Wire")
				requireValue(t, "latin1 episode", decoded.Episodes[0].Title, "Café One")
			},
		},
		{
			Requirement: feedReq, Invariant: feedIdemInv, Case: "edge", State: "repeat", Assertion: "feed-route-repeat-returns-the-same-payload",
			Sources: []string{feedFetchSrc, feedSrc},
			Claim:   "Two POST /api/podcasts/feed calls for the same unchanged feed answer the same metadata and episode values and create no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/daily.xml", dailyFeed(stub).document())
				before := takeSnapshot(t, e)
				first := feedOf(t, e, e.Token(t, "root"), stub.url("/daily.xml"))
				second := feedOf(t, e, e.Token(t, "root"), stub.url("/daily.xml"))
				if !reflect.DeepEqual(first, second) {
					t.Fatalf("the second fetch differs:\n%+v\n%+v", first, second)
				}
				before.requireUnchanged(t, e, "reading a feed")
			},
		},
		{
			Requirement: feedReq, Invariant: feedIdemInv, Case: "normal", State: "changed", Assertion: "feed-route-changed-feed-is-reparsed",
			Sources: []string{feedFetchSrc},
			Claim:   "When the remote feed changes between calls, the next POST /api/podcasts/feed answers the new title, episode list and publication time instead of a cached payload.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/daily.xml", dailyFeed(stub).document())
				first := feedOf(t, e, e.Token(t, "root"), stub.url("/daily.xml"))
				requireValue(t, "first title", first.Metadata.Title, "Daily Wire")
				changed := feed{Title: "Daily Wire Reboot", Categories: []string{"News"}, Items: []feedItem{{Title: "Reboot One", EnclosureURL: stub.url("/r1.mp3"), EnclosureType: "audio/mpeg", Duration: "5"}}}
				stub.setFeed("/daily.xml", changed.document())
				second := feedOf(t, e, e.Token(t, "root"), stub.url("/daily.xml"))
				requireValue(t, "second title", second.Metadata.Title, "Daily Wire Reboot")
				if len(second.Episodes) != 1 || second.Episodes[0].Title != "Reboot One" {
					t.Fatalf("episodes = %+v", second.Episodes)
				}
				if second.Episodes[0].DurationSeconds == nil || *second.Episodes[0].DurationSeconds != 5 {
					t.Fatalf("durationSeconds = %v, want 5", second.Episodes[0].DurationSeconds)
				}
			},
		},
		{
			Requirement: feedReq, Invariant: feedAuthInv, Case: "error", State: "failure", Assertion: "feed-route-rejections-change-nothing",
			Sources: []string{feedSrc, "server/utils/index.js:233-241", "server/utils/podcastUtils.js:296-306"},
			Claim:   "POST /api/podcasts/feed answers 401 without a token and 403 for a non-admin without fetching, 400 for a missing, non-string or unparsable rssFeed, and 404 when the feed is unreachable, answers 404, is not RSS, has no channel, has no items or has an empty body; no rejected request creates rows.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/empty.xml", feed{Title: "Empty"}.document())
				before := takeSnapshot(t, e)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/podcasts/feed", JSON: map[string]string{"rssFeed": stub.url("/daily.xml")}}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/daily.xml")}), http.StatusForbidden)
				if hits := stub.hitsOf("/daily.xml"); hits != 0 {
					t.Fatalf("a rejected request fetched the feed %d times", hits)
				}
				for _, body := range []map[string]any{
					{},
					{"rssFeed": ""},
					{"rssFeed": "not a url"},
					{"rssFeed": 42},
				} {
					wantStatus(t, "invalid rssFeed", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", body), http.StatusBadRequest)
				}
				wantStatus(t, "unreachable", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": deadURL(t)}), http.StatusNotFound)
				wantStatus(t, "http 404", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/missing.xml")}), http.StatusNotFound)
				stub.setBytes("/no-channel.xml", "application/rss+xml", []byte(`<rss version="2.0"></rss>`))
				wantStatus(t, "no channel", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/no-channel.xml")}), http.StatusNotFound)
				stub.setBytes("/empty-body.xml", "application/rss+xml", nil)
				wantStatus(t, "empty body", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/empty-body.xml")}), http.StatusNotFound)
				stub.setFeed("/malformed.xml", "<rss><channel><title>broken")
				wantStatus(t, "malformed xml", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/malformed.xml")}), http.StatusNotFound)
				stub.setBytes("/not-rss.xml", "application/xml", []byte(`{"rss":false}`))
				wantStatus(t, "json body", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/not-rss.xml")}), http.StatusNotFound)
				wantStatus(t, "no items", e.Post(t, e.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/empty.xml")}), http.StatusNotFound)
				before.requireUnchanged(t, e, "rejected feed reads")
			},
		},
		{
			Requirement: feedReq, Invariant: feedFetchInv, Case: "error", State: "failure", Assertion: "feed-route-slow-feed-times-out",
			Sources: []string{feedFetchSrc, "server/Server.js:107"},
			Claim:   "When the feed takes longer than the configured podcast download timeout, POST /api/podcasts/feed answers 404 instead of waiting for the feed and creates no rows.",
			Run: func(t *testing.T, _ *harness.Env) {
				server := harness.Start(t, harness.Options{Env: map[string]string{"PODCAST_DOWNLOAD_TIMEOUT": "1500"}})
				stub := newFeedStub(t)
				stub.setFeed("/slow.xml", dailyFeed(stub).document())
				stub.setDelay("/slow.xml", 6*time.Second)
				before := takeSnapshot(t, server)
				started := time.Now()
				resp := server.Post(t, server.Token(t, "root"), "/api/podcasts/feed", map[string]string{"rssFeed": stub.url("/slow.xml")})
				elapsed := time.Since(started)
				wantStatus(t, "slow feed", resp, http.StatusNotFound)
				if elapsed >= 6*time.Second {
					t.Fatalf("the request waited %s for the slow feed instead of timing out", elapsed)
				}
				before.requireUnchanged(t, server, "timing out on a feed")
			},
		},
	}
}

// strPtr returns the string behind a pointer or "" for nil.
func strPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
