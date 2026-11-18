package feeds

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	podcastSerialSlug   = "feeds-podcast-serial"
	podcastEpisodicSlug = "feeds-podcast-episodic"
)

// podcastChangedScenario makes the fixture podcast serial and proves the feed
// orders episodes oldest first and refreshes after a media change.
func podcastChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r007", Invariant: "INV-R007-CONTRACT-003", Case: "edge", State: "changed", Assertion: "feed-xml-podcast-serial-order-and-refresh",
		Sources: []string{"server/models/FeedEpisode.js:88-110", "server/models/Feed.js:496-507", "server/managers/RssFeedManager.js:88-120"},
		Claim:   "A podcast whose podcastType is serial produces a feed that orders its episodes oldest first (the reverse of the episodic fixture) and, after the podcast title changes, the next read refreshes the feed to the new title while all three episode ids stay.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			episodic := mustOpenFeed(t, e, "item", fx.podcast, podcastEpisodicSlug, nil)
			episodicXML := mustFeedXML(t, e, podcastEpisodicSlug)
			wantText(t, episodicXML.channel, "itunes:type", "episodic")
			episodicXML.wantItemOrder(t, "Third Time", "Second Outing", "Pilot Episode")
			if count := len(feedEpisodeRows(t, e, episodic.ID)); count != 3 {
				t.Fatalf("episodic podcast episodes = %d, want 3", count)
			}

			patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.podcast+"/media", map[string]any{
				"metadata": map[string]any{"type": "serial"},
			})
			wantStatus(t, patch, http.StatusOK, "make the podcast serial")

			feed := mustOpenFeed(t, e, "item", fx.podcast, podcastSerialSlug, nil)
			row := feedRowBySlug(t, e, podcastSerialSlug)
			column(t, row, "podcastType", "serial")
			episodes := feedEpisodeRows(t, e, feed.ID)
			if len(episodes) != 3 {
				t.Fatalf("serial podcast episodes = %d, want 3", len(episodes))
			}
			ids := make([]string, len(episodes))
			for i, episode := range episodes {
				ids[i], _ = episode["id"].(string)
			}
			feedXML := mustFeedXML(t, e, podcastSerialSlug)
			wantText(t, feedXML.channel, "itunes:type", "serial")
			feedXML.wantItemOrder(t, "Pilot Episode", "Second Outing", "Third Time")

			patch = e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.podcast+"/media", map[string]any{
				"metadata": map[string]any{"title": "Fixture Cast Updated"},
			})
			wantStatus(t, patch, http.StatusOK, "rename the podcast")
			refreshed := mustFeedXML(t, e, podcastSerialSlug)
			wantText(t, refreshed.channel, "title", "Fixture Cast Updated")
			refreshed.wantItemOrder(t, "Pilot Episode", "Second Outing", "Third Time")
			after := feedEpisodeRows(t, e, feed.ID)
			if len(after) != 3 {
				t.Fatalf("refreshed podcast episodes = %d, want 3", len(after))
			}
			for i := range ids {
				if got, _ := after[i]["id"].(string); got != ids[i] {
					t.Fatalf("podcast episode %d id %q changed to %q", i, ids[i], got)
				}
			}
			if row["updatedAt"] == feedRowBySlug(t, e, podcastSerialSlug)["updatedAt"] {
				t.Fatal("podcast feed was not refreshed after the rename")
			}
		},
	}
}

// restartScenario proves the persisted feeds survive a server restart and are
// still served, and that no valid feed is removed at boot.
func restartScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r007", Invariant: "INV-R007-CONTRACT-001", Case: "edge", State: "repeat", Assertion: "feed-xml-serves-after-restart",
		Sources: []string{"server/managers/RssFeedManager.js:17-52,165-189", "server/models/Feed.js:452-481"},
		Claim:   "After the server restarts on the same install every previously listed feed is still listed with the same id and slug, and one item, collection and series feed each still serves its XML with its previous episode ids, so boot-time cleanup removes only feeds whose entity is gone.",
		Run: func(t *testing.T, e *harness.Env) {
			before := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
			itemEpisodes := len(feedEpisodeRows(t, e, novelFeedID))
			e.Restart(t)
			after := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
			if len(after) != len(before) {
				t.Fatalf("feed count after restart = %d, want %d", len(after), len(before))
			}
			for _, id := range before {
				if !hasString(after, id) {
					t.Fatalf("feed %s disappeared at restart", id)
				}
			}
			for _, slug := range []string{novelFeedSlug, favouritesSlug, sagaFeedSlug} {
				feed := mustFeedXML(t, e, slug)
				if len(feed.items(t)) == 0 {
					t.Fatalf("feed %s has no items after restart", slug)
				}
			}
			if got := len(feedEpisodeRows(t, e, novelFeedID)); got != itemEpisodes {
				t.Fatalf("novel feed episodes after restart = %d, want %d", got, itemEpisodes)
			}
		},
	}
}
