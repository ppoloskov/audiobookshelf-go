package podcast_feed

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	searchReq     = "route-r156"
	searchInv     = "INV-R156-MATCH-001"
	searchAuthInv = "INV-R156-AUTH-001"
	searchSrc     = "server/controllers/PodcastController.js:336-358"
	searchUtilSrc = "server/utils/podcastUtils.js:412-456"
)

// episodeMatch is one search result: the feed episode that matched.
type episodeMatch struct {
	Episode rssEpisode `json:"episode"`
}

// episodeMatches is the body of GET /api/podcasts/:id/search-episode.
type episodeMatches struct {
	Episodes []episodeMatch `json:"episodes"`
}

// searchEpisodes searches the podcast feed for a title through the API.
func searchEpisodes(t *testing.T, e *harness.Env, token, itemID, title string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: "/api/podcasts/" + itemID + "/search-episode", Token: token, Query: queryValues("title", title)})
}

// matchedTitles returns the titles of a search answer.
func matchedTitles(t *testing.T, resp harness.Response) []string {
	t.Helper()
	var matches episodeMatches
	if err := json.Unmarshal(resp.Body, &matches); err != nil {
		t.Fatalf("decode search: %v: %.400s", err, resp.Body)
	}
	titles := make([]string, 0, len(matches.Episodes))
	for _, match := range matches.Episodes {
		titles = append(titles, match.Episode.Title)
	}
	return titles
}

// searchFeed is the feed the episode search matches against.
func searchFeed(stub *feedStub) feed {
	return feed{
		Title: "Fixture Cast",
		Items: []feedItem{
			{
				Title: "Episode One", Subtitle: "The first", GUID: "search-1",
				EnclosureURL: stub.url("/search-1.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673",
				PubDate: "Thu, 02 Jan 2025 10:00:00 GMT", Duration: "10:05",
			},
			{
				Title: "Episode Two", Subtitle: "The second", GUID: "search-2",
				EnclosureURL: stub.url("/search-2.mp3"), EnclosureType: "audio/mpeg",
				PubDate: "Fri, 03 Jan 2025 10:00:00 GMT", Duration: "20:00",
			},
			{
				Title: "Bonus: Episode One Trailer", Subtitle: "",
				EnclosureURL: stub.url("/search-3.mp3"), EnclosureType: "audio/mpeg",
				PubDate: "Sat, 04 Jan 2025 10:00:00 GMT",
			},
			{
				Title: "Überblick", Subtitle: "Umlaut",
				EnclosureURL: stub.url("/search-4.mp3"), EnclosureType: "audio/mpeg",
				PubDate: "Sun, 05 Jan 2025 10:00:00 GMT",
			},
		},
	}
}

func searchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: searchReq, Invariant: searchInv, Case: "normal", State: "stateless", Assertion: "search-episode-matches-feed-titles",
			Sources: []string{searchSrc, searchUtilSrc},
			Claim:   "GET /api/podcasts/:id/search-episode fetches the podcast feed and answers its episodes ordered by match quality for the title query, each result carrying the complete feed episode with title, subtitle, enclosure, guid, duration and publication time.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/search.xml", searchFeed(stub).document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/search.xml")}})
				resp := searchEpisodes(t, e, e.Token(t, "root"), e.IDs["podcast"], "Episode One")
				wantStatus(t, "search", resp, http.StatusOK)
				matches := decode[episodeMatches](t, resp.Body)
				titles := matchedTitles(t, resp)
				want := []string{"Episode One", "Bonus: Episode One Trailer", "Episode Two"}
				if len(titles) != len(want) {
					t.Fatalf("matches = %v, want %v", titles, want)
				}
				for i, title := range want {
					if titles[i] != title {
						t.Fatalf("matches = %v, want %v", titles, want)
					}
				}
				first := matches.Episodes[0].Episode
				requireValue(t, "title", first.Title, "Episode One")
				requireValue(t, "subtitle", first.Subtitle, "The first")
				requireValue(t, "enclosure", first.Enclosure.URL, stub.url("/search-1.mp3"))
				requireValue(t, "enclosure type", first.Enclosure.Type, "audio/mpeg")
				requireValue(t, "guid", first.GUID, "search-1")
				requireValue(t, "duration", first.Duration, "10:05")
				if first.DurationSeconds == nil || *first.DurationSeconds != 605 {
					t.Fatalf("durationSeconds = %v", first.DurationSeconds)
				}
				if first.PublishedAt == nil || *first.PublishedAt != 1735812000000 {
					t.Fatalf("publishedAt = %v", first.PublishedAt)
				}
				lowercase := searchEpisodes(t, e, e.Token(t, "root"), e.IDs["podcast"], "episode one")
				wantStatus(t, "lowercase search", lowercase, http.StatusOK)
				if got := matchedTitles(t, lowercase); len(got) != len(want) {
					t.Fatalf("a lower-case query returned %v, want %v", got, want)
				}
			},
		},
		{
			Requirement: searchReq, Invariant: searchInv, Case: "edge", State: "stateless", Assertion: "search-episode-edge-queries",
			Sources: []string{searchSrc, searchUtilSrc},
			Claim:   "A query that matches no feed title answers an empty array, a diacritic title matches its accented episode, an unreachable feed answers an empty array, and a search changes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newFeedStub(t)
				stub.setFeed("/search.xml", searchFeed(stub).document())
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/search.xml")}})
				before := takeSnapshot(t, e)
				resp := searchEpisodes(t, e, e.Token(t, "root"), e.IDs["podcast"], "Grand Finale")
				wantStatus(t, "no match", resp, http.StatusOK)
				if titles := matchedTitles(t, resp); len(titles) != 0 {
					t.Fatalf("no-match query returned %v", titles)
				}
				umlaut := searchEpisodes(t, e, e.Token(t, "root"), e.IDs["podcast"], "Überblick")
				wantStatus(t, "umlaut", umlaut, http.StatusOK)
				if titles := matchedTitles(t, umlaut); len(titles) != 1 || titles[0] != "Überblick" {
					t.Fatalf("umlaut query returned %v", titles)
				}
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"feedUrl": deadURL(t)}})
				unreachable := searchEpisodes(t, e, e.Token(t, "root"), e.IDs["podcast"], "Episode One")
				wantStatus(t, "unreachable feed", unreachable, http.StatusOK)
				if titles := matchedTitles(t, unreachable); len(titles) != 0 {
					t.Fatalf("unreachable feed returned %v", titles)
				}
				before.requireUnchanged(t, e, "an episode search")
			},
		},
		{
			Requirement: searchReq, Invariant: searchAuthInv, Case: "error", State: "stateless", Assertion: "search-episode-rejections",
			Sources: []string{searchSrc, "server/controllers/PodcastController.js:560-585"},
			Claim:   "GET /api/podcasts/:id/search-episode answers 401 without a token, 403 for an admin who cannot access the item's library, 404 for an unknown item, 500 for an item that is not a podcast and for a missing or repeated title parameter, and 400 for a podcast without a feed URL.",
			Run: func(t *testing.T, e *harness.Env) {
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Path: "/api/podcasts/" + e.IDs["podcast"] + "/search-episode", Query: queryValues("title", "Episode")}), http.StatusUnauthorized)
				wantStatus(t, "unknown item", searchEpisodes(t, e, e.Token(t, "root"), "not-an-item", "Episode"), http.StatusNotFound)
				restricted := createAdmin(t, e, "search-restricted-admin", e.IDs["library"])
				wantStatus(t, "admin without the library", searchEpisodes(t, e, restricted, e.IDs["podcast"], "Episode"), http.StatusForbidden)
				wantStatus(t, "book item", searchEpisodes(t, e, e.Token(t, "root"), e.IDs["novel"], "Episode"), http.StatusInternalServerError)
				_, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				noFeed := itemOfByPath(t, e, folderPath+"/Minimal Show")
				wantStatus(t, "no feed url", searchEpisodes(t, e, e.Token(t, "root"), noFeed.ID, "Episode"), http.StatusBadRequest)
				wantStatus(t, "no feed url before the title check", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+noFeed.ID+"/search-episode"), http.StatusBadRequest)
				wantStatus(t, "missing title", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/search-episode"), http.StatusInternalServerError)
				wantStatus(t, "repeated title", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/search-episode?title=a&title=b"), http.StatusInternalServerError)
			},
		},
	}
}
