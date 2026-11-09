package playback

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// makePodcast turns the fixture's item into a podcast with two episodes of
// 3 and 4 seconds ("episode-one", "episode-two") that share the fixture file.
func makePodcast(t *testing.T, h *env, itemID, mediaPath string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`UPDATE libraryItems SET mediaType='podcast',mediaId='podcast-one' WHERE id=?`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO podcasts (id,title,author,feedURL,language,podcastType,explicit,autoDownloadEpisodes,maxEpisodesToKeep,maxNewEpisodesToDownload,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('podcast-one','Pod','Host','https://f.example/rss','en','episodic',0,0,0,3,'[]','[]',2,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"episode-one", "episode-two"} {
		af, _ := json.Marshal(map[string]any{"index": 1, "ino": "ep-" + id, "metadata": map[string]any{"filename": id + ".wav", "ext": ".wav", "path": mediaPath, "size": 1000}, "duration": 3 + i, "exclude": false})
		if _, err := h.db.Exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,createdAt,updatedAt) VALUES (?,'podcast-one',?,?,'[]',?,?)`, id, "Title "+id, string(af), now, now); err != nil {
			t.Fatal(err)
		}
	}
}

// DeviceInfo.setData (server/objects/DeviceInfo.js:76-120) and
// Device.getFromOld (server/models/Device.js:49-80): the device row is built
// from the client's deviceInfo. httptest requests carry no User-Agent, so no
// browser/OS values are mixed in.
func TestDeviceRowIsWrittenFromClientDeviceInfo(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	play := func(deviceInfo string) {
		t.Helper()
		w := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, `{"forceDirectPlay":true,"deviceInfo":`+deviceInfo+`}`)
		if w.Code != http.StatusOK {
			t.Fatalf("play: %d %s", w.Code, w.Body.String())
		}
	}
	type device struct {
		id, clientName, clientVersion, deviceName, deviceVersion, userID string
		extra                                                            map[string]any
	}
	read := func(where string, arg any) device {
		t.Helper()
		var d device
		var name, version, extra *string
		var raw string
		err := h.db.QueryRow(`SELECT id,clientName,clientVersion,deviceName,deviceVersion,userId,extraData FROM devices WHERE `+where, arg).Scan(&d.id, &d.clientName, &d.clientVersion, &name, &version, &d.userID, &extra)
		if err != nil {
			t.Fatalf("device %s=%v: %v", where, arg, err)
		}
		if name != nil {
			d.deviceName = *name
		}
		if version != nil {
			d.deviceVersion = *version
		}
		if extra != nil {
			raw = *extra
		}
		if err := json.Unmarshal([]byte(raw), &d.extra); err != nil {
			t.Fatalf("extraData %q: %v", raw, err)
		}
		return d
	}

	// A numeric sdkVersion makes it an Android device named "<manufacturer> <model>";
	// unknown keys are not stored.
	play(`{"deviceId":"dev-1","manufacturer":"Google","model":"Pixel 8","sdkVersion":34,"clientName":"Abs Android","clientVersion":"0.9.79","bogus":"x"}`)
	d := read(`deviceId=?`, "dev-1")
	if d.clientName != "Abs Android" || d.clientVersion != "0.9.79" || d.deviceName != "Google Pixel 8" || d.deviceVersion != "34" || d.userID != user.ID {
		t.Errorf("android device = %+v", d)
	}
	if len(d.extra) != 2 || d.extra["manufacturer"] != "Google" || d.extra["model"] != "Pixel 8" {
		t.Errorf("android extraData = %v, want only manufacturer and model", d.extra)
	}

	// A model without an SDK version is an iOS device; the version defaults to the server's.
	play(`{"deviceId":"dev-2","clientName":"Abs iOS","model":"iPhone"}`)
	d = read(`deviceId=?`, "dev-2")
	if d.clientName != "Abs iOS" || d.clientVersion != "2.37.2" || d.deviceName != "Unknown iPhone" || d.deviceVersion != "" || d.extra["model"] != "iPhone" {
		t.Errorf("ios device = %+v", d)
	}

	// Without a deviceId the row's own id stands in for it, and a repeat play reuses the row.
	play(`{"clientName":"Abs Web"}`)
	var id, deviceID string
	if err := h.db.QueryRow(`SELECT id,deviceId FROM devices WHERE clientName='Abs Web'`).Scan(&id, &deviceID); err != nil || id == "" || id != deviceID {
		t.Errorf("web device id=%q deviceId=%q err=%v, want deviceId equal to the row id", id, deviceID, err)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&rows); err != nil || rows != 3 {
		t.Errorf("device rows = %d err=%v, want 3", rows, err)
	}
	play(`{"deviceId":"dev-1","manufacturer":"Google","model":"Pixel 8","sdkVersion":34,"clientName":"Abs Android","clientVersion":"0.9.79"}`)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE deviceId='dev-1'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("rows for dev-1 after a second play = %d err=%v, want 1", rows, err)
	}
}

// PlaybackSessionManager.syncSession (:373-411) updates the progress of the
// session's own media item, an episode here, and closeSession syncs once more.
func TestEpisodeSessionSyncAndCloseWriteEpisodeProgress(t *testing.T) {
	h, user, itemID, mediaPath := fixture(t)
	makePodcast(t, h, itemID, mediaPath)
	w := request(h, http.MethodPost, "/api/items/"+itemID+"/play/episode-two", user, `{"forceDirectPlay":true,"mediaPlayer":"html5"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("play episode: %d %s", w.Code, w.Body.String())
	}
	id, _ := decode(t, w)["id"].(string)
	progress := func() (mediaType, podcastID string, current, duration float64, finished int) {
		t.Helper()
		err := h.db.QueryRow(`SELECT mediaItemType,podcastId,currentTime,duration,isFinished FROM mediaProgresses WHERE userId=? AND mediaItemId='episode-two'`, user.ID).Scan(&mediaType, &podcastID, &current, &duration, &finished)
		if err != nil {
			t.Fatalf("episode progress: %v", err)
		}
		return
	}
	if w := request(h, http.MethodPost, "/api/session/"+id+"/sync", user, `{"currentTime":2,"timeListened":2,"duration":4}`); w.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	// The first sync creates the progress row, and creation never marks it finished (User.js:800-822).
	if kind, podcast, current, duration, finished := progress(); kind != "podcastEpisode" || podcast != "podcast-one" || current != 2 || duration != 4 || finished != 0 {
		t.Errorf("after sync: progress = %s %s current=%v duration=%v finished=%d", kind, podcast, current, duration, finished)
	}
	if w := request(h, http.MethodPost, "/api/session/"+id+"/close", user, `{"currentTime":3,"timeListened":1}`); w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	// An update does (MediaProgress.js:232-236): 1s remain, under the 10s default.
	if _, _, current, duration, finished := progress(); current != 3 || duration != 4 || finished != 1 {
		t.Errorf("after close: progress current=%v duration=%v finished=%d, want 3, 4, 1", current, duration, finished)
	}
	var itemType, title string
	var listening float64
	if err := h.db.QueryRow(`SELECT mediaItemType,mediaItemId,timeListening FROM playbackSessions WHERE id=?`, id).Scan(&itemType, &title, &listening); err != nil || itemType != "podcastEpisode" || title != "episode-two" || listening != 3 {
		t.Errorf("session row = %q %q %v err=%v, want podcastEpisode episode-two listened 3", itemType, title, listening, err)
	}
	var progressRows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mediaProgresses`).Scan(&progressRows); err != nil || progressRows != 1 {
		t.Errorf("progress rows = %d err=%v, want only the episode's", progressRows, err)
	}
}

// LibraryItemController.js:460-473: an unknown episode is 404.
func TestPlayUnknownEpisodeIsNotFound(t *testing.T) {
	h, user, itemID, mediaPath := fixture(t)
	makePodcast(t, h, itemID, mediaPath)
	if w := request(h, http.MethodPost, "/api/items/"+itemID+"/play/nope", user, `{"forceDirectPlay":true}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown episode = %d %s, want 404", w.Code, w.Body.String())
	}
	var sessions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions`).Scan(&sessions); err != nil || sessions != 0 || len(openIDs(t, h)) != 0 {
		t.Errorf("a refused play left %d session rows (err=%v) / open sessions %v", sessions, err, openIDs(t, h))
	}
}

// PlaybackSessionManager.syncLocalSession (:182-206): a new local session is
// built from the client JSON, so its display fields, cover, play method and
// start time are the client's; only missing metadata keys come from the item.
func TestLocalSessionKeepsClientSuppliedFields(t *testing.T) {
	h, user, _, _ := fixture(t)
	now := time.Now().UnixMilli()
	body := `{"id":"play_local_meta","libraryItemId":"item-one","currentTime":20,"timeListening":5,"startTime":2,"startedAt":` + strconv.FormatInt(now-1000, 10) + `,"updatedAt":` + strconv.FormatInt(now, 10) +
		`,"mediaPlayer":"offline","coverPath":"/c.jpg","displayTitle":"Mine","displayAuthor":"Me","playMethod":0,"mediaMetadata":{"title":"Client title"},` +
		`"deviceInfo":{"deviceId":"dev-local","manufacturer":"Google","model":"Pixel","sdkVersion":33,"clientName":"Abs Android","clientVersion":"0.9"}}`
	if w := request(h, http.MethodPost, "/api/session/local", user, body); w.Code != http.StatusOK {
		t.Fatalf("local = %d %s", w.Code, w.Body.String())
	}
	var title, author, player, cover, metadata string
	var method, start float64
	var deviceID string
	err := h.db.QueryRow(`SELECT displayTitle,displayAuthor,mediaPlayer,coverPath,playMethod,startTime,mediaMetadata,deviceId FROM playbackSessions`).Scan(&title, &author, &player, &cover, &method, &start, &metadata, &deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Mine" || author != "Me" || player != "offline" || cover != "/c.jpg" || method != 0 || start != 2 {
		t.Errorf("row = title %q author %q player %q cover %q playMethod %v startTime %v", title, author, player, cover, method, start)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["title"] != "Client title" || meta["authors"] == nil {
		t.Errorf("mediaMetadata = %s, want the client's title with the item's other keys", metadata)
	}
	var clientName, deviceName string
	if err := h.db.QueryRow(`SELECT clientName,deviceName FROM devices WHERE id=?`, deviceID).Scan(&clientName, &deviceName); err != nil || clientName != "Abs Android" || deviceName != "Google Pixel" {
		t.Errorf("device %q = %q %q err=%v", deviceID, clientName, deviceName, err)
	}
}

// syncLocalSessionsRequest (:103-118) answers every entry in order. Go keeps
// serving the rest when an entry is not an object, and timestamps sent as strings
// (startedAt, updatedAt) are accepted.
func TestLocalAllToleratesNonObjectEntriesAndStringTimestamps(t *testing.T) {
	h, user, _, _ := fixture(t)
	started := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	body := `{"sessions":[
		"junk",
		{"id":"play_local_str","libraryItemId":"item-one","currentTime":7,"timeListening":3.5,"startedAt":"` + strconv.FormatInt(started.UnixMilli(), 10) + `","updatedAt":"2026-01-05T10:00:00.000Z"},
		{"libraryItemId":"item-one","currentTime":1,"timeListening":1,"updatedAt":` + strconv.FormatInt(started.UnixMilli()+30000, 10) + `}
	]}`
	w := request(h, http.MethodPost, "/api/session/local-all", user, body)
	if w.Code != http.StatusOK {
		t.Fatalf("local-all = %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Results []struct {
			ID      *string `json:"id"`
			Success bool    `json:"success"`
			Error   string  `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	r := res.Results
	if len(r) != 3 {
		t.Fatalf("results = %s, want one per entry", w.Body.String())
	}
	if r[0].Success || r[0].Error != "Media item not found" {
		t.Errorf("non-object entry = %+v, want a Media item not found failure", r[0])
	}
	if !r[1].Success || r[1].ID == nil || strings.HasPrefix(*r[1].ID, "play_local_") {
		t.Errorf("string-typed entry = %+v, want success under a server id", r[1])
	}
	if !r[2].Success || r[2].ID == nil || *r[2].ID == "" {
		t.Errorf("entry without an id = %+v, want success under a new server id", r[2])
	}
	var current, listening float64
	var created string
	if err := h.db.QueryRow(`SELECT currentTime,timeListening,createdAt FROM playbackSessions WHERE id=?`, *r[1].ID).Scan(&current, &listening, &created); err != nil {
		t.Fatal(err)
	}
	if current != 7 || listening != 3.5 || !strings.HasPrefix(created, "2026-01-05 08:00:00") {
		t.Errorf("stored string entry: currentTime %v timeListening %v createdAt %q, want 7, 3.5, 2026-01-05 08:00:00", current, listening, created)
	}
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("session rows = %d err=%v, want 2 (junk stores nothing)", rows, err)
	}
}
