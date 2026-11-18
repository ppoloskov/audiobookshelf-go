package podcast_episodes

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	podcastDownloadsReq  = "route-r154"
	podcastDownloadsInv  = "INV-ROUTE-R154-CONTRACT"
	podcastDownloadsAuth = "INV-ROUTE-R154-AUTH-001"
	podcastDownloadsSrc  = "server/controllers/PodcastController.js:320-330"
	podcastDownloadsMgr  = "server/managers/PodcastManager.js:36-38"
)

func podcastDownloadScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: podcastDownloadsReq, Invariant: podcastDownloadsInv, Case: "normal", State: "stateless", Assertion: "podcast-downloads-lists-waiting-in-order",
			Sources: []string{podcastDownloadsSrc, podcastDownloadsMgr},
			Claim:   "GET /api/podcasts/:id/downloads lists only the item's waiting downloads in queue order (not the running one), answers an empty array when the queue drains, and ignores download entries of another item.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Podcast Downloads Show")
				urls := startSlowDownload(t, e, show.ID, "podcast", "Running Zero", "Waiting One", "Waiting Two")
				downloads := queueOf(t, e, show.ID)
				if len(downloads.Downloads) != 2 {
					t.Fatalf("%d waiting downloads, want 2: %+v", len(downloads.Downloads), downloads.Downloads)
				}
				if downloads.Downloads[0].URL != urls[1] || downloads.Downloads[1].URL != urls[2] {
					t.Fatalf("queue order = %+v, want %s before %s", downloads.Downloads, urls[1], urls[2])
				}
				if downloads.Downloads[0].ID == "" || downloads.Downloads[0].IsFinished {
					t.Fatalf("waiting download = %+v", downloads.Downloads[0])
				}
				if other := queueOf(t, e, e.IDs["podcast"]); len(other.Downloads) != 0 {
					t.Fatalf("fixture podcast lists another item's downloads: %+v", other.Downloads)
				}
				waitIdle(t, e)
				if got := e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID); got != 3 {
					t.Fatalf("%d stored episodes after the downloads, want 3", got)
				}
			},
		},
		{
			Requirement: podcastDownloadsReq, Invariant: podcastDownloadsInv, Case: "edge", State: "stateless", Assertion: "podcast-downloads-empty-is-an-array",
			Sources: []string{podcastDownloadsSrc},
			Claim:   "A podcast library item with no waiting episode downloads answers {downloads: []} with an empty array, not null.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Podcast Downloads Empty Show")
				got := queueOf(t, e, show.ID)
				if got.Downloads == nil || len(got.Downloads) != 0 {
					t.Fatalf("downloads = %+v, want []", got.Downloads)
				}
			},
		},
		{
			Requirement: podcastDownloadsReq, Invariant: podcastDownloadsAuth, Case: "error", State: "stateless", Assertion: "podcast-downloads-rejections-change-nothing",
			Sources: []string{episodeMidSrc, podcastDownloadsSrc},
			Claim:   "GET /api/podcasts/:id/downloads answers 401 without a token, 403 for a user without access to the library, 404 for an unknown item, 500 for a book item and 200 for a non-admin listener with access; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "podcast-downloads-restricted")
				path := "/api/podcasts/" + e.IDs["podcast"] + "/downloads"
				wantStatus(t, "anonymous", e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "restricted", e.Get(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "unknown item", e.Get(t, e.Token(t, "root"), "/api/podcasts/not-an-item/downloads"), http.StatusNotFound)
				wantStatus(t, "book item", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/downloads"), http.StatusInternalServerError)
				wantStatus(t, "listener", e.Get(t, e.Token(t, "listener"), path), http.StatusOK)
				before.requireUnchanged(t, e, "rejected podcast downloads requests")
			},
		},
	}
}
