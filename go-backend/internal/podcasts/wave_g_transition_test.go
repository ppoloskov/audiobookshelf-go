package podcasts

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// PodcastController.removeEpisode catches an audio-file removal error and
// continues the episode, progress, playlist, and library-file cleanup.
func TestWaveGHardDeleteMissingAudioStillCleansEpisodeState(t *testing.T) {
	r := newNotifyRig(t)
	missingPath := filepath.Join(r.root, "already-missing.mp3")
	if _, err := r.db.Exec(`UPDATE podcastEpisodes SET audioFile=? WHERE id='e1'`, `{"ino":"i1","metadata":{"path":"`+missingPath+`"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO mediaProgresses(id,mediaItemId) VALUES('progress-e1','e1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO playlists(id,name,userId) VALUES('pl','Mix','u1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO playlistMediaItems(id,mediaItemId,mediaItemType,"order",playlistId) VALUES('pmi','e1','podcastEpisode',1,'pl')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='item'`, `[{"ino":"i1","metadata":{"path":"`+missingPath+`"}}]`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/podcasts/item/episode/e1?hard=1", nil)
	if !serveAdmin(r.h, w, req) {
		t.Fatal("DELETE episode route did not match")
	}
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM podcastEpisodes WHERE id='e1'`:             0,
		`SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId='e1'`:    0,
		`SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemId='e1'`: 0,
	} {
		var got int
		if err := r.db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %d, err %v; want %d", query, got, err, want)
		}
	}
	var fileCount, episodeCount int
	if err := r.db.QueryRow(`SELECT json_array_length(libraryFiles) FROM libraryItems WHERE id='item'`).Scan(&fileCount); err != nil || fileCount != 0 {
		t.Errorf("libraryFiles length = %d, err %v; want 0", fileCount, err)
	}
	if err := r.db.QueryRow(`SELECT numEpisodes FROM podcasts WHERE id='pod'`).Scan(&episodeCount); err != nil || episodeCount != 1 {
		t.Errorf("numEpisodes = %d, err %v; want 1", episodeCount, err)
	}
	if len(r.events) == 0 || r.events[len(r.events)-1].event != "item_updated" {
		t.Errorf("events = %+v, want item_updated after cleanup", r.events)
	}
}
