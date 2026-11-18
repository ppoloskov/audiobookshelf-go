package podcast_episodes

import (
	"net/http"
	"os"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	episodeDeleteReq  = "route-r161"
	episodeDeleteInv  = "INV-ROUTE-R161-CONTRACT"
	episodeDeleteAuth = "INV-ROUTE-R161-AUTH-001"
	episodeDeleteSrc  = "server/controllers/PodcastController.js:498-557"
)

// deleteEpisode removes one episode, optionally hard.
func deleteEpisode(t *testing.T, e *harness.Env, token, itemID, episodeID string, hard bool) harness.Response {
	t.Helper()
	path := "/api/podcasts/" + itemID + "/episode/" + episodeID
	if hard {
		path += "?hard=1"
	}
	return e.Delete(t, token, path)
}

// downloadedEpisode returns one stored episode of a show by guid.
func downloadedEpisode(t *testing.T, e *harness.Env, item libraryItemJSON, guid string) episodeJSON {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ? AND extraData LIKE ?", item.Media.ID, "%"+guid+"%")
	if len(rows) != 1 {
		t.Fatalf("%d episodes for guid %q", len(rows), guid)
	}
	id, _ := rows[0]["id"].(string)
	return episodeOf(t, itemOf(t, e, item.ID), id)
}

func episodeDeleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: episodeDeleteReq, Invariant: episodeDeleteInv, Case: "normal", State: "initial", Assertion: "episode-delete-soft-removes-row-and-keeps-file",
			Sources: []string{episodeDeleteSrc},
			Claim:   "DELETE /api/podcasts/:id/episode/:episodeId without hard removes the episode row, its library file entry and its media progress, keeps the audio file on disk, decrements numEpisodes, emits item_updated and answers the library item without the episode.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Download Episodes Show")
				episode := downloadedEpisode(t, e, show, "episodes-a")
				if episode.AudioFile == nil {
					t.Fatalf("episode %s has no audio file", episode.ID)
				}
				audioPath := episode.AudioFile.Metadata.Path
				wantStatus(t, "create progress", e.Patch(t, e.Token(t, "root"), "/api/me/progress/"+show.ID+"/"+episode.ID,
					map[string]any{"duration": 24.0, "currentTime": 1.0, "progress": 0.04}), http.StatusOK)
				if got := e.Count(t, "SELECT count(*) FROM mediaProgresses WHERE mediaItemId = ?", episode.ID); got != 1 {
					t.Fatalf("%d media progress rows before the delete, want 1", got)
				}
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				resp := deleteEpisode(t, e, e.Token(t, "root"), show.ID, episode.ID, false)
				wantStatus(t, "delete episode", resp, http.StatusOK)
				item := decode[libraryItemJSON](t, resp.Body)
				if item.ID != show.ID || len(item.Media.Episodes) != 2 || len(item.LibraryFiles) != 2 {
					t.Fatalf("item after soft delete: episodes %d files %d", len(item.Media.Episodes), len(item.LibraryFiles))
				}
				if read := itemOf(t, e, show.ID); read.Media.NumEpisodes != 2 || len(read.Media.Episodes) != 2 {
					t.Fatalf("stored item after soft delete: numEpisodes %d episodes %d", read.Media.NumEpisodes, len(read.Media.Episodes))
				}
				for _, remaining := range item.Media.Episodes {
					if remaining.ID == episode.ID {
						t.Fatalf("deleted episode is still listed")
					}
				}
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE id = ?", episode.ID); len(rows) != 0 {
					t.Fatalf("deleted episode row remains")
				}
				if got := e.Count(t, "SELECT count(*) FROM mediaProgresses WHERE mediaItemId = ?", episode.ID); got != 0 {
					t.Fatalf("%d media progress rows remain after the delete", got)
				}
				if _, err := os.Stat(audioPath); err != nil {
					t.Fatalf("soft delete removed the audio file: %v", err)
				}
				audience.receive(t, "item_updated", itemUpdatedMatcher(show.ID), []string{"admin", "listener"}, nil)
			},
		},
		{
			Requirement: episodeDeleteReq, Invariant: episodeDeleteInv, Case: "normal", State: "force", Assertion: "episode-delete-hard-removes-the-file",
			Sources: []string{episodeDeleteSrc},
			Claim:   "DELETE with hard=1 removes the episode row, its library file entry and its audio file from disk and answers the library item without it.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Download Episodes Show")
				episode := downloadedEpisode(t, e, show, "episodes-b")
				if episode.AudioFile == nil {
					t.Fatalf("episode %s has no audio file", episode.ID)
				}
				audioPath := episode.AudioFile.Metadata.Path
				resp := deleteEpisode(t, e, e.Token(t, "root"), show.ID, episode.ID, true)
				wantStatus(t, "hard delete episode", resp, http.StatusOK)
				item := decode[libraryItemJSON](t, resp.Body)
				if len(item.Media.Episodes) != 1 || len(item.LibraryFiles) != 1 {
					t.Fatalf("item after hard delete: episodes %d files %d", len(item.Media.Episodes), len(item.LibraryFiles))
				}
				if read := itemOf(t, e, show.ID); read.Media.NumEpisodes != 1 || len(read.Media.Episodes) != 1 {
					t.Fatalf("stored item after hard delete: numEpisodes %d episodes %d", read.Media.NumEpisodes, len(read.Media.Episodes))
				}
				if rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE id = ?", episode.ID); len(rows) != 0 {
					t.Fatalf("hard deleted episode row remains")
				}
				if _, err := os.Stat(audioPath); !os.IsNotExist(err) {
					t.Fatalf("hard delete left the audio file: %v", err)
				}
			},
		},
		{
			Requirement: episodeDeleteReq, Invariant: episodeDeleteInv, Case: "edge", State: "repeat", Assertion: "episode-delete-repeat-is-not-found",
			Sources: []string{episodeDeleteSrc},
			Claim:   "Deleting an already deleted episode answers 404 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Download Episodes Show")
				rows := e.Rows(t, "SELECT id FROM podcastEpisodes WHERE podcastId = ?", show.Media.ID)
				if len(rows) != 1 {
					t.Fatalf("%d remaining episodes", len(rows))
				}
				episodeID, _ := rows[0]["id"].(string)
				wantStatus(t, "first delete", deleteEpisode(t, e, e.Token(t, "root"), show.ID, episodeID, false), http.StatusOK)
				before := takeSnapshot(t, e)
				wantStatus(t, "repeat delete", deleteEpisode(t, e, e.Token(t, "root"), show.ID, episodeID, false), http.StatusNotFound)
				wantStatus(t, "repeat hard delete", deleteEpisode(t, e, e.Token(t, "root"), show.ID, episodeID, true), http.StatusNotFound)
				before.requireUnchanged(t, e, "repeated episode deletes")
			},
		},
		{
			Requirement: episodeDeleteReq, Invariant: episodeDeleteInv, Case: "normal", State: "changed", Assertion: "episode-delete-is-reflected-in-the-item",
			Sources: []string{episodeDeleteSrc},
			Claim:   "After deleting an episode, a fresh item read no longer lists the episode or its library file and reports the decremented numEpisodes, while the audio file remains.",
			Run: func(t *testing.T, e *harness.Env) {
				show := showByTitle(t, e, "Socket Added Show")
				episode := downloadedEpisode(t, e, show, "added-a")
				if episode.AudioFile == nil {
					t.Fatalf("episode %s has no audio file", episode.ID)
				}
				audioPath := episode.AudioFile.Metadata.Path
				wantStatus(t, "delete episode", deleteEpisode(t, e, e.Token(t, "root"), show.ID, episode.ID, false), http.StatusOK)
				read := itemOf(t, e, show.ID)
				if read.Media.NumEpisodes != 0 || len(read.Media.Episodes) != 0 || len(read.LibraryFiles) != 0 {
					t.Fatalf("item after the delete: numEpisodes %d episodes %d files %d", read.Media.NumEpisodes, len(read.Media.Episodes), len(read.LibraryFiles))
				}
				if _, err := os.Stat(audioPath); err != nil {
					t.Fatalf("soft delete removed the audio file: %v", err)
				}
			},
		},
		{
			Requirement: episodeDeleteReq, Invariant: episodeDeleteAuth, Case: "error", State: "failure", Assertion: "episode-delete-rejections-change-nothing",
			Sources: []string{episodeMidSrc, episodeDeleteSrc},
			Claim:   "DELETE /api/podcasts/:id/episode/:episodeId answers 401 without a token, 403 for a user without delete permission or without access to the library, 404 for an unknown item or episode and 500 for a book item; no rejected request changes a row or a file.",
			Run: func(t *testing.T, e *harness.Env) {
				show := ensureShow(t, e, "Delete Reject Show")
				before := takeSnapshot(t, e)
				episodeID := e.IDs["ep1"]
				restricted := libraryRestrictedToken(t, e, "episode-delete-restricted")
				path := "/api/podcasts/" + show.ID + "/episode/" + episodeID
				wantStatus(t, "anonymous", e.Delete(t, "", path), http.StatusUnauthorized)
				wantStatus(t, "listener without delete", e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden)
				wantStatus(t, "unknown item before permission", e.Delete(t, restricted, "/api/podcasts/not-an-item/episode/"+episodeID), http.StatusNotFound)
				wantStatus(t, "restricted", e.Delete(t, restricted, path), http.StatusForbidden)
				wantStatus(t, "book item", e.Delete(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/episode/"+episodeID), http.StatusInternalServerError)
				wantStatus(t, "unknown episode", e.Delete(t, e.Token(t, "root"), "/api/podcasts/"+show.ID+"/episode/not-an-episode"), http.StatusNotFound)
				before.requireUnchanged(t, e, "rejected episode deletes")
			},
		},
	}
}
