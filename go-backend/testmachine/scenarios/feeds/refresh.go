package feeds

import (
	"bytes"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	middleSecondSlug = "feeds-middle-second"
	finalMiddleTitle = "The Middle Final"
	revisedSagaTitle = "The Beginning Revised"
	changedPodSlug   = "feeds-episode-change"
)

var imageURLPattern = regexp.MustCompile(`^/feed/([a-z0-9-]+)/cover\.jpg\?ts=(\d+)$`)

// itemChangedScenario covers route-r188 changed state: an item feed opened
// after a media change reflects it, and an older feed for the same entity
// refreshes to the same title on its next read.
func itemChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r188", Invariant: "INV-R188-CONTRACT-001", Case: "edge", State: "changed", Assertion: "open-item-feed-after-media-change",
		Sources: []string{"server/controllers/RSSFeedController.js:41-88", "server/managers/RssFeedManager.js:88-157"},
		Claim:   "After the item's media title changed, opening a second feed for the same item serves the new title immediately, the earlier feed for that item refreshes to the same title on its next read, the two feeds keep distinct ids and slugs, and both appear in the list.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			old := feedRowBySlug(t, e, coverlessSlug)
			oldID, _ := old["id"].(string)

			second := mustOpenFeed(t, e, "item", fx.saga2, middleSecondSlug, nil)
			if second.ID == oldID {
				t.Fatalf("second feed reused the first feed id %s", oldID)
			}
			if second.Meta.Title == nil || *second.Meta.Title != seriesRenamed {
				t.Fatalf("new feed title = %v, want %q", second.Meta.Title, seriesRenamed)
			}
			if second.FeedURL != "/feed/"+middleSecondSlug {
				t.Fatalf("new feed url = %q", second.FeedURL)
			}

			feedXML := mustFeedXML(t, e, coverlessSlug)
			wantText(t, feedXML.channel, "title", seriesRenamed)
			feedXML.wantItemOrder(t, seriesRenamed)

			refreshed := feedRowBySlug(t, e, coverlessSlug)
			column(t, refreshed, "title", seriesRenamed)
			if refreshed["updatedAt"] == old["updatedAt"] {
				t.Fatalf("earlier feed was not refreshed: updatedAt still %v", old["updatedAt"])
			}

			ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root")))
			if !hasString(ids, oldID) || !hasString(ids, second.ID) {
				t.Fatalf("both feeds for the item must be listed: %v (old %s, new %s)", ids, oldID, second.ID)
			}
		},
	}
}

// feedChangedScenario covers route-r007 changed state: the XML reflects a media
// write after the feed is refreshed, while episode identity is preserved.
func feedChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r007", Invariant: "INV-R007-CONTRACT-003", Case: "edge", State: "changed", Assertion: "feed-xml-refreshes-changed-media-keeping-ids",
		Sources: []string{"server/managers/RssFeedManager.js:88-157,180-188", "server/models/Feed.js:488-563"},
		Claim:   "After the book title changes, a public feed read refreshes the persisted feed and returns the new channel and item titles with the same episode id, enclosure URL and GUID, and the feed row's entityUpdatedAt is the new book timestamp.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			row := feedRowBySlug(t, e, coverlessSlug)
			feedID, _ := row["id"].(string)
			episode := feedEpisodeRows(t, e, feedID)
			if len(episode) != 1 {
				t.Fatalf("episodes before the rename = %d, want 1", len(episode))
			}
			episodeID, _ := episode[0]["id"].(string)
			enclosureURL, _ := episode[0]["enclosureURL"].(string)
			beforeUpdated, _ := row["entityUpdatedAt"].(string)

			patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.saga2+"/media", map[string]any{
				"metadata": map[string]any{"title": finalMiddleTitle},
			})
			wantStatus(t, patch, http.StatusOK, "rename the item")

			feed := mustFeedXML(t, e, coverlessSlug)
			wantText(t, feed.channel, "title", finalMiddleTitle)
			item := feed.item(t, finalMiddleTitle)
			wantText(t, item, "title", finalMiddleTitle)
			wantText(t, item, "pubDate", "Tue, 03 Nov 2026 12:46:25 GMT")
			absolute := e.Base + enclosureURL
			wantEnclosure(t, item, absolute, 218599, "audio/flac")
			wantGUID(t, item, absolute)

			after := feedEpisodeRows(t, e, feedID)
			if len(after) != 1 {
				t.Fatalf("episodes after the rename = %d, want 1", len(after))
			}
			if after[0]["id"] != episodeID || after[0]["enclosureURL"] != enclosureURL {
				t.Fatalf("episode identity changed: %v -> %v", episode[0], after[0])
			}
			refreshed := feedRowBySlug(t, e, coverlessSlug)
			column(t, refreshed, "title", finalMiddleTitle)
			if refreshed["entityUpdatedAt"] == beforeUpdated {
				t.Fatalf("entityUpdatedAt did not advance from %q", beforeUpdated)
			}
		},
	}
}

// coverChangedScenario covers route-r008 changed state: the image cache buster
// follows the refreshed entity timestamp while the cover bytes stay the same.
func coverChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r008", Invariant: "INV-R008-CONTRACT-001", Case: "edge", State: "changed", Assertion: "cover-url-follows-refresh-and-still-serves",
		Sources: []string{"server/models/Feed.js:90-100,488-563", "server/managers/RssFeedManager.js:234-265"},
		Claim:   "After the book's media title changes, the refreshed item feed publishes a new imageURL whose ?ts= equals the new entityUpdatedAt, and both that URL and the cover route still serve the unchanged cover bytes.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			before := feedRowBySlug(t, e, saga1FeedSlug)
			beforeImage, _ := before["imageURL"].(string)
			cover := readMediaFile(t, feedCoverPath(t, before))

			patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.saga1+"/media", map[string]any{
				"metadata": map[string]any{"title": revisedSagaTitle},
			})
			wantStatus(t, patch, http.StatusOK, "rename the saga book")
			feed := mustFeedXML(t, e, saga1FeedSlug)
			wantText(t, feed.channel, "title", revisedSagaTitle)

			refreshed := feedRowBySlug(t, e, saga1FeedSlug)
			image, _ := refreshed["imageURL"].(string)
			if image == beforeImage || image == "" {
				t.Fatalf("imageURL did not change after the refresh: %q", image)
			}
			match := imageURLPattern.FindStringSubmatch(image)
			if match == nil || match[1] != saga1FeedSlug {
				t.Fatalf("imageURL %q does not name the feed cover", image)
			}
			entityUpdatedAt, ok := refreshed["entityUpdatedAt"].(string)
			if !ok {
				t.Fatalf("entityUpdatedAt is not a date: %v", refreshed["entityUpdatedAt"])
			}
			ts := strconv.FormatInt(parseDate(t, entityUpdatedAt).UnixMilli(), 10)
			if match[2] != ts {
				t.Fatalf("imageURL ts = %s, want the entityUpdatedAt %s", match[2], ts)
			}
			if cover2 := readMediaFile(t, feedCoverPath(t, refreshed)); !bytes.Equal(cover, cover2) {
				t.Fatal("the cover file changed")
			}
			xmlImage := child(t, feed.channel, "image")
			wantText(t, xmlImage, "url", e.Base+image)
			for _, path := range []string{"/feed/" + saga1FeedSlug + "/cover.jpg", relativeToBase(t, e, e.Base+image)} {
				resp := e.Do(t, harness.Request{Path: path})
				wantStatus(t, resp, http.StatusOK, "cover after refresh "+path)
				if !bytes.Equal(resp.Body, cover) {
					t.Fatalf("cover %s body differs after the refresh", path)
				}
			}
		},
	}
}

// parseDate parses a Sequelize SQLite timestamp.
func parseDate(t *testing.T, value string) time.Time {
	t.Helper()
	for _, layout := range []string{"2006-01-02 15:04:05.999999999 -07:00", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	t.Fatalf("date %q is not parseable", value)
	return time.Time{}
}

// episodeFileChangedScenario covers route-r009 changed state: closing a feed
// invalidates its enclosure URLs and reopening generates new episode ids.
func episodeFileChangedScenario() harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r009", Invariant: "INV-R009-CONTRACT-002", Case: "edge", State: "changed", Assertion: "enclosure-dies-with-feed-and-returns-on-reopen",
		Sources: []string{"server/managers/RssFeedManager.js:197-219", "server/controllers/RSSFeedController.js:176-190"},
		Claim:   "An enclosure URL serves its episode while the feed is open; after the feed is closed the same URL answers 404 while the media file remains on disk, and reopening the podcast item generates new episode ids whose enclosures serve the same bytes.",
		Run: func(t *testing.T, e *harness.Env) {
			fx := fixtureOf(e)
			feed := mustOpenFeed(t, e, "item", fx.podcast, changedPodSlug, nil)
			episodes := feedEpisodeRows(t, e, feed.ID)
			if len(episodes) != 3 {
				t.Fatalf("podcast feed episodes = %d, want 3", len(episodes))
			}
			firstID, _ := episodes[0]["id"].(string)
			firstURL, _ := episodes[0]["enclosureURL"].(string)
			file := readMediaFile(t, episodeFile(t, episodes[0]))
			open := e.Do(t, harness.Request{Path: firstURL})
			wantStatus(t, open, http.StatusOK, "enclosure while open")
			if !bytes.Equal(open.Body, file) {
				t.Fatal("open enclosure body differs from the media file")
			}

			wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "close the podcast feed")
			wantStatus(t, e.Do(t, harness.Request{Path: firstURL}), http.StatusNotFound, "enclosure after close")
			if got := readMediaFile(t, episodeFile(t, episodes[0])); !bytes.Equal(got, file) {
				t.Fatal("closing the feed changed the media file")
			}

			reopened := mustOpenFeed(t, e, "item", fx.podcast, changedPodSlug+"-2", nil)
			if reopened.ID == feed.ID {
				t.Fatalf("reopened feed reused id %s", feed.ID)
			}
			fresh := feedEpisodeRows(t, e, reopened.ID)
			if len(fresh) != 3 {
				t.Fatalf("reopened episodes = %d, want 3", len(fresh))
			}
			newID, _ := fresh[0]["id"].(string)
			if newID == firstID {
				t.Fatalf("reopened feed reused episode id %s", firstID)
			}
			newURL, _ := fresh[0]["enclosureURL"].(string)
			if !strings.Contains(newURL, newID) || newURL == firstURL {
				t.Fatalf("reopened enclosure url = %q", newURL)
			}
			resp := e.Do(t, harness.Request{Path: newURL})
			wantStatus(t, resp, http.StatusOK, "reopened enclosure")
			if !bytes.Equal(resp.Body, file) {
				t.Fatal("reopened enclosure body differs from the media file")
			}
			wantStatus(t, e.Do(t, harness.Request{Path: firstURL}), http.StatusNotFound, "closed feed enclosure stays gone")
		},
	}
}
