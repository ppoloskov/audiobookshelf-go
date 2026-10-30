package playback

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

func TestTranscodeAudioCodecMatchesPinnedStreamPolicy(t *testing.T) {
	tests := []struct {
		name  string
		files []audioFile
		want  string
	}{
		{name: "AAC in MP4 copies", files: []audioFile{{Codec: "aac", MimeType: "audio/mp4"}}, want: "copy"},
		{name: "FLAC forces AAC", files: []audioFile{{Codec: "flac", MimeType: "audio/flac"}}, want: "aac"},
		{name: "Opus forces AAC", files: []audioFile{{Codec: "opus", MimeType: "audio/ogg"}}, want: "aac"},
		{name: "AC3 forces AAC", files: []audioFile{{Codec: "ac3", MimeType: "audio/ac3"}}, want: "aac"},
		{name: "later track does not override first track policy", files: []audioFile{{Codec: "aac", MimeType: "audio/mp4"}, {Codec: "flac", MimeType: "audio/flac"}}, want: "copy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := transcodeAudioCodec(tt.files); got != tt.want {
				t.Fatalf("transcodeAudioCodec()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestDirectPlaybackRangeOwnershipAndProgressPersistence(t *testing.T) {
	h, user, itemID, mediaPath := fixture(t)
	type emitted struct{ userID, event string }
	var emissions []emitted
	h.emit = func(userID, event string, _ any) { emissions = append(emissions, emitted{userID, event}) }
	body := `{"mediaPlayer":"test","forceDirectPlay":true,"deviceInfo":{"deviceId":"device-one","clientName":"test"}}`
	start := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, body)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	var sessionJSON map[string]any
	if err := json.Unmarshal(start.Body.Bytes(), &sessionJSON); err != nil {
		t.Fatal(err)
	}
	sid, _ := sessionJSON["id"].(string)
	if sid == "" || sessionJSON["playMethod"] != float64(0) {
		t.Fatalf("unexpected session payload: %v", sessionJSON)
	}
	if len(emissions) == 0 || emissions[0] != (emitted{"@admins", "user_stream_update"}) {
		t.Fatalf("stream update audience mismatch: %+v", emissions)
	}

	stream := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/public/session/"+sid+"/track/1", nil)
	r.Header.Set("Range", "bytes=100-199")
	if !h.Handle(stream, r, store.User{}) {
		t.Fatal("public track route not claimed")
	}
	if stream.Code != http.StatusPartialContent {
		t.Fatalf("range status %d, want 206", stream.Code)
	}
	if got := stream.Header().Get("Content-Range"); !strings.HasPrefix(got, "bytes 100-199/") {
		t.Fatalf("unexpected content-range %q", got)
	}
	if len(stream.Body.Bytes()) != 100 {
		t.Fatalf("range body has %d bytes", stream.Body.Len())
	}
	if st, err := os.Stat(mediaPath); err != nil || st.Size() <= 200 {
		t.Fatalf("fixture media not available: %v", err)
	}

	other := store.User{ID: "user-2", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	denied := request(h, http.MethodGet, "/api/session/"+sid, other, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("foreign session access status %d, want 403", denied.Code)
	}

	sync := request(h, http.MethodPost, "/api/session/"+sid+"/sync", user, `{"currentTime":50,"timeListened":5,"duration":100}`)
	if sync.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", sync.Code, sync.Body.String())
	}
	if got := emissions[len(emissions)-1]; got != (emitted{user.ID, "user_item_progress_updated"}) {
		t.Fatalf("progress event audience mismatch: %+v", got)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions WHERE id=? AND timeListening=5`, sid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("playback session not persisted: count=%d err=%v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mediaProgresses WHERE userId=? AND mediaItemId='book-one' AND currentTime=50`, user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("progress not persisted: count=%d err=%v", count, err)
	}

	close := request(h, http.MethodPost, "/api/session/"+sid+"/close", user, `{}`)
	if close.Code != http.StatusOK {
		t.Fatalf("close: %d %s", close.Code, close.Body.String())
	}
	if got := emissions[len(emissions)-1]; got != (emitted{user.ID, "user_session_closed"}) {
		t.Fatalf("close event audience mismatch: %+v", got)
	}
	missing := httptest.NewRecorder()
	if !h.Handle(missing, httptest.NewRequest(http.MethodGet, "/public/session/"+sid+"/track/1", nil), store.User{}) {
		t.Fatal("public track route not claimed")
	}
	if missing.Code != http.StatusNotFound {
		t.Fatalf("closed session stream status %d, want 404", missing.Code)
	}
}

func TestPlaybackSessionDeviceAndEmptyMediaArraysMatchOldJSON(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	_, err := h.db.Exec(`UPDATE books SET narrators='null',genres='null',tags='null' WHERE id='book-one'`)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"forceDirectPlay":true,"mediaPlayer":"Parity Fixture Client","deviceInfo":{"deviceId":"device-parity","clientName":"Parity Fixture Client","clientVersion":"1.2.3","deviceName":"Fixture Device"}}`
	w := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, body)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// PlaybackSession.js:86-104 uses Device.getOldDevice(); Device.js:149-173
	// fills DeviceInfo; DeviceInfo.js:55-77 toJSON deletes null fields and
	// setData(:84-115) never copies client deviceName — it only synthesizes it
	// from manufacturer/model/UA. The fixture body sends deviceName only, so
	// all four client-supplied-only keys stay absent from the response.
	device := got["deviceInfo"].(map[string]any)
	for _, key := range []string{"deviceName", "manufacturer", "model", "sdkVersion"} {
		if _, ok := device[key]; ok {
			t.Errorf("deviceInfo unexpectedly includes %q (upstream deletes null fields): %#v", key, device)
		}
	}
	metadata := got["mediaMetadata"].(map[string]any)
	for _, key := range []string{"genres", "narrators"} {
		value, ok := metadata[key].([]any)
		if !ok || len(value) != 0 {
			t.Errorf("mediaMetadata.%s=%#v, want []", key, metadata[key])
		}
	}
	item := got["libraryItem"].(map[string]any)
	media := item["media"].(map[string]any)
	for path, value := range map[string]any{"media.tags": media["tags"], "media.metadata.genres": media["metadata"].(map[string]any)["genres"], "media.metadata.narrators": media["metadata"].(map[string]any)["narrators"]} {
		array, ok := value.([]any)
		if !ok || len(array) != 0 {
			t.Errorf("%s=%#v, want []", path, value)
		}
	}
}

func TestTranscodedSessionServesHLSWithoutBearerAndRejectsBadFiles(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	start := request(h, http.MethodPost, "/api/items/"+itemID+"/play", user, `{"forceTranscode":true,"mediaPlayer":"test"}`)
	if start.Code != http.StatusOK {
		t.Fatalf("start transcode: %d %s", start.Code, start.Body.String())
	}
	var s map[string]any
	_ = json.Unmarshal(start.Body.Bytes(), &s)
	sid, _ := s["id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(h.cfg.MetadataPath, "streams", sid, "output-0.ts")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	playlist := httptest.NewRecorder()
	if !h.Handle(playlist, httptest.NewRequest(http.MethodGet, "/hls/"+sid+"/output.m3u8", nil), store.User{}) {
		t.Fatal("hls route not claimed")
	}
	if playlist.Code != http.StatusOK || !strings.Contains(playlist.Body.String(), "#EXTM3U") {
		t.Fatalf("playlist response %d: %s", playlist.Code, playlist.Body.String())
	}
	segment := httptest.NewRecorder()
	if !h.Handle(segment, httptest.NewRequest(http.MethodGet, "/hls/"+sid+"/output-0.ts", nil), store.User{}) {
		t.Fatal("segment route not claimed")
	}
	if segment.Code != http.StatusOK || segment.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatalf("segment response %d content-type %q", segment.Code, segment.Header().Get("Content-Type"))
	}
	bad := httptest.NewRecorder()
	if !h.Handle(bad, httptest.NewRequest(http.MethodGet, "/hls/"+sid+"/secret.json", nil), store.User{}) {
		t.Fatal("bad hls route not claimed")
	}
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid extension status %d, want 400", bad.Code)
	}
	closed := request(h, http.MethodPost, "/api/session/"+sid+"/close", user, `{}`)
	if closed.Code != http.StatusOK {
		t.Fatalf("close transcoded: %d", closed.Code)
	}
}

func TestPodcastEpisodeTrackFallbackAndFileRoute(t *testing.T) {
	h, user, itemID, mediaPath := fixture(t)
	if _, err := h.db.Exec(`UPDATE libraryItems SET mediaType='podcast',mediaId='podcast-one' WHERE id=?`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO podcasts (id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,createdAt,updatedAt) VALUES ('podcast-one','Pod','Host',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,0,NULL,NULL,0,3,NULL,'[]','[]',?,?)`, time.Now().Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	af, _ := json.Marshal(map[string]any{"index": 1, "ino": "ep-file", "metadata": map[string]any{"filename": "episode.wav", "ext": ".wav", "path": mediaPath, "size": 1000}, "duration": 3, "exclude": false})
	if _, err := h.db.Exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,createdAt,updatedAt) VALUES ('episode-one','podcast-one','Episode',?,'[]',?,?)`, string(af), time.Now().Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	started := request(h, http.MethodPost, "/api/items/"+itemID+"/play/episode-one", user, `{"forceDirectPlay":true,"supportedMimeTypes":["audio/mpeg"]}`)
	if started.Code != http.StatusOK {
		t.Fatalf("episode play: %d %s", started.Code, started.Body.String())
	}
	var s map[string]any
	_ = json.Unmarshal(started.Body.Bytes(), &s)
	sid, _ := s["id"].(string)
	track := httptest.NewRecorder()
	if !h.Handle(track, httptest.NewRequest(http.MethodGet, "/public/session/"+sid+"/track/0", nil), store.User{}) {
		t.Fatal("podcast track route not claimed")
	}
	if track.Code != http.StatusOK {
		t.Fatalf("episode index 0 compatibility status %d", track.Code)
	}
	apiFile := request(h, http.MethodGet, "/api/items/"+itemID+"/file/ep-file", user, "")
	if apiFile.Code != http.StatusOK {
		t.Fatalf("episode file endpoint: %d", apiFile.Code)
	}
}

func TestOfflineLocalSyncAndAdminHistoryRoutes(t *testing.T) {
	h, user, itemID, _ := fixture(t)
	updated := time.Now().UnixMilli()
	body := `{"id":"play_local_abc","libraryItemId":"` + itemID + `","currentTime":20,"timeListening":5,"duration":100,"startedAt":` + strconv.FormatInt(updated-60000, 10) + `,"updatedAt":` + strconv.FormatInt(updated, 10) + `,"mediaPlayer":"offline"}`
	local := request(h, http.MethodPost, "/api/session/local", user, body)
	if local.Code != http.StatusOK {
		t.Fatalf("local sync status %d: %s", local.Code, local.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions WHERE timeListening=5 AND mediaItemId='book-one'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("local session not persisted: %d %v", count, err)
	}
	admin := store.User{ID: "admin-one", Username: "admin", Type: "admin", IsActive: true, Permissions: json.RawMessage(`{"delete":true,"accessAllLibraries":true,"accessAllTags":true}`)}
	denied := request(h, http.MethodGet, "/api/sessions", user, "")
	if denied.Code != http.StatusNotFound {
		t.Fatalf("non-admin session list=%d want 404", denied.Code)
	}
	list := request(h, http.MethodGet, "/api/sessions?sort=createdAt&desc=1&itemsPerPage=10&page=0", admin, "")
	if list.Code != http.StatusOK {
		t.Fatalf("admin session list %d: %s", list.Code, list.Body.String())
	}
	var payload struct {
		Total    int              `json:"total"`
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Sessions) != 1 || payload.Sessions[0]["episodeId"] != nil {
		t.Fatalf("unexpected history response: %+v", payload)
	}
	deleteBody := `{"sessions":["` + payload.Sessions[0]["id"].(string) + `"]}`
	del := request(h, http.MethodPost, "/api/sessions/batch/delete", admin, deleteBody)
	if del.Code != http.StatusOK {
		t.Fatalf("batch delete: %d %s", del.Code, del.Body.String())
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("history row survived delete: %d %v", count, err)
	}
}

func request(h *Handler, method, path string, user store.User, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	h.Handle(w, r, user)
	return w
}

func fixture(t *testing.T) (*Handler, store.User, string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		`CREATE TABLE libraryItems (id TEXT PRIMARY KEY,libraryId TEXT,mediaId TEXT,mediaType TEXT,title TEXT,authorNamesFirstLast TEXT,path TEXT,relPath TEXT,ino TEXT,isFile INTEGER,isMissing INTEGER,isInvalid INTEGER,mtime TEXT,ctime TEXT,birthtime TEXT,lastScan TEXT,lastScanVersion TEXT,libraryFiles TEXT,extraData TEXT,size INTEGER,createdAt TEXT,updatedAt TEXT,libraryFolderId TEXT)`,
		`CREATE TABLE books (id TEXT PRIMARY KEY,title TEXT,subtitle TEXT,publishedYear TEXT,publishedDate TEXT,publisher TEXT,description TEXT,isbn TEXT,asin TEXT,language TEXT,explicit INTEGER,abridged INTEGER,narrators TEXT,coverPath TEXT,duration REAL,audioFiles TEXT,chapters TEXT,tags TEXT,genres TEXT)`,
		`CREATE TABLE authors (id TEXT,name TEXT)`, `CREATE TABLE bookAuthors (bookId TEXT,authorId TEXT,createdAt TEXT)`, `CREATE TABLE series (id TEXT,name TEXT)`, `CREATE TABLE bookSeries (bookId TEXT,seriesId TEXT,sequence TEXT,createdAt TEXT)`,
		`CREATE TABLE podcasts (id TEXT,title TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit INTEGER,autoDownloadEpisodes INTEGER,autoDownloadSchedule TEXT,lastEpisodeCheck TEXT,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags TEXT,genres TEXT,createdAt TEXT,updatedAt TEXT)`,
		`CREATE TABLE podcastEpisodes (id TEXT,podcastId TEXT,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,pubDate TEXT,audioFile TEXT,chapters TEXT,extraData TEXT,publishedAt TEXT,createdAt TEXT,updatedAt TEXT)`,
		`CREATE TABLE libraries (id TEXT PRIMARY KEY,settings TEXT)`,
		`CREATE TABLE devices (id TEXT PRIMARY KEY,deviceId TEXT,clientName TEXT,clientVersion TEXT,ipAddress TEXT,deviceName TEXT,deviceVersion TEXT,extraData TEXT,userId TEXT,createdAt TEXT,updatedAt TEXT)`,
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,duration REAL,currentTime REAL,isFinished INTEGER,hideFromContinueListening INTEGER,finishedAt TEXT,extraData TEXT,podcastId TEXT,userId TEXT,createdAt TEXT,updatedAt TEXT)`,
		`CREATE TABLE playbackSessions (id TEXT PRIMARY KEY,mediaItemId TEXT,mediaItemType TEXT,displayTitle TEXT,displayAuthor TEXT,duration REAL,playMethod INTEGER,mediaPlayer TEXT,startTime REAL,currentTime REAL,serverVersion TEXT,coverPath TEXT,timeListening INTEGER,mediaMetadata TEXT,date TEXT,dayOfWeek TEXT,extraData TEXT,userId TEXT,deviceId TEXT,libraryId TEXT,createdAt TEXT,updatedAt TEXT)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "test.wav")
	writeWAV(t, path)
	raw, _ := json.Marshal([]map[string]any{{"index": 1, "ino": "audio-one", "metadata": map[string]any{"filename": "test.wav", "ext": ".wav", "path": path, "size": 441044}, "duration": 3.0, "bitRate": 705600, "channels": 1, "codec": "pcm_s16le", "format": "wav", "exclude": false}})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO libraryItems VALUES ('item-one','library-one','book-one','book','Book','Author',?,'test.wav','item-ino',0,0,0,?,?,?,NULL,NULL,'[]','{}',441044,?,?,'folder-one')`, path, path, now, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO books VALUES ('book-one','Book',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,0,'[]',NULL,3,?,'[]','[]','[]')`, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO libraries VALUES ('library-one','{"markAsFinishedTimeRemaining":10}')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO authors VALUES ('author-one','Author')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO bookAuthors VALUES ('book-one','author-one',?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{MetadataPath: t.TempDir()}
	h := New(db, cfg, nil)
	user := store.User{ID: "user-one", Username: "tester", Type: "user", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	return h, user, "item-one", path
}

func writeWAV(t *testing.T, path string) {
	t.Helper()
	const sampleRate = 8000
	const seconds = 3
	samples := sampleRate * seconds
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(8000)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
