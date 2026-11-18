package podcast_episodes

import (
	"fmt"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	recentReq    = "route-r039"
	recentInv    = "INV-ROUTE-R039-CONTRACT"
	recentPerms  = "INV-ROUTE-R039-PERMS-001"
	recentAuth   = "INV-ROUTE-R039-AUTH-001"
	recentSrc    = "server/controllers/LibraryController.js:1278-1300"
	recentMidSrc = "server/controllers/LibraryController.js:1475-1498"
	recentQrySrc = "server/utils/queries/libraryItemsPodcastFilters.js:16-41,506-558"
	recentEpSrc  = "server/models/PodcastEpisode.js:193-238"
	recentCliSrc = "client/pages/library/_library/podcast/latest.vue:250"
)

// recentEpisodes reads one page of the library's recent episodes.
func recentEpisodes(t *testing.T, e *harness.Env, libraryID, query string) recentPayload {
	t.Helper()
	path := "/api/libraries/" + libraryID + "/recent-episodes"
	if query != "" {
		path += "?" + query
	}
	resp := e.Get(t, e.Token(t, "root"), path)
	wantStatus(t, "recent episodes", resp, http.StatusOK)
	return decode[recentPayload](t, resp.Body)
}

// episodeIDs lists the ids of a recent-episode page in order.
func episodeIDs(episodes []episodeJSON) []string {
	ids := make([]string, len(episodes))
	for i, episode := range episodes {
		ids[i] = episode.ID
	}
	return ids
}

// requireOrder asserts the episode ids of a page.
func requireOrder(t *testing.T, what string, episodes []episodeJSON, want ...string) {
	t.Helper()
	got := episodeIDs(episodes)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s ids = %v, want %v", what, got, want)
	}
}

func recentScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: recentReq, Invariant: recentInv, Case: "normal", State: "stateless", Assertion: "recent-episodes-newest-first-and-paginated",
			Sources: []string{recentSrc, recentMidSrc, recentQrySrc, recentEpSrc, recentCliSrc},
			Claim:   "GET /api/libraries/:id/recent-episodes returns the fixture podcast's downloaded episodes newest first with the full old episode JSON (podcast object without episodes, libraryId, audioTrack content URL), honours limit and page (limit 0 or absent returns no episodes), and answers limit and page as numbers.",
			Run: func(t *testing.T, e *harness.Env) {
				podcast := itemOf(t, e, e.IDs["podcast"])
				page0 := recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=2&page=0")
				if page0.Limit != 2 || page0.Page != 0 {
					t.Fatalf("limit/page = %d/%d, want 2/0", page0.Limit, page0.Page)
				}
				requireOrder(t, "page 0", page0.Episodes, e.IDs["ep3"], e.IDs["ep2"])
				first := page0.Episodes[0]
				if first.ID != e.IDs["ep3"] || first.LibraryItemID != e.IDs["podcast"] || first.PodcastID != podcast.Media.ID {
					t.Fatalf("episode identity = %+v", first)
				}
				requireValue(t, "title", first.Title, "Third Time")
				requireValue(t, "description", first.Description, "Episode three closes the season.")
				requireValue(t, "season", first.Season, "1")
				requireValue(t, "episode", first.Episode, "3")
				requireValue(t, "pubDate", first.PubDate, "2025-03-15")
				if first.PublishedAt == nil || *first.PublishedAt != 1741996800000 {
					t.Fatalf("publishedAt = %v, want 1741996800000", first.PublishedAt)
				}
				if first.Enclosure != nil || first.GUID != nil {
					t.Fatalf("enclosure/guid = %+v/%v, want null", first.Enclosure, first.GUID)
				}
				if first.AudioFile == nil || first.AudioTrack == nil {
					t.Fatalf("audio file/track missing: %+v / %+v", first.AudioFile, first.AudioTrack)
				}
				file := first.AudioFile.Metadata
				if file.Filename != "episode-3.mp3" || file.Ext != ".mp3" || file.Size == 0 || first.Duration <= 0 {
					t.Fatalf("audio file = %+v duration %v", file, first.Duration)
				}
				wantContent := "/api/items/" + e.IDs["podcast"] + "/file/" + first.AudioFile.Ino
				if first.AudioTrack.ContentURL != wantContent || first.AudioTrack.StartOffset != 0 || first.AudioTrack.Index != 1 {
					t.Fatalf("audio track = %+v, want contentUrl %q", first.AudioTrack, wantContent)
				}
				if first.LibraryID != e.IDs["podcastLibrary"] {
					t.Fatalf("libraryId = %q", first.LibraryID)
				}
				if first.Podcast == nil || first.Podcast.ID != podcast.Media.ID || first.Podcast.LibraryItemID != e.IDs["podcast"] {
					t.Fatalf("podcast = %+v", first.Podcast)
				}
				requireValue(t, "podcast title", first.Podcast.Metadata.Title, "Fixture Cast")
				requireValue(t, "podcast author", first.Podcast.Metadata.Author, "Fixture Host")
				if len(first.Podcast.Episodes) != 0 {
					t.Fatalf("recent episode podcast has %d nested episodes, want 0", len(first.Podcast.Episodes))
				}
				page1 := recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=2&page=1")
				if page1.Limit != 2 || page1.Page != 1 {
					t.Fatalf("page 1 limit/page = %d/%d", page1.Limit, page1.Page)
				}
				requireOrder(t, "page 1", page1.Episodes, e.IDs["ep1"])
				all := recentEpisodes(t, e, e.IDs["podcastLibrary"], "")
				if all.Limit != 0 || all.Page != 0 {
					t.Fatalf("default limit/page = %d/%d, want 0/0", all.Limit, all.Page)
				}
				if len(all.Episodes) != 0 {
					t.Fatalf("absent limit = %v, want no episodes (limit 0)", episodeIDs(all.Episodes))
				}
				zero := recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=0&page=0")
				if len(zero.Episodes) != 0 {
					t.Fatalf("limit 0 = %v, want no episodes", episodeIDs(zero.Episodes))
				}
				empty := recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=2&page=2")
				if len(empty.Episodes) != 0 {
					t.Fatalf("page beyond the end = %v", episodeIDs(empty.Episodes))
				}
			},
		},
		{
			Requirement: recentReq, Invariant: recentPerms, Case: "edge", State: "stateless", Assertion: "recent-episodes-hide-finished-and-explicit",
			Sources: []string{recentQrySrc, recentEpSrc},
			Claim:   "Recent episodes exclude a podcast episode the requesting user has finished and, for a user without explicit content access, every episode of an explicit podcast, while a user with access still sees them.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := e.Token(t, "listener")
				resp := e.Get(t, listener, "/api/libraries/"+e.IDs["podcastLibrary"]+"/recent-episodes?limit=10")
				wantStatus(t, "listener recent", resp, http.StatusOK)
				listenerPage := decode[recentPayload](t, resp.Body)
				requireOrder(t, "listener", listenerPage.Episodes, e.IDs["ep3"], e.IDs["ep1"])

				finisher := e.Token(t, "finisher")
				resp = e.Get(t, finisher, "/api/libraries/"+e.IDs["podcastLibrary"]+"/recent-episodes?limit=10")
				wantStatus(t, "finisher recent", resp, http.StatusOK)
				requireOrder(t, "finisher", decode[recentPayload](t, resp.Body).Episodes, e.IDs["ep3"], e.IDs["ep2"], e.IDs["ep1"])

				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"explicit": true}})
				requireOrder(t, "root after explicit", recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=10").Episodes, e.IDs["ep3"], e.IDs["ep2"], e.IDs["ep1"])
				resp = e.Get(t, finisher, "/api/libraries/"+e.IDs["podcastLibrary"]+"/recent-episodes?limit=10")
				wantStatus(t, "finisher explicit recent", resp, http.StatusOK)
				if hidden := decode[recentPayload](t, resp.Body).Episodes; len(hidden) != 0 {
					t.Fatalf("user without explicit access sees %v", episodeIDs(hidden))
				}
				resp = e.Get(t, listener, "/api/libraries/"+e.IDs["podcastLibrary"]+"/recent-episodes?limit=10")
				wantStatus(t, "listener explicit recent", resp, http.StatusOK)
				requireOrder(t, "listener after explicit", decode[recentPayload](t, resp.Body).Episodes, e.IDs["ep3"], e.IDs["ep1"])
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"metadata": map[string]any{"explicit": false}})
			},
		},
		{
			Requirement: recentReq, Invariant: recentPerms, Case: "edge", State: "stateless", Assertion: "recent-episodes-tag-filters",
			Sources: []string{recentQrySrc},
			Claim:   "When the podcast is tagged \"premium\", a deny-mode user whose selected tags include \"premium\" and an allow-mode user whose selected tags do not include it both get no recent episodes, while an allow-mode user whose selected tags include it gets the episodes.",
			Run: func(t *testing.T, e *harness.Env) {
				blocked := createUser(t, e, "recent-tag-blocked", map[string]any{
					"accessAllTags": false, "selectedTagsNotAccessible": true, "itemTagsSelected": []string{"premium"},
				})
				allowed := createUser(t, e, "recent-tag-allowed", map[string]any{
					"accessAllTags": false, "selectedTagsNotAccessible": false, "itemTagsSelected": []string{"premium"},
				})
				other := createUser(t, e, "recent-tag-other", map[string]any{
					"accessAllTags": false, "selectedTagsNotAccessible": false, "itemTagsSelected": []string{"other"},
				})
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"tags": []string{"premium"}})
				cases := []struct {
					name  string
					token string
					want  int
				}{
					{"deny-mode selected tag", blocked, 0},
					{"allow-mode matching tag", allowed, 3},
					{"allow-mode other tag", other, 0},
				}
				for _, testCase := range cases {
					resp := e.Get(t, testCase.token, "/api/libraries/"+e.IDs["podcastLibrary"]+"/recent-episodes?limit=10")
					wantStatus(t, "recent episodes "+testCase.name, resp, http.StatusOK)
					if got := len(decode[recentPayload](t, resp.Body).Episodes); got != testCase.want {
						t.Fatalf("%s sees %d episodes, want %d", testCase.name, got, testCase.want)
					}
				}
				requireOrder(t, "root with tag", recentEpisodes(t, e, e.IDs["podcastLibrary"], "limit=10").Episodes, e.IDs["ep3"], e.IDs["ep2"], e.IDs["ep1"])
				patchMedia(t, e, e.IDs["podcast"], map[string]any{"tags": []string{}})
			},
		},
		{
			Requirement: recentReq, Invariant: recentAuth, Case: "error", State: "stateless", Assertion: "recent-episodes-rejections-change-nothing",
			Sources: []string{recentMidSrc},
			Claim:   "GET /api/libraries/:id/recent-episodes answers 401 without a token, 403 for a user with no access to a podcast library, 404 for a book library and for an unknown library, and 400 for a negative or fractional limit or page; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "recent-restricted")
				path := "/api/libraries/" + e.IDs["podcastLibrary"] + "/recent-episodes"
				wantStatus(t, "anonymous", e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "restricted", e.Get(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "book library", e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"/recent-episodes"), http.StatusNotFound)
				wantStatus(t, "unknown library", e.Get(t, e.Token(t, "root"), "/api/libraries/not-a-library/recent-episodes"), http.StatusNotFound)
				wantStatus(t, "negative limit", e.Get(t, e.Token(t, "root"), path+"?limit=-1"), http.StatusBadRequest)
				wantStatus(t, "fractional page", e.Get(t, e.Token(t, "root"), path+"?page=1.5"), http.StatusBadRequest)
				wantStatus(t, "restricted before validation", e.Get(t, restricted, path+"?limit=-1"), http.StatusForbidden)
				before.requireUnchanged(t, e, "rejected recent episodes requests")
			},
		},
	}
}
