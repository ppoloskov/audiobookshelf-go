package podcast_episodes

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	episodeGetReq  = "route-r159"
	episodeGetInv  = "INV-ROUTE-R159-CONTRACT"
	episodeGetAuth = "INV-ROUTE-R159-AUTH-001"
	episodeGetSrc  = "server/controllers/PodcastController.js:479-495"
	episodeMidSrc  = "server/controllers/PodcastController.js:560-585"
	episodeJsonSrc = "server/models/PodcastEpisode.js:193-228"
)

// getEpisode reads one episode through the API.
func getEpisode(t *testing.T, e *harness.Env, itemID, episodeID string) episodeJSON {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/podcasts/"+itemID+"/episode/"+episodeID)
	wantStatus(t, "get episode", resp, http.StatusOK)
	return decode[episodeJSON](t, resp.Body)
}

func episodeGetScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: episodeGetReq, Invariant: episodeGetInv, Case: "normal", State: "stateless", Assertion: "episode-get-returns-old-episode-json",
			Sources: []string{episodeGetSrc, episodeJsonSrc},
			Claim:   "GET /api/podcasts/:id/episode/:episodeId returns the stored episode's old JSON with its libraryItemId, podcastId, nullable enclosure and guid, chapters, audioFile, publication and update times.",
			Run: func(t *testing.T, e *harness.Env) {
				podcast := itemOf(t, e, e.IDs["podcast"])
				first := getEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"])
				if first.ID != e.IDs["ep1"] || first.LibraryItemID != e.IDs["podcast"] || first.PodcastID != podcast.Media.ID {
					t.Fatalf("episode identity = %+v", first)
				}
				requireValue(t, "title", first.Title, "Pilot Episode")
				requireValue(t, "description", first.Description, "The first episode of the fixture cast.")
				requireValue(t, "season", first.Season, "1")
				requireValue(t, "episode", first.Episode, "1")
				requireValue(t, "pubDate", first.PubDate, "2025-03-01")
				if first.Subtitle != nil || first.EpisodeType != nil || first.Index != nil || first.OldEpisodeID != nil {
					t.Fatalf("nullable fields = subtitle %v episodeType %v index %v oldEpisodeId %v", first.Subtitle, first.EpisodeType, first.Index, first.OldEpisodeID)
				}
				if first.Enclosure != nil || first.GUID != nil {
					t.Fatalf("enclosure/guid = %+v/%v, want null", first.Enclosure, first.GUID)
				}
				if first.PublishedAt == nil || *first.PublishedAt != 1740787200000 {
					t.Fatalf("publishedAt = %v, want 1740787200000", first.PublishedAt)
				}
				if first.AddedAt <= 0 || first.UpdatedAt < first.AddedAt {
					t.Fatalf("addedAt %d updatedAt %d", first.AddedAt, first.UpdatedAt)
				}
				if first.AudioFile == nil || first.AudioFile.Metadata.Filename != "episode-1.mp3" || first.AudioFile.Metadata.Ext != ".mp3" {
					t.Fatalf("audioFile = %+v", first.AudioFile)
				}
				if first.Chapters == nil || len(first.Chapters) != 0 {
					t.Fatalf("chapters = %+v, want []", first.Chapters)
				}
				second := getEpisode(t, e, e.IDs["podcast"], e.IDs["ep2"])
				requireValue(t, "second title", second.Title, "Second Outing")
				requireValue(t, "second description", second.Description, "A <b>second</b> episode.")
				if second.PublishedAt == nil || *second.PublishedAt != 1741392000000 {
					t.Fatalf("second publishedAt = %v", second.PublishedAt)
				}
			},
		},
		{
			Requirement: episodeGetReq, Invariant: episodeGetAuth, Case: "error", State: "stateless", Assertion: "episode-get-rejections-change-nothing",
			Sources: []string{episodeMidSrc, episodeGetSrc},
			Claim:   "GET /api/podcasts/:id/episode/:episodeId answers 401 without a token, 404 for an unknown library item before the access check, 403 for a user without access to the item's library, 500 for a book item and 404 for an episode of another item; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "episode-get-restricted")
				path := "/api/podcasts/" + e.IDs["podcast"] + "/episode/" + e.IDs["ep1"]
				wantStatus(t, "anonymous", e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "unknown item before access", e.Get(t, restricted, "/api/podcasts/not-an-item/episode/"+e.IDs["ep1"]), http.StatusNotFound)
				wantStatus(t, "restricted", e.Get(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "book item", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/episode/"+e.IDs["ep1"]), http.StatusInternalServerError)
				wantStatus(t, "unknown episode", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/episode/not-an-episode"), http.StatusNotFound)
				wantStatus(t, "other item's episode", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/episode/"+e.IDs["listener"]), http.StatusNotFound)
				before.requireUnchanged(t, e, "rejected episode reads")
				if got := getEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"]); got.ID != e.IDs["ep1"] {
					t.Fatalf("episode changed after rejections: %+v", got)
				}
			},
		},
	}
}

// episodeGetEdgeScenarios reads an episode that was created by a download, so
// the enclosure, guid and publishedAt variants are asserted too. It runs after
// the download scenarios have created that episode.
func episodeGetEdgeScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: episodeGetReq, Invariant: episodeGetInv, Case: "edge", State: "stateless", Assertion: "episode-get-reads-a-downloaded-episode",
			Sources: []string{episodeGetSrc, episodeJsonSrc},
			Claim:   "GET /api/podcasts/:id/episode/:episodeId answers a downloaded episode with its non-null enclosure URL, type and string length, its guid and millisecond publishedAt, and an empty chapters array.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Download Episodes Show")
				rows := e.Rows(t, "SELECT id, enclosureURL FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%episodes-a%")
				if len(rows) != 1 {
					t.Fatalf("%d downloaded episodes for the edge read", len(rows))
				}
				episodeID, _ := rows[0]["id"].(string)
				episode := getEpisode(t, e, show.ID, episodeID)
				requireValue(t, "guid", episode.GUID, "episodes-a")
				requireValue(t, "title", episode.Title, "Episode A")
				if episode.Enclosure == nil || episode.Enclosure.URL == "" {
					t.Fatalf("enclosure = %+v", episode.Enclosure)
				}
				requireValue(t, "enclosure type", episode.Enclosure.Type, "audio/mpeg")
				requireValue(t, "enclosure length", episode.Enclosure.Length, "96673")
				if episode.PublishedAt == nil || *episode.PublishedAt != 1746439200000 {
					t.Fatalf("publishedAt = %v, want 1746439200000", episode.PublishedAt)
				}
				if episode.AudioFile == nil || len(episode.Chapters) != 0 {
					t.Fatalf("audioFile/chapters = %+v/%+v", episode.AudioFile, episode.Chapters)
				}
			},
		},
	}
}
