package feeds

import (
	"bytes"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Slugs of the feeds the fixture database ships with.
const (
	podcastFeedSlug = "fixture-cast-feed" // podcast item feed, three episodes
	saga1FeedSlug   = "trial-feed-saga1"  // three-track book item feed
	novelFeedSlug   = "trial-feed-novel"  // single-file book item feed
)

// Fixture feed and episode ids (testdata/install/config/absdatabase.sqlite).
const (
	podcastFeedID = "88e5eb3a-0ec5-45ca-856f-f9d7a25fe073"
	saga1FeedID   = "601e397d-3b81-44b6-94ed-2b0d860e532a"
	novelFeedID   = "34dfa385-5620-4d0e-afb9-ebe319c4f253"

	novelEpisodeID   = "d24dce70-d0bc-47b6-af08-599d62455b77"
	saga1EpisodeOne  = "cadcc786-2cd2-433d-82c6-8855cee49d92"
	podcastEpisodeID = "9ab2d898-1f41-4d8f-ac81-08ba5ac378d7"
)

// Fixture pubDates: audiobook tracks are offset by one minute from the
// library item's createdAt, podcast episodes keep their own date.
const (
	novelPubDate  = "Tue, 03 Nov 2026 12:46:25 GMT"
	saga1PubDate1 = "Tue, 03 Nov 2026 12:46:26 GMT"
	saga1PubDate2 = "Tue, 03 Nov 2026 12:47:26 GMT"
	saga1PubDate3 = "Tue, 03 Nov 2026 12:48:26 GMT"
	podcastDate1  = "Sat, 15 Mar 2025 00:00:00 GMT"
	podcastDate2  = "Sat, 08 Mar 2025 00:00:00 GMT"
	podcastDate3  = "Sat, 01 Mar 2025 00:00:00 GMT"
)

var lastBuildDatePattern = regexp.MustCompile(`<lastBuildDate>[^<]*</lastBuildDate>`)

// listScenarios covers route-r187: GET /api/feeds.
func listScenarios() []harness.Scenario {
	const req = "route-r187"
	const inv = "INV-R187-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "stateless", Assertion: "list-fixture-feeds-with-full-episodes",
			Sources: []string{
				"server/controllers/RSSFeedController.js:25-33",
				"server/managers/RssFeedManager.js:399-405",
				"server/models/Feed.js:645-691",
				"server/models/FeedEpisode.js:287-308",
			},
			Claim: "GET /api/feeds as root returns one feeds[] entry and one minified[] entry for each of the three fixture feeds, in the same order, with the full client shape: id/slug/userId/entity ids, entityUpdatedAt and created/updated millisecond timestamps, coverPath, serverAddress, feedUrl, every meta field and the persisted episodes with enclosure size/type and fullPath.",
			Run: func(t *testing.T, e *harness.Env) {
				list := listFeeds(t, e, e.Token(t, "root"))
				ids := feedIDs(t, list)
				if len(ids) != 3 {
					t.Fatalf("fixture feed count = %d, want 3: %v", len(ids), ids)
				}
				for _, want := range []string{podcastFeedID, saga1FeedID, novelFeedID} {
					if !hasString(ids, want) {
						t.Fatalf("fixture feed %s missing from %v", want, ids)
					}
				}
				ownerID := rootUserID(t, e)

				novel := feedByID(t, list, novelFeedID)
				if novel.Slug != novelFeedSlug || novel.EntityType != "libraryItem" || novel.EntityID != e.IDs["novel"] || novel.UserID != ownerID {
					t.Fatalf("novel feed identity = %+v", novel)
				}
				if novel.EntityUpdatedAt != 1793709985743 || novel.CreatedAt != 1793709988428 || novel.UpdatedAt != 1793709988428 {
					t.Fatalf("novel feed timestamps = %d/%d/%d", novel.EntityUpdatedAt, novel.CreatedAt, novel.UpdatedAt)
				}
				if novel.ServerAddress != "http://trial.invalid" || novel.FeedURL != "/feed/"+novelFeedSlug {
					t.Fatalf("novel feed addresses = %q/%q", novel.ServerAddress, novel.FeedURL)
				}
				if novel.CoverPath == nil || !strings.HasSuffix(*novel.CoverPath, "/items/"+e.IDs["novel"]+"/cover.jpg") {
					t.Fatalf("novel feed coverPath = %v", novel.CoverPath)
				}
				wantMeta(t, novel.Meta, "Single File Novel", nil, "Ada Example",
					"/feed/trial-feed-novel/cover.jpg?ts=1793709985743", "/feed/"+novelFeedSlug, "/item/"+e.IDs["novel"], "serial", "Trial Owner", "owner@example.com")
				if len(novel.Episodes) != 1 {
					t.Fatalf("novel feed episodes = %d, want 1", len(novel.Episodes))
				}
				episode := novel.Episodes[0]
				if episode.ID != novelEpisodeID || episode.Title != "Single File Novel" || episode.Description != "" || episode.Author != "Ada Example" {
					t.Fatalf("novel episode identity = %+v", episode)
				}
				if episode.Enclosure.URL != "/feed/trial-feed-novel/item/"+novelEpisodeID+"/media.m4b" || episode.Enclosure.Size != 193740 || episode.Enclosure.Type != "audio/mp4" {
					t.Fatalf("novel episode enclosure = %+v", episode.Enclosure)
				}
				if episode.PubDate != "Tue, 03 Nov 2026 12:46:25 GMT" || episode.Duration != 45 || episode.Explicit || episode.Link != "/item/"+e.IDs["novel"] {
					t.Fatalf("novel episode fields = %+v", episode)
				}
				if episode.Season != nil || episode.Episode != nil || episode.EpisodeType != nil {
					t.Fatalf("novel episode podcast fields must be null: %+v", episode)
				}
				if !strings.HasSuffix(episode.FullPath, "/Single File Novel/Single File Novel.m4b") {
					t.Fatalf("novel episode fullPath = %q", episode.FullPath)
				}

				saga1 := feedByID(t, list, saga1FeedID)
				if saga1.Meta.Title == nil || *saga1.Meta.Title != "The Beginning" || saga1.Meta.Type == nil || *saga1.Meta.Type != "serial" || saga1.EntityUpdatedAt != 1793709986209 || saga1.CreatedAt != 1793709988394 {
					t.Fatalf("saga1 feed = %+v", saga1)
				}
				if saga1.CoverPath == nil || !strings.HasSuffix(*saga1.CoverPath, "/Book 1 - The Beginning/cover.jpg") {
					t.Fatalf("saga1 coverPath = %v", saga1.CoverPath)
				}
				var titles []string
				for _, ep := range saga1.Episodes {
					titles = append(titles, ep.Title)
					if ep.Enclosure.Type != "audio/mpeg" || ep.Enclosure.Size != 80558 || ep.Duration != 20.062041 {
						t.Fatalf("saga1 episode enclosure/duration = %+v", ep)
					}
					if ep.PubDate != "" && ep.PubDate != saga1PubDate1 && ep.PubDate != saga1PubDate2 && ep.PubDate != saga1PubDate3 {
						t.Fatalf("saga1 episode pubDate = %q", ep.PubDate)
					}
				}
				if len(titles) != 3 || titles[0] != "Part 1" || titles[1] != "Part 2" || titles[2] != "Part 3" {
					t.Fatalf("saga1 episode titles = %v", titles)
				}

				podcast := feedByID(t, list, podcastFeedID)
				if podcast.Meta.Title == nil || *podcast.Meta.Title != "Fixture Cast" || podcast.Meta.Description != nil || podcast.Meta.Author == nil || *podcast.Meta.Author != "Fixture Host" {
					t.Fatalf("podcast feed meta = %+v", podcast.Meta)
				}
				if podcast.Meta.Type == nil || *podcast.Meta.Type != "episodic" || podcast.Meta.Language != nil || podcast.Meta.OwnerName != nil || podcast.Meta.OwnerEmail != nil {
					t.Fatalf("podcast feed meta variants = %+v", podcast.Meta)
				}
				if podcast.EntityUpdatedAt != 1793709987721 || podcast.CreatedAt != 1793709988357 {
					t.Fatalf("podcast feed timestamps = %+v", podcast)
				}
				if len(podcast.Episodes) != 3 || podcast.Episodes[0].Title != "Third Time" || podcast.Episodes[2].Title != "Pilot Episode" {
					t.Fatalf("podcast episodes = %+v", podcast.Episodes)
				}
				if got := podcast.Episodes[0]; got.Season == nil || *got.Season != "1" || got.Episode == nil || *got.Episode != "3" || got.EpisodeType != nil {
					t.Fatalf("podcast episode season/episode = %+v", got)
				}
				if got := podcast.Episodes[0].Description; got != "Episode three closes the season." {
					t.Fatalf("podcast episode description = %q", got)
				}

				for _, feed := range list.Feeds {
					min := minifiedByID(t, list, feed.ID)
					wantMinified(t, min, feed)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "stateless", Assertion: "list-feeds-denies-non-admin-and-anonymous",
			Sources: []string{"server/controllers/RSSFeedController.js:194-204", "server/routers/ApiRouter.js:310"},
			Claim:   "GET /api/feeds answers 401 without a token and 403 for the listener and finisher users, and no rejected body contains feed data.",
			Run: func(t *testing.T, e *harness.Env) {
				cases := []struct {
					name, token string
					want        int
				}{
					{"anonymous", "", http.StatusUnauthorized},
					{"listener", e.Token(t, "listener"), http.StatusForbidden},
					{"finisher", e.Token(t, "finisher"), http.StatusForbidden},
				}
				for _, test := range cases {
					resp := e.Get(t, test.token, "/api/feeds")
					wantStatus(t, resp, test.want, "list feeds as "+test.name)
					if bytes.Contains(resp.Body, []byte(novelFeedSlug)) || bytes.Contains(resp.Body, []byte("Single File Novel")) {
						t.Fatalf("rejected list leaked feed data: %.200s", resp.Body)
					}
				}
			},
		},
	}
}

// wantMeta asserts the full feed meta of a book item feed.
func wantMeta(t *testing.T, meta feedMeta, title string, description *string, author, imageURL, feedURL, link, kind, ownerName, ownerEmail string) {
	t.Helper()
	if meta.Title == nil || *meta.Title != title {
		t.Fatalf("meta title = %v, want %q", meta.Title, title)
	}
	if description == nil {
		if meta.Description != nil {
			t.Fatalf("meta description = %q, want null", *meta.Description)
		}
	} else if meta.Description == nil || *meta.Description != *description {
		t.Fatalf("meta description = %v, want %q", meta.Description, *description)
	}
	if meta.Author == nil || *meta.Author != author {
		t.Fatalf("meta author = %v, want %q", meta.Author, author)
	}
	if meta.ImageURL == nil || *meta.ImageURL != imageURL {
		t.Fatalf("meta imageUrl = %v, want %q", meta.ImageURL, imageURL)
	}
	if meta.FeedURL == nil || *meta.FeedURL != feedURL {
		t.Fatalf("meta feedUrl = %v, want %q", meta.FeedURL, feedURL)
	}
	if meta.Link == nil || *meta.Link != link {
		t.Fatalf("meta link = %v, want %q", meta.Link, link)
	}
	if meta.Explicit == nil || *meta.Explicit {
		t.Fatalf("meta explicit = %v, want false", meta.Explicit)
	}
	if meta.Type == nil || *meta.Type != kind {
		t.Fatalf("meta type = %v, want %q", meta.Type, kind)
	}
	if meta.Language != nil {
		t.Fatalf("meta language = %q, want null", *meta.Language)
	}
	if meta.PreventIndexing == nil || !*meta.PreventIndexing {
		t.Fatalf("meta preventIndexing = %v, want true", meta.PreventIndexing)
	}
	if meta.OwnerName == nil || *meta.OwnerName != ownerName {
		t.Fatalf("meta ownerName = %v, want %q", meta.OwnerName, ownerName)
	}
	if meta.OwnerEmail == nil || *meta.OwnerEmail != ownerEmail {
		t.Fatalf("meta ownerEmail = %v, want %q", meta.OwnerEmail, ownerEmail)
	}
}

// wantMinified asserts the minified list entry mirrors the full one.
func wantMinified(t *testing.T, min feedMinJSON, full feedJSON) {
	t.Helper()
	if min.ID != full.ID || min.EntityType != full.EntityType || min.EntityID != full.EntityID || min.FeedURL != full.FeedURL {
		t.Fatalf("minified %+v does not mirror %s", min, full.ID)
	}
	if min.Meta.Title == nil || full.Meta.Title == nil || *min.Meta.Title != *full.Meta.Title {
		t.Fatalf("minified title %v differs from %v", min.Meta.Title, full.Meta.Title)
	}
	if (min.Meta.Description == nil) != (full.Meta.Description == nil) {
		t.Fatalf("minified description %v differs from %v", min.Meta.Description, full.Meta.Description)
	}
	if min.Meta.PreventIndexing == nil || full.Meta.PreventIndexing == nil || *min.Meta.PreventIndexing != *full.Meta.PreventIndexing {
		t.Fatalf("minified preventIndexing %v differs from %v", min.Meta.PreventIndexing, full.Meta.PreventIndexing)
	}
	if (min.Meta.OwnerName == nil) != (full.Meta.OwnerName == nil) || (min.Meta.OwnerEmail == nil) != (full.Meta.OwnerEmail == nil) {
		t.Fatalf("minified owner %v/%v differs from %v/%v", min.Meta.OwnerName, min.Meta.OwnerEmail, full.Meta.OwnerName, full.Meta.OwnerEmail)
	}
}

// feedXMLScenarios covers route-r007: GET /feed/:slug.
func feedXMLScenarios() []harness.Scenario {
	const req = "route-r007"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-001", Case: "normal", State: "initial", Assertion: "feed-xml-serves-exact-item-channel",
			Sources: []string{
				"server/managers/RssFeedManager.js:165-189",
				"server/models/Feed.js:575-632",
				"server/models/FeedEpisode.js:314-346",
				"server/libs/rss/index.js:35-140",
			},
			Claim: "An unauthenticated GET /feed/trial-feed-novel returns text/xml; charset=utf-8 with the exact channel elements of the fixture item feed (title/description/link/image/generator/atom self link/language/author/itunes type/image/explicit/owner/block) and the single item with its CDATA title, absolute enclosure and GUID, creator, GMT pubDate, rounded itunes:duration and no podcast-only elements.",
			Run: func(t *testing.T, e *harness.Env) {
				feed := mustFeedXML(t, e, novelFeedSlug)
				siteURL := "/item/" + e.IDs["novel"]
				imageURL := e.Base + "/feed/trial-feed-novel/cover.jpg?ts=1793709985743"
				wantShape(t, feed.channel,
					"title", "description", "link", "image", "generator", "lastBuildDate", "atom:link",
					"language", "author", "itunes:author", "itunes:type", "itunes:image",
					"itunes:explicit", "itunes:owner", "itunes:block", "googleplay:block", "item")
				wantText(t, feed.channel, "title", "Single File Novel")
				wantText(t, feed.channel, "description", "Single File Novel")
				wantText(t, feed.channel, "link", e.Base+siteURL)
				wantText(t, feed.channel, "generator", "Audiobookshelf")
				wantText(t, feed.channel, "language", "en")
				wantText(t, feed.channel, "author", "Ada Example")
				wantText(t, feed.channel, "itunes:author", "Ada Example")
				wantText(t, feed.channel, "itunes:type", "serial")
				wantText(t, feed.channel, "itunes:explicit", "false")
				wantText(t, feed.channel, "itunes:block", "yes")
				wantText(t, feed.channel, "googleplay:block", "yes")
				image := child(t, feed.channel, "image")
				wantShape(t, image, "url", "title", "link")
				wantText(t, image, "url", imageURL)
				wantText(t, image, "title", "Single File Novel")
				wantText(t, image, "link", e.Base+siteURL)
				self := child(t, feed.channel, "atom:link")
				wantAttr(t, self, "href", e.Base+"/feed/trial-feed-novel")
				wantAttr(t, self, "rel", "self")
				wantAttr(t, self, "type", "application/rss+xml")
				wantAttr(t, child(t, feed.channel, "itunes:image"), "href", imageURL)
				owner := child(t, feed.channel, "itunes:owner")
				wantShape(t, owner, "itunes:name", "itunes:email")
				wantText(t, owner, "itunes:name", "Trial Owner")
				wantText(t, owner, "itunes:email", "owner@example.com")
				feed.wantLastBuildDate(t)

				feed.wantItemOrder(t, "Single File Novel")
				item := feed.item(t, "Single File Novel")
				wantShape(t, item, "title", "link", "guid", "dc:creator", "pubDate", "enclosure",
					"itunes:author", "itunes:duration", "itunes:explicit")
				wantText(t, item, "link", e.Base+siteURL)
				wantText(t, item, "dc:creator", "Ada Example")
				wantText(t, item, "pubDate", novelPubDate)
				wantText(t, item, "itunes:author", "Ada Example")
				wantText(t, item, "itunes:duration", "45")
				wantText(t, item, "itunes:explicit", "false")
				enclosureURL := e.Base + "/feed/trial-feed-novel/item/" + novelEpisodeID + "/media.m4b"
				wantEnclosure(t, item, enclosureURL, 193740, "audio/mp4")
				wantGUID(t, item, enclosureURL)
				absent(t, item, "description")
				absent(t, item, "itunes:summary")
				absent(t, item, "itunes:season")
			},
		},
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-002", Case: "normal", State: "initial", Assertion: "feed-xml-multi-track-pubdates",
			Sources: []string{"server/models/FeedEpisode.js:141-179,189-206", "server/models/Feed.js:612-631"},
			Claim:   "The three-track item feed lists Part 1..3 in track order with pubDates one minute apart from the library item createdAt in GMT, each item's GUID equal to its absolute enclosure URL and isPermaLink false, and identical 80558-byte audio/mpeg enclosures.",
			Run: func(t *testing.T, e *harness.Env) {
				feed := mustFeedXML(t, e, saga1FeedSlug)
				wantText(t, feed.channel, "title", "The Beginning")
				wantText(t, feed.channel, "itunes:type", "serial")
				feed.wantItemOrder(t, "Part 1", "Part 2", "Part 3")
				want := []struct {
					title, date, id string
				}{
					{"Part 1", saga1PubDate1, "cadcc786-2cd2-433d-82c6-8855cee49d92"},
					{"Part 2", saga1PubDate2, "f302d177-9039-487a-b32f-52b895649e57"},
					{"Part 3", saga1PubDate3, "220fc396-2fa2-4c0d-9017-9eda2aae1ddc"},
				}
				for _, expected := range want {
					item := feed.item(t, expected.title)
					wantText(t, item, "pubDate", expected.date)
					enclosureURL := e.Base + "/feed/trial-feed-saga1/item/" + expected.id + "/media.mp3"
					wantEnclosure(t, item, enclosureURL, 80558, "audio/mpeg")
					wantGUID(t, item, enclosureURL)
					wantText(t, item, "itunes:duration", "20")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-002", Case: "edge", State: "initial", Assertion: "feed-xml-podcast-episodic-shape",
			Sources: []string{"server/models/FeedEpisode.js:58-110", "server/models/Feed.js:575-632"},
			Claim:   "The podcast item feed declares itunes:type episodic, orders episodes newest first, and emits description, itunes:season/episode and itunes:summary per episode (no itunes:episodeType when null), with the episode date parsed from YYYY-MM-DD to a GMT pubDate; its itunes:owner has a name but no email.",
			Run: func(t *testing.T, e *harness.Env) {
				feed := mustFeedXML(t, e, podcastFeedSlug)
				wantText(t, feed.channel, "title", "Fixture Cast")
				wantText(t, feed.channel, "itunes:type", "episodic")
				wantText(t, feed.channel, "author", "Fixture Host")
				wantShape(t, child(t, feed.channel, "itunes:owner"), "itunes:name")
				wantText(t, child(t, feed.channel, "itunes:owner"), "itunes:name", "Fixture Host")
				feed.wantItemOrder(t, "Third Time", "Second Outing", "Pilot Episode")
				want := []struct {
					title, description, date, episode, size, id string
				}{
					{"Third Time", "Episode three closes the season.", podcastDate1, "3", "48703", "9ab2d898-1f41-4d8f-ac81-08ba5ac378d7"},
					{"Second Outing", "A <b>second</b> episode.", podcastDate2, "2", "72731", "c435c03a-1eaf-40e1-b595-5c8c660f5312"},
					{"Pilot Episode", "The first episode of the fixture cast.", podcastDate3, "1", "96673", "45479f6e-0f80-4e43-878f-161a58421a98"},
				}
				for _, expected := range want {
					item := feed.item(t, expected.title)
					wantShape(t, item, "title", "description", "link", "guid", "dc:creator", "pubDate",
						"enclosure", "itunes:author", "itunes:duration", "itunes:explicit",
						"itunes:season", "itunes:episode", "itunes:summary")
					wantText(t, item, "description", expected.description)
					wantText(t, item, "itunes:summary", expected.description)
					wantText(t, item, "pubDate", expected.date)
					wantText(t, item, "itunes:season", "1")
					wantText(t, item, "itunes:episode", expected.episode)
					size, err := strconv.ParseInt(expected.size, 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					enclosureURL := e.Base + "/feed/fixture-cast-feed/item/" + expected.id + "/media.mp3"
					wantEnclosure(t, item, enclosureURL, size, "audio/mpeg")
					wantGUID(t, item, enclosureURL)
					absent(t, item, "itunes:episodeType")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-003", Case: "edge", State: "repeat", Assertion: "feed-xml-repeat-is-stable",
			Sources: []string{"server/managers/RssFeedManager.js:88-157,180-188", "server/libs/rss/index.js:46"},
			Claim:   "Reading the same unchanged feed twice returns the same XML except the generated lastBuildDate, with identical item GUIDs and no feed or episode row written.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				first := feedBody(t, e, saga1FeedSlug)
				second := feedBody(t, e, saga1FeedSlug)
				wantStatus(t, first, http.StatusOK, "first saga1 read")
				wantStatus(t, second, http.StatusOK, "second saga1 read")
				strip := func(body []byte) string {
					return lastBuildDatePattern.ReplaceAllString(string(body), "<lastBuildDate/>")
				}
				if strip(first.Body) != strip(second.Body) {
					t.Fatalf("repeated feed reads differ beyond lastBuildDate:\n%s\n%s", first.Body, second.Body)
				}
				if !bytes.Contains(second.Body, []byte("<guid isPermaLink=\"false\">")) {
					t.Fatalf("repeated read lost GUIDs: %.300s", second.Body)
				}
				wantSameCounts(t, e, before, "repeated feed read")
			},
		},
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-003", Case: "error", State: "initial", Assertion: "feed-unknown-slug-is-404",
			Sources: []string{"server/managers/RssFeedManager.js:165-178"},
			Claim:   "GET /feed/:slug answers 404 for a slug that no feed uses and for an empty slug, and serves no XML document.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, slug := range []string{"no-such-feed", "trial-feed-novel-extra", "TRIAL-FEED-NOVEL"} {
					resp := feedBody(t, e, slug)
					wantStatus(t, resp, http.StatusNotFound, "feed "+slug)
					if bytes.Contains(resp.Body, []byte("<rss")) {
						t.Fatalf("unknown feed %s returned a document: %.200s", slug, resp.Body)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R007-CONTRACT-003", Case: "error", State: "failure", Assertion: "feed-rejected-read-changes-nothing",
			Sources: []string{"server/managers/RssFeedManager.js:165-189"},
			Claim:   "A rejected feed read leaves the feed and episode rows and the episode media bytes exactly as they were.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0]))
				wantStatus(t, feedBody(t, e, "no-such-feed"), http.StatusNotFound, "rejected read")
				wantSameCounts(t, e, before, "rejected feed read")
				if after := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0])); !bytes.Equal(file, after) {
					t.Fatal("rejected feed read changed the episode media file")
				}
			},
		},
	}
}
