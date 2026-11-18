package feeds

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	favouritesSlug = "feeds-favourites"
	mixedSlug      = "feeds-mixed"
	silentSlug     = "feeds-silent"
)

// createCollection creates a collection of the given library items and
// returns its id.
func createCollection(t *testing.T, e *harness.Env, name string, libraryItemIDs []string) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/collections", map[string]any{
		"libraryId": e.IDs["library"],
		"name":      name,
		"books":     libraryItemIDs,
	})
	wantStatus(t, resp, http.StatusOK, "create collection "+name)
	id, _ := resp.Object(t)["id"].(string)
	if !uuidV4.MatchString(id) {
		t.Fatalf("created collection %s has no v4 id: %.200s", name, resp.Body)
	}
	return id
}

// openCollectionScenarios covers route-r189: POST /api/feeds/collection/:collectionId/open.
func openCollectionScenarios() []harness.Scenario {
	const req = "route-r189"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: "INV-R189-CONTRACT-001", Case: "normal", State: "initial", Assertion: "open-collection-feed-persists-books-in-order",
			Sources: []string{
				"server/controllers/RSSFeedController.js:90-131",
				"server/models/Feed.js:201-279",
				"server/models/FeedEpisode.js:217-244",
			},
			Claim: "Opening a feed for the two-book collection answers 200 and persists one collection feed whose title/description/author are the collection's (distinct authors joined, cover and image cache buster from the first book with a cover, updatedAt timestamp) with four episodes ordered by the collection's book order, each book's tracks offset by one minute from the earliest added item, and the XML lists them with absolute enclosures.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "collection", fx.collection, favouritesSlug, map[string]any{"ownerName": "Collection Owner"})
				if feed.EntityType != "collection" || feed.EntityID != fx.collection {
					t.Fatalf("open collection identity = %+v", feed)
				}
				if feed.Meta.Title == nil || *feed.Meta.Title != "Favourites" || feed.Meta.Description == nil || *feed.Meta.Description != "" {
					t.Fatalf("open collection meta = %+v", feed.Meta)
				}
				if feed.Meta.PreventIndexing == nil || !*feed.Meta.PreventIndexing || feed.Meta.OwnerName == nil || *feed.Meta.OwnerName != "Collection Owner" || feed.Meta.OwnerEmail != nil {
					t.Fatalf("open collection options = %+v", feed.Meta)
				}

				row := feedRowBySlug(t, e, favouritesSlug)
				column(t, row, "entityType", "collection")
				column(t, row, "entityId", fx.collection)
				column(t, row, "entityUpdatedAt", "2026-11-03 12:46:27.103 +00:00")
				column(t, row, "imageURL", "/feed/"+favouritesSlug+"/cover.jpg?ts=1793709987103")
				column(t, row, "siteURL", "/collection/"+fx.collection)
				column(t, row, "title", "Favourites")
				column(t, row, "description", "")
				column(t, row, "author", "Terry Fixture, Ada Example")
				column(t, row, "podcastType", "serial")
				column(t, row, "language", nil)
				column(t, row, "ownerName", "Collection Owner")
				column(t, row, "ownerEmail", nil)
				column(t, row, "explicit", 0)
				column(t, row, "preventIndexing", 1)
				if cover, _ := row["coverPath"].(string); !strings.HasSuffix(cover, "/Book 1 - The Beginning/cover.jpg") {
					t.Fatalf("collection feed coverPath = %q, want the first book cover", cover)
				}

				episodes := feedEpisodeRows(t, e, feed.ID)
				want := []struct {
					title, pubDate, mime string
					size                 int64
					duration             string
				}{
					{"Part 1", "Tue, 03 Nov 2026 12:46:25 GMT", "audio/mpeg", 80558, "20.062041"},
					{"Part 2", "Tue, 03 Nov 2026 12:47:25 GMT", "audio/mpeg", 80558, "20.062041"},
					{"Part 3", "Tue, 03 Nov 2026 12:48:25 GMT", "audio/mpeg", 80558, "20.062041"},
					{"Single File Novel", "Tue, 03 Nov 2026 12:49:25 GMT", "audio/mp4", 193740, "45"},
				}
				if len(episodes) != len(want) {
					t.Fatalf("collection episodes = %d, want %d", len(episodes), len(want))
				}
				for i, expected := range want {
					column(t, episodes[i], "title", expected.title)
					column(t, episodes[i], "pubDate", expected.pubDate)
					column(t, episodes[i], "enclosureType", expected.mime)
					column(t, episodes[i], "enclosureSize", expected.size)
					column(t, episodes[i], "duration", expected.duration)
					column(t, episodes[i], "author", "Terry Fixture, Ada Example")
					id, _ := episodes[i]["id"].(string)
					ext := ".mp3"
					if expected.mime == "audio/mp4" {
						ext = ".m4b"
					}
					column(t, episodes[i], "enclosureURL", "/feed/"+favouritesSlug+"/item/"+id+"/media"+ext)
				}

				feedXML := mustFeedXML(t, e, favouritesSlug)
				wantText(t, feedXML.channel, "title", "Favourites")
				wantText(t, feedXML.channel, "description", "Favourites")
				wantText(t, feedXML.channel, "link", e.Base+"/collection/"+fx.collection)
				wantText(t, feedXML.channel, "author", "Terry Fixture, Ada Example")
				wantText(t, feedXML.channel, "itunes:type", "serial")
				wantText(t, feedXML.channel, "itunes:block", "yes")
				wantText(t, child(t, feedXML.channel, "image"), "url", e.Base+"/feed/"+favouritesSlug+"/cover.jpg?ts=1793709987103")
				wantShape(t, child(t, feedXML.channel, "itunes:owner"), "itunes:name")
				wantText(t, child(t, feedXML.channel, "itunes:owner"), "itunes:name", "Collection Owner")
				feedXML.wantItemOrder(t, "Part 1", "Part 2", "Part 3", "Single File Novel")
				for i, expected := range want {
					item := feedXML.item(t, expected.title)
					wantText(t, item, "pubDate", expected.pubDate)
					id, _ := episodes[i]["id"].(string)
					ext := ".mp3"
					if expected.mime == "audio/mp4" {
						ext = ".m4b"
					}
					enclosureURL := e.Base + "/feed/" + favouritesSlug + "/item/" + id + "/media" + ext
					wantEnclosure(t, item, enclosureURL, expected.size, expected.mime)
					wantGUID(t, item, enclosureURL)
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R189-CONTRACT-002", Case: "error", State: "initial", Assertion: "open-collection-feed-validation-order",
			Sources: []string{"server/controllers/RSSFeedController.js:90-131"},
			Claim:   "The collection open route validates the body before the entity (unknown collection without a body answers 400, with a body 404), rejects a collection whose books have no audio tracks with 400, rejects a non-admin with 403 and persists nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				missingID := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, e.Post(t, e.Token(t, "root"), openPath("collection", missingID), nil), http.StatusBadRequest, "unknown collection without a body")
				wantStatus(t, e.Post(t, e.Token(t, "root"), openPath("collection", missingID), openBody(silentSlug, nil)), http.StatusNotFound, "unknown collection with a body")
				silent := createCollection(t, e, "Feeds Silent", []string{fx.ebook})
				wantStatus(t, openFeed(t, e, "collection", silent, silentSlug, nil), http.StatusBadRequest, "collection without audio tracks")
				wantStatus(t, e.Post(t, e.Token(t, "listener"), openPath("collection", fx.collection), openBody("feeds-coll-listener", nil)), http.StatusForbidden, "non-admin collection open")
				for _, slug := range []string{silentSlug, "feeds-coll-listener"} {
					if rows := e.Rows(t, "SELECT id FROM feeds WHERE slug = ?", slug); len(rows) != 0 {
						t.Fatalf("rejected collection open persisted %s", slug)
					}
				}
				wantSameCounts(t, e, before, "rejected collection opens")
			},
		},
		{
			Requirement: req, Invariant: "INV-R189-CONTRACT-001", Case: "edge", State: "initial", Assertion: "open-collection-feed-filters-books-without-audio",
			Sources: []string{"server/models/Feed.js:201-248", "server/models/FeedEpisode.js:217-244"},
			Claim:   "A collection that holds an ebook-only book and a single-file audiobook yields exactly one episode (the ebook is filtered), an empty-string description for a NULL collection description, and the XML description falls back to the title.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				mixed := createCollection(t, e, "Feeds Mixed", []string{fx.ebook, fx.novel})
				feed := mustOpenFeed(t, e, "collection", mixed, mixedSlug, nil)
				if feed.Meta.Description == nil || *feed.Meta.Description != "" {
					t.Fatalf("mixed collection description = %v, want empty string", feed.Meta.Description)
				}
				episodes := feedEpisodeRows(t, e, feed.ID)
				if len(episodes) != 1 {
					t.Fatalf("mixed collection episodes = %d, want 1 (ebook filtered)", len(episodes))
				}
				column(t, episodes[0], "title", "Single File Novel")
				feedXML := mustFeedXML(t, e, mixedSlug)
				wantText(t, feedXML.channel, "title", "Feeds Mixed")
				wantText(t, feedXML.channel, "description", "Feeds Mixed")
				feedXML.wantItemOrder(t, "Single File Novel")
				absent(t, feedXML.channel, "itunes:summary")
			},
		},
		{
			Requirement: req, Invariant: "INV-R189-CONTRACT-002", Case: "edge", State: "repeat", Assertion: "open-collection-feed-duplicate-slug-rejected",
			Sources: []string{"server/controllers/RSSFeedController.js:94-100", "server/managers/RssFeedManager.js:72-80"},
			Claim:   "Reopening the mixed collection with its existing slug answers 400 and leaves that feed and its episodes untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				id := feedRowID(t, e, mixedSlug)
				before := snapshotCounts(t, e)
				mixed := feedRowBySlug(t, e, mixedSlug)["entityId"]
				resp := openFeed(t, e, "collection", mixed.(string), mixedSlug, nil)
				wantStatus(t, resp, http.StatusBadRequest, "duplicate collection slug")
				wantSameCounts(t, e, before, "duplicate collection open")
				if feedRowID(t, e, mixedSlug) != id || len(feedEpisodeRows(t, e, id)) != 1 {
					t.Fatal("duplicate collection open changed the existing feed")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R189-CONTRACT-002", Case: "error", State: "failure", Assertion: "open-collection-feed-rejection-is-silent",
			Sources: []string{"server/models/Feed.js:260-278", "server/controllers/RSSFeedController.js:90-131"},
			Claim:   "Failed collection opens (unknown collection, body missing, no audio books) write no feed or episode row and no XML becomes readable.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "collection", "00000000-0000-4000-8000-000000000000", "feeds-coll-fail", nil), http.StatusNotFound, "unknown collection")
				wantStatus(t, e.Post(t, e.Token(t, "root"), openPath("collection", fx.collection), nil), http.StatusBadRequest, "missing body")
				wantStatus(t, feedBody(t, e, "feeds-coll-fail"), http.StatusNotFound, "failed collection feed")
				wantSameCounts(t, e, before, "failed collection opens")
			},
		},
	}
}

// addBookToCollection adds one library item to a collection.
func addBookToCollection(t *testing.T, e *harness.Env, collectionID, libraryItemID string) {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/collections/"+collectionID+"/book", map[string]string{"id": libraryItemID})
	wantStatus(t, resp, http.StatusOK, "add book to collection")
}

// collectionChangedScenario asserts the collection feed refresh after a book
// was added; it runs after the series/item refresh writes.
func collectionChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r189", Invariant: "INV-R189-CONTRACT-001", Case: "edge", State: "changed", Assertion: "open-collection-feed-refresh-adds-book-episodes",
		Sources: []string{"server/managers/RssFeedManager.js:88-157", "server/models/Feed.js:488-563"},
		Claim:   "After a book with three tracks is added to the mixed collection, the next unauthenticated feed read refreshes the persisted feed: entityUpdatedAt advances, four episodes exist in collection order, the existing novel episode keeps its id and enclosure, and the three new tracks appear with one-minute pubDate offsets from the same base.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			row := feedRowBySlug(t, e, mixedSlug)
			feedID, _ := row["id"].(string)
			collectionID, _ := row["entityId"].(string)
			before := feedEpisodeRows(t, e, feedID)
			if len(before) != 1 {
				t.Fatalf("mixed feed episodes before the change = %d, want 1", len(before))
			}
			novelEpisode, _ := before[0]["id"].(string)
			beforeUpdated, _ := row["entityUpdatedAt"].(string)

			addBookToCollection(t, e, collectionID, fx.saga1)
			feed := mustFeedXML(t, e, mixedSlug)
			feed.wantItemOrder(t, "Single File Novel", "Part 1", "Part 2", "Part 3")

			after := feedEpisodeRows(t, e, feedID)
			if len(after) != 4 {
				t.Fatalf("mixed feed episodes after the change = %d, want 4", len(after))
			}
			if after[0]["id"] != novelEpisode || after[0]["enclosureURL"] != before[0]["enclosureURL"] {
				t.Fatalf("existing episode identity changed: %v -> %v", before[0], after[0])
			}
			want := []struct{ title, pubDate string }{
				{"Single File Novel", "Tue, 03 Nov 2026 12:46:25 GMT"},
				{"Part 1", "Tue, 03 Nov 2026 12:47:25 GMT"},
				{"Part 2", "Tue, 03 Nov 2026 12:48:25 GMT"},
				{"Part 3", "Tue, 03 Nov 2026 12:49:25 GMT"},
			}
			for i, expected := range want {
				column(t, after[i], "title", expected.title)
				column(t, after[i], "pubDate", expected.pubDate)
			}
			refreshedRow := feedRowBySlug(t, e, mixedSlug)
			if refreshedRow["updatedAt"] == row["updatedAt"] {
				t.Fatalf("feed updatedAt did not advance from %v after the collection changed", row["updatedAt"])
			}
			if refreshedRow["entityUpdatedAt"] != beforeUpdated {
				t.Fatalf("adding a book must not change collection entityUpdatedAt (%q -> %v)", beforeUpdated, refreshedRow["entityUpdatedAt"])
			}
			item := feed.item(t, "Part 1")
			size := int64(80558)
			episodeID, _ := after[1]["id"].(string)
			wantEnclosure(t, item, e.Base+"/feed/"+mixedSlug+"/item/"+episodeID+"/media.mp3", size, "audio/mpeg")
		},
	}
}
