package playback

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Source evidence: server/models/Device.js:56-73 builds extraData by copying
// only truthy values; PlaybackSessionManager.js:52-71 calls it on both create
// and update paths. An empty UA/client therefore persists {}.
func TestDeviceExtraDataOmitsAbsentFieldsOnCreateAndUpdate(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	for i := 0; i < 2; i++ {
		w := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user,
			`{"forceDirectPlay":true,"deviceInfo":{"deviceId":"empty-device"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("start %d: %s", w.Code, w.Body.String())
		}
		var raw string
		if err := h.db.QueryRow(`SELECT extraData FROM devices WHERE deviceId='empty-device'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("device extraData=%s, want {}", raw)
		}
	}
}

// Source evidence: PlaybackSession.js:209-218 assigns oldMetadataToJSON;
// Book.js:550-569 defines precisely the legacy persisted book metadata keys.
func TestPersistedPlaybackMetadataUsesLegacyBookSubset(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	w := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, `{"forceDirectPlay":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %s", w.Body.String())
	}
	var sid string
	var session map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	sid, _ = session["id"].(string)
	if synced := request(h, http.MethodPost, "/api/session/"+sid+"/sync", user, `{"currentTime":1,"timeListened":1}`); synced.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", synced.Code, synced.Body.String())
	}
	var meta string
	if err := h.db.QueryRow(`SELECT mediaMetadata FROM playbackSessions WHERE id=?`, sid).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(meta), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"titleIgnorePrefix", "authorName", "authorNameLF", "narratorName", "seriesName", "descriptionPlain"} {
		if _, ok := got[key]; ok {
			t.Errorf("persisted metadata includes expanded key %q", key)
		}
	}
	for _, key := range []string{"title", "subtitle", "authors", "narrators", "series", "genres", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language", "explicit", "abridged"} {
		if _, ok := got[key]; !ok {
			t.Errorf("persisted metadata missing legacy key %q", key)
		}
	}
}

// Source evidence: PlaybackSessionManager.js:373-411 sends session.progress
// (PlaybackSession.js:176-197 derives currentTime/duration) during sync; close
// calls syncSession at PlaybackSessionManager.js:421-425. MediaProgress.js:178-182
// clamps that ratio to [0,1], and applyProgressUpdate:206-212 stores it.
func TestSyncProgressRecomputesStoredRatioFromCurrentTime(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	w := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, `{"forceDirectPlay":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %s", w.Body.String())
	}
	var id string
	var session map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	id, _ = session["id"].(string)
	// Set a non-derived legacy extraData value, then sync at 1/3 of duration.
	if _, err := h.db.Exec(`INSERT INTO mediaProgresses (id,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,extraData,userId) VALUES ('p','book-one','book',3,3,0,0,'{"progress":1}','user-one')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE libraries SET settings='{"markAsFinishedPercentComplete":99,"markAsFinishedTimeRemaining":0}'`); err != nil {
		t.Fatal(err)
	}
	sync := request(h, http.MethodPost, "/api/session/"+id+"/sync", user, `{"currentTime":1,"timeListened":1,"duration":3}`)
	if sync.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", sync.Code, sync.Body.String())
	}
	var extra string
	if err := h.db.QueryRow(`SELECT extraData FROM mediaProgresses WHERE id='p'`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(extra), &got)
	if got["progress"] != 1.0/3 {
		t.Fatalf("stored progress=%v, want 1/3", got["progress"])
	}
}
