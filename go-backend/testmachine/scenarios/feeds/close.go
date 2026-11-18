package feeds

import (
	"bytes"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const reopenedSlug = "feeds-middle-reopened"

// closeScenarios covers route-r191: POST /api/feeds/:id/close.
// Order: a created feed is closed, rejected closes are proven silent, then the
// fixture podcast feed is closed (and its repeat rejected), then the entity is
// reopened to prove a close can be undone.
func closeScenarios() []harness.Scenario {
	const req = "route-r191"
	const inv = "INV-R191-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "close-feed-deletes-feed-and-episodes",
			Sources: []string{
				"server/controllers/RSSFeedController.js:176-190",
				"server/managers/RssFeedManager.js:355-361",
				"server/models/Feed.js:80-88",
			},
			Claim: "Closing an open feed answers 200, deletes its feeds row and every feed episode row (the cascade), makes its public XML 404 and leaves the library item row and the episode media file untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				row := feedRowBySlug(t, e, coverlessSlug)
				feedID, _ := row["id"].(string)
				episodes := feedEpisodeRows(t, e, feedID)
				if len(episodes) != 1 {
					t.Fatalf("feed %s has %d episodes, want 1", coverlessSlug, len(episodes))
				}
				file := readMediaFile(t, episodeFile(t, episodes[0]))
				wantStatus(t, feedBody(t, e, coverlessSlug), http.StatusOK, "feed before close")

				resp := closeFeed(t, e, e.Token(t, "root"), feedID)
				wantStatus(t, resp, http.StatusOK, "close feed")

				noFeedRow(t, e, coverlessSlug, feedID)
				if remaining := feedEpisodeRows(t, e, feedID); len(remaining) != 0 {
					t.Fatalf("close left %d feed episodes: %v", len(remaining), remaining)
				}
				wantStatus(t, feedBody(t, e, coverlessSlug), http.StatusNotFound, "feed after close")
				if got := readMediaFile(t, episodeFile(t, episodes[0])); !bytes.Equal(got, file) {
					t.Fatal("close changed the episode media file")
				}
				if rows := e.Rows(t, "SELECT id FROM libraryItems WHERE id = ?", fx.saga2); len(rows) != 1 {
					t.Fatalf("close removed the library item: %v", rows)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "close-feed-denies-unknown-and-non-admin",
			Sources: []string{"server/controllers/RSSFeedController.js:176-190,194-204"},
			Claim:   "POST /api/feeds/:id/close answers 404 for an unknown feed id, 403 for a non-admin and 401 without a token, and none of the rejected calls changes feed or episode rows.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				missing := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), missing), http.StatusNotFound, "unknown feed close")
				wantStatus(t, closeFeed(t, e, e.Token(t, "listener"), saga1FeedID), http.StatusForbidden, "non-admin close")
				wantStatus(t, closeFeed(t, e, "", saga1FeedID), http.StatusUnauthorized, "anonymous close")
				wantSameCounts(t, e, before, "rejected closes")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "close-feed-rejection-changes-nothing",
			Sources: []string{"server/controllers/RSSFeedController.js:176-181"},
			Claim:   "A rejected close of an unknown id and of an existing feed by a non-admin leaves every feed row, episode row and public feed readable exactly as before.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown close")
				wantStatus(t, closeFeed(t, e, e.Token(t, "listener"), saga1FeedID), http.StatusForbidden, "non-admin close")
				wantSameCounts(t, e, before, "rejected close")
				if after := feedIDs(t, listFeeds(t, e, e.Token(t, "root"))); len(after) != len(ids) {
					t.Fatalf("rejected close changed the feed list: %v -> %v", ids, after)
				}
				wantStatus(t, feedBody(t, e, saga1FeedSlug), http.StatusOK, "saga1 feed after rejected close")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "initial", Assertion: "close-fixture-feed-cascades-podcast-episodes",
			Sources: []string{"server/managers/RssFeedManager.js:355-361", "server/models/FeedEpisode.js:279-285"},
			Claim:   "Closing the three-episode fixture podcast feed answers 200, cascades its three feed episodes, keeps the podcast library item and its media files, and the feed XML and cover become 404.",
			Run: func(t *testing.T, e *harness.Env) {
				rows := feedEpisodeRows(t, e, podcastFeedID)
				if len(rows) != 3 {
					t.Fatalf("podcast feed episodes = %d, want 3", len(rows))
				}
				files := make([][]byte, len(rows))
				for i, episode := range rows {
					files[i] = readMediaFile(t, episodeFile(t, episode))
				}
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), podcastFeedID), http.StatusOK, "close podcast feed")
				noFeedRow(t, e, podcastFeedSlug, podcastFeedID)
				if remaining := feedEpisodeRows(t, e, podcastFeedID); len(remaining) != 0 {
					t.Fatalf("podcast close left episodes: %v", remaining)
				}
				wantStatus(t, feedBody(t, e, podcastFeedSlug), http.StatusNotFound, "podcast feed after close")
				wantStatus(t, e.Do(t, harness.Request{Path: "/feed/" + podcastFeedSlug + "/cover.jpg"}), http.StatusNotFound, "podcast cover after close")
				if count := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", e.IDs["podcast"]); count != 1 {
					t.Fatalf("podcast library item count = %d", count)
				}
				for i, episode := range rows {
					if got := readMediaFile(t, episodeFile(t, episode)); !bytes.Equal(got, files[i]) {
						t.Fatalf("podcast episode %v media changed", episode["id"])
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "close-feed-twice-is-404",
			Sources: []string{"server/controllers/RSSFeedController.js:176-183"},
			Claim:   "Closing an already closed feed answers 404 and changes nothing, so the close is not idempotent but also not destructive.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), podcastFeedID), http.StatusNotFound, "second close")
				wantSameCounts(t, e, before, "second close")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "changed", Assertion: "close-feed-allows-reopening-the-entity",
			Sources: []string{"server/controllers/RSSFeedController.js:41-88,176-190", "server/managers/RssFeedManager.js:59-65"},
			Claim:   "After its feed was closed, opening the same library item again with a new slug succeeds with a new feed id, the list no longer contains the closed id, and the new feed serves the item's current episodes.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				first := mustOpenFeed(t, e, "item", fx.saga1, reopenedSlug, nil)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), first.ID), http.StatusOK, "close the first feed")
				if ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root"))); hasString(ids, first.ID) {
					t.Fatalf("closed feed %s is still listed", first.ID)
				}
				second := mustOpenFeed(t, e, "item", fx.saga1, reopenedSlug+"-2", nil)
				if second.ID == first.ID {
					t.Fatalf("reopened feed reused the closed id %s", first.ID)
				}
				ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
				if !hasString(ids, second.ID) || hasString(ids, first.ID) {
					t.Fatalf("feed list after reopen = %v (new %s, closed %s)", ids, second.ID, first.ID)
				}
				feedXML := mustFeedXML(t, e, reopenedSlug+"-2")
				if len(feedXML.items(t)) != 3 {
					t.Fatalf("reopened feed items = %d, want 3", len(feedXML.items(t)))
				}
			},
		},
	}
}
