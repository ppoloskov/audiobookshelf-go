package feeds

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// coverlessSlug is the item feed opened for "The Middle" (no cover).
	coverlessSlug = "feeds-middle"
	// defaultsSlug and badOwnerSlug are opened with absent/invalid metadata.
	defaultsSlug = "feeds-defaults"
	badOwnerSlug = "feeds-bad-owner"
)

// openItemScenarios covers route-r188: POST /api/feeds/item/:itemId/open.
func openItemScenarios() []harness.Scenario {
	const req = "route-r188"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: "INV-R188-CONTRACT-001", Case: "normal", State: "initial", Assertion: "open-item-feed-persists-and-serves",
			Sources: []string{
				"server/controllers/RSSFeedController.js:41-88",
				"server/managers/RssFeedManager.js:272-305",
				"server/models/Feed.js:112-189",
				"server/models/FeedEpisode.js:141-206",
			},
			Claim: "Opening a feed for the single-file book item answers 200 with the minified feed, persists one feeds row owned by the caller (imageURL /Logo.png because the item has no cover, serial type, explicit/preventIndexing flags from the request) and one episode row per audio track with its pubDate/enclosure/duration, and the unauthenticated XML serves that item with the requested owner and no indexing block.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				details := map[string]any{"preventIndexing": false, "ownerName": "Feed Owner", "ownerEmail": "feed@example.test"}
				feed := mustOpenFeed(t, e, "item", fx.saga2, coverlessSlug, details)
				if feed.EntityType != "libraryItem" || feed.EntityID != fx.saga2 || feed.FeedURL != "/feed/"+coverlessSlug {
					t.Fatalf("open response identity = %+v", feed)
				}
				if feed.Meta.Title == nil || *feed.Meta.Title != "The Middle" {
					t.Fatalf("open response title = %v", feed.Meta.Title)
				}
				if feed.Meta.Description == nil || *feed.Meta.Description != "The second book of the saga, narrated slowly." {
					t.Fatalf("open response description = %v", feed.Meta.Description)
				}
				if feed.Meta.PreventIndexing == nil || *feed.Meta.PreventIndexing {
					t.Fatalf("open response preventIndexing = %v, want false", feed.Meta.PreventIndexing)
				}
				if feed.Meta.OwnerName == nil || *feed.Meta.OwnerName != "Feed Owner" || feed.Meta.OwnerEmail == nil || *feed.Meta.OwnerEmail != "feed@example.test" {
					t.Fatalf("open response owner = %v/%v", feed.Meta.OwnerName, feed.Meta.OwnerEmail)
				}

				if after := snapshotCounts(t, e); after.feeds != before.feeds+1 || after.episodes != before.episodes+1 {
					t.Fatalf("open persisted counts %+v, want feeds+1 episodes+1 from %+v", after, before)
				}
				row := feedRowBySlug(t, e, coverlessSlug)
				if row["id"] != feed.ID {
					t.Fatalf("row id %v differs from response %s", row["id"], feed.ID)
				}
				column(t, row, "entityType", "libraryItem")
				column(t, row, "entityId", fx.saga2)
				column(t, row, "entityUpdatedAt", "2026-11-03 12:46:25.966 +00:00")
				column(t, row, "serverAddress", serverAddress)
				column(t, row, "feedURL", "/feed/"+coverlessSlug)
				column(t, row, "imageURL", "/Logo.png")
				column(t, row, "siteURL", "/item/"+fx.saga2)
				column(t, row, "title", "The Middle")
				column(t, row, "description", "The second book of the saga, narrated slowly.")
				column(t, row, "author", "Terry Fixture")
				column(t, row, "podcastType", "serial")
				column(t, row, "language", nil)
				column(t, row, "ownerName", "Feed Owner")
				column(t, row, "ownerEmail", "feed@example.test")
				column(t, row, "explicit", 0)
				column(t, row, "preventIndexing", 0)
				column(t, row, "coverPath", nil)
				column(t, row, "userId", rootUserID(t, e))

				episodes := feedEpisodeRows(t, e, feed.ID)
				if len(episodes) != 1 {
					t.Fatalf("persisted episodes = %d, want 1", len(episodes))
				}
				episodeID, _ := episodes[0]["id"].(string)
				if !uuidV4.MatchString(episodeID) {
					t.Fatalf("episode id %q is not a v4 uuid", episodeID)
				}
				column(t, episodes[0], "title", "The Middle")
				column(t, episodes[0], "pubDate", "Tue, 03 Nov 2026 12:46:25 GMT")
				column(t, episodes[0], "enclosureURL", "/feed/"+coverlessSlug+"/item/"+episodeID+"/media.flac")
				column(t, episodes[0], "enclosureType", "audio/flac")
				column(t, episodes[0], "enclosureSize", 218599)
				column(t, episodes[0], "duration", 30)
				column(t, episodes[0], "author", "Terry Fixture")
				column(t, episodes[0], "siteURL", "/item/"+fx.saga2)
				column(t, episodes[0], "explicit", 0)
				column(t, episodes[0], "season", nil)
				file := episodeFile(t, episodes[0])
				if !strings.HasSuffix(file, "/Book 2 - The Middle/The Middle.flac") {
					t.Fatalf("episode filePath = %q", file)
				}

				feedXML := mustFeedXML(t, e, coverlessSlug)
				wantText(t, feedXML.channel, "title", "The Middle")
				wantText(t, feedXML.channel, "description", "The second book of the saga, narrated slowly.")
				wantText(t, feedXML.channel, "link", e.Base+"/item/"+fx.saga2)
				wantText(t, feedXML.channel, "itunes:type", "serial")
				wantText(t, feedXML.channel, "itunes:summary", "The second book of the saga, narrated slowly.")
				absent(t, feedXML.channel, "itunes:block")
				absent(t, feedXML.channel, "googleplay:block")
				wantText(t, child(t, feedXML.channel, "image"), "url", e.Base+"/Logo.png")
				owner := child(t, feedXML.channel, "itunes:owner")
				wantText(t, owner, "itunes:name", "Feed Owner")
				wantText(t, owner, "itunes:email", "feed@example.test")
				feedXML.wantItemOrder(t, "The Middle")
				item := feedXML.item(t, "The Middle")
				wantText(t, item, "pubDate", "Tue, 03 Nov 2026 12:46:25 GMT")
				wantText(t, item, "description", "The second book of the saga, narrated slowly.")
				wantText(t, item, "itunes:duration", "30")
				enclosureURL := e.Base + "/feed/" + coverlessSlug + "/item/" + episodeID + "/media.flac"
				wantEnclosure(t, item, enclosureURL, 218599, "audio/flac")
				wantGUID(t, item, enclosureURL)
				// The feed reader can fetch the exact file the XML advertises.
				audio := e.Do(t, harness.Request{Path: relativeToBase(t, e, enclosureURL)})
				wantStatus(t, audio, http.StatusOK, "opened feed enclosure")
				if !bytes.Equal(audio.Body, readMediaFile(t, file)) {
					t.Fatal("opened feed enclosure body differs from the media file")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R188-CONTRACT-002", Case: "error", State: "initial", Assertion: "open-item-feed-validates-entity-access-and-body",
			Sources: []string{
				"server/controllers/RSSFeedController.js:41-88,194-204",
				"server/models/User.js:646-683",
			},
			Claim: "The item open route answers 404 for an unknown item even when the body is missing, 400 for a missing/invalid body or an item without audio tracks after the item and access checks, 403 for a non-admin and for a restricted admin denied the item's library, and persists nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				missingID := "00000000-0000-4000-8000-000000000000"
				valid := openBody("feeds-reject-a", nil)
				cases := []struct {
					name, token, entityID string
					body                  any
					want                  int
				}{
					{"unknown item, no body", e.Token(t, "root"), missingID, nil, http.StatusNotFound},
					{"unknown item, valid body", e.Token(t, "root"), missingID, valid, http.StatusNotFound},
					{"item without audio tracks", e.Token(t, "root"), fx.ebook, openBody("feeds-reject-b", nil), http.StatusBadRequest},
					{"missing body", e.Token(t, "root"), fx.novel, nil, http.StatusBadRequest},
					{"slug is not a string", e.Token(t, "root"), fx.novel, map[string]any{"serverAddress": serverAddress, "slug": 7}, http.StatusBadRequest},
					{"no serverAddress", e.Token(t, "root"), fx.novel, map[string]any{"slug": "feeds-reject-c"}, http.StatusBadRequest},
					{"non-admin", e.Token(t, "listener"), fx.novel, openBody("feeds-reject-d", nil), http.StatusForbidden},
				}
				for _, test := range cases {
					resp := e.Post(t, test.token, openPath("item", test.entityID), test.body)
					wantStatus(t, resp, test.want, test.name)
				}

				restricted := createRestrictedAdmin(t, e)
				resp := e.Post(t, restricted, openPath("item", fx.novel), openBody("feeds-reject-e", nil))
				wantStatus(t, resp, http.StatusForbidden, "restricted admin opens a book item feed")
				for _, slug := range []string{"feeds-reject-a", "feeds-reject-b", "feeds-reject-c", "feeds-reject-d", "feeds-reject-e"} {
					if rows := e.Rows(t, "SELECT id FROM feeds WHERE slug = ?", slug); len(rows) != 0 {
						t.Fatalf("rejected open persisted feed %s", slug)
					}
				}
				wantSameCounts(t, e, before, "rejected item opens")
			},
		},
		{
			Requirement: req, Invariant: "INV-R188-CONTRACT-001", Case: "edge", State: "initial", Assertion: "open-item-feed-defaults-and-invalid-metadata",
			Sources: []string{"server/managers/RssFeedManager.js:272-284", "server/controllers/RSSFeedController.js:41-56"},
			Claim:   "A request without metadataDetails persists preventIndexing true with NULL owner fields, while non-string owner values are ignored (NULL) and the same defaults apply; the open response reports the stored defaults.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				plain := mustOpenFeed(t, e, "item", fx.saga1, defaultsSlug, nil)
				if plain.Meta.PreventIndexing == nil || !*plain.Meta.PreventIndexing || plain.Meta.OwnerName != nil || plain.Meta.OwnerEmail != nil {
					t.Fatalf("absent metadataDetails meta = %+v", plain.Meta)
				}
				row := feedRowBySlug(t, e, defaultsSlug)
				column(t, row, "preventIndexing", 1)
				column(t, row, "ownerName", nil)
				column(t, row, "ownerEmail", nil)

				bad := mustOpenFeed(t, e, "item", fx.saga1, badOwnerSlug, map[string]any{"preventIndexing": true, "ownerName": 42, "ownerEmail": []string{"x@example.test"}})
				if bad.Meta.OwnerName != nil || bad.Meta.OwnerEmail != nil {
					t.Fatalf("non-string owner values were accepted: %+v", bad.Meta)
				}
				row = feedRowBySlug(t, e, badOwnerSlug)
				column(t, row, "ownerName", nil)
				column(t, row, "ownerEmail", nil)
				column(t, row, "preventIndexing", 1)
			},
		},
		{
			Requirement: req, Invariant: "INV-R188-CONTRACT-002", Case: "edge", State: "repeat", Assertion: "open-item-feed-duplicate-slug-rejected",
			Sources: []string{"server/controllers/RSSFeedController.js:57-61", "server/managers/RssFeedManager.js:72-80"},
			Claim:   "Repeating the open with an already used slug answers 400 and leaves exactly one feed and its episodes in place, so the duplicate is rejected after the item/access checks and before any write.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				existing := feedRowBySlug(t, e, coverlessSlug)
				existingID, _ := existing["id"].(string)
				episodes := len(feedEpisodeRows(t, e, existingID))
				before := snapshotCounts(t, e)
				resp := openFeed(t, e, "item", fx.saga2, coverlessSlug, map[string]any{})
				wantStatus(t, resp, http.StatusBadRequest, "duplicate slug")
				wantSameCounts(t, e, before, "duplicate slug open")
				row := feedRowBySlug(t, e, coverlessSlug)
				if row["id"] != existingID || len(feedEpisodeRows(t, e, existingID)) != episodes {
					t.Fatalf("duplicate open changed the existing feed: %v", row)
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R188-CONTRACT-002", Case: "error", State: "failure", Assertion: "open-item-feed-rejection-is-silent",
			Sources: []string{"server/models/Feed.js:165-189", "server/controllers/RSSFeedController.js:41-88"},
			Claim:   "Failed item opens (unknown item, no audio, duplicate slug) write no feed or episode row and never expose the requested slug in the XML.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "item", "00000000-0000-4000-8000-000000000000", "feeds-fail-a", nil), http.StatusNotFound, "unknown item")
				wantStatus(t, openFeed(t, e, "item", fx.ebook, "feeds-fail-b", nil), http.StatusBadRequest, "no audio tracks")
				wantStatus(t, openFeed(t, e, "item", fx.saga2, coverlessSlug, nil), http.StatusBadRequest, "duplicate slug")
				wantSameCounts(t, e, before, "failed item opens")
				for _, slug := range []string{"feeds-fail-a", "feeds-fail-b"} {
					wantStatus(t, feedBody(t, e, slug), http.StatusNotFound, "failed open feed "+slug)
				}
			},
		},
	}
}

// createRestrictedAdmin creates an admin user who can see only the podcast
// library and returns its access token (the create response carries it).
func createRestrictedAdmin(t *testing.T, e *harness.Env) string {
	t.Helper()
	body := map[string]any{
		"username": "feeds-restricted-admin",
		"password": "restricted-password",
		"type":     "admin",
		"isActive": true,
		"permissions": map[string]any{
			"accessAllLibraries":  false,
			"librariesAccessible": []string{e.IDs["podcastLibrary"]},
		},
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, resp, http.StatusOK, "create restricted admin")
	user, _ := resp.Object(t)["user"].(map[string]any)
	token, _ := user["token"].(string)
	if token == "" {
		t.Fatalf("restricted admin response has no token: %.300s", resp.Body)
	}
	return token
}

// listEdgeScenario asserts the list reflects a feed opened after the fixture
// snapshot; it runs after the open groups.
func listEdgeScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r187", Invariant: "INV-R187-CONTRACT-001", Case: "edge", State: "stateless", Assertion: "list-reflects-opened-feeds",
		Sources: []string{"server/controllers/RSSFeedController.js:25-33", "server/managers/RssFeedManager.js:399-405"},
		Claim:   "After feeds are opened, GET /api/feeds lists each one with the same id/entity/feedUrl as the open response, its full meta (including owner values and preventIndexing) and the persisted episodes, and both arrays stay index-aligned.",
		Run: func(t *testing.T, e *harness.Env) {
			list := listFeeds(t, e, e.Token(t, "root"))
			ids := feedIDs(t, list)
			for _, slug := range []string{coverlessSlug, defaultsSlug, badOwnerSlug} {
				row := feedRowBySlug(t, e, slug)
				id, _ := row["id"].(string)
				if !hasString(ids, id) {
					t.Fatalf("opened feed %s is not listed: %v", slug, ids)
				}
				feed := feedByID(t, list, id)
				if feed.Slug != slug || feed.FeedURL != "/feed/"+slug || feed.ServerAddress != serverAddress {
					t.Fatalf("listed feed %s = %+v", slug, feed)
				}
				episodes := feedEpisodeRows(t, e, id)
				if len(feed.Episodes) != len(episodes) {
					t.Fatalf("listed episodes for %s = %d, want %d", slug, len(feed.Episodes), len(episodes))
				}
				wantMinified(t, minifiedByID(t, list, id), feed)
			}
			feed := feedByID(t, list, feedRowID(t, e, coverlessSlug))
			if feed.Meta.PreventIndexing == nil || *feed.Meta.PreventIndexing || feed.Meta.OwnerName == nil || *feed.Meta.OwnerName != "Feed Owner" || feed.CoverPath != nil {
				t.Fatalf("coverless feed list meta = %+v cover=%v", feed.Meta, feed.CoverPath)
			}
			if feed.Meta.ImageURL == nil || *feed.Meta.ImageURL != "/Logo.png" {
				t.Fatalf("coverless feed imageUrl = %v", feed.Meta.ImageURL)
			}
			defaults := feedByID(t, list, feedRowID(t, e, defaultsSlug))
			if defaults.Meta.PreventIndexing == nil || !*defaults.Meta.PreventIndexing || defaults.Meta.OwnerName != nil {
				t.Fatalf("defaults feed list meta = %+v", defaults.Meta)
			}
		},
	}
}

// feedRowID returns the id column of the feed with the slug.
func feedRowID(t *testing.T, e *harness.Env, slug string) string {
	t.Helper()
	id, _ := feedRowBySlug(t, e, slug)["id"].(string)
	if !uuidV4.MatchString(id) {
		t.Fatalf("feed %s has no v4 id: %q", slug, id)
	}
	return id
}
