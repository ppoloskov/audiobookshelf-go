package podcast_episodes

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	clearQueueReq  = "route-r155"
	clearQueueInv  = "INV-ROUTE-R155-CONTRACT"
	clearQueueAuth = "INV-ROUTE-R155-AUTH-001"
	clearQueueSrc  = "server/controllers/PodcastController.js:299-318"
	clearQueueMgr  = "server/managers/PodcastManager.js:40-52"
	clearQueueCli  = "client/pages/item/_id/index.vue:460-474"
)

// clearQueue calls the clear-queue route.
func clearQueue(t *testing.T, e *harness.Env, token, itemID string) harness.Response {
	t.Helper()
	return e.Get(t, token, "/api/podcasts/"+itemID+"/clear-queue")
}

func clearQueueScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: clearQueueReq, Invariant: clearQueueInv, Case: "edge", State: "stateless", Assertion: "clear-queue-empty-queue-is-silent",
			Sources: []string{clearQueueSrc, clearQueueMgr},
			Claim:   "GET /api/podcasts/:id/clear-queue with an empty queue answers 200 and emits no episode_download_queue_cleared to the admin, the listener or an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Clear Silent Show")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				resp := clearQueue(t, e, e.Token(t, "root"), show.ID)
				wantStatus(t, "clear empty queue", resp, http.StatusOK)
				audience.receive(t, "episode_download_queue_cleared", nil, nil, []string{"admin", "listener", "anonymous"})
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 0 {
					t.Fatalf("downloads = %+v", got.Downloads)
				}
			},
		},
		{
			Requirement: clearQueueReq, Invariant: clearQueueInv, Case: "normal", State: "stateless", Assertion: "clear-queue-drops-only-one-items-waiting-downloads",
			Sources: []string{clearQueueSrc, clearQueueMgr, clearQueueCli},
			Claim:   "GET /api/podcasts/:id/clear-queue answers 200, emits episode_download_queue_cleared with that library item id to the admin and the listener, drops only that item's waiting downloads, and leaves the running download and another item's queue untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				first := ensureShow(t, e, "Clear Show A")
				second := ensureShow(t, e, "Clear Show B")
				startSlowDownload(t, e, first.ID, "clear-a", "A Running", "A Waiting")
				startSlowDownload(t, e, second.ID, "clear-b", "B Waiting")
				if got := queueOf(t, e, first.ID); len(got.Downloads) != 1 {
					t.Fatalf("first show queue before clearing = %+v", got.Downloads)
				}
				audience := connect(t, e, map[string]string{
					"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": "",
				})
				resp := clearQueue(t, e, e.Token(t, "root"), first.ID)
				wantStatus(t, "clear queue", resp, http.StatusOK)
				events := audience.receive(t, "episode_download_queue_cleared", nil,
					[]string{"admin", "listener"}, nil)
				for who, event := range events {
					if len(event.Args) != 1 {
						t.Fatalf("%s payload args = %d, want 1", who, len(event.Args))
					}
					var libraryItemID string
					if err := json.Unmarshal(event.Args[0], &libraryItemID); err != nil {
						t.Fatalf("%s payload: %v: %.200s", who, err, event.Args[0])
					}
					if libraryItemID != first.ID {
						t.Fatalf("%s payload = %q, want %q", who, libraryItemID, first.ID)
					}
				}
				if got := queueOf(t, e, first.ID); len(got.Downloads) != 0 {
					t.Fatalf("cleared show still queues %+v", got.Downloads)
				}
				if got := queueOf(t, e, second.ID); len(got.Downloads) != 1 || got.Downloads[0].URL == "" {
					t.Fatalf("other show queue = %+v, want its waiting download", got.Downloads)
				}
				audience.sockets["anonymous"].None(t, audience.marks["anonymous"], "episode_download_queue_cleared", quietWindow, nil)
				waitIdle(t, e)
			},
		},
		{
			Requirement: clearQueueReq, Invariant: clearQueueAuth, Case: "error", State: "stateless", Assertion: "clear-queue-rejections-change-nothing",
			Sources: []string{episodeMidSrc, clearQueueSrc},
			Claim:   "GET /api/podcasts/:id/clear-queue answers 401 without a token, 403 for a non-admin with access and for a user without access to the library, 404 for an unknown item before the admin check and 500 for a book item; the queue is unchanged and no event is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Clear Reject Show")
				startSlowDownload(t, e, show.ID, "clear-reject", "Held Running", "Held Waiting")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "clear-queue-restricted")
				path := "/api/podcasts/" + show.ID + "/clear-queue"
				wantStatus(t, "anonymous", e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Get(t, e.Token(t, "listener"), path), http.StatusForbidden)
				wantStatus(t, "unknown item before admin check", e.Get(t, restricted, "/api/podcasts/not-an-item/clear-queue"), http.StatusNotFound)
				wantStatus(t, "restricted", e.Get(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "book item", e.Get(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/clear-queue"), http.StatusInternalServerError)
				before.requireUnchanged(t, e, "rejected clear queue requests")
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 1 {
					t.Fatalf("queue after rejections = %+v, want the waiting download", got.Downloads)
				}
				audience.receive(t, "episode_download_queue_cleared", nil, nil, []string{"admin", "listener", "anonymous"})
				waitIdle(t, e)
			},
		},
	}
}
