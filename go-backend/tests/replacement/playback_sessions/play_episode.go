package playback_sessions

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const episodeSource = "server/controllers/LibraryItemController.js:451-472"

var podcastMetadata = map[string]any{
	"title": "Fixture Cast", "author": "Fixture Host", "description": nil, "releaseDate": nil, "genres": []any{"Technology"},
	"feedUrl": nil, "imageUrl": nil, "itunesPageUrl": nil, "itunesId": nil, "itunesArtistId": nil, "explicit": false, "language": nil, "type": "episodic",
}

func episodeScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r060", Invariant: "INV-R060-CONTRACT-001", Case: "normal", State: "initial", Assertion: "play-episode-direct-session",
			Sources: []string{"server/routers/ApiRouter.js:118", episodeSource, startSource, "server/models/Podcast.js:302-333"},
			Claim:   "POST /api/items/:id/play/:episodeId starts a session for that episode with the episode's title, duration, single track, resume position and the podcast as item.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				doc := play(t, e, listener.Token, e.IDs["podcast"], e.IDs["ep1"], playBody{"mediaPlayer": "AVPlayer", "supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-episode-1", "clientVersion": "1.4.0", "manufacturer": "Apple", "model": "iPhone15,2"}})
				if doc.UserID != listener.ID || doc.LibraryID != e.IDs["podcastLibrary"] || doc.LibraryItemID == nil || *doc.LibraryItemID != e.IDs["podcast"] ||
					doc.BookID != nil || doc.EpisodeID == nil || *doc.EpisodeID != e.IDs["ep1"] || doc.MediaType != "podcast" {
					t.Fatalf("episode session ids wrong: %+v", doc)
				}
				requireEqual(t, "mediaMetadata", doc.MediaMetadata, podcastMetadata)
				if doc.Chapters == nil || len(doc.Chapters) != 0 {
					t.Fatalf("an episode has no chapters, got %v", doc.Chapters)
				}
				if doc.DisplayTitle != "Pilot Episode" || doc.DisplayAuthor != "Fixture Host" ||
					doc.CoverPath == nil || *doc.CoverPath != mediaPath(e, "podcasts/Fixture Cast/cover.jpg") {
					t.Fatalf("display fields: %q by %q cover %v", doc.DisplayTitle, doc.DisplayAuthor, doc.CoverPath)
				}
				if doc.Duration == nil || !near(*doc.Duration, ep1Duration) || doc.PlayMethod == nil || *doc.PlayMethod != 0 || doc.MediaPlayer == nil || *doc.MediaPlayer != "AVPlayer" {
					t.Fatalf("duration %v playMethod %v mediaPlayer %v", doc.Duration, doc.PlayMethod, doc.MediaPlayer)
				}
				requireNear(t, "startTime of the seeded episode progress", doc.StartTime, 10.5)
				requireNear(t, "currentTime", doc.CurrentTime, 10.5)
				if len(doc.AudioTracks) != 1 {
					t.Fatalf("an episode has one track, got %d", len(doc.AudioTracks))
				}
				track := doc.AudioTracks[0]
				if track.Index != 1 || track.Ino != "1319949" || !near(track.Duration, ep1Duration) || track.Offset != 0 || track.MimeType != "audio/mpeg" ||
					track.Codec == nil || *track.Codec != "mp3" || track.Title != "episode-1.mp3" || track.URL != "/api/items/"+e.IDs["podcast"]+"/file/1319949" ||
					track.Metadata == nil || track.Metadata.Filename != "episode-1.mp3" || track.Metadata.Path != mediaPath(e, "podcasts/Fixture Cast/episode-1.mp3") {
					t.Fatalf("episode track wrong: %+v", track)
				}
				if doc.LibraryItem == nil || doc.LibraryItem.ID != e.IDs["podcast"] || doc.LibraryItem.MediaType != "podcast" || doc.LibraryItem.LibraryID != e.IDs["podcastLibrary"] {
					t.Fatalf("expanded libraryItem wrong: %+v", doc.LibraryItem)
				}
				read := mustGetSession(t, e, listener.Token, doc.ID)
				requireEqual(t, "GET session equals play answer", read, doc)
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r060", Invariant: "INV-R060-CONTRACT-001", Case: "edge", State: "initial", Assertion: "play-episode-method-and-start-times",
			Sources: []string{episodeSource, startSource, "server/models/Podcast.js:302-313"},
			Claim:   "For an episode the mime type of that episode decides direct play against one HLS track, and each episode resumes at its own progress, finished episodes at zero.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				device := map[string]any{"deviceId": "ps-episode-2"}
				transcode := play(t, e, listener.Token, e.IDs["podcast"], e.IDs["ep3"], playBody{"supportedMimeTypes": []string{"audio/ogg"}, "deviceInfo": device})
				if transcode.PlayMethod == nil || *transcode.PlayMethod != 2 {
					t.Fatalf("unsupported episode mime type: playMethod %v, want 2", transcode.PlayMethod)
				}
				requireHLSTrack(t, transcode, transcode.AudioTracks[0].Title, ep3Duration)
				requireNear(t, "ep3 startTime", transcode.StartTime, 6)
				forced := play(t, e, listener.Token, e.IDs["podcast"], e.IDs["ep2"], playBody{"forceDirectPlay": true, "deviceInfo": device})
				if forced.PlayMethod == nil || *forced.PlayMethod != 0 || len(forced.AudioTracks) != 1 || forced.AudioTracks[0].MimeType != "audio/mpeg" {
					t.Fatalf("forced direct play of ep2: %+v", forced.AudioTracks)
				}
				requireNear(t, "finished ep2 startTime", forced.StartTime, 0)
				if forced.EpisodeID == nil || *forced.EpisodeID != e.IDs["ep2"] {
					t.Fatalf("session must be for ep2: %v", forced.EpisodeID)
				}
				closeQuietly(t, e, listener.Token, forced.ID)
				if _, status := getSession(t, e, listener.Token, transcode.ID); status != http.StatusNotFound {
					t.Fatalf("same device replaced the first episode session, GET answered %d", status)
				}
			},
		},
		{
			Requirement: "route-r060", Invariant: "INV-R060-CONTRACT-001", Case: "normal", State: "changed", Assertion: "play-episode-sync-moves-episode-progress",
			Sources: []string{episodeSource, "server/managers/PlaybackSessionManager.js:340-373", "server/models/User.js:730-762"},
			Claim:   "Syncing an episode session updates only that episode's progress and the stored session; the next play of the episode resumes there.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				progressOf := func(id string) map[string]any { return progressRow(t, e, listener.ID, id) }
				ep2, ep3 := progressOf(e.IDs["ep2"]), progressOf(e.IDs["ep3"])
				before := progressOf(e.IDs["ep1"])
				doc := play(t, e, listener.Token, e.IDs["podcast"], e.IDs["ep1"], playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-episode-3", "clientVersion": "1.0", "model": "TestPhone"}})
				socket := e.Socket(t, listener.Token)
				mark := socket.Mark()
				syncSession(t, e, listener.Token, doc.ID, map[string]any{"currentTime": 13, "timeListened": 5, "duration": ep1Duration})
				event := waitProgressEvent(t, socket, mark, doc.ID)
				if event.DeviceDescription != "TestPhone / v1.0" {
					t.Fatalf("deviceDescription %q", event.DeviceDescription)
				}
				if event.ID != before["id"] || event.Data.EpisodeID == nil || *event.Data.EpisodeID != e.IDs["ep1"] || event.Data.MediaItemType != "podcastEpisode" ||
					event.Data.LibraryItemID != e.IDs["podcast"] || event.Data.MediaItemID != e.IDs["ep1"] || !near(event.Data.CurrentTime, 13) || !near(event.Data.Progress, 13/ep1Duration) {
					t.Fatalf("progress event wrong: %+v", event)
				}
				row := progressOf(e.IDs["ep1"])
				if !near(number(t, row["currentTime"]), 13) || !near(progressFraction(t, row), 13/ep1Duration) || row["isFinished"].(int64) != 0 || dbTime(t, row["updatedAt"]) <= dbTime(t, before["updatedAt"]) {
					t.Fatalf("episode progress row after sync: %v", row)
				}
				for name, want := range map[string]map[string]any{e.IDs["ep2"]: ep2, e.IDs["ep3"]: ep3} {
					requireEqual(t, "progress of another episode "+name, progressOf(name), want)
				}
				stored := waitListening(t, e, doc.ID, 5)
				if stored["mediaItemId"] != e.IDs["ep1"] || stored["mediaItemType"] != "podcastEpisode" || stored["libraryId"] != e.IDs["podcastLibrary"] || stored["displayTitle"] != "Pilot Episode" {
					t.Fatalf("stored episode session: %v", stored)
				}
				if stored["extraData"] != `{"libraryItemId":"`+e.IDs["podcast"]+`"}` {
					t.Fatalf("stored extraData: %v", stored["extraData"])
				}
				closeSession(t, e, listener.Token, doc.ID, map[string]any{"currentTime": 13.5, "timeListened": 0.5, "duration": ep1Duration})
				again := play(t, e, listener.Token, e.IDs["podcast"], e.IDs["ep1"], playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-episode-3"}})
				requireNear(t, "resume after sync", again.StartTime, 13.5)
				closeQuietly(t, e, listener.Token, again.ID)
			},
		},
	}
}
