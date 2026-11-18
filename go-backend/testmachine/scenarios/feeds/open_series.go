package feeds

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	sagaFeedSlug  = "feeds-saga"
	seriesSilent  = "feeds-silent-saga"
	seriesRenamed = "The Middle Revisited"
)

// openSeriesScenarios covers route-r190: POST /api/feeds/series/:seriesId/open.
func openSeriesScenarios() []harness.Scenario {
	const req = "route-r190"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: "INV-R190-CONTRACT-001", Case: "normal", State: "initial", Assertion: "open-series-feed-persists-ordered-books",
			Sources: []string{
				"server/controllers/RSSFeedController.js:133-174",
				"server/models/Feed.js:291-368",
				"server/models/Series.js:144-166",
				"server/models/FeedEpisode.js:217-244",
			},
			Claim: "Opening a feed for the two-book series answers 200 with the minified series feed and persists one row whose siteURL is /library/:libraryId/series/:seriesId, image cache buster is the newest book timestamp, author is the deduplicated author list, with four episodes in bookSeries sequence order (Book 1's three tracks then Book 2's single track) offset one minute from the earliest added item, and the XML lists them in that order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "series", fx.series, sagaFeedSlug, nil)
				if feed.EntityType != "series" || feed.EntityID != fx.series {
					t.Fatalf("open series identity = %+v", feed)
				}
				if feed.Meta.Title == nil || *feed.Meta.Title != "Long Saga" || feed.Meta.Description == nil || *feed.Meta.Description != "" {
					t.Fatalf("open series meta = %+v", feed.Meta)
				}
				if feed.Meta.PreventIndexing == nil || !*feed.Meta.PreventIndexing || feed.Meta.OwnerName != nil || feed.Meta.OwnerEmail != nil {
					t.Fatalf("open series defaults = %+v", feed.Meta)
				}

				row := feedRowBySlug(t, e, sagaFeedSlug)
				column(t, row, "entityType", "series")
				column(t, row, "entityId", fx.series)
				column(t, row, "entityUpdatedAt", "2026-11-03 12:46:26.209 +00:00")
				column(t, row, "imageURL", "/feed/"+sagaFeedSlug+"/cover.jpg?ts=1793709986209")
				column(t, row, "siteURL", "/library/"+fx.library+"/series/"+fx.series)
				column(t, row, "title", "Long Saga")
				column(t, row, "description", "")
				column(t, row, "author", "Terry Fixture")
				column(t, row, "podcastType", "serial")
				column(t, row, "language", nil)
				column(t, row, "ownerName", nil)
				column(t, row, "ownerEmail", nil)
				column(t, row, "explicit", 0)
				column(t, row, "preventIndexing", 1)
				if cover, _ := row["coverPath"].(string); !strings.HasSuffix(cover, "/Book 1 - The Beginning/cover.jpg") {
					t.Fatalf("series feed coverPath = %q, want the first book with a cover", cover)
				}
				column(t, row, "userId", rootUserID(t, e))

				episodes := feedEpisodeRows(t, e, feed.ID)
				want := []struct {
					title, pubDate, mime string
					size                 int64
				}{
					{"Part 1", "Tue, 03 Nov 2026 12:46:25 GMT", "audio/mpeg", 80558},
					{"Part 2", "Tue, 03 Nov 2026 12:47:25 GMT", "audio/mpeg", 80558},
					{"Part 3", "Tue, 03 Nov 2026 12:48:25 GMT", "audio/mpeg", 80558},
					{"The Middle", "Tue, 03 Nov 2026 12:49:25 GMT", "audio/flac", 218599},
				}
				if len(episodes) != len(want) {
					t.Fatalf("series episodes = %d, want %d", len(episodes), len(want))
				}
				for i, expected := range want {
					column(t, episodes[i], "title", expected.title)
					column(t, episodes[i], "pubDate", expected.pubDate)
					column(t, episodes[i], "enclosureType", expected.mime)
					column(t, episodes[i], "enclosureSize", expected.size)
					id, _ := episodes[i]["id"].(string)
					ext := ".mp3"
					if expected.mime == "audio/flac" {
						ext = ".flac"
					}
					column(t, episodes[i], "enclosureURL", "/feed/"+sagaFeedSlug+"/item/"+id+"/media"+ext)
				}

				feedXML := mustFeedXML(t, e, sagaFeedSlug)
				wantText(t, feedXML.channel, "title", "Long Saga")
				wantText(t, feedXML.channel, "description", "Long Saga")
				wantText(t, feedXML.channel, "link", e.Base+"/library/"+fx.library+"/series/"+fx.series)
				feedXML.wantItemOrder(t, "Part 1", "Part 2", "Part 3", "The Middle")
				wantText(t, feedXML.item(t, "The Middle"), "itunes:duration", "30")
			},
		},
		{
			Requirement: req, Invariant: "INV-R190-CONTRACT-002", Case: "error", State: "initial", Assertion: "open-series-feed-validation-and-no-audio",
			Sources: []string{"server/controllers/RSSFeedController.js:133-174"},
			Claim:   "The series open route validates the body before the entity (unknown series without a body answers 400, with a body 404), rejects a series whose only book has no audio tracks with 400, rejects a non-admin with 403, and persists nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				before := snapshotCounts(t, e)
				missingID := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, e.Post(t, e.Token(t, "root"), openPath("series", missingID), nil), http.StatusBadRequest, "unknown series without a body")
				wantStatus(t, e.Post(t, e.Token(t, "root"), openPath("series", missingID), openBody(seriesSilent, nil)), http.StatusNotFound, "unknown series with a body")

				// Give the ebook-only book its own series; it has no audio files.
				patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.ebook+"/media", map[string]any{
					"metadata": map[string]any{"series": []map[string]string{{"name": "Silent Saga", "sequence": "1"}}},
				})
				wantStatus(t, patch, http.StatusOK, "attach a series to the ebook book")
				rows := e.Rows(t, "SELECT id FROM series WHERE name = 'Silent Saga'")
				if len(rows) != 1 {
					t.Fatalf("Silent Saga series rows = %d", len(rows))
				}
				silentID, _ := rows[0]["id"].(string)
				wantStatus(t, openFeed(t, e, "series", silentID, seriesSilent, nil), http.StatusBadRequest, "series without audio tracks")
				wantStatus(t, e.Post(t, e.Token(t, "listener"), openPath("series", fx.series), openBody("feeds-ser-listener", nil)), http.StatusForbidden, "non-admin series open")
				for _, slug := range []string{seriesSilent, "feeds-ser-listener"} {
					if rows := e.Rows(t, "SELECT id FROM feeds WHERE slug = ?", slug); len(rows) != 0 {
						t.Fatalf("rejected series open persisted %s", slug)
					}
				}
				wantSameCounts(t, e, before, "rejected series opens")
			},
		},
		{
			Requirement: req, Invariant: "INV-R190-CONTRACT-001", Case: "edge", State: "initial", Assertion: "open-series-feed-author-and-site-shape",
			Sources: []string{"server/models/Feed.js:291-337"},
			Claim:   "The series feed derives its author from the books' distinct author names (each name once even when both books share it), uses the library/series site link rather than an item link, and omits itunes:summary because the series description is NULL.",
			Run: func(t *testing.T, e *harness.Env) {
				feed := mustFeedXML(t, e, sagaFeedSlug)
				wantText(t, feed.channel, "author", "Terry Fixture")
				wantText(t, feed.channel, "itunes:author", "Terry Fixture")
				wantText(t, feed.channel, "language", "en")
				absent(t, feed.channel, "itunes:summary")
				link := child(t, feed.channel, "link")
				if strings.Contains(link.Text, "/item/") {
					t.Fatalf("series feed link = %q, want a library series link", link.Text)
				}
				if !strings.HasSuffix(link.Text, "/series/"+seriesID) {
					t.Fatalf("series feed link = %q, want the series siteURL", link.Text)
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R190-CONTRACT-002", Case: "edge", State: "repeat", Assertion: "open-series-feed-duplicate-slug-rejected",
			Sources: []string{"server/controllers/RSSFeedController.js:137-143"},
			Claim:   "Reopening the series with its existing slug answers 400 and leaves the feed row and episodes untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				id := feedRowID(t, e, sagaFeedSlug)
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "series", seriesID, sagaFeedSlug, nil), http.StatusBadRequest, "duplicate series slug")
				wantSameCounts(t, e, before, "duplicate series open")
				if feedRowID(t, e, sagaFeedSlug) != id || len(feedEpisodeRows(t, e, id)) != 4 {
					t.Fatal("duplicate series open changed the existing feed")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R190-CONTRACT-002", Case: "error", State: "failure", Assertion: "open-series-feed-rejection-is-silent",
			Sources: []string{"server/models/Feed.js:349-368", "server/controllers/RSSFeedController.js:133-174"},
			Claim:   "Failed series opens write no feed or episode row and the requested slug never becomes readable.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "series", "00000000-0000-4000-8000-000000000000", "feeds-ser-fail", nil), http.StatusNotFound, "unknown series")
				wantStatus(t, feedBody(t, e, "feeds-ser-fail"), http.StatusNotFound, "failed series feed")
				wantSameCounts(t, e, before, "failed series opens")
			},
		},
	}
}

// seriesChangedScenario renames a series book and proves the next read refreshes
// the persisted series feed while keeping episode identity.
func seriesChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r190", Invariant: "INV-R190-CONTRACT-001", Case: "edge", State: "changed", Assertion: "open-series-feed-refresh-keeps-episode-ids",
		Sources: []string{"server/managers/RssFeedManager.js:88-157,180-188", "server/models/Feed.js:488-563"},
		Claim:   "After the second book's media title changes, the next unauthenticated read of the series feed refreshes the feed row (entityUpdatedAt and updatedAt advance) and renames exactly the affected episode while all four episode ids and enclosure URLs stay the same.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			row := feedRowBySlug(t, e, sagaFeedSlug)
			feedID, _ := row["id"].(string)
			beforeUpdated, _ := row["entityUpdatedAt"].(string)
			before := feedEpisodeRows(t, e, feedID)
			if len(before) != 4 {
				t.Fatalf("series episodes before the change = %d, want 4", len(before))
			}
			ids := make([]string, len(before))
			for i, episode := range before {
				ids[i], _ = episode["id"].(string)
			}

			patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.saga2+"/media", map[string]any{
				"metadata": map[string]any{"title": seriesRenamed},
			})
			wantStatus(t, patch, http.StatusOK, "rename the second series book")

			feed := mustFeedXML(t, e, sagaFeedSlug)
			feed.wantItemOrder(t, "Part 1", "Part 2", "Part 3", seriesRenamed)
			wantText(t, feed.item(t, seriesRenamed), "itunes:duration", "30")

			after := feedEpisodeRows(t, e, feedID)
			if len(after) != 4 {
				t.Fatalf("series episodes after the change = %d, want 4", len(after))
			}
			for i := range ids {
				got, _ := after[i]["id"].(string)
				if got != ids[i] {
					t.Fatalf("series episode %d id %q changed to %q", i, ids[i], got)
				}
			}
			column(t, after[3], "title", seriesRenamed)
			refreshed := feedRowBySlug(t, e, sagaFeedSlug)
			updated, _ := refreshed["entityUpdatedAt"].(string)
			if updated == beforeUpdated {
				t.Fatalf("feed entityUpdatedAt did not advance from %q", beforeUpdated)
			}
			if refreshed["updatedAt"] == row["updatedAt"] {
				t.Fatalf("feed updatedAt did not advance from %v", row["updatedAt"])
			}
		},
	}
}
