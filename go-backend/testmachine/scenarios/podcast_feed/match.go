package podcast_feed

import (
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	matchReq     = "route-r158"
	matchInv     = "INV-R158-MATCH-001"
	matchForce   = "INV-R158-FORCE-001"
	matchAuthInv = "INV-R158-SAMPLED"
	matchFailInv = "INV-R158-FAILURE-001"
	matchSrc     = "server/controllers/PodcastController.js:390-410"
	matchScanSrc = "server/scanner/Scanner.js:359-430"
	matchUtilSrc = "server/utils/podcastUtils.js:412-456"
)

// matchResult is the body of POST /api/podcasts/:id/match-episodes.
type matchResult struct {
	NumEpisodesUpdated int `json:"numEpisodesUpdated"`
}

// matchFeed is the feed the quick match matches episode titles against.
func matchFeed(stub *feedStub) feed {
	return feed{
		Title: "Fixture Cast",
		Items: []feedItem{
			{
				Title: "Pilot Episode", Subtitle: "Fresh subtitle", GUID: "match-guid-1",
				ContentEncoded: "<p>Fresh <b>description</b></p>", PubDate: "Thu, 02 Jan 2025 10:00:00 GMT",
				EnclosureURL: stub.url("/match-1.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673",
				Episode: "1", Season: "1", EpisodeType: "full",
			},
			{
				Title: "Third Time", Subtitle: "Third subtitle", GUID: "match-guid-3",
				PubDate:      "Sat, 04 Jan 2025 10:00:00 GMT",
				EnclosureURL: stub.url("/match-3.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "48703",
				Episode: "3", Season: "1",
			},
			{
				Title: "Second Outing", GUID: "match-guid-2",
				PubDate:      "Fri, 03 Jan 2025 10:00:00 GMT",
				EnclosureURL: stub.url("/match-2.mp3"), EnclosureType: "audio/mpeg",
			},
		},
	}
}

// quickMatch posts the match-episodes route and returns the update count.
func quickMatch(t *testing.T, e *harness.Env, itemID, query string) int {
	t.Helper()
	path := "/api/podcasts/" + itemID + "/match-episodes"
	if query != "" {
		path += "?" + query
	}
	resp := e.Post(t, e.Token(t, "root"), path, nil)
	wantStatus(t, "match episodes", resp, http.StatusOK)
	return decode[matchResult](t, resp.Body).NumEpisodesUpdated
}

// episodeRows returns the stored episodes of a podcast ordered by id.
func episodeRows(t *testing.T, e *harness.Env, podcastID string) []map[string]any {
	t.Helper()
	return e.Rows(t, `SELECT id, title, subtitle, description, enclosureURL, enclosureSize,
		enclosureType, episode, season, episodeType, pubDate, publishedAt, extraData
		FROM podcastEpisodes WHERE podcastId = ? ORDER BY id`, podcastID)
}

// episodeRow returns the stored row of one episode.
func episodeRow(t *testing.T, e *harness.Env, episodeID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, `SELECT id, title, subtitle, description, enclosureURL, enclosureSize,
		enclosureType, episode, season, episodeType, pubDate, publishedAt, extraData
		FROM podcastEpisodes WHERE id = ?`, episodeID)
	if len(rows) != 1 {
		t.Fatalf("episode %s: %d rows", episodeID, len(rows))
	}
	return rows[0]
}

func matchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: matchReq, Invariant: matchInv, Case: "normal", State: "initial", Assertion: "match-episodes-fills-unmatched-episodes",
			Sources: []string{matchSrc, matchScanSrc, matchUtilSrc, episodeModelSrc},
			Claim:   "POST /api/podcasts/:id/match-episodes matches every episode without an enclosure against the podcast feed by title and fills its empty enclosure, subtitle, episode type and season from the feed while keeping the values the episode already has, answers the number of updated episodes and emits item_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/match.xml", matchFeed(stub).document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/match.xml")}})
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				if updated := quickMatch(t, e, e.IDs["podcast"], ""); updated != 2 {
					t.Fatalf("numEpisodesUpdated = %d, want 2", updated)
				}
				socket.Wait(t, mark, "item_updated", eventTimeout, itemIDMatcher(e.IDs["podcast"]))
				first := episodeRow(t, e, e.IDs["ep1"])
				requireString(t, first, "enclosureURL", stub.url("/match-1.mp3"))
				requireString(t, first, "enclosureType", "audio/mpeg")
				requireString(t, first, "subtitle", "Fresh subtitle")
				requireString(t, first, "description", "The first episode of the fixture cast.")
				requireString(t, first, "pubDate", "2025-03-01")
				requireString(t, first, "episode", "1")
				requireString(t, first, "season", "1")
				requireString(t, first, "episodeType", "full")
				if got := first["enclosureSize"]; got != int64(96673) {
					t.Fatalf("enclosureSize = %v (%T)", got, got)
				}
				if got, ok := first["publishedAt"].(string); !ok || got == "" {
					t.Fatalf("publishedAt = %v", first["publishedAt"])
				}
				if first["extraData"] != nil {
					t.Fatalf("quick match stored extraData = %v", first["extraData"])
				}
				third := episodeRow(t, e, e.IDs["ep3"])
				requireString(t, third, "enclosureURL", stub.url("/match-3.mp3"))
				requireString(t, third, "title", "Third Time")
				read := itemOf(t, e, e.IDs["podcast"])
				episode := episodeOf(t, read, e.IDs["ep1"])
				if episode.Enclosure == nil || episode.Enclosure.URL != stub.url("/match-1.mp3") {
					t.Fatalf("api enclosure = %+v", episode.Enclosure)
				}
				requireValue(t, "api subtitle", episode.Subtitle, "Fresh subtitle")
			},
		},
		{
			Requirement: matchReq, Invariant: matchInv, Case: "edge", State: "repeat", Assertion: "match-episodes-repeat-updates-nothing",
			Sources: []string{matchScanSrc},
			Claim:   "Running the match again once every episode has an enclosure answers 0, leaves every episode row unchanged and emits no item_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				podcastID := podcastIDOf(t, e, e.IDs["podcast"])
				before := episodeRows(t, e, podcastID)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				if updated := quickMatch(t, e, e.IDs["podcast"], ""); updated != 0 {
					t.Fatalf("numEpisodesUpdated = %d, want 0", updated)
				}
				if after := episodeRows(t, e, podcastID); !reflect.DeepEqual(before, after) {
					t.Fatalf("episode rows changed:\n%+v\n%+v", before, after)
				}
				socket.None(t, mark, "item_updated", quietWindow, itemIDMatcher(e.IDs["podcast"]))
			},
		},
		{
			Requirement: matchReq, Invariant: matchInv, Case: "edge", State: "changed", Assertion: "match-episodes-keeps-existing-values",
			Sources: []string{matchScanSrc},
			Claim:   "A plain match only fills empty fields: for an episode whose enclosure was cleared but whose subtitle is set, the enclosure is filled and the existing subtitle is kept.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/match.xml", matchFeed(stub).document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/match.xml")}})
				patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"enclosure": nil, "subtitle": "Old subtitle"})
				if updated := quickMatch(t, e, e.IDs["podcast"], ""); updated != 1 {
					t.Fatalf("numEpisodesUpdated = %d, want 1", updated)
				}
				row := episodeRow(t, e, e.IDs["ep1"])
				requireString(t, row, "enclosureURL", stub.url("/match-1.mp3"))
				requireString(t, row, "subtitle", "Old subtitle")
			},
		},
		{
			Requirement: matchReq, Invariant: matchForce, Case: "normal", State: "force", Assertion: "match-episodes-override-replaces-details",
			Sources: []string{matchSrc, matchScanSrc, "client/components/tables/podcast/LazyEpisodesTable.vue:267-302"},
			Claim:   "With ?override=1 the match also replaces details the episode already has: a cleared enclosure is restored and the stored subtitle and description are overwritten by the feed values.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/match.xml", matchFeed(stub).document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/match.xml")}})
				patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"enclosure": nil})
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				if updated := quickMatch(t, e, e.IDs["podcast"], "override=1"); updated != 1 {
					t.Fatalf("numEpisodesUpdated = %d, want 1", updated)
				}
				socket.Wait(t, mark, "item_updated", eventTimeout, itemIDMatcher(e.IDs["podcast"]))
				row := episodeRow(t, e, e.IDs["ep1"])
				requireString(t, row, "enclosureURL", stub.url("/match-1.mp3"))
				requireString(t, row, "subtitle", "Fresh subtitle")
				requireString(t, row, "description", "<p>Fresh <b>description</b></p>")
				requireString(t, row, "enclosureType", "audio/mpeg")
			},
		},
		{
			Requirement: matchReq, Invariant: matchAuthInv, Case: "error", State: "failure", Assertion: "match-episodes-rejections-change-nothing",
			Sources: []string{matchSrc, "server/controllers/PodcastController.js:560-585"},
			Claim:   "POST /api/podcasts/:id/match-episodes answers 401 without a token, 403 for a non-admin (from the item middleware or, with update permission, from the admin role check) and for an admin who cannot access the item's library, 404 for an unknown item and 500 for an item that is not a podcast; no rejected request changes an episode row.",
			Run: func(t *testing.T, e *harness.Env) {
				podcastID := podcastIDOf(t, e, e.IDs["podcast"])
				before := episodeRows(t, e, podcastID)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/podcasts/" + e.IDs["podcast"] + "/match-episodes"}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/podcasts/"+e.IDs["podcast"]+"/match-episodes", nil), http.StatusForbidden)
				updater := createUser(t, e, "match-updater", map[string]any{"permissions": map[string]any{"update": true}})
				wantStatus(t, "non-admin with update permission", e.Post(t, updater, "/api/podcasts/"+e.IDs["podcast"]+"/match-episodes", nil), http.StatusForbidden)
				restricted := createAdmin(t, e, "match-restricted-admin", e.IDs["library"])
				wantStatus(t, "admin without the library", e.Post(t, restricted, "/api/podcasts/"+e.IDs["podcast"]+"/match-episodes", nil), http.StatusForbidden)
				wantStatus(t, "unknown item", e.Post(t, e.Token(t, "root"), "/api/podcasts/not-an-item/match-episodes", nil), http.StatusNotFound)
				wantStatus(t, "book item", e.Post(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/match-episodes", nil), http.StatusInternalServerError)
				if after := episodeRows(t, e, podcastID); !reflect.DeepEqual(before, after) {
					t.Fatalf("a rejected match changed episode rows:\n%+v\n%+v", before, after)
				}
			},
		},
		{
			Requirement: matchReq, Invariant: matchFailInv, Case: "error", State: "failure", Assertion: "match-episodes-unreachable-feed-changes-nothing",
			Sources: []string{matchScanSrc},
			Claim:   "When the feed cannot be fetched the match answers 0 for the episodes still lacking an enclosure, leaves every episode row unchanged and emits no item_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"enclosure": nil})
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": deadURL(t)}})
				podcastID := podcastIDOf(t, e, e.IDs["podcast"])
				before := episodeRows(t, e, podcastID)
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				if updated := quickMatch(t, e, e.IDs["podcast"], ""); updated != 0 {
					t.Fatalf("numEpisodesUpdated = %d, want 0 for an unreachable feed", updated)
				}
				if after := episodeRows(t, e, podcastID); !reflect.DeepEqual(before, after) {
					t.Fatalf("a failed match changed episode rows:\n%+v\n%+v", before, after)
				}
				socket.None(t, mark, "item_updated", quietWindow, itemIDMatcher(e.IDs["podcast"]))
			},
		},
	}
}
