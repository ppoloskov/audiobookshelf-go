package item_write

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r067          = "route-r067"
	deleteFileSrc = controllerSrc + ":1012-1064"
)

func deleteFile(t *testing.T, e *harness.Env, token, itemID, fileID string) harness.Response {
	t.Helper()
	return e.Delete(t, token, "/api/items/"+itemID+"/file/"+fileID)
}

// libraryFileNames lists the stored library file names of an item in stored order.
func libraryFileNames(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	var out []string
	for _, file := range libraryFileList(t, e, itemID) {
		name, _ := harness.Dig(file, "metadata", "filename").(string)
		out = append(out, name)
	}
	return out
}

func deleteFileScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R067-DELETE-FILE-001"
	const errInv = "INV-ROUTE-R067-CONTRACT"
	return []harness.Scenario{
		scenario(r067, errInv, "error", "failure", "file-delete-authorisation",
			"DELETE file answers 401 without a token, 404 for an unknown item or file id (also for users without delete permission), and 403 for users without delete permission or access to the item; the file stays on disk and no row changes.",
			srcs(middlewareSrc, deleteFileSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				cover := inoOf(t, e, id, "cover.jpg")
				path := filepath.Join(itemPath(t, e, id), "cover.jpg")
				rows := snapshot(t, e)
				a := itemAudience(t, e, s)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/items/" + id + "/file/" + cover}), http.StatusUnauthorized)
				wantStatus(t, "unknown item", deleteFile(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", cover), http.StatusNotFound)
				wantStatus(t, "unknown file before permission", deleteFile(t, e, e.Token(t, "listener"), id, "999999999"), http.StatusNotFound)
				for _, name := range []string{"updater", "podcastonly", "tagallow"} {
					wantStatus(t, name, deleteFile(t, e, s.token(t, e, name), id, cover), http.StatusForbidden)
				}
				wantStatus(t, "listener", deleteFile(t, e, e.Token(t, "listener"), id, cover), http.StatusForbidden)
				if !pathExists(path) {
					t.Fatal("a rejected request removed the file")
				}
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", nil, nil, []string{"root"})
			}),
		scenario(r067, inv, "normal", "initial", "file-delete-non-media-file",
			"DELETE of a cover image by a user with only delete permission removes the file and its libraryFiles entry, keeps the audio files, answers 200, moves updatedAt and emits item_updated.",
			srcs(deleteFileSrc, middlewareSrc, "client/components/tables/LibraryFilesTableRow.vue"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				cover := inoOf(t, e, id, "cover.jpg")
				path := filepath.Join(itemPath(t, e, id), "cover.jpg")
				requireEqual(t, "fixture files", libraryFileNames(t, e, id), []string{"01 - Part 1.mp3", "02 - Part 2.mp3", "03 - Part 3.mp3", "cover.jpg"})
				a := itemAudience(t, e, s)
				before := updatedAt(t, e, id)
				audio := audioNames(audioFileList(t, e, id))
				wantStatus(t, "delete", deleteFile(t, e, s.token(t, e, "deleter"), id, cover), http.StatusOK)
				if pathExists(path) {
					t.Fatal("file still on disk")
				}
				requireEqual(t, "libraryFiles", libraryFileNames(t, e, id), []string{"01 - Part 1.mp3", "02 - Part 2.mp3", "03 - Part 3.mp3"})
				requireEqual(t, "audioFiles", audioNames(audioFileList(t, e, id)), audio)
				if itemRow(t, e, id)["isMissing"] != int64(0) {
					t.Fatal("item flagged missing")
				}
				wantMoved(t, e, id, before)
				events := a.receive(t, "item_updated", idOf(id), []string{"root", "listener"}, []string{"podcastonly", "anonymous"})
				if files, _ := harness.Dig(payload(t, events["root"]), "libraryFiles").([]any); len(files) != 3 {
					t.Fatalf("payload libraryFiles %v", files)
				}
				repeat := deleteFile(t, e, e.Token(t, "root"), id, cover)
				wantStatus(t, "repeat", repeat, http.StatusNotFound)
			}),
		scenario(r067, inv, "normal", "changed", "file-delete-audio-file",
			"DELETE of an audio file removes it from disk, libraryFiles and the book's audioFiles; the item stays present and the book duration column is unchanged.",
			srcs(deleteFileSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				part := inoOf(t, e, id, "03 - Part 3.mp3")
				path := filepath.Join(itemPath(t, e, id), "03 - Part 3.mp3")
				duration := mediaRow(t, e, id)["duration"]
				if duration != 60.186123 {
					t.Fatalf("fixture duration %v", duration)
				}
				a := itemAudience(t, e, s)
				wantStatus(t, "delete", deleteFile(t, e, e.Token(t, "root"), id, part), http.StatusOK)
				if pathExists(path) {
					t.Fatal("audio file still on disk")
				}
				requireEqual(t, "audioFiles", audioNames(audioFileList(t, e, id)), []string{"01 - Part 1.mp3:1:false", "02 - Part 2.mp3:2:false"})
				requireEqual(t, "libraryFiles", libraryFileNames(t, e, id), []string{"01 - Part 1.mp3", "02 - Part 2.mp3"})
				if itemRow(t, e, id)["isMissing"] != int64(0) || mediaRow(t, e, id)["duration"] != duration {
					t.Fatalf("isMissing %v duration %v", itemRow(t, e, id)["isMissing"], mediaRow(t, e, id)["duration"])
				}
				events := a.receive(t, "item_updated", idOf(id), []string{"root"}, []string{"podcastonly"})
				if tracks, _ := harness.Dig(payload(t, events["root"]), "media", "tracks").([]any); len(tracks) != 2 {
					t.Fatalf("payload tracks %v", tracks)
				}
			}),
		scenario(r067, inv, "edge", "changed", "file-delete-ebook-files",
			"Deleting a supplementary ebook leaves the primary ebookFile; deleting the primary ebook clears ebookFile and flags the item missing.",
			srcs(deleteFileSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				root := e.Token(t, "root")
				if supplementaryOf(t, e, id, extraEbook) != true || primaryName(t, e, id) != ebookName {
					t.Fatal("ebook scenarios did not leave the original primary and the extra supplementary")
				}
				wantStatus(t, "delete supplementary", deleteFile(t, e, root, id, inoOf(t, e, id, extraEbook)), http.StatusOK)
				if primaryName(t, e, id) != ebookName || itemRow(t, e, id)["isMissing"] != int64(0) {
					t.Fatal("deleting the supplementary file changed the primary or the missing flag")
				}
				requireEqual(t, "libraryFiles", libraryFileNames(t, e, id), []string{ebookName})
				wantStatus(t, "delete primary", deleteFile(t, e, root, id, inoOf(t, e, id, ebookName)), http.StatusOK)
				if column(t, mediaRow(t, e, id), "ebookFile") != nil || itemRow(t, e, id)["isMissing"] != int64(1) {
					t.Fatal("ebookFile not cleared or item not flagged missing")
				}
				requireEqual(t, "libraryFiles", libraryFileNames(t, e, id), []string(nil))
				if _, err := os.Stat(filepath.Join(itemPath(t, e, id), ebookName)); err == nil {
					t.Fatal("ebook still on disk")
				}
			}),
		scenario(r067, inv, "normal", "changed", "file-delete-podcast-episode-cascades",
			"Deleting an episode's file removes the episode row, its progress and its playlist entries (renumbering the playlist, emptying and removing it with playlist_removed to its owner only) and keeps the other episodes.",
			srcs(deleteFileSrc, "server/models/Playlist.js:172-208"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				root := e.Token(t, "root")
				a := connect(t, e, map[string]string{"root": root, "listener": e.Token(t, "listener"), "anonymous": ""})
				requireEqual(t, "fixture playlist", playlistOrder(t, e, e.IDs["podcastPlaylist"]), []string{e.IDs["ep3"] + ":1", e.IDs["ep1"] + ":2"})
				if e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN (?, ?)", e.IDs["ep1"], e.IDs["ep3"]) != 2 {
					t.Fatal("fixture progress of ep1/ep3 missing")
				}
				wantStatus(t, "delete ep3", deleteFile(t, e, root, id, inoOf(t, e, id, "episode-3.mp3")), http.StatusOK)
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE id = ?", e.IDs["ep3"]) != 0 || e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", e.IDs["ep3"]) != 0 {
					t.Fatal("episode or its progress remains")
				}
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId = ?", mediaRow(t, e, id)["id"]) != 2 {
					t.Fatal("other episodes were removed")
				}
				requireEqual(t, "playlist after first delete", playlistOrder(t, e, e.IDs["podcastPlaylist"]), []string{e.IDs["ep1"] + ":1"})
				updated := a.receive(t, "playlist_updated", idOf(e.IDs["podcastPlaylist"]), []string{"listener"}, []string{"root", "anonymous"})
				items, _ := payload(t, updated["listener"])["items"].([]any)
				if len(items) != 1 || harness.Dig(items, 0, "episodeId") != e.IDs["ep1"] {
					t.Fatalf("playlist_updated items %v", items)
				}
				a.receive(t, "item_updated", idOf(id), []string{"root", "listener"}, []string{"anonymous"})
				a.reset()
				wantStatus(t, "delete ep1", deleteFile(t, e, root, id, inoOf(t, e, id, "episode-1.mp3")), http.StatusOK)
				if e.Count(t, "SELECT COUNT(*) FROM playlists WHERE id = ?", e.IDs["podcastPlaylist"]) != 0 || e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId = ?", e.IDs["podcastPlaylist"]) != 0 {
					t.Fatal("emptied playlist remains")
				}
				if e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", e.IDs["ep1"]) != 0 {
					t.Fatal("progress of ep1 remains")
				}
				removed := a.receive(t, "playlist_removed", idOf(e.IDs["podcastPlaylist"]), []string{"listener"}, []string{"root", "anonymous"})
				if got := payload(t, removed["listener"]); got["userId"] != e.IDs["listener"] || got["name"] != "Cast queue" {
					t.Fatalf("playlist_removed payload %v", got)
				}
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE id = ?", e.IDs["ep2"]) != 1 || e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", e.IDs["ep2"]) != 1 {
					t.Fatal("ep2 or its progress was removed")
				}
				requireEqual(t, "libraryFiles", libraryFileNames(t, e, id), []string{"episode-2.mp3", "cover.jpg"})
			}),
	}
}

// playlistOrder lists "mediaItemId:order" of a playlist in order.
func playlistOrder(t *testing.T, e *harness.Env, playlistID string) []string {
	t.Helper()
	var out []string
	for _, row := range e.Rows(t, "SELECT mediaItemId, `order` AS ord FROM playlistMediaItems WHERE playlistId = ? ORDER BY `order`", playlistID) {
		out = append(out, row["mediaItemId"].(string)+":"+itoa(row["ord"]))
	}
	return out
}
