package podcast_feed

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	checkNewReq      = "route-r153"
	checkNewInv      = "INV-R153-CHECK-001"
	checkNewLimitInv = "INV-R153-LIMIT-001"
	checkNewIDInv    = "INV-R153-IDENTITY-001"
	checkNewAuthInv  = "INV-R153-SAMPLED"
	checkNewDownInv  = "INV-R153-UNREACHABLE-001"
	checkNewSrc      = "server/controllers/PodcastController.js:277-303"
	checkNewMgrSrc   = "server/managers/PodcastManager.js:380-455"
	checkNewModelSrc = "server/models/Podcast.js:393-399"
	episodeModelSrc  = "server/models/PodcastEpisode.js:66-107,168-176"
)

// episodesPayload is the body of GET /api/podcasts/:id/checknew.
type episodesPayload struct {
	Episodes []rssEpisode `json:"episodes"`
}

// checkNew asks the server to look for new episodes.
func checkNew(t *testing.T, e *harness.Env, itemID, limit string) harness.Response {
	t.Helper()
	path := "/api/podcasts/" + itemID + "/checknew"
	if limit != "" {
		path += "?limit=" + limit
	}
	return e.Get(t, e.Token(t, "root"), path)
}

// checkNewEpisodes calls checknew and returns the feed episodes it reports.
func checkNewEpisodes(t *testing.T, e *harness.Env, itemID, limit string) []rssEpisode {
	t.Helper()
	resp := checkNew(t, e, itemID, limit)
	wantStatus(t, "checknew", resp, http.StatusOK)
	return decode[episodesPayload](t, resp.Body).Episodes
}

// patchMedia updates the podcast media of a library item through the API.
func patchMedia(t *testing.T, e *harness.Env, itemID string, media map[string]any) libraryItemJSON {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", media)
	wantStatus(t, "patch media", resp, http.StatusOK)
	var payload struct {
		Updated     bool            `json:"updated"`
		LibraryItem libraryItemJSON `json:"libraryItem"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("decode patch media: %v: %.400s", err, resp.Body)
	}
	if !payload.Updated {
		t.Fatalf("patch media did not update: %.400s", resp.Body)
	}
	return payload.LibraryItem
}

// patchEpisode updates one episode through the API.
func patchEpisode(t *testing.T, e *harness.Env, itemID, episodeID string, body map[string]any) libraryItemJSON {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/podcasts/"+itemID+"/episode/"+episodeID, body)
	wantStatus(t, "patch episode", resp, http.StatusOK)
	return decode[libraryItemJSON](t, resp.Body)
}

// lastEpisodeCheck reads the stored lastEpisodeCheck of a podcast item.
func lastEpisodeCheck(t *testing.T, e *harness.Env, itemID string) int64 {
	t.Helper()
	item := itemOf(t, e, itemID)
	if item.Media.LastEpisodeCheck == nil {
		t.Fatalf("item %s has no lastEpisodeCheck", itemID)
	}
	return *item.Media.LastEpisodeCheck
}

// feedEpisode builds one feed item with an enclosure.
func feedEpisode(title, guid, enclosureURL, pubDate string) feedItem {
	return feedItem{Title: title, GUID: guid, EnclosureURL: enclosureURL, EnclosureType: "audio/mpeg", PubDate: pubDate}
}

func checkNewScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: checkNewReq, Invariant: checkNewAuthInv, Case: "error", State: "failure", Assertion: "checknew-rejections-change-nothing",
			Sources: []string{checkNewSrc, "server/controllers/PodcastController.js:560-585"},
			Claim:   "GET /api/podcasts/:id/checknew answers 401 without a token, 403 for a non-admin and for an admin who cannot access the item's library, 404 for an unknown item, 500 for an item that is not a podcast and 400 for a podcast without a feed URL; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				podcast := itemOf(t, e, e.IDs["podcast"])
				if podcast.Media.Metadata.FeedURL != nil {
					t.Fatalf("the fixture podcast already has a feed URL: %q", *podcast.Media.Metadata.FeedURL)
				}
				before := takeSnapshot(t, e)
				wantStatus(t, "anonymous", e.Get(t, "", "/api/podcasts/"+e.IDs["podcast"]+"/checknew"), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Get(t, e.Token(t, "listener"), "/api/podcasts/"+e.IDs["podcast"]+"/checknew"), http.StatusForbidden)
				wantStatus(t, "unknown item", e.Get(t, e.Token(t, "root"), "/api/podcasts/not-an-item/checknew"), http.StatusNotFound)
				wantStatus(t, "book item", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/checknew"), http.StatusInternalServerError)
				restricted := createAdmin(t, e, "checknew-restricted-admin", e.IDs["library"])
				wantStatus(t, "admin without the library", e.Get(t, restricted, "/api/podcasts/"+e.IDs["podcast"]+"/checknew"), http.StatusForbidden)
				wantStatus(t, "no feed url", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/checknew"), http.StatusBadRequest)
				wantStatus(t, "non-admin before the feed check", e.Get(t, e.Token(t, "listener"), "/api/podcasts/"+e.IDs["podcast"]+"/checknew"), http.StatusForbidden)
				before.requireUnchanged(t, e, "rejected checknew requests")
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewInv, Case: "normal", State: "initial", Assertion: "checknew-reports-new-episodes-and-advances-the-check",
			Sources: []string{checkNewSrc, checkNewMgrSrc},
			Claim:   "A check after a feed URL and an earlier lastEpisodeCheck are set reports the feed episode published after that check with its guid, enclosure and publication time, moves the stored lastEpisodeCheck strictly forward, and emits item_updated with the new check time.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				dead := deadURL(t)
				stub.setFeed("/check.xml", feed{Title: "Fixture Cast", Items: []feedItem{
					feedEpisode("Pilot Episode", "check-guid-1", dead, "Thu, 02 Jan 2025 10:00:00 GMT"),
					feedEpisode("Second Outing", "check-guid-2", dead, "Fri, 03 Jan 2025 10:00:00 GMT"),
				}}.document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/check.xml")}, "lastEpisodeCheck": 0})
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				started := time.Now().UnixMilli()
				episodes := checkNewEpisodes(t, e, e.IDs["podcast"], "1")
				if len(episodes) != 1 {
					t.Fatalf("%d episodes, want 1 (limit=1): %+v", len(episodes), episodes)
				}
				first := episodes[0]
				requireValue(t, "title", first.Title, "Pilot Episode")
				requireValue(t, "guid", first.GUID, "check-guid-1")
				requireValue(t, "enclosure", first.Enclosure.URL, dead)
				if first.PublishedAt == nil || *first.PublishedAt != 1735812000000 {
					t.Fatalf("publishedAt = %v, want 1735812000000", first.PublishedAt)
				}
				advanced := lastEpisodeCheck(t, e, e.IDs["podcast"])
				if advanced < started || advanced > time.Now().UnixMilli() {
					t.Fatalf("lastEpisodeCheck = %d, want within [%d, now]", advanced, started)
				}
				event := socket.Wait(t, mark, "item_updated", eventTimeout, itemIDMatcher(e.IDs["podcast"]))
				if got := harness.Dig(decode[map[string]any](t, event.Args[0]), "media", "lastEpisodeCheck"); got != float64(advanced) {
					t.Fatalf("event lastEpisodeCheck = %v, want %d", got, advanced)
				}
				row := podcastRow(t, e, podcastIDOf(t, e, e.IDs["podcast"]))
				if got, ok := row["lastEpisodeCheck"].(string); !ok || !strings.Contains(got, "-") {
					t.Fatalf("stored lastEpisodeCheck = %v", row["lastEpisodeCheck"])
				}
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewInv, Case: "edge", State: "repeat", Assertion: "checknew-repeat-returns-no-new-and-advances",
			Sources: []string{checkNewMgrSrc},
			Claim:   "Checking again with unchanged feed content reports no episodes because they are not published after the new lastEpisodeCheck, moves the stored check time strictly forward again and still emits item_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				first := lastEpisodeCheck(t, e, e.IDs["podcast"])
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				episodes := checkNewEpisodes(t, e, e.IDs["podcast"], "1")
				if len(episodes) != 0 {
					t.Fatalf("repeat returned %+v, want none", episodes)
				}
				second := lastEpisodeCheck(t, e, e.IDs["podcast"])
				if second <= first {
					t.Fatalf("lastEpisodeCheck = %d, want strictly after %d", second, first)
				}
				socket.Wait(t, mark, "item_updated", eventTimeout, itemIDMatcher(e.IDs["podcast"]))
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewLimitInv, Case: "edge", State: "changed", Assertion: "checknew-limit-parsing",
			Sources: []string{checkNewSrc},
			Claim:   "The limit query caps the reported episodes; a non-numeric limit falls back to three and zero or a negative limit reports every new episode, each call with lastEpisodeCheck reset first.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/many.xml", feed{Title: "Fixture Cast", Items: []feedItem{
					feedEpisode("Many One", "many-1", deadURL(t), "Sat, 04 Jan 2025 10:00:00 GMT"),
					feedEpisode("Many Two", "many-2", deadURL(t), "Sun, 05 Jan 2025 10:00:00 GMT"),
					feedEpisode("Many Three", "many-3", deadURL(t), "Mon, 06 Jan 2025 10:00:00 GMT"),
					feedEpisode("Many Four", "many-4", deadURL(t), "Tue, 07 Jan 2025 10:00:00 GMT"),
				}}.document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/many.xml")}})
				cases := []struct {
					limit string
					want  int
				}{{"2", 2}, {"abc", 3}, {"0", 4}, {"-1", 4}}
				for _, testCase := range cases {
					patchMedia(t, e, e.IDs["podcast"], map[string]any{"lastEpisodeCheck": 0})
					episodes := checkNewEpisodes(t, e, e.IDs["podcast"], testCase.limit)
					if len(episodes) != testCase.want {
						t.Fatalf("limit %q reported %d episodes, want %d", testCase.limit, len(episodes), testCase.want)
					}
				}
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewIDInv, Case: "normal", State: "changed", Assertion: "checknew-enclosure-identity",
			Sources: []string{checkNewModelSrc, episodeModelSrc, checkNewMgrSrc},
			Claim:   "A feed episode whose enclosure URL equals a stored episode's enclosure URL is not new even when its title and guid differ, while an episode with a new enclosure and guid is reported.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stored := patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep2"], map[string]any{
					"enclosure": map[string]any{"url": stub.url("/stored.mp3"), "type": "audio/mpeg"},
				})
				episode := episodeOf(t, stored, e.IDs["ep2"])
				if episode.Enclosure == nil || episode.Enclosure.URL != stub.url("/stored.mp3") {
					t.Fatalf("episode enclosure = %+v", episode.Enclosure)
				}
				stub.setFeed("/identity.xml", feed{Title: "Fixture Cast", Items: []feedItem{
					{Title: "Renamed Stored", EnclosureURL: stub.url("/stored.mp3"), EnclosureType: "audio/mpeg", PubDate: "Thu, 02 Jan 2025 10:00:00 GMT"},
					feedEpisode("Brand New", "brand-new-guid", deadURL(t), "Fri, 03 Jan 2025 10:00:00 GMT"),
				}}.document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/identity.xml")}, "lastEpisodeCheck": 0})
				episodes := checkNewEpisodes(t, e, e.IDs["podcast"], "10")
				if len(episodes) != 1 || episodes[0].Title != "Brand New" {
					t.Fatalf("episodes = %+v, want only the new episode", episodes)
				}
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewIDInv, Case: "normal", State: "initial", Assertion: "checknew-new-episode-is-downloaded-with-its-guid",
			Sources: []string{checkNewMgrSrc, "server/managers/PodcastManager.js:206-262", episodeModelSrc},
			Claim:   "A reported new episode is downloaded into the podcast folder and stored as a podcast episode whose guid, enclosure URL, title and publication time come from the feed and whose downloaded audio file is a library file of the item.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setBytes("/d.mp3", "audio/mpeg", fixtureEpisodeMP3(t, e))
				stub.setFeed("/download.xml", feed{Title: "Download Show", Items: []feedItem{
					feedEpisode("Downloaded Episode", "download-guid", stub.url("/d.mp3"), "Sun, 01 Jun 2025 09:00:00 GMT"),
				}}.document())
				folderID, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				_, item := createPodcast(t, e, e.Token(t, "root"), createBody(e.IDs["podcastLibrary"], folderID, filepath.Join(folderPath, "Download Show"), metadataOnly(map[string]any{
					"title": "Download Show", "author": "Download Author", "feedUrl": stub.url("/download.xml"),
				})))
				patchMedia(t, e, item.ID, map[string]any{"lastEpisodeCheck": 0})
				episodes := checkNewEpisodes(t, e, item.ID, "3")
				if len(episodes) != 1 || episodes[0].GUID == nil || *episodes[0].GUID != "download-guid" {
					t.Fatalf("checknew episodes = %+v", episodes)
				}
				harness.Eventually(t, createWait, "the episode download to create its row", func() bool {
					return e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", item.Media.ID) == 1
				})
				rows := e.Rows(t, "SELECT id, title, enclosureURL, enclosureType, pubDate, publishedAt, extraData FROM podcastEpisodes WHERE podcastId = ?", item.Media.ID)
				stored := rows[0]
				requireString(t, stored, "title", "Downloaded Episode")
				requireString(t, stored, "enclosureURL", stub.url("/d.mp3"))
				requireString(t, stored, "enclosureType", "audio/mpeg")
				requireString(t, stored, "pubDate", "Sun, 01 Jun 2025 09:00:00 GMT")
				extra, _ := stored["extraData"].(string)
				if !strings.Contains(extra, `"guid":"download-guid"`) {
					t.Fatalf("stored extraData = %q", extra)
				}
				read := itemOf(t, e, item.ID)
				if len(read.Media.Episodes) != 1 {
					t.Fatalf("%d episodes after the download", len(read.Media.Episodes))
				}
				episode := read.Media.Episodes[0]
				requireValue(t, "episode guid", episode.GUID, "download-guid")
				requireValue(t, "episode title", episode.Title, "Downloaded Episode")
				if episode.Enclosure == nil || episode.Enclosure.URL != stub.url("/d.mp3") {
					t.Fatalf("episode enclosure = %+v", episode.Enclosure)
				}
				if len(episode.AudioFile) == 0 || len(read.LibraryFiles) != 1 {
					t.Fatalf("audio file = %s, library files = %+v", episode.AudioFile, read.LibraryFiles)
				}
				if info, err := filepath.Glob(filepath.Join(item.Path, "*.mp3")); err != nil || len(info) != 1 {
					t.Fatalf("downloaded files = %v (%v)", info, err)
				}
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewIDInv, Case: "edge", State: "repeat", Assertion: "checknew-guid-identity-survives-title-and-url-change",
			Sources: []string{checkNewModelSrc, episodeModelSrc},
			Claim:   "After a download, a feed episode with the same guid but a new title and enclosure URL is still not new and the stored episode keeps its downloaded title and enclosure.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/download2.xml", feed{Title: "Download Show", Items: []feedItem{
					feedEpisode("Renamed Episode", "download-guid", stub.url("/d2.mp3"), "Sun, 01 Jun 2025 09:00:00 GMT"),
				}}.document())
				items := e.Rows(t, "SELECT id, path, mediaId FROM libraryItems WHERE path LIKE ?", filepath.Join(e.Media, "podcasts", "Download Show"))
				if len(items) != 1 {
					t.Fatalf("%d download show items", len(items))
				}
				itemID, _ := items[0]["id"].(string)
				patchMedia(t, e, itemID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/download2.xml")}, "lastEpisodeCheck": 0})
				episodes := checkNewEpisodes(t, e, itemID, "3")
				if len(episodes) != 0 {
					t.Fatalf("a known guid was reported as new: %+v", episodes)
				}
				read := itemOf(t, e, itemID)
				if len(read.Media.Episodes) != 1 {
					t.Fatalf("%d stored episodes", len(read.Media.Episodes))
				}
				episode := read.Media.Episodes[0]
				requireValue(t, "stored title", episode.Title, "Downloaded Episode")
				if episode.Enclosure == nil || !strings.HasSuffix(episode.Enclosure.URL, "/d.mp3") {
					t.Fatalf("stored enclosure = %+v", episode.Enclosure)
				}
			},
		},
		{
			Requirement: checkNewReq, Invariant: checkNewDownInv, Case: "error", State: "failure", Assertion: "checknew-unreachable-feed-still-advances",
			Sources: []string{checkNewMgrSrc},
			Claim:   "An unreachable feed reports no episodes but still moves lastEpisodeCheck forward, emits item_updated and creates no episode row.",
			Run: func(t *testing.T, e *harness.Env) {
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": deadURL(t)}})
				before := takeSnapshot(t, e)
				previous := lastEpisodeCheck(t, e, e.IDs["podcast"])
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				episodes := checkNewEpisodes(t, e, e.IDs["podcast"], "3")
				if len(episodes) != 0 {
					t.Fatalf("episodes = %+v", episodes)
				}
				if after := lastEpisodeCheck(t, e, e.IDs["podcast"]); after <= previous {
					t.Fatalf("lastEpisodeCheck = %d, want after %d", after, previous)
				}
				socket.Wait(t, mark, "item_updated", eventTimeout, itemIDMatcher(e.IDs["podcast"]))
				before.requireUnchanged(t, e, "a failed episode check")
			},
		},
	}
}

// itemIDMatcher matches an item event whose payload id is the item.
func itemIDMatcher(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args[0], &item); err != nil {
			return false
		}
		return item.ID == id
	}
}

// podcastIDOf returns the podcast media id of a library item.
func podcastIDOf(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	return itemOf(t, e, itemID).Media.ID
}

// episodeOf returns one episode of an item by id.
func episodeOf(t *testing.T, item libraryItemJSON, id string) episodeJSON {
	t.Helper()
	for _, episode := range item.Media.Episodes {
		if episode.ID == id {
			return episode
		}
	}
	t.Fatalf("item %s has no episode %s", item.ID, id)
	return episodeJSON{}
}
