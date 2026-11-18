package feeds

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	authorsSlug     = "feeds-authors"
	brokenCoverSlug = "feeds-cover-broken"
)

// chapterTitleScenario proves the episode title rule: chapter titles are used
// only when every chapter start matches the track offset within one second.
func chapterTitleScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r007", Invariant: "INV-R007-CONTRACT-002", Case: "edge", State: "changed", Assertion: "feed-xml-episode-titles-follow-chapter-match",
		Sources: []string{"server/models/FeedEpisode.js:112-128,141-162", "server/models/FeedEpisode.js:189-206"},
		Claim:   "When a multi-track book's chapters start at each track offset within a second the refreshed feed uses the chapter titles, after the chapters no longer align it falls back to the audio file names, and restoring aligned chapters brings the titles back.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			base := feedRowBySlug(t, e, defaultsSlug)
			feedID, _ := base["id"].(string)
			matching := []map[string]any{
				{"title": "Alpha", "start": 0, "end": 20.062041},
				{"title": "Beta", "start": 20.062041, "end": 40.124082},
				{"title": "Gamma", "start": 40.124082, "end": 60.186123},
			}
			wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/items/"+fx.saga1+"/chapters", map[string]any{"chapters": matching}), http.StatusOK, "set matching chapters")
			feed := mustFeedXML(t, e, defaultsSlug)
			feed.wantItemOrder(t, "Alpha", "Beta", "Gamma")

			mismatched := []map[string]any{
				{"title": "Alpha", "start": 0, "end": 20},
				{"title": "Beta", "start": 30, "end": 45},
				{"title": "Gamma", "start": 50, "end": 60},
			}
			wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/items/"+fx.saga1+"/chapters", map[string]any{"chapters": mismatched}), http.StatusOK, "set mismatched chapters")
			feed = mustFeedXML(t, e, defaultsSlug)
			feed.wantItemOrder(t, "01 - Part 1", "02 - Part 2", "03 - Part 3")

			// Restore the fixture's chapter titles so later refreshes keep them.
			wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/items/"+fx.saga1+"/chapters", map[string]any{"chapters": []map[string]any{
				{"title": "Part 1", "start": 0, "end": 20.062041},
				{"title": "Part 2", "start": 20.062041, "end": 40.124082},
				{"title": "Part 3", "start": 40.124082, "end": 60.186123},
			}}), http.StatusOK, "restore matching chapters")
			feed = mustFeedXML(t, e, defaultsSlug)
			feed.wantItemOrder(t, "Part 1", "Part 2", "Part 3")
			if len(feedEpisodeRows(t, e, feedID)) != 3 {
				t.Fatalf("chapter change changed the episode count")
			}
		},
	}
}

// authorListScenario proves the joined-author rule for collections and series,
// including the "& more" suffix beyond three distinct authors.
func authorListScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r189", Invariant: "INV-R189-CONTRACT-001", Case: "edge", State: "changed", Assertion: "open-collection-feed-author-list-more-than-three",
		Sources: []string{"server/models/Feed.js:211-218,300-307,488-526"},
		Claim:   "With four distinct authors across a collection's books the feed author is the first three names joined with ', ' plus ' & more', and the series feed containing the same books reports the same joined author after its next read.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			authors := []map[string]string{{"name": "Alice One"}, {"name": "Bob Two"}, {"name": "Carol Three"}, {"name": "Dave Four"}}
			wantStatus(t, e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.saga2+"/media", map[string]any{
				"metadata": map[string]any{"authors": authors},
			}), http.StatusOK, "set four authors")
			collection := createCollection(t, e, "Feeds Many Authors", []string{fx.saga1, fx.saga2})
			feed := mustOpenFeed(t, e, "collection", collection, authorsSlug, nil)
			wantAuthor := "Terry Fixture, Alice One, Bob Two & more"
			column(t, feedRowBySlug(t, e, authorsSlug), "author", wantAuthor)
			if feed.Meta.Title == nil || *feed.Meta.Title != "Feeds Many Authors" {
				t.Fatalf("author collection feed = %+v", feed.Meta)
			}
			wantText(t, mustFeedXML(t, e, authorsSlug).channel, "author", wantAuthor)

			seriesXML := mustFeedXML(t, e, sagaFeedSlug)
			wantText(t, seriesXML.channel, "author", wantAuthor)
			column(t, feedRowBySlug(t, e, sagaFeedSlug), "author", wantAuthor)
		},
	}
}

// collectionRemovalScenario proves a book removed from a collection disappears
// from the feed on the next read while the remaining episode ids stay.
func collectionRemovalScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r189", Invariant: "INV-R189-CONTRACT-001", Case: "edge", State: "changed", Assertion: "open-collection-feed-refresh-drops-removed-book",
		Sources: []string{"server/managers/RssFeedManager.js:88-157", "server/models/Feed.js:536-552", "server/models/FeedEpisode.js:217-244"},
		Claim:   "After the single-file book is removed from the fixture collection, the next feed read refreshes it to the three remaining track episodes, drops the removed book's episode id and keeps the other ids and their enclosures.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			row := feedRowBySlug(t, e, favouritesSlug)
			feedID, _ := row["id"].(string)
			before := feedEpisodeRows(t, e, feedID)
			if len(before) != 4 {
				t.Fatalf("collection episodes before removal = %d, want 4", len(before))
			}
			removed := ""
			kept := map[string]string{}
			for _, episode := range before {
				id, _ := episode["id"].(string)
				if episode["title"] == "Single File Novel" {
					removed = id
					continue
				}
				url, _ := episode["enclosureURL"].(string)
				kept[id] = url
			}
			wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/"+fx.collection+"/book/"+fx.novel), http.StatusOK, "remove the book")
			feed := mustFeedXML(t, e, favouritesSlug)
			feed.wantItemOrder(t, "Part 1", "Part 2", "Part 3")
			after := feedEpisodeRows(t, e, feedID)
			if len(after) != 3 {
				t.Fatalf("collection episodes after removal = %d, want 3", len(after))
			}
			for _, episode := range after {
				id, _ := episode["id"].(string)
				if id == removed {
					t.Fatalf("removed book episode %s survived the refresh", removed)
				}
				url, _ := episode["enclosureURL"].(string)
				if kept[id] != url {
					t.Fatalf("kept episode %s changed enclosure %q -> %q", id, kept[id], url)
				}
			}
			refreshed := feedRowBySlug(t, e, favouritesSlug)
			if refreshed["updatedAt"] == row["updatedAt"] {
				t.Fatal("collection feed was not refreshed after the removal")
			}
		},
	}
}

// brokenCoverScenario proves a feed whose cover file disappeared answers 404
// instead of failing the request.
func brokenCoverScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r008", Invariant: "INV-R008-CONTRACT-002", Case: "error", State: "changed", Assertion: "cover-missing-file-is-404",
		Sources: []string{"server/managers/RssFeedManager.js:252-264"},
		Claim:   "A feed whose persisted coverPath file no longer exists answers 404 for the cover URL and stays listed, rather than returning a partial or 500 response.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			feed := mustOpenFeed(t, e, "item", fx.podcast, brokenCoverSlug, nil)
			cover := feedCoverPath(t, feedRowBySlug(t, e, brokenCoverSlug))
			if err := os.Remove(cover); err != nil {
				t.Fatalf("remove cover %s: %v", cover, err)
			}
			resp := e.Do(t, harness.Request{Path: "/feed/" + brokenCoverSlug + "/cover.jpg"})
			wantStatus(t, resp, http.StatusNotFound, "missing cover file")
			if strings.Contains(string(resp.Body), "\x89PNG") {
				t.Fatal("missing cover streamed image bytes")
			}
			if ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root"))); !hasString(ids, feed.ID) {
				t.Fatalf("feed %s is no longer listed", feed.ID)
			}
		},
	}
}
