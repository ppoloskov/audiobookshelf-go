package podcast_episodes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	downloadEpisodesReq  = "route-r157"
	downloadEpisodesInv  = "INV-ROUTE-R157-CONTRACT"
	downloadEpisodesOrd  = "INV-ROUTE-R157-ORDER-001"
	downloadEpisodesAuth = "INV-ROUTE-R157-AUTH-001"
	downloadEpisodesSrc  = "server/controllers/PodcastController.js:367-387"
	downloadEpisodesMgr  = "server/managers/PodcastManager.js:54-86"
)

// waitIdle waits until the podcast library has no running or waiting download.
// It reads with a unique page value because the ApiCacheManager caches the
// library queue endpoint and in-memory queue changes do not clear it.
func waitIdle(t *testing.T, e *harness.Env) {
	t.Helper()
	harness.Eventually(t, downloadWait, "the episode downloads to drain", func() bool {
		details := liveLibraryQueue(t, e)
		return details.CurrentDownload == nil && len(details.Queue) == 0
	})
}

// eventURLs lists the url field of the download events with the name.
func eventURLs(events []harness.Event, name string) []string {
	var urls []string
	for _, event := range events {
		if event.Name != name {
			continue
		}
		var item downloadItem
		if len(event.Args) == 1 && json.Unmarshal(event.Args[0], &item) == nil {
			urls = append(urls, item.URL)
		}
	}
	return urls
}

func downloadEpisodeScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: downloadEpisodesReq, Invariant: downloadEpisodesInv, Case: "normal", State: "initial", Assertion: "download-episodes-starts-first-and-queues-the-rest",
			Sources: []string{downloadEpisodesSrc, downloadEpisodesMgr},
			Claim:   "POST /api/podcasts/:id/download-episodes answers 200, starts the first episode's download, queues the rest in request order and emits episode_download_started and one episode_download_queued per waiting episode with the client fields to authenticated sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Download Episodes Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running := stub.url("/episodes-a.mp3")
				waitingOne := stub.url("/episodes-b.mp3")
				waitingTwo := stub.url("/episodes-c.mp3")
				stub.setHeldAudio("/episodes-a.mp3", audio, len(audio)/2, 1500*time.Millisecond)
				stub.setAudio("/episodes-b.mp3", audio)
				stub.setAudio("/episodes-c.mp3", audio)
				episodes := []map[string]any{
					downloadEpisode("Episode A", "episodes-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000),
					downloadEpisode("Episode B", "episodes-b", waitingOne, "Tue, 06 May 2025 10:00:00 GMT", 1746525600000),
					downloadEpisode("Episode C", "episodes-c", waitingTwo, "Wed, 07 May 2025 10:00:00 GMT", 1746612000000),
				}
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				resp := postDownloads(t, e, show.ID, e.Token(t, "root"), episodes)
				wantStatus(t, "download episodes", resp, http.StatusOK)
				started := audience.receive(t, "episode_download_started", downloadMatcher(running),
					[]string{"admin", "listener"}, nil)["admin"]
				payload := payloadOf[downloadItem](t, started)
				requireValue(t, "episodeDisplayTitle", payload.EpisodeDisplayTitle, "Episode A")
				requireValue(t, "libraryItemId", payload.LibraryItemID, show.ID)
				requireValue(t, "libraryId", payload.LibraryID, e.IDs["podcastLibrary"])
				requireValue(t, "podcastTitle", payload.PodcastTitle, "Download Episodes Show")
				requireValue(t, "season", payload.Season, "2")
				requireValue(t, "episode", payload.Episode, "7")
				requireValue(t, "episodeType", payload.EpisodeType, "full")
				requireValue(t, "guid", payload.GUID, "episodes-a")
				if payload.PublishedAt == nil || *payload.PublishedAt != 1746439200000 {
					t.Fatalf("publishedAt = %v", payload.PublishedAt)
				}
				if payload.ID == "" || payload.IsFinished || payload.Failed || payload.PodcastExplicit || payload.AppendRandomID {
					t.Fatalf("started download flags = %+v", payload)
				}
				queuedOne := audience.receive(t, "episode_download_queued", downloadMatcher(waitingOne), []string{"admin", "listener"}, nil)["admin"]
				queuedTwo := audience.receive(t, "episode_download_queued", downloadMatcher(waitingTwo), []string{"admin"}, nil)["admin"]
				for _, check := range []struct {
					event   harness.Event
					wantURL string
				}{{queuedOne, waitingOne}, {queuedTwo, waitingTwo}} {
					queuedPayload := payloadOf[downloadItem](t, check.event)
					if queuedPayload.URL != check.wantURL || queuedPayload.IsFinished || queuedPayload.ID == "" {
						t.Fatalf("queued payload = %+v", queuedPayload)
					}
				}
				downloads := queueOf(t, e, show.ID)
				if len(downloads.Downloads) != 2 || downloads.Downloads[0].URL != waitingOne || downloads.Downloads[1].URL != waitingTwo {
					t.Fatalf("waiting downloads = %+v", downloads.Downloads)
				}
				if downloads.Downloads[0].ID != payloadOf[downloadItem](t, queuedOne).ID {
					t.Fatalf("queue id does not match the queued event")
				}
				for _, name := range []string{"episode_download_started", "episode_download_queued"} {
					audience.sockets["anonymous"].None(t, audience.marks["anonymous"], name, quietWindow, nil)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: downloadEpisodesReq, Invariant: downloadEpisodesInv, Case: "normal", State: "changed", Assertion: "download-episodes-store-rows-files-and-library-files",
			Sources: []string{downloadEpisodesSrc, downloadEpisodesMgr, "server/models/PodcastEpisode.js:66-107", "server/managers/PodcastManager.js:192-262"},
			Claim:   "The three downloaded episodes are stored with the feed title, enclosure, guid, season, episode and publication time and with a probed audio file that exists in the podcast folder, and the library item lists them with matching library files.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Download Episodes Show")
				rows := e.Rows(t, `SELECT id, season, episode, episodeType, title, subtitle, description, pubDate,
					enclosureURL, enclosureSize, enclosureType, publishedAt, extraData, audioFile
					FROM podcastEpisodes WHERE podcastId = ? ORDER BY title`, show.Media.ID)
				if len(rows) != 3 {
					t.Fatalf("%d stored episodes, want 3: %+v", len(rows), rows)
				}
				wantGuids := []string{"episodes-a", "episodes-b", "episodes-c"}
				for i, row := range rows {
					title := "Episode " + string(rune('A'+i))
					requireString(t, row, "title", title)
					requireString(t, row, "season", "2")
					requireString(t, row, "episode", "7")
					requireString(t, row, "episodeType", "full")
					requireString(t, row, "enclosureType", "audio/mpeg")
					if row["enclosureSize"] != int64(96673) {
						t.Fatalf("enclosureSize = %v (%T)", row["enclosureSize"], row["enclosureSize"])
					}
					extra, _ := row["extraData"].(string)
					if !strings.Contains(extra, `"guid":"`+wantGuids[i]+`"`) {
						t.Fatalf("extraData = %q, want guid %q", extra, wantGuids[i])
					}
					audio, _ := row["audioFile"].(string)
					if !strings.Contains(audio, `"filename":"`+title+`.mp3"`) {
						t.Fatalf("stored audioFile = %q for %q", audio, title)
					}
					requireString(t, row, "description", "<p>"+title+"</p>")
					requireString(t, row, "pubDate", []string{"Mon, 05 May 2025 10:00:00 GMT", "Tue, 06 May 2025 10:00:00 GMT", "Wed, 07 May 2025 10:00:00 GMT"}[i])
					episodeID, _ := row["id"].(string)
					read := getEpisode(t, e, show.ID, episodeID)
					if read.AudioFile == nil || read.AudioFile.Metadata.Size <= 0 || read.AudioFile.Duration <= 0 {
						t.Fatalf("episode %s audio = %+v", title, read.AudioFile)
					}
					if read.Enclosure == nil || strValue(t, "enclosure url", &read.Enclosure.URL) == "" {
						t.Fatalf("episode %s enclosure = %+v", title, read.Enclosure)
					}
					requireValue(t, "enclosure length", read.Enclosure.Length, "96673")
					requireValue(t, "guid", read.GUID, wantGuids[i])
					data := readEpisodeFile(t, read.AudioFile.Metadata.Path)
					if int64(len(data)) != read.AudioFile.Metadata.Size {
						t.Fatalf("episode %s file size %d, metadata %d", title, len(data), read.AudioFile.Metadata.Size)
					}
					if !strings.HasPrefix(read.AudioFile.Metadata.Path, show.Path) || read.AudioFile.Metadata.RelPath != read.AudioFile.Metadata.Filename {
						t.Fatalf("episode %s audio path = %+v", title, read.AudioFile.Metadata)
					}
				}
				item := itemOf(t, e, show.ID)
				if len(item.Media.Episodes) != 3 || item.Media.NumEpisodes != 3 || len(item.LibraryFiles) != 3 {
					t.Fatalf("item episodes %d numEpisodes %d libraryFiles %d", len(item.Media.Episodes), item.Media.NumEpisodes, len(item.LibraryFiles))
				}
				if item.Media.Size <= 0 {
					t.Fatalf("item size = %d, want the summed file size", item.Media.Size)
				}
			},
		},
		{
			Requirement: downloadEpisodesReq, Invariant: downloadEpisodesOrd, Case: "edge", State: "repeat", Assertion: "download-episodes-skips-a-waiting-duplicate",
			Sources: []string{downloadEpisodesMgr},
			Claim:   "Posting an episode whose URL is already waiting for the same item answers 200 without a second queued event or a second queue entry.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Dedupe Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running, waiting := stub.url("/dedupe-a.mp3"), stub.url("/dedupe-b.mp3")
				stub.setHeldAudio("/dedupe-a.mp3", audio, len(audio)/2, 4*time.Second)
				stub.setAudio("/dedupe-b.mp3", audio)
				a := downloadEpisode("Dedupe A", "dedupe-a", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
				b := downloadEpisode("Dedupe B", "dedupe-b", waiting, "Tue, 06 May 2025 10:00:00 GMT", 1746525600000)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "first batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{a, b}), http.StatusOK)
				bSocket := e.Socket(t, e.Token(t, "root"))
				bMark := bSocket.Mark()
				wantStatus(t, "duplicate batch", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{b}), http.StatusOK)
				bSocket.None(t, bMark, "episode_download_queued", quietWindow, downloadMatcher(waiting))
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 1 {
					t.Fatalf("queue = %+v, want one waiting download", got.Downloads)
				}
				socket.Wait(t, 0, "episode_download_started", eventWait, downloadMatcher(running))
				waitIdle(t, e)
			},
		},
		{
			Requirement: downloadEpisodesReq, Invariant: downloadEpisodesOrd, Case: "edge", State: "repeat", Assertion: "download-episodes-skips-the-running-episode",
			Sources: []string{downloadEpisodesMgr},
			Claim:   "Posting the episode that is already downloading for the same item answers 200 without a second started event or a queue entry.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Dedupe Running Show")
				stub := newAudioStub(t)
				audio := fixtureEpisodeMP3(t, e)
				running := stub.url("/dedupe-running.mp3")
				stub.setHeldAudio("/dedupe-running.mp3", audio, len(audio)/2, 4*time.Second)
				episode := downloadEpisode("Running Once", "dedupe-running", running, "Mon, 05 May 2025 10:00:00 GMT", 1746439200000)
				socket := e.Socket(t, e.Token(t, "root"))
				wantStatus(t, "first post", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{episode}), http.StatusOK)
				socket.Wait(t, 0, "episode_download_started", eventWait, downloadMatcher(running))
				duplicate := e.Socket(t, e.Token(t, "root"))
				mark := duplicate.Mark()
				wantStatus(t, "duplicate post", postDownloads(t, e, show.ID, e.Token(t, "root"), []map[string]any{episode}), http.StatusOK)
				duplicate.None(t, mark, "episode_download_started", quietWindow, downloadMatcher(running))
				duplicate.None(t, mark, "episode_download_queued", quietWindow, nil)
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 0 {
					t.Fatalf("queue = %+v, want none", got.Downloads)
				}
				waitIdle(t, e)
			},
		},
		{
			Requirement: downloadEpisodesReq, Invariant: downloadEpisodesAuth, Case: "error", State: "failure", Assertion: "download-episodes-rejects-empty-and-unauthorised-requests",
			Sources: []string{downloadEpisodesSrc, episodeMidSrc},
			Claim:   "POST /api/podcasts/:id/download-episodes answers 400 for an empty or non-array body and 401/403 for anonymous, non-admin and library-restricted callers, 404 for an unknown item and 500 for a book item, and queues nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Download Reject Show")
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": ""})
				before := takeSnapshot(t, e)
				restricted := libraryRestrictedToken(t, e, "download-episodes-restricted")
				updater := createUser(t, e, "download-episodes-updater", map[string]any{"update": true})
				path := "/api/podcasts/" + show.ID + "/download-episodes"
				wantStatus(t, "empty body", e.Post(t, e.Token(t, "root"), path, []any{}), http.StatusBadRequest)
				wantStatus(t, "object body", e.Post(t, e.Token(t, "root"), path, map[string]any{"title": "x"}), http.StatusBadRequest)
				wantStatus(t, "anonymous", e.Post(t, "", path, []any{map[string]any{"title": "x"}}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), path, []any{map[string]any{"title": "x"}}), http.StatusForbidden)
				wantStatus(t, "non-admin with update permission", e.Post(t, updater, path, []any{map[string]any{"title": "x"}}), http.StatusForbidden)
				wantStatus(t, "unknown item before permission", e.Post(t, restricted, "/api/podcasts/not-an-item/download-episodes", []any{map[string]any{"title": "x"}}), http.StatusNotFound)
				wantStatus(t, "restricted", e.Post(t, restricted, path, []any{map[string]any{"title": "x"}}), http.StatusForbidden)
				wantStatus(t, "book item", e.Post(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/download-episodes", []any{map[string]any{"title": "x"}}), http.StatusInternalServerError)
				audience.receive(t, "episode_download_started", nil, nil, []string{"admin", "listener", "anonymous"})
				audience.receive(t, "episode_download_queued", nil, nil, []string{"admin", "listener", "anonymous"})
				before.requireUnchanged(t, e, "rejected download requests")
				if got := queueOf(t, e, show.ID); len(got.Downloads) != 0 {
					t.Fatalf("queue = %+v", got.Downloads)
				}
			},
		},
	}
}
