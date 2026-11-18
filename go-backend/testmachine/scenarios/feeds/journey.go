package feeds

import (
	"bytes"
	"net/http"
	"regexp"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	journeySlug  = "feeds-journey"
	journeySlug2 = "feeds-journey-2"
	journeySlug3 = "feeds-journey-3"
	journeyBad   = "feeds-journey-bad"
)

// flowScenarios covers flow-client-feed-share: the web client's feed
// management journey plus the unauthenticated feed-reader consumption.
func flowScenarios() []harness.Scenario {
	const req = "flow-client-feed-share"
	const invJourney = "INV-FLOW-CLIENT-FEED-SHARE-001"
	const invReader = "INV-FLOW-CLIENT-FEED-SHARE-002"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: invJourney, Case: "normal", State: "initial", Assertion: "flow-open-read-close-a-feed",
			Sources: []string{
				"client/components/modals/rssfeed/OpenCloseModal.vue:123-176",
				"server/controllers/RSSFeedController.js:41-88,176-190",
				"server/managers/RssFeedManager.js:165-189",
			},
			Claim: "The client journey open → read → close works end to end: the open modal's request persists a feed that appears in the management list, an unauthenticated reader can fetch its XML, cover and enclosure, and the close the modal sends removes the feed so every public URL answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "item", fx.novel, journeySlug, map[string]any{"preventIndexing": true, "ownerName": "Journey Reader"})
				if ids := feedIDs(t, listFeeds(t, e, e.Token(t, "root"))); !hasString(ids, feed.ID) {
					t.Fatalf("opened feed %s missing from the list %v", feed.ID, ids)
				}
				row := feedRowBySlug(t, e, journeySlug)
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, feed.ID)[0]))
				cover := readMediaFile(t, feedCoverPath(t, row))

				xml := mustFeedXML(t, e, journeySlug)
				wantText(t, xml.channel, "title", "Single File Novel")
				wantText(t, child(t, xml.channel, "itunes:owner"), "itunes:name", "Journey Reader")
				xml.wantItemOrder(t, "Single File Novel")
				item := xml.item(t, "Single File Novel")
				enclosureURL, _ := feedEpisodeRows(t, e, feed.ID)[0]["enclosureURL"].(string)
				wantEnclosure(t, item, e.Base+enclosureURL, 193740, "audio/mp4")

				coverResp := e.Do(t, harness.Request{Path: "/feed/" + journeySlug + "/cover.jpg"})
				wantStatus(t, coverResp, http.StatusOK, "journey cover")
				if !bytes.Equal(coverResp.Body, cover) {
					t.Fatal("journey cover body differs from the persisted cover file")
				}
				audio := e.Do(t, harness.Request{Path: enclosureURL})
				wantStatus(t, audio, http.StatusOK, "journey enclosure")
				if !bytes.Equal(audio.Body, file) {
					t.Fatal("journey enclosure body differs from the media file")
				}

				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "journey close")
				noFeedRow(t, e, journeySlug, feed.ID)
				for _, path := range []string{"/feed/" + journeySlug, "/feed/" + journeySlug + "/cover.jpg", enclosureURL} {
					wantStatus(t, e.Do(t, harness.Request{Path: path}), http.StatusNotFound, "public url after close "+path)
				}
			},
		},
		{
			Requirement: req, Invariant: invJourney, Case: "error", State: "initial", Assertion: "flow-failed-open-leaves-no-feed",
			Sources: []string{"server/controllers/RSSFeedController.js:41-88,90-131", "server/models/Feed.js:165-189"},
			Claim:   "Failed feed creation attempts of the modal (item without audio tracks, unknown item, already used slug) answer 4xx, persist nothing and leave no readable feed for the requested slug.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				held := mustOpenFeed(t, e, "item", fx.novel, journeyBad+"-held", nil)
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "item", fx.ebook, journeyBad, nil), http.StatusBadRequest, "ebook feed")
				wantStatus(t, openFeed(t, e, "item", "00000000-0000-4000-8000-000000000000", journeyBad, nil), http.StatusNotFound, "unknown item")
				wantStatus(t, openFeed(t, e, "item", fx.novel, journeyBad+"-held", nil), http.StatusBadRequest, "duplicate slug")
				wantStatus(t, feedBody(t, e, journeyBad), http.StatusNotFound, "failed journey feed")
				wantSameCounts(t, e, before, "failed journey opens")
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), held.ID), http.StatusOK, "cleanup held feed")
			},
		},
		{
			Requirement: req, Invariant: invJourney, Case: "edge", State: "repeat", Assertion: "flow-second-journey-repeats-the-shape",
			Sources: []string{"server/models/FeedEpisode.js:141-206", "server/managers/RssFeedManager.js:165-189"},
			Claim:   "A second open/read/close journey on the same item with a new slug produces the same channel and item shape and values (title, author, type, enclosure size/type, duration, pubDate) and the same cover and audio bytes, so a closed feed leaves nothing that changes the next one.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "item", fx.novel, journeySlug2, nil)
				first := feedBody(t, e, journeySlug2)
				second := feedBody(t, e, journeySlug2)
				wantStatus(t, first, http.StatusOK, "first journey read")
				wantStatus(t, second, http.StatusOK, "second journey read")
				if lastBuildDatePattern.ReplaceAllString(string(first.Body), "") != lastBuildDatePattern.ReplaceAllString(string(second.Body), "") {
					t.Fatal("repeated journey reads differ beyond lastBuildDate")
				}
				xml := mustFeedXML(t, e, journeySlug2)
				wantText(t, xml.channel, "title", "Single File Novel")
				wantText(t, xml.channel, "author", "Ada Example")
				wantText(t, xml.channel, "itunes:type", "serial")
				xml.wantItemOrder(t, "Single File Novel")
				item := xml.item(t, "Single File Novel")
				wantText(t, item, "pubDate", novelPubDate)
				wantText(t, item, "itunes:duration", "45")
				enclosureURL, _ := feedEpisodeRows(t, e, feed.ID)[0]["enclosureURL"].(string)
				wantEnclosure(t, item, e.Base+enclosureURL, 193740, "audio/mp4")
				wantGUID(t, item, e.Base+enclosureURL)
				if !regexp.MustCompile(`/media\.m4b$`).MatchString(enclosureURL) {
					t.Fatalf("journey enclosure url = %q", enclosureURL)
				}
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "second journey close")
			},
		},
		{
			Requirement: req, Invariant: invReader, Case: "edge", State: "changed", Assertion: "flow-reader-sees-changed-title-keeping-guid",
			Sources: []string{"server/managers/RssFeedManager.js:88-157,180-188", "server/models/FeedEpisode.js:314-346"},
			Claim:   "When the item's title changes while its feed is open, the feed reader's next fetch returns the new channel and item titles with the same GUID and enclosure URL, so a podcast app updates the entry without downloading the media again.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "item", fx.novel, journeySlug3, nil)
				before := mustFeedXML(t, e, journeySlug3)
				oldGUID := child(t, before.item(t, "Single File Novel"), "guid")
				oldURL := oldGUID.Text

				patch := e.Patch(t, e.Token(t, "root"), "/api/items/"+fx.novel+"/media", map[string]any{
					"metadata": map[string]any{"title": "Single File Novel Revised"},
				})
				wantStatus(t, patch, http.StatusOK, "rename the item")

				after := mustFeedXML(t, e, journeySlug3)
				wantText(t, after.channel, "title", "Single File Novel Revised")
				item := after.item(t, "Single File Novel Revised")
				wantGUID(t, item, oldURL)
				if got := child(t, item, "guid").Text; got != oldURL {
					t.Fatalf("guid changed from %q to %q", oldURL, got)
				}
				enclosureURL, _ := feedEpisodeRows(t, e, feed.ID)[0]["enclosureURL"].(string)
				wantEnclosure(t, item, e.Base+enclosureURL, 193740, "audio/mp4")
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "close after the change")
			},
		},
		{
			Requirement: req, Invariant: invReader, Case: "error", State: "failure", Assertion: "flow-closed-feed-leaves-media-intact",
			Sources: []string{"server/managers/RssFeedManager.js:355-361", "server/models/Feed.js:80-88"},
			Claim:   "After the journey feed is closed every reader URL answers 404, the episode media file and item row are unchanged, and a rejected close of the same feed changes no persisted row.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0]))
				before := snapshotCounts(t, e)
				for _, path := range []string{"/feed/" + journeySlug2, "/feed/" + journeySlug2 + "/cover.jpg"} {
					wantStatus(t, e.Do(t, harness.Request{Path: path}), http.StatusNotFound, "closed journey url "+path)
				}
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "rejected close of an unknown feed")
				wantSameCounts(t, e, before, "rejected journey close")
				if got := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0])); !bytes.Equal(got, file) {
					t.Fatal("closed journey changed the media file")
				}
				if count := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", fx.novel); count != 1 {
					t.Fatalf("novel item rows = %d", count)
				}
			},
		},
	}
}
