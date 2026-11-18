package podcast_episodes

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	socketAdded      = "socket-outgoing-episode-added"
	socketQueued     = "socket-outgoing-episode-download-queued"
	socketStarted    = "socket-outgoing-episode-download-started"
	socketFinished   = "socket-outgoing-episode-download-finished"
	socketCleared    = "socket-outgoing-episode-download-queue-cleared"
	invAdded         = "INV-SOCKET-OUTGOING-EPISODE-ADDED"
	invQueued        = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-QUEUED"
	invStarted       = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-STARTED"
	invFinished      = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-FINISHED"
	invCleared       = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-QUEUE-CLEARED"
	invAddedAud      = "INV-SOCKET-OUTGOING-EPISODE-ADDED-AUDIENCE-001"
	invQueuedAud     = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-QUEUED-AUDIENCE-001"
	invStartedAud    = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-STARTED-AUDIENCE-001"
	invFinishedAud   = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-FINISHED-AUDIENCE-001"
	invClearedAud    = "INV-SOCKET-OUTGOING-EPISODE-DOWNLOAD-QUEUE-CLEARED-AUDIENCE-001"
	socketMgrSrc     = "server/managers/PodcastManager.js:257"
	socketQueuedSrc  = "server/managers/PodcastManager.js:85"
	socketStartedSrc = "server/managers/PodcastManager.js:104"
	socketFinishSrc  = "server/managers/PodcastManager.js:177"
	socketClearSrc   = "server/managers/PodcastManager.js:50"
	socketAuthSrc    = "server/SocketAuthority.js:57-65"
	socketCliSrc     = "client/pages/item/_id/index.vue:790-810"
)

// requireDownloadPayload asserts the fields a client reads on a download event.
func requireDownloadPayload(t *testing.T, item downloadItem, title, audioURL, itemID, libraryID, podcastTitle string) {
	t.Helper()
	requireValue(t, "episodeDisplayTitle", item.EpisodeDisplayTitle, title)
	if item.URL != audioURL || item.ID == "" {
		t.Fatalf("download payload id/url = %q/%q, want url %q", item.ID, item.URL, audioURL)
	}
	requireValue(t, "libraryItemId", item.LibraryItemID, itemID)
	requireValue(t, "libraryId", item.LibraryID, libraryID)
	requireValue(t, "podcastTitle", item.PodcastTitle, podcastTitle)
	requireValue(t, "season", item.Season, "2")
	requireValue(t, "episode", item.Episode, "7")
	requireValue(t, "episodeType", item.EpisodeType, "full")
	if item.PublishedAt == nil || *item.PublishedAt != 1746439200000 {
		t.Fatalf("publishedAt = %v, want 1746439200000", item.PublishedAt)
	}
	if item.PodcastExplicit || item.AppendRandomID {
		t.Fatalf("download flags = %+v", item)
	}
}

// requireEpisodeAdded asserts the fields a client reads on episode_added.
func requireEpisodeAdded(t *testing.T, added episodeJSON, itemID, libraryID, podcastID, title, guid, audioURL string, publishedAt int64) {
	t.Helper()
	if added.ID == "" || added.LibraryItemID != itemID || added.PodcastID != podcastID {
		t.Fatalf("episode_added identity = %+v", added)
	}
	requireValue(t, "title", added.Title, title)
	requireValue(t, "guid", added.GUID, guid)
	if added.Enclosure == nil || added.Enclosure.URL != audioURL {
		t.Fatalf("episode_added enclosure = %+v, want %q", added.Enclosure, audioURL)
	}
	if added.PublishedAt == nil || *added.PublishedAt != publishedAt {
		t.Fatalf("episode_added publishedAt = %v, want %d", added.PublishedAt, publishedAt)
	}
	if added.AudioFile == nil || added.AudioFile.Metadata.Path == "" || added.Size <= 0 || added.Duration <= 0 {
		t.Fatalf("episode_added audio = %+v size %d duration %v", added.AudioFile, added.Size, added.Duration)
	}
	if added.LibraryItem == nil || added.LibraryItem.ID != itemID || added.LibraryItem.LibraryID != libraryID {
		t.Fatalf("episode_added libraryItem = %+v", added.LibraryItem)
	}
	found := false
	for _, episode := range added.LibraryItem.Media.Episodes {
		if episode.ID == added.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("episode_added library item does not list the episode")
	}
}

func socketScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: socketQueued, Invariant: invQueued, Case: "normal", State: "initial", Assertion: "episode-socket-queued-payload-and-audience",
			Sources: []string{socketQueuedSrc, socketAuthSrc, socketCliSrc},
			Claim:   "Queueing an episode emits episode_download_queued with the downloading episode's client fields to the admin and the listener sockets, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Queued Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running, waiting := stub.url("/queued-a.mp3"), stub.url("/queued-b.mp3")
				stub.setHeldAudio("/queued-a.mp3", audio, len(audio)/2, 1500*time.Millisecond)
				stub.setAudio("/queued-b.mp3", audio)
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				episodes := []map[string]any{
					downloadEpisode("Queued A", "queued-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Queued B", "queued-b", waiting, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), episodes), http.StatusOK)
				events := audience.receive(t, "episode_download_queued", downloadMatcher(waiting),
					[]string{"admin", "listener"}, []string{"anonymous"})
				payload := payloadOf[downloadItem](t, events["admin"])
				requireDownloadPayload(t, payload, "Queued B", waiting, show.ID, e.IDs["podcastLibrary"], "Socket Queued Show")
				if payload.IsFinished || payload.CreatedAt == nil || *payload.CreatedAt == 0 {
					t.Fatalf("queued payload = %+v", payload)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketQueued, Invariant: invQueued, Case: "edge", State: "repeat", Assertion: "episode-socket-queued-repeats-per-waiting-episode",
			Sources: []string{socketQueuedSrc},
			Claim:   "Each additional waiting episode emits its own episode_download_queued with that episode's url and title.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Queued Repeat")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running := stub.url("/queued-repeat-a.mp3")
				firstQueue, secondQueue := stub.url("/queued-repeat-b.mp3"), stub.url("/queued-repeat-c.mp3")
				stub.setHeldAudio("/queued-repeat-a.mp3", audio, len(audio)/2, 3*time.Second)
				stub.setAudio("/queued-repeat-b.mp3", audio)
				stub.setAudio("/queued-repeat-c.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "first batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Repeat A", "repeat-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Repeat B", "repeat-b", firstQueue, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_queued", eventWait, downloadMatcher(firstQueue))
				wantStatus(t, "second batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Repeat C", "repeat-c", secondQueue, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				second := payloadOf[downloadItem](t, socket.Wait(t, 0, "episode_download_queued", eventWait, downloadMatcher(secondQueue)))
				requireDownloadPayload(t, second, "Repeat C", secondQueue, show.ID, e.IDs["podcastLibrary"], "Socket Queued Repeat")
				if got := eventURLs(socket.Events(), "episode_download_queued"); len(got) != 2 {
					t.Fatalf("queued events = %v, want two", got)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketQueued, Invariant: invQueued, Case: "normal", State: "changed", Assertion: "episode-socket-queued-after-a-clear",
			Sources: []string{socketQueuedSrc, socketClearSrc},
			Claim:   "After clearing an item's waiting queue, queueing another episode emits episode_download_queued for it while the running download continues.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Queued Changed")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running := stub.url("/queued-changed-a.mp3")
				cleared, later := stub.url("/queued-changed-b.mp3"), stub.url("/queued-changed-c.mp3")
				stub.setHeldAudio("/queued-changed-a.mp3", audio, len(audio)/2, 3*time.Second)
				stub.setAudio("/queued-changed-b.mp3", audio)
				stub.setAudio("/queued-changed-c.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "first batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Changed A", "changed-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Changed B", "changed-b", cleared, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_queued", eventWait, downloadMatcher(cleared))
				wantStatus(t, "clear queue", clearQueue(t, e, e.Token(t, "root"), show.ID), http.StatusOK)
				socket.Wait(t, 0, "episode_download_queue_cleared", eventWait, nil)
				wantStatus(t, "second batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Changed C", "changed-c", later, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				payload := payloadOf[downloadItem](t, socket.Wait(t, 0, "episode_download_queued", eventWait, downloadMatcher(later)))
				requireDownloadPayload(t, payload, "Changed C", later, show.ID, e.IDs["podcastLibrary"], "Socket Queued Changed")
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketQueued, Invariant: invQueuedAud, Case: "error", State: "failure", Assertion: "episode-socket-queued-not-sent-for-a-rejected-caller",
			Sources: []string{socketQueuedSrc, socketAuthSrc, downloadEpisodesSrc},
			Claim:   "A non-admin download request is rejected with 403 and emits no episode_download_queued to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Queued Reject")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				body := []map[string]any{downloadEpisode("Rejected", "rejected", deadURL(t), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)}
				wantStatus(t, "listener", postDownloads(t, e, show.ID, e.Token(t, "listener"), body), http.StatusForbidden)
				audience.receive(t, "episode_download_queued", nil, nil, []string{"admin", "listener", "anonymous"})
			},
		},
		{
			Requirement: socketStarted, Invariant: invStarted, Case: "normal", State: "initial", Assertion: "episode-socket-started-payload-and-audience",
			Sources: []string{socketStartedSrc, socketAuthSrc, socketCliSrc},
			Claim:   "Starting an episode download emits episode_download_started with the download's client fields and no finish time to the admin and listener sockets, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Started Show")
				stub := newAudioStub(t)
				stub.setAudio("/started-a.mp3", fixtureEpisodeMP3(t, e))
				audioURL := stub.url("/started-a.mp3")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Started A", "started-a", audioURL, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				events := audience.receive(t, "episode_download_started", downloadMatcher(audioURL), []string{"admin", "listener"}, []string{"anonymous"})
				payload := payloadOf[downloadItem](t, events["admin"])
				requireDownloadPayload(t, payload, "Started A", audioURL, show.ID, e.IDs["podcastLibrary"], "Socket Started Show")
				if payload.IsFinished || payload.StartedAt != nil || payload.FinishedAt != nil {
					t.Fatalf("started payload = %+v", payload)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketStarted, Invariant: invStarted, Case: "edge", State: "repeat", Assertion: "episode-socket-started-repeats-for-the-next-episode",
			Sources: []string{socketStartedSrc},
			Claim:   "When the running download finishes, the next waiting episode emits episode_download_started with its own url, in queue order.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Started Repeat")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				first, second := stub.url("/started-repeat-a.mp3"), stub.url("/started-repeat-b.mp3")
				stub.setHeldAudio("/started-repeat-a.mp3", audio, len(audio)/2, 1200*time.Millisecond)
				stub.setAudio("/started-repeat-b.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Started Repeat A", "started-repeat-a", first, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Started Repeat B", "started-repeat-b", second, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_started", eventWait, downloadMatcher(second))
				requireURLSequence(t, socket.Events(), "episode_download_started", first, second)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketStarted, Invariant: invStarted, Case: "normal", State: "changed", Assertion: "episode-socket-started-after-a-clear",
			Sources: []string{socketStartedSrc, socketClearSrc},
			Claim:   "After the waiting queue is cleared, a newly requested episode starts only after the running one finishes and emits episode_download_started.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Started Changed")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running := stub.url("/started-changed-a.mp3")
				cleared, later := stub.url("/started-changed-b.mp3"), stub.url("/started-changed-c.mp3")
				stub.setHeldAudio("/started-changed-a.mp3", audio, len(audio)/2, 1200*time.Millisecond)
				stub.setAudio("/started-changed-b.mp3", audio)
				stub.setAudio("/started-changed-c.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "first batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Started Changed A", "started-changed-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Started Changed B", "started-changed-b", cleared, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_queued", eventWait, downloadMatcher(cleared))
				wantStatus(t, "clear queue", clearQueue(t, e, e.Token(t, "root"), show.ID), http.StatusOK)
				wantStatus(t, "second batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Started Changed C", "started-changed-c", later, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_started", eventWait, downloadMatcher(later))
				requireURLSequence(t, socket.Events(), "episode_download_started", running, later)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketStarted, Invariant: invStartedAud, Case: "error", State: "failure", Assertion: "episode-socket-started-not-sent-for-a-rejected-caller",
			Sources: []string{socketStartedSrc, socketAuthSrc},
			Claim:   "A rejected (403) download request emits no episode_download_started to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Started Reject")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				body := []map[string]any{downloadEpisode("Rejected", "rejected", deadURL(t), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)}
				wantStatus(t, "listener", postDownloads(t, e, show.ID, e.Token(t, "listener"), body), http.StatusForbidden)
				audience.receive(t, "episode_download_started", nil, nil, []string{"admin", "listener", "anonymous"})
			},
		},
		{
			Requirement: socketFinished, Invariant: invFinished, Case: "normal", State: "initial", Assertion: "episode-socket-finished-success-payload-and-audience",
			Sources: []string{socketFinishSrc, socketAuthSrc, socketCliSrc},
			Claim:   "A successful download emits episode_download_finished with isFinished true, failed false and a finish time to the admin and listener sockets, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Finished Show")
				stub := newAudioStub(t)
				stub.setAudio("/finished-a.mp3", fixtureEpisodeMP3(t, e))
				audioURL := stub.url("/finished-a.mp3")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Finished A", "finished-a", audioURL, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				events := audience.receive(t, "episode_download_finished", downloadMatcher(audioURL), []string{"admin", "listener"}, []string{"anonymous"})
				payload := payloadOf[downloadItem](t, events["admin"])
				requireDownloadPayload(t, payload, "Finished A", audioURL, show.ID, e.IDs["podcastLibrary"], "Socket Finished Show")
				if !payload.IsFinished || payload.Failed || payload.FinishedAt == nil || *payload.FinishedAt == 0 {
					t.Fatalf("finished payload = %+v", payload)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketFinished, Invariant: invFinished, Case: "edge", State: "repeat", Assertion: "episode-socket-finished-repeats-per-episode",
			Sources: []string{socketFinishSrc},
			Claim:   "Two downloaded episodes emit two episode_download_finished events with their own urls and finish times.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Finished Repeat")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				first, second := stub.url("/finished-repeat-a.mp3"), stub.url("/finished-repeat-b.mp3")
				stub.setAudio("/finished-repeat-a.mp3", audio)
				stub.setAudio("/finished-repeat-b.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Finished Repeat A", "finished-repeat-a", first, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Finished Repeat B", "finished-repeat-b", second, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				waitDownloadEvents(t, socket, "episode_download_finished", 2)
				requireURLSequence(t, socket.Events(), "episode_download_finished", first, second)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketFinished, Invariant: invFinished, Case: "normal", State: "changed", Assertion: "episode-socket-finished-for-a-checknew-download",
			Sources: []string{socketFinishSrc, queueCheck},
			Claim:   "A download started by the new-episode check emits episode_download_finished with the feed episode's url.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Finished Checknew")
				stub := newAudioStub(t)
				stub.setAudio("/socket-check.mp3", fixtureEpisodeMP3(t, e))
				stub.setFeed("/socket-check.xml", feed{Title: "Socket Finished Checknew", Items: []feedItem{
					{Title: "Socket Check", GUID: "socket-check", PubDate: "Sun, 01 Jun 2025 10:00:00 GMT", EnclosureURL: stub.url("/socket-check.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
				}}.document())
				patchMedia(t, e, show.ID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/socket-check.xml")}, "lastEpisodeCheck": 0})
				socket := e.Socket(t, e.Token(t, "root"))
				checkNew(t, e, show.ID, "5")
				payload := payloadOf[downloadItem](t, socket.Wait(t, 0, "episode_download_finished", downloadWait, downloadMatcher(stub.url("/socket-check.mp3"))))
				requireValue(t, "episodeDisplayTitle", payload.EpisodeDisplayTitle, "Socket Check")
				requireValue(t, "guid", payload.GUID, "socket-check")
				if !payload.IsFinished || payload.Failed {
					t.Fatalf("checknew finished payload = %+v", payload)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketFinished, Invariant: invFinished, Case: "error", State: "failure", Assertion: "episode-socket-finished-failure-payload",
			Sources: []string{socketFinishSrc},
			Claim:   "A failed download emits episode_download_finished with isFinished true, failed true and a finish time.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Finished Failure")
				bad := deadURL(t)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Finished Failure", "finished-failure", bad, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				payload := payloadOf[downloadItem](t, socket.Wait(t, 0, "episode_download_finished", downloadWait, downloadMatcher(bad)))
				if !payload.IsFinished || !payload.Failed || payload.FinishedAt == nil || *payload.FinishedAt == 0 {
					t.Fatalf("failure payload = %+v", payload)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketFinished, Invariant: invFinishedAud, Case: "error", State: "failure", Assertion: "episode-socket-finished-not-sent-for-a-rejected-caller",
			Sources: []string{socketFinishSrc, socketAuthSrc},
			Claim:   "A rejected (403) download request emits no episode_download_finished to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Finished Reject")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				body := []map[string]any{downloadEpisode("Rejected", "rejected", deadURL(t), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)}
				wantStatus(t, "listener", postDownloads(t, e, show.ID, e.Token(t, "listener"), body), http.StatusForbidden)
				audience.receive(t, "episode_download_finished", nil, nil, []string{"admin", "listener", "anonymous"})
			},
		},
		{
			Requirement: socketCleared, Invariant: invCleared, Case: "normal", State: "initial", Assertion: "episode-socket-cleared-payload-and-audience",
			Sources: []string{socketClearSrc, socketAuthSrc, socketCliSrc},
			Claim:   "Clearing an item's waiting downloads emits episode_download_queue_cleared with the library item id to the admin and listener sockets, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Cleared Show")
				startSlowDownload(t, e, show.ID, "socket-clear", "Clear Running", "Clear Waiting")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "clear queue", clearQueue(t, e, e.Token(t, "root"), show.ID), http.StatusOK)
				events := audience.receive(t, "episode_download_queue_cleared", nil, []string{"admin", "listener"}, []string{"anonymous"})
				for who, event := range events {
					if len(event.Args) != 1 || string(event.Args[0]) != `"`+show.ID+`"` {
						t.Fatalf("%s payload = %.100s, want %q", who, event.Args[0], show.ID)
					}
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketCleared, Invariant: invCleared, Case: "edge", State: "repeat", Assertion: "episode-socket-cleared-silent-repeat",
			Sources: []string{socketClearSrc},
			Claim:   "Clearing an already empty queue emits no episode_download_queue_cleared.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Cleared Quiet")
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				wantStatus(t, "first clear", clearQueue(t, e, e.Token(t, "root"), show.ID), http.StatusOK)
				wantStatus(t, "second clear", clearQueue(t, e, e.Token(t, "root"), show.ID), http.StatusOK)
				socket.None(t, mark, "episode_download_queue_cleared", quietWindow, nil)
			},
		},
		{
			Requirement: socketCleared, Invariant: invCleared, Case: "normal", State: "changed", Assertion: "episode-socket-cleared-only-names-the-cleared-item",
			Sources: []string{socketClearSrc},
			Claim:   "Clearing one item's queue while another item also has waiting downloads names only the cleared item and leaves the other item's queue waiting.",
			Run: func(t *testing.T, e *harness.Env) {
				first := ensureShow(t, e, "Socket Cleared A")
				second := ensureShow(t, e, "Socket Cleared B")
				startSlowDownload(t, e, first.ID, "socket-clear-a", "Clear A Running", "Clear A Waiting")
				startSlowDownload(t, e, second.ID, "socket-clear-b", "Clear B Waiting")
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "clear first", clearQueue(t, e, e.Token(t, "root"), first.ID), http.StatusOK)
				event := socket.Wait(t, 0, "episode_download_queue_cleared", eventWait, nil)
				if len(event.Args) != 1 || string(event.Args[0]) != `"`+first.ID+`"` {
					t.Fatalf("payload = %.100s, want %q", event.Args[0], first.ID)
				}
				socket.None(t, 0, "episode_download_queue_cleared", quietWindow, func(args []json.RawMessage) bool {
					return len(args) == 1 && string(args[0]) == `"`+second.ID+`"`
				})
				if got := queueOf(t, e, second.ID); len(got.Downloads) != 1 {
					t.Fatalf("second item queue = %+v", got.Downloads)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketCleared, Invariant: invClearedAud, Case: "error", State: "failure", Assertion: "episode-socket-cleared-not-sent-for-a-rejected-caller",
			Sources: []string{socketClearSrc, socketAuthSrc},
			Claim:   "A non-admin clear request is rejected with 403 and emits no episode_download_queue_cleared.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Cleared Reject")
				startSlowDownload(t, e, show.ID, "socket-clear-reject", "Reject Running", "Reject Waiting")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "listener", clearQueue(t, e, e.Token(t, "listener"), show.ID), http.StatusForbidden)
				audience.receive(t, "episode_download_queue_cleared", nil, nil, []string{"admin", "listener", "anonymous"})
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketAdded, Invariant: invAdded, Case: "normal", State: "initial", Assertion: "episode-socket-added-payload-and-audience",
			Sources: []string{socketMgrSrc, socketAuthSrc, socketCliSrc},
			Claim:   "A downloaded episode emits episode_added with the episode's old JSON and the expanded library item to the admin and listener sockets, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Added Show")
				stub := newAudioStub(t)
				audioURL := stub.url("/added-a.mp3")
				stub.setAudio("/added-a.mp3", fixtureEpisodeMP3(t, e))
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Added A", "added-a", audioURL, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				events := audience.receive(t, "episode_added", episodeAddedMatcher("Added A"), []string{"admin", "listener"}, []string{"anonymous"})
				requireEpisodeAdded(t, payloadOf[episodeJSON](t, events["admin"]), show.ID, e.IDs["podcastLibrary"], show.Media.ID, "Added A", "added-a", audioURL, 1746439200000)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketAdded, Invariant: invAdded, Case: "edge", State: "repeat", Assertion: "episode-socket-added-repeats-per-episode",
			Sources: []string{socketMgrSrc},
			Claim:   "Each newly downloaded episode emits its own episode_added with its own id and title.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Added Repeat")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				first, second := stub.url("/added-repeat-a.mp3"), stub.url("/added-repeat-b.mp3")
				stub.setAudio("/added-repeat-a.mp3", audio)
				stub.setAudio("/added-repeat-b.mp3", audio)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Added Repeat A", "added-repeat-a", first, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Added Repeat B", "added-repeat-b", second, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				waitDownloadEvents(t, socket, "episode_added", 2)
				payload := payloadOf[episodeJSON](t, socket.Wait(t, 0, "episode_added", eventWait, episodeAddedMatcher("Added Repeat B")))
				requireEpisodeAdded(t, payload, show.ID, e.IDs["podcastLibrary"], show.Media.ID, "Added Repeat B", "added-repeat-b", second, 1746439200000)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketAdded, Invariant: invAdded, Case: "normal", State: "changed", Assertion: "episode-socket-added-for-a-checknew-download",
			Sources: []string{socketMgrSrc, queueCheck},
			Claim:   "A feed episode downloaded by the new-episode check emits episode_added with its guid and the library item.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Added Checknew")
				stub := newAudioStub(t)
				audioURL := stub.url("/added-check.mp3")
				stub.setAudio("/added-check.mp3", fixtureEpisodeMP3(t, e))
				stub.setFeed("/added-check.xml", feed{Title: "Socket Added Checknew", Items: []feedItem{
					{Title: "Added Check", GUID: "added-check", PubDate: "Sun, 01 Jun 2025 10:00:00 GMT", EnclosureURL: audioURL, EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
				}}.document())
				patchMedia(t, e, show.ID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/added-check.xml")}, "lastEpisodeCheck": 0})
				socket := e.Socket(t, e.Token(t, "root"))
				checkNew(t, e, show.ID, "5")
				payload := payloadOf[episodeJSON](t, socket.Wait(t, 0, "episode_added", downloadWait, episodeAddedMatcher("Added Check")))
				requireEpisodeAdded(t, payload, show.ID, e.IDs["podcastLibrary"], show.Media.ID, "Added Check", "added-check", audioURL, 1748772000000)
				waitIdle(t, e)
			},
		},
		{
			Requirement: socketAdded, Invariant: invAddedAud, Case: "error", State: "failure", Assertion: "episode-socket-added-not-sent-for-a-failed-download",
			Sources: []string{socketMgrSrc, socketAuthSrc},
			Claim:   "A failed download emits no episode_added to the admin, the listener or an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Socket Added Failure")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				bad := deadURL(t)
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{
					downloadEpisode("Added Failure", "added-failure", bad, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
				}), http.StatusOK)
				audience.sockets["admin"].Wait(t, 0, "episode_download_finished", downloadWait, downloadMatcher(bad))
				audience.receive(t, "episode_added", nil, nil, []string{"admin", "listener", "anonymous"})
				waitIdle(t, e)
			},
		},
	}
}
