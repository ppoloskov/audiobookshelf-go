package podcast_episodes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	queueReq    = "sub-podcast-download-queue"
	queueLife   = "INV-SUB-PODCAST-DOWNLOAD-QUEUE"
	queueFail   = "INV-SUB-PODCAST-DOWNLOAD-QUEUE-FAILURE-001"
	queueAuto   = "INV-SUB-PODCAST-DOWNLOAD-QUEUE-AUTO-001"
	queueRest   = "INV-SUB-PODCAST-DOWNLOAD-QUEUE-RESTART-001"
	queueMgrSrc = "server/managers/PodcastManager.js:54-262"
	queueCheck  = "server/managers/PodcastManager.js:380-455"
)

// checkNew runs the new-episode check and returns its reported episodes.
func checkNew(t *testing.T, e *harness.Env, itemID, limit string) checkNewPayload {
	t.Helper()
	path := "/api/podcasts/" + itemID + "/checknew"
	if limit != "" {
		path += "?limit=" + limit
	}
	resp := e.Get(t, e.Token(t, "root"), path)
	wantStatus(t, "checknew", resp, http.StatusOK)
	return decode[checkNewPayload](t, resp.Body)
}

// waitDownloadEvents waits until the socket saw at least count events with the name.
func waitDownloadEvents(t *testing.T, socket *harness.Socket, name string, count int) []harness.Event {
	t.Helper()
	harness.Eventually(t, downloadWait, "the download events", func() bool {
		return len(eventURLs(socket.Events(), name)) >= count
	})
	return socket.Events()
}

// requireURLSequence asserts the url order of one event name.
func requireURLSequence(t *testing.T, events []harness.Event, name string, want ...string) {
	t.Helper()
	got := eventURLs(events, name)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s urls = %v, want %v", name, got, want)
	}
}

func queueScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: queueReq, Invariant: queueLife, Case: "normal", State: "initial", Assertion: "download-queue-runs-one-at-a-time-in-order",
			Sources: []string{queueMgrSrc},
			Claim:   "Three requested episodes download one at a time in request order: the started and finished events follow the queue order, the second and third are queued first, each leaves one episode row and one audio file, and an existing file at a target name is kept while the new download is written with a random-id suffix.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Queue Order Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				// One URL is already percent-encoded and one carries a query string,
				// covering the download URL normalisation branches.
				urls := []string{stub.url("/order-a.mp3"), stub.url("/order%20b.mp3"), stub.url("/order-c") + "?v=1"}
				stub.setHeldAudio("/order-a.mp3", audio, len(audio)/2, 1200*time.Millisecond)
				stub.setAudio("/order b.mp3", audio)
				stub.setAudio("/order-c", audio)
				episodes := []map[string]any{
					downloadEpisode("Order A", "order-a", urls[0], "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Order B", "order-b", urls[1], "Tue, 06 May 2025 10:00:00 GMT", 1746525600000),
					downloadEpisode("Order C", "order-c", urls[2], "Wed, 07 May 2025 10:00:00 GMT", 1746612000000),
				}
				existing := filepath.Join(show.Path, "Order C.mp3")
				if err := os.WriteFile(existing, audio, 0644); err != nil {
					t.Fatal(err)
				}
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), episodes), http.StatusOK)
				events := waitDownloadEvents(t, socket, "episode_download_finished", 3)
				requireURLSequence(t, events, "episode_download_started", urls...)
				requireURLSequence(t, events, "episode_download_finished", urls...)
				requireURLSequence(t, events, "episode_download_queued", urls[1], urls[2])
				waitIdle(t, e)
				for _, guid := range []string{"order-a", "order-b", "order-c"} {
					if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%"+guid+"%"); len(rows) != 1 {
						t.Fatalf("guid %s has %d rows, want 1", guid, len(rows))
					}
				}
				files, err := filepath.Glob(filepath.Join(show.Path, "*.mp3"))
				if err != nil || len(files) != 4 {
					t.Fatalf("files = %v (%v), want 3 downloads and the pre-existing target", files, err)
				}
				if data, err := os.ReadFile(existing); err != nil || len(data) != len(audio) {
					t.Fatalf("pre-existing target changed: %d bytes (%v)", len(data), err)
				}
				rows := e.Rows(t, "SELECT audioFile FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%order-c%")
				if len(rows) != 1 {
					t.Fatalf("%d rows for the colliding download", len(rows))
				}
				audioJSON, _ := rows[0]["audioFile"].(string)
				if !strings.Contains(audioJSON, `"filename":"Order C (`) || !strings.Contains(audioJSON, `.mp3"`) {
					t.Fatalf("colliding download audioFile = %q, want a random-id suffix", audioJSON)
				}
				if err := os.Remove(existing); err != nil {
					t.Fatalf("remove the pre-existing target: %v", err)
				}
			},
		},
		{
			Requirement: queueReq, Invariant: queueFail, Case: "error", State: "failure", Assertion: "download-queue-failed-download-keeps-no-row-and-continues",
			Sources: []string{queueMgrSrc, "server/utils/ffmpegHelpers.js:107-232"},
			Claim:   "A download whose request fails emits episode_download_finished with failed and isFinished true and a finish time, creates no episode row and no file, and the next queued episode still downloads.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Queue Failure Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				bad := deadURL(t)
				good := stub.url("/failure-good.mp3")
				stub.setAudio("/failure-good.mp3", audio)
				episodes := []map[string]any{
					downloadEpisode("Bad Episode", "queue-bad", bad, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Good Episode", "queue-good", good, "Tue, 06 May 2025 10:00:00 GMT", 1746525600000),
				}
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episodes", postDownloads(t, e, show.ID, e.Token(t, "root"), episodes), http.StatusOK)
				failedEvent := socket.Wait(t, 0, "episode_download_finished", downloadWait, downloadMatcher(bad))
				failed := payloadOf[downloadItem](t, failedEvent)
				if !failed.IsFinished || !failed.Failed || failed.FinishedAt == nil || *failed.FinishedAt == 0 {
					t.Fatalf("failed download payload = %+v", failed)
				}
				waitDownloadEvents(t, socket, "episode_download_finished", 2)
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%queue-bad%"); len(rows) != 0 {
					t.Fatalf("failed download created %d rows", len(rows))
				}
				if _, err := os.Stat(filepath.Join(show.Path, "Bad Episode.mp3")); !os.IsNotExist(err) {
					t.Fatalf("failed download left a file: %v", err)
				}
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%queue-good%"); len(rows) != 1 {
					t.Fatalf("good episode rows = %d, want 1", len(rows))
				}
				events := socket.Events()
				failedIndex, startedIndex := -1, -1
				for i, event := range events {
					var item downloadItem
					if len(event.Args) != 1 || json.Unmarshal(event.Args[0], &item) != nil {
						continue
					}
					if event.Name == "episode_download_finished" && item.URL == bad {
						failedIndex = i
					}
					if event.Name == "episode_download_started" && item.URL == good && failedIndex >= 0 {
						startedIndex = i
					}
				}
				if failedIndex < 0 || startedIndex < 0 {
					t.Fatalf("missing events: failed %d started %d", failedIndex, startedIndex)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: queueReq, Invariant: queueFail, Case: "edge", State: "failure", Assertion: "download-queue-retries-without-tagging-and-removes-the-file",
			Sources: []string{queueMgrSrc, "server/utils/ffmpegHelpers.js:145-150", "server/utils/fileUtils.js:290-330"},
			Claim:   "When ffmpeg cannot tag a reachable response, the download retries with a plain file download, and when the file still does not probe the download finishes failed, creates no row and removes the file.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Queue Retry Show")
				stub := newAudioStub(t)
				stub.setBody("/retry-bad.mp3", "audio/mpeg", []byte("not an audio stream"))
				episode := downloadEpisode("Tagless Episode", "retry-bad", stub.url("/retry-bad.mp3"), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{episode}), http.StatusOK)
				event := socket.Wait(t, 0, "episode_download_finished", downloadWait, downloadMatcher(stub.url("/retry-bad.mp3")))
				failed := payloadOf[downloadItem](t, event)
				if !failed.Failed || !failed.IsFinished {
					t.Fatalf("retry download payload = %+v", failed)
				}
				if hits := stub.hitsOf("/retry-bad.mp3"); hits != 2 {
					t.Fatalf("audio requests = %d, want 2 (ffmpeg then plain download)", hits)
				}
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%retry-bad%"); len(rows) != 0 {
					t.Fatalf("retry created %d rows", len(rows))
				}
				if _, err := os.Stat(filepath.Join(show.Path, "Tagless Episode.mp3")); !os.IsNotExist(err) {
					t.Fatalf("failed retry left the file: %v", err)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: queueReq, Invariant: queueAuto, Case: "normal", State: "changed", Assertion: "download-queue-auto-downloads-checknew-episodes-with-stable-identity",
			Sources: []string{queueCheck, "server/models/PodcastEpisode.js:161-176", "server/models/Podcast.js:393-399"},
			Claim:   "checknew automatically downloads the new feed episodes with their guid, enclosure and publication time, and a later check does not download again an episode with the same guid but a new URL nor one with the same enclosure but a new guid.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Checknew Queue Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				for _, path := range []string{"/check-a.mp3", "/check-b.mp3", "/check-moved.mp3", "/check-d.mp3"} {
					stub.setAudio(path, audio)
				}
				stub.setFeed("/first.xml", feed{Title: "Checknew Queue Show", Items: []feedItem{
					{Title: "Check A", GUID: "check-a", PubDate: "Sun, 01 Jun 2025 10:00:00 GMT", EnclosureURL: stub.url("/check-a.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
					{Title: "Check B", GUID: "check-b", PubDate: "Mon, 02 Jun 2025 10:00:00 GMT", EnclosureURL: stub.url("/check-b.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
				}}.document())
				patchMedia(t, e, show.ID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/first.xml")}, "lastEpisodeCheck": 0})
				socket := e.Socket(t, e.Token(t, "root"))
				first := checkNew(t, e, show.ID, "5")
				if len(first.Episodes) != 2 {
					t.Fatalf("first check reported %+v", first.Episodes)
				}
				harness.Eventually(t, downloadWait, "the two checknew downloads", func() bool {
					return e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID) == 2
				})
				waitDownloadEvents(t, socket, "episode_download_finished", 2)
				rows := e.Rows(t, "SELECT extraData, enclosureURL, title FROM podcastEpisodes WHERE podcastId = ? ORDER BY title", show.Media.ID)
				if len(rows) != 2 {
					t.Fatalf("%d rows after the first check", len(rows))
				}
				requireString(t, rows[0], "enclosureURL", stub.url("/check-a.mp3"))
				requireString(t, rows[1], "enclosureURL", stub.url("/check-b.mp3"))

				future := func(hours int) string {
					return time.Now().UTC().Add(time.Duration(hours) * time.Hour).Format(http.TimeFormat)
				}
				stub.setFeed("/second.xml", feed{Title: "Checknew Queue Show", Items: []feedItem{
					{Title: "Check A Renamed", GUID: "check-a", PubDate: future(2), EnclosureURL: stub.url("/check-moved.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
					{Title: "Check B Reissued", GUID: "different-guid", PubDate: future(3), EnclosureURL: stub.url("/check-b.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
					{Title: "Check D", GUID: "check-d", PubDate: future(4), EnclosureURL: stub.url("/check-d.mp3"), EnclosureType: "audio/mpeg", EnclosureLen: "96673"},
				}}.document())
				patchMedia(t, e, show.ID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/second.xml")}})
				second := checkNew(t, e, show.ID, "5")
				if len(second.Episodes) != 1 || second.Episodes[0].Title != "Check D" {
					t.Fatalf("second check reported %+v, want only Check D", second.Episodes)
				}
				harness.Eventually(t, downloadWait, "the third download", func() bool {
					return e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID) == 3
				})
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND title = ?", show.Media.ID, "Check A Renamed"); len(rows) != 0 {
					t.Fatalf("a known guid was downloaded again")
				}
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND title = ?", show.Media.ID, "Check B Reissued"); len(rows) != 0 {
					t.Fatalf("a known enclosure was downloaded again")
				}
				patchMedia(t, e, show.ID, map[string]any{"metadata": map[string]any{"feedUrl": stub.url("/first.xml")}})
				if none := checkNew(t, e, show.ID, "5"); len(none.Episodes) != 0 {
					t.Fatalf("a check without new episodes reported %+v", none.Episodes)
				}
				if got := e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID); got != 3 {
					t.Fatalf("%d episodes after a check without new ones", got)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: queueReq, Invariant: queueRest, Case: "edge", State: "repeat", Assertion: "download-queue-restart-drops-the-queue-and-keeps-no-partial-episode",
			Sources: []string{queueMgrSrc},
			Claim:   "Stopping the server during a download creates no episode row, loses the in-memory queue on restart, and a retried request downloads the episode once with its audio file.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Queue Restart Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				stub.setHeldAudio("/restart-held.mp3", audio, len(audio)/2, 5*time.Second)
				stub.setAudio("/restart-retry.mp3", audio)
				episode := downloadEpisode("Restart Episode", "restart-episode", stub.url("/restart-held.mp3"), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "download episode", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{episode}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_started", eventWait, downloadMatcher(stub.url("/restart-held.mp3")))
				harness.Eventually(t, eventWait, "the audio request", func() bool {
					return stub.hitsOf("/restart-held.mp3") > 0
				})
				e.Stop(t)
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%restart-episode%"); len(rows) != 0 {
					t.Fatalf("stopped download created %d rows", len(rows))
				}
				entries, err := os.ReadDir(show.Path)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					info, err := entry.Info()
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasSuffix(entry.Name(), ".mp3") && info.Size() >= int64(len(audio)) {
						t.Fatalf("partial download %q has the full size %d", entry.Name(), info.Size())
					}
				}
				e.Restart(t)
				requireEmptyQueue(t, "queue after restart", libraryQueueOf(t, e, e.IDs["podcastLibrary"], ""))
				if item := itemOf(t, e, show.ID); len(item.Media.Episodes) != 0 {
					t.Fatalf("episodes after restart = %+v", item.Media.Episodes)
				}
				retry := downloadEpisode("Restart Episode", "restart-episode", stub.url("/restart-retry.mp3"), "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
				wantStatus(t, "retry download", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{retry}), http.StatusOK)
				harness.Eventually(t, downloadWait, "the retried download", func() bool {
					return e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID) == 1
				})
				read := itemOf(t, e, show.ID)
				if len(read.Media.Episodes) != 1 || read.Media.Episodes[0].AudioFile == nil {
					t.Fatalf("retried episode = %+v", read.Media.Episodes)
				}
				readEpisodeFile(t, read.Media.Episodes[0].AudioFile.Metadata.Path)
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", show.Media.ID, "%restart-episode%"); len(rows) != 1 {
					t.Fatalf("retried rows = %d, want 1", len(rows))
				}
			},
		},
	}
}
