package podcast_episodes

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	libraryQueueReq  = "route-r024"
	libraryQueueInv  = "INV-ROUTE-R024-CONTRACT"
	libraryQueueAuth = "INV-ROUTE-R024-AUTH-001"
	libraryQueueSrc  = "server/controllers/LibraryController.js:236-247"
	libraryQueueMgmt = "server/managers/PodcastManager.js:37-38,300-311"
	libraryQueueCli  = "client/pages/library/_library/podcast/download-queue.vue:109"
)

// liveLibraryQueue reads the library queue with a unique page value, so the
// response is not served from the ApiCacheManager cache (in-memory queue
// changes do not clear it).
func liveLibraryQueue(t *testing.T, e *harness.Env) queueDetails {
	t.Helper()
	return libraryQueueOf(t, e, e.IDs["podcastLibrary"], fmt.Sprintf("page=%d", time.Now().UnixNano()))
}

// startSlowDownload starts one download of a held audio response and queues the
// rest, returning the audio URLs.
func startSlowDownload(t *testing.T, e *harness.Env, itemID, prefix string, titles ...string) []string {
	t.Helper()
	stub := newAudioStub(t)
	audio := fixtureEpisodeMP3(t, e)
	urls := make([]string, len(titles))
	episodes := make([]map[string]any, len(titles))
	for i, title := range titles {
		path := fmt.Sprintf("/%s-%d.mp3", prefix, i)
		stub.setHeldAudio(path, audio, len(audio)/2, 1500*time.Millisecond)
		urls[i] = stub.url(path)
		episodes[i] = downloadEpisode(title, prefix+"-"+title, urls[i], "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
	}
	wantStatus(t, "download episodes", postDownloads(t, e, itemID, e.Token(t, "root"), episodes), http.StatusOK)
	return urls
}

func libraryDownloadScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: libraryQueueReq, Invariant: libraryQueueInv, Case: "edge", State: "stateless", Assertion: "library-episode-downloads-empty-queue",
			Sources: []string{libraryQueueSrc, libraryQueueMgmt, libraryQueueCli},
			Claim:   "With no episode download running or queued, GET /api/libraries/:id/episode-downloads answers {currentDownload: null, queue: []} for the podcast library and for the book library, and accepts limit and page query values without adding payload fields.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Library Queue Show")
				empty := libraryQueueOf(t, e, e.IDs["podcastLibrary"], "")
				requireEmptyQueue(t, "podcast library", empty)
				requireEmptyQueue(t, "podcast library with paging", libraryQueueOf(t, e, e.IDs["podcastLibrary"], "limit=2&page=0"))
				requireEmptyQueue(t, "podcast library with a non numeric limit", libraryQueueOf(t, e, e.IDs["podcastLibrary"], "limit=abc"))
				requireEmptyQueue(t, "book library", libraryQueueOf(t, e, e.IDs["library"], ""))
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 0 {
					t.Fatalf("show %s has downloads %+v", show.ID, got.Downloads)
				}
			},
		},
		{
			Requirement: libraryQueueReq, Invariant: libraryQueueInv, Case: "normal", State: "stateless", Assertion: "library-episode-downloads-shows-running-and-queued",
			Sources: []string{libraryQueueSrc, libraryQueueMgmt, libraryQueueCli},
			Claim:   "While one episode downloads, GET /api/libraries/:id/episode-downloads returns the running download as currentDownload and the waiting ones in queue order with the client fields, filtered to the requested library, and an empty payload after they finish.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Library Queue Show")
				// The /libraries responses are served from the API cache, which
				// only a database write clears; a media write makes this read live.
				patchMedia(t, e, show.ID, map[string]any{"tags": []string{"queue-live"}})
				urls := startSlowDownload(t, e, show.ID, "library", "Running A", "Waiting B")
				details := libraryQueueOf(t, e, e.IDs["podcastLibrary"], "")
				if details.CurrentDownload == nil || details.CurrentDownload.URL != urls[0] {
					t.Fatalf("current download = %+v, want %s", details.CurrentDownload, urls[0])
				}
				running := details.CurrentDownload
				requireValue(t, "episodeDisplayTitle", running.EpisodeDisplayTitle, "Running A")
				requireValue(t, "libraryItemId", running.LibraryItemID, show.ID)
				requireValue(t, "libraryId", running.LibraryID, e.IDs["podcastLibrary"])
				requireValue(t, "podcastTitle", running.PodcastTitle, "Library Queue Show")
				requireValue(t, "episodeType", running.EpisodeType, "full")
				if running.ID == "" || running.IsFinished || running.Failed || running.AppendRandomID || running.PodcastExplicit {
					t.Fatalf("running download flags/id = %+v", running)
				}
				if running.CreatedAt == nil || *running.CreatedAt == 0 || running.StartedAt != nil || running.FinishedAt != nil {
					t.Fatalf("running times = created %v started %v finished %v", running.CreatedAt, running.StartedAt, running.FinishedAt)
				}
				if len(details.Queue) != 1 || details.Queue[0].URL != urls[1] {
					t.Fatalf("queue = %+v, want the waiting download", details.Queue)
				}
				requireValue(t, "queued title", details.Queue[0].EpisodeDisplayTitle, "Waiting B")
				requireEmptyQueue(t, "book library during a podcast download", libraryQueueOf(t, e, e.IDs["library"], ""))
				waitIdle(t, e)
				requireEmptyQueue(t, "podcast library after the downloads", liveLibraryQueue(t, e))
			},
		},
		{
			Requirement: libraryQueueReq, Invariant: libraryQueueAuth, Case: "error", State: "stateless", Assertion: "library-episode-downloads-rejections-change-nothing",
			Sources: []string{"server/controllers/LibraryController.js:1475-1498"},
			Claim:   "GET /api/libraries/:id/episode-downloads answers 401 without a token, 403 for a user without access to the library, 404 for an unknown library, 400 for a negative limit or fractional page and 200 for a non-numeric limit coerced to zero; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Library Queue Reject Show")
				patchMedia(t, e, show.ID, map[string]any{"tags": []string{"queue-reject"}})
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "library-queue-restricted")
				path := "/api/libraries/" + e.IDs["podcastLibrary"] + "/episode-downloads"
				wantStatus(t, "anonymous", e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "restricted", e.Get(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "unknown library", e.Get(t, e.Token(t, "root"), "/api/libraries/not-a-library/episode-downloads"), http.StatusNotFound)
				wantStatus(t, "negative limit", e.Get(t, e.Token(t, "root"), path+"?limit=-1"), http.StatusBadRequest)
				wantStatus(t, "fractional page", e.Get(t, e.Token(t, "root"), path+"?page=1.5"), http.StatusBadRequest)
				coerced := e.Get(t, e.Token(t, "root"), path+"?limit=abc")
				wantStatus(t, "non numeric limit", coerced, http.StatusOK)
				requireEmptyQueue(t, "non numeric limit", decode[queueDetails](t, coerced.Body))
				before.requireUnchanged(t, e, "rejected library queue requests")
			},
		},
	}
}
