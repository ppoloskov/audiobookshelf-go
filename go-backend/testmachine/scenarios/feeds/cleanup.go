package feeds

import (
	"fmt"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	emptySeriesSlug = "feeds-empty-series"
	orphanSlug      = "feeds-orphan"
)

// emptySeriesScenario proves the emptied-series call site closes its feed and
// that deleting an item without a feed leaves the feed tables untouched.
// It runs last among the stateful scenarios: it deletes fixture items.
func emptySeriesScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r191", Invariant: "INV-R191-CONTRACT-001", Case: "edge", State: "changed", Assertion: "close-feed-on-empty-series-and-feedless-item-delete",
		Sources: []string{"server/routers/ApiRouter.js:445-459", "server/managers/RssFeedManager.js:355-393",
			"server/controllers/LibraryItemController.js:228-249"},
		Claim: "Hard-deleting a library item that is the only book of a series with an open feed removes the series, closes that feed (row, episodes and slug) and deletes the item, while deleting an item that has no feed also succeeds and leaves the other feed rows intact.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.novel+"/media", map[string]any{
				"metadata": map[string]any{"series": []map[string]string{{"name": "Feeds Empty Saga", "sequence": "1"}}},
			})
			wantStatus(t, patch, http.StatusOK, "attach an empty-able series to the novel")
			rows := e.Rows(t, "SELECT id FROM series WHERE name = 'Feeds Empty Saga'")
			if len(rows) != 1 {
				t.Fatalf("Feeds Empty Saga rows = %d", len(rows))
			}
			seriesID, _ := rows[0]["id"].(string)
			feed := mustOpenFeed(t, e, "series", seriesID, emptySeriesSlug, nil)

			wantStatus(t, e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/items/" + fx.novel, Query: map[string][]string{"hard": {"1"}}, Token: e.Token(t, "root")}), http.StatusOK, "hard delete the novel")

			noFeedRow(t, e, emptySeriesSlug, feed.ID)
			if remaining := feedEpisodeRows(t, e, feed.ID); len(remaining) != 0 {
				t.Fatalf("emptied-series close left episodes: %v", remaining)
			}
			if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", seriesID); count != 0 {
				t.Fatalf("emptied series still has %d rows", count)
			}
			wantStatus(t, feedBody(t, e, emptySeriesSlug), http.StatusNotFound, "emptied series feed")
			if count := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", fx.novel); count != 0 {
				t.Fatalf("deleted novel still has %d rows", count)
			}

			wantStatus(t, e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/items/" + fx.ebook, Query: map[string][]string{"hard": {"1"}}, Token: e.Token(t, "root")}), http.StatusOK, "delete the feedless ebook item")
			if count := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", fx.ebook); count != 0 {
				t.Fatalf("ebook item still has %d rows", count)
			}
		},
	}
}

// orphanBootScenario proves RssFeedManager.init removes a feed whose entity row
// no longer exists, and serves no XML for its slug afterwards.
func orphanBootScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r007", Invariant: "INV-R007-CONTRACT-003", Case: "edge", State: "changed", Assertion: "feed-removed-at-boot-when-entity-missing",
		Sources: []string{"server/managers/RssFeedManager.js:17-52", "server/controllers/RSSFeedController.js:25-33"},
		Claim:   "A persisted feed whose library item row disappeared while the server was stopped is removed at the next boot: it is absent from GET /api/feeds, its slug answers 404, and every other feed row survives.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			feed := mustOpenFeed(t, e, "item", fx.saga2, orphanSlug, nil)
			before := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
			snapshot := map[string]string{}
			for _, row := range e.Rows(t, "SELECT id, slug, entityType, entityId FROM feeds") {
				id, _ := row["id"].(string)
				snapshot[id] = fmt.Sprint(row["slug"], " ", row["entityType"], " ", row["entityId"])
			}
			// Feeds of the entity deleted below are orphaned by the restart, and so
			// are feeds whose entity row is already gone (item deletion closes only
			// one feed per entity, so earlier deletes can leave rows behind).
			orphaned := map[string]bool{feed.ID: true}
			for _, row := range e.Rows(t, "SELECT id FROM feeds WHERE entityId = ?", fx.saga2) {
				id, _ := row["id"].(string)
				orphaned[id] = true
			}
			for _, query := range []string{
				"SELECT f.id FROM feeds f LEFT JOIN libraryItems i ON i.id = f.entityId WHERE f.entityType = 'libraryItem' AND i.id IS NULL",
				"SELECT f.id FROM feeds f LEFT JOIN collections c ON c.id = f.entityId WHERE f.entityType = 'collection' AND c.id IS NULL",
				"SELECT f.id FROM feeds f LEFT JOIN series s ON s.id = f.entityId WHERE f.entityType = 'series' AND s.id IS NULL",
			} {
				for _, row := range e.Rows(t, query) {
					id, _ := row["id"].(string)
					orphaned[id] = true
				}
			}
			if len(orphaned) < 2 {
				t.Fatalf("expected the opened feed to be orphaned, got %v", orphaned)
			}
			e.Stop(t)
			e.Exec(t, "DELETE FROM libraryItems WHERE id = ?", fx.saga2)
			e.Restart(t)

			after := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
			for id := range orphaned {
				if hasString(after, id) {
					t.Fatalf("orphaned feed %s survived the restart", id)
				}
			}
			for _, id := range before {
				if orphaned[id] {
					continue
				}
				if !hasString(after, id) {
					t.Fatalf("unrelated feed %s (%s) was removed at boot; orphaned set %v", id, snapshot[id], orphaned)
				}
			}
			wantStatus(t, feedBody(t, e, orphanSlug), http.StatusNotFound, "orphaned feed slug")
			wantStatus(t, feedBody(t, e, defaultsSlug), http.StatusOK, "unrelated item feed")
		},
	}
}
