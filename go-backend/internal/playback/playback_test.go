package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/media"
	"audiobookshelf-compat/internal/settings"
)

// Fixture ids (the upstream-created install of internal/contracttest).
const (
	rootID     = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listenerID = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	finisherID = "7c538a8f-9711-47bd-89e4-8d0139473dcf"
	novelItem  = "91bb63f7-97c9-4230-8fd0-1dfd994977ea" // 45 s, one .m4b
	novelBook  = "85ab0f32-b17f-4c9a-883e-a61e260f1e46"
	sagaItem   = "b3c61dfc-dd4b-47ee-8998-0340c1228f52" // 60.186123 s, one .mp3, listener progress at 27.5
	sagaBook   = "5e33de11-3e94-4c51-87d8-d06d2928ac38"
	podcast    = "eeb6224f-1316-40c8-b8bf-00804cc90436"
	episode1   = "c2b5b947-590f-4fe7-a9cd-ef18855a1ddb"
	episode2   = "68794f9c-22d7-4591-baa0-8e0efd559a7e"
)

type emitted struct{ Audience, Event string }

type env struct {
	*Handler
	d       *db.DB
	streams string
	media   string
	mu      sync.Mutex
	sent    []emitted
	payload map[string]any // the last payload per event name
}

func (e *env) record(audience, event string, payload any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, emitted{audience, event})
	if e.payload == nil {
		e.payload = map[string]any{}
	}
	e.payload[event] = payload
}

func (e *env) events(name string) (n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.sent {
		if s.Event == name {
			n++
		}
	}
	return n
}

func (e *env) lastEvent() emitted {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sent[len(e.sent)-1]
}

// newEnv is a Handler over a copy of the fixture whose audio files are one real
// file, with a fake ffmpeg that only sleeps.
func newEnv(t *testing.T) *env {
	t.Helper()
	d := dbtest.Fixture(t)
	media := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(media, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", ffmpeg)
	ctx := context.Background()
	var books []db.Book
	if err := d.Select(ctx, &books, `SELECT * FROM books`); err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		for i := range b.AudioFiles.V {
			b.AudioFiles.V[i].Metadata.Path = media
		}
		if err := d.Update(ctx, "books", &b, "audioFiles"); err != nil {
			t.Fatal(err)
		}
	}
	var episodes []db.PodcastEpisode
	if err := d.Select(ctx, &episodes, `SELECT * FROM podcastEpisodes`); err != nil {
		t.Fatal(err)
	}
	for _, e := range episodes {
		e.AudioFile.V.Metadata.Path = media
		if err := d.Update(ctx, "podcastEpisodes", &e, "audioFile"); err != nil {
			t.Fatal(err)
		}
	}
	store, err := settings.NewStore(ctx, d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, media: media}
	cfg := config.Config{MetadataPath: t.TempDir()}
	e.streams = cfg.Dirs().Streams
	e.Handler = New(d, store, cfg, e.record)
	t.Cleanup(e.Close)
	return e
}

func (e *env) serve(t *testing.T, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.Background()
	if userID != "" {
		var row db.User
		if err := e.d.Get(ctx, &row, `SELECT * FROM users WHERE id = ?`, userID); err != nil {
			t.Fatal(err)
		}
		ctx = auth.WithUser(ctx, auth.NewPrincipal(row))
	}
	mux := http.NewServeMux()
	e.Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
	return w
}

func (e *env) expect(t *testing.T, want int, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := e.serve(t, userID, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body.String(), want)
	}
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return out
}

// startDirect starts a direct-play session for the listener and returns its id.
func (e *env) startDirect(t *testing.T, item string) string {
	t.Helper()
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+item+"/play", `{"forceDirectPlay":true,"mediaPlayer":"test"}`)
	id, _ := decode(t, w)["id"].(string)
	if id == "" {
		t.Fatalf("no session id: %s", w.Body.String())
	}
	return id
}

func (e *env) openIDs(t *testing.T) []string {
	t.Helper()
	var body struct {
		Sessions []struct {
			ID   string `json:"id"`
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(e.expect(t, 200, rootID, "GET", "/api/sessions/open", "").Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range body.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

// row reads one value of the single-row query.
func (e *env) row(t *testing.T, dest any, query string, args ...any) {
	t.Helper()
	if err := e.d.Get(context.Background(), dest, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (e *env) count(t *testing.T, query string, args ...any) (n int) {
	t.Helper()
	e.row(t, &n, query, args...)
	return n
}

func TestTranscodeAudioCodec(t *testing.T) {
	file := func(codec, mime string) db.AudioFile { return db.AudioFile{Codec: &codec, MimeType: mime} }
	for name, tc := range map[string]struct {
		files []db.AudioFile
		want  string
	}{
		"aac in mp4 copies":   {[]db.AudioFile{file("aac", "audio/mp4")}, "copy"},
		"flac":                {[]db.AudioFile{file("flac", "audio/flac")}, "aac"},
		"opus":                {[]db.AudioFile{file("opus", "audio/ogg")}, "aac"},
		"ac3":                 {[]db.AudioFile{file("ac3", "audio/ac3")}, "aac"},
		"first track decides": {[]db.AudioFile{file("aac", "audio/mp4"), file("flac", "audio/flac")}, "copy"},
		"no files":            {nil, "copy"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := transcodeAudioCodec(tc.files); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDirectPlayRangeOwnershipAndProgress(t *testing.T) {
	e := newEnv(t)
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+sagaItem+"/play", `{"mediaPlayer":"test","forceDirectPlay":true,"deviceInfo":{"deviceId":"device-one","clientName":"test"}}`)
	session := decode(t, w)
	id, _ := session["id"].(string)
	if session["playMethod"] != float64(0) || session["startTime"] != 27.5 || session["bookId"] != sagaBook || session["episodeId"] != nil || session["libraryItem"] == nil {
		t.Fatalf("session = %v, want direct play resuming at 27.5 of the book", session)
	}
	if got := e.lastEvent(); got != (emitted{"@admins", "user_stream_update"}) {
		t.Fatalf("event = %+v", got)
	}
	var announced struct {
		Session map[string]any `json:"session"`
	}
	raw, _ := json.Marshal(e.payload["user_stream_update"])
	_ = json.Unmarshal(raw, &announced)
	if item, present := announced.Session["libraryItem"]; !present || item != nil {
		t.Fatalf("the admin event carries libraryItem %v (present %v), want an explicit null", item, present)
	}

	r := httptest.NewRequest("GET", "/public/session/"+id+"/track/1", nil)
	r.Header.Set("Range", "bytes=100-199")
	ranged := httptest.NewRecorder()
	mux := http.NewServeMux()
	e.Routes(mux)
	mux.ServeHTTP(ranged, r)
	if ranged.Code != http.StatusPartialContent || ranged.Body.Len() != 100 || !strings.HasPrefix(ranged.Header().Get("Content-Range"), "bytes 100-199/") {
		t.Fatalf("range = %d %q len %d", ranged.Code, ranged.Header().Get("Content-Range"), ranged.Body.Len())
	}

	e.expect(t, 403, finisherID, "GET", "/api/session/"+id, "")
	e.expect(t, 200, rootID, "GET", "/api/session/"+id, "")
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":50,"timeListened":5,"duration":100}`)
	if got := e.lastEvent(); got != (emitted{listenerID, "user_item_progress_updated"}) {
		t.Fatalf("event = %+v", got)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE id = ? AND timeListening = 5`, id); n != 1 {
		t.Fatalf("session not stored: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM mediaProgresses WHERE userId = ? AND mediaItemId = ? AND currentTime = 50 AND duration = 100`, listenerID, sagaBook); n != 1 {
		t.Fatalf("progress not stored: %d", n)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", `{}`)
	if got := e.lastEvent(); got != (emitted{listenerID, "user_session_closed"}) {
		t.Fatalf("event = %+v", got)
	}
	e.expect(t, 404, "", "GET", "/public/session/"+id+"/track/1", "")
}

func TestPlayRefusals(t *testing.T) {
	e := newEnv(t)
	body := `{"forceDirectPlay":true}`
	e.expect(t, 404, listenerID, "POST", "/api/items/nope/play", body)
	e.expect(t, 404, listenerID, "POST", "/api/items/bf4f5bac-080b-465e-adf0-1a3ea5018ac6/play", body) // no audio files
	e.expect(t, 404, listenerID, "POST", "/api/items/"+podcast+"/play", body)                          // podcast without an episode
	e.expect(t, 404, listenerID, "POST", "/api/items/"+podcast+"/play/nope", body)
	e.expect(t, 404, listenerID, "POST", "/api/items/"+sagaItem+"/play/"+episode1, body)
	e.expect(t, 400, listenerID, "POST", "/api/items/"+sagaItem+"/play", `{"forceDirectPlay":"yes"}`)
	if _, err := e.d.Exec(context.Background(), `UPDATE users SET permissions = json_set(permissions, '$.accessAllLibraries', json('false')) WHERE id = ?`, finisherID); err != nil {
		t.Fatal(err)
	}
	e.expect(t, 403, finisherID, "POST", "/api/items/"+sagaItem+"/play", body)
	if got := e.openIDs(t); len(got) != 0 {
		t.Fatalf("a refused play left open sessions %v", got)
	}
}

func TestSessionOpenSyncCloseLifecycle(t *testing.T) {
	e := newEnv(t)
	id := e.startDirect(t, novelItem)
	if got := e.openIDs(t); len(got) != 1 || got[0] != id {
		t.Fatalf("open = %v", got)
	}
	e.expect(t, 403, finisherID, "POST", "/api/session/"+id+"/sync", `{"currentTime":1,"timeListened":1}`)
	e.expect(t, 404, listenerID, "GET", "/api/sessions/open", "")
	for _, body := range []string{`{"currentTime":-1,"timeListened":1}`, `{"currentTime":1}`, `{"timeListened":1}`, `{"currentTime":1,"timeListened":-2}`} {
		e.expect(t, 400, listenerID, "POST", "/api/session/"+id+"/sync", body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE id = ?`, id); n != 0 {
		t.Fatal("a rejected sync stored the session")
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":1.5,"timeListened":2,"duration":45}`)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":2,"timeListened":3,"duration":45}`)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", `{"currentTime":2.5,"timeListened":1}`)
	var stored struct {
		TimeListening float64 `db:"timeListening"`
		CurrentTime   float64 `db:"currentTime"`
	}
	e.row(t, &stored, `SELECT timeListening, currentTime FROM playbackSessions WHERE id = ?`, id)
	if stored.TimeListening != 6 || stored.CurrentTime != 2.5 {
		t.Fatalf("stored %+v, want 6 and 2.5", stored)
	}
	if got := e.openIDs(t); len(got) != 0 {
		t.Fatalf("open after close = %v", got)
	}
	for _, step := range [][2]string{{"GET", "/api/session/" + id}, {"POST", "/api/session/" + id + "/sync"}, {"POST", "/api/session/" + id + "/close"}} {
		e.expect(t, 404, listenerID, step[0], step[1], `{"currentTime":1,"timeListened":1}`)
	}
	if n := e.events("user_session_closed"); n != 1 {
		t.Fatalf("user_session_closed sent %d times", n)
	}
}

func TestCloseWithoutSyncDataSavesOnlyListenedSessions(t *testing.T) {
	e := newEnv(t)
	silent := e.startDirect(t, novelItem)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+silent+"/close", ``)
	listened := e.startDirect(t, novelItem)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+listened+"/sync", `{"currentTime":1,"timeListened":2}`)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+listened+"/close", ``)
	if e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE id = ?`, silent) != 0 || e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE id = ? AND timeListening = 2`, listened) != 1 {
		t.Fatal("only the listened session should be stored")
	}
}

func TestHandlerCloseForgetsOpenSessions(t *testing.T) {
	e := newEnv(t)
	a, b := e.startDirect(t, novelItem), e.startDirect(t, sagaItem)
	e.Close()
	if got := e.openIDs(t); len(got) != 0 {
		t.Fatalf("open after Close = %v", got)
	}
	for _, id := range []string{a, b} {
		e.expect(t, 404, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":1,"timeListened":1}`)
	}
}

func TestOpenSessionsAndSessionOf(t *testing.T) {
	e := newEnv(t)
	id := e.startDirect(t, novelItem)
	listing, ok := e.OpenSessions().([]Session)
	if !ok || len(listing) != 1 || listing[0].ID != id || listing[0].User == nil || listing[0].User.Username != "trial-listener" || listing[0].LibraryItem != nil {
		t.Fatalf("OpenSessions = %+v", e.OpenSessions())
	}
	if e.SessionOf(listenerID) == nil || e.SessionOf(rootID) != nil {
		t.Fatalf("SessionOf = %v / %v, want the listener's session and an untyped nil", e.SessionOf(listenerID), e.SessionOf(rootID))
	}
}

func TestConcurrentSyncsAreSafe(t *testing.T) {
	e := newEnv(t)
	id := e.startDirect(t, novelItem)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.serve(t, listenerID, "POST", "/api/session/"+id+"/sync", fmt.Sprintf(`{"currentTime":%d,"timeListened":1}`, i+1))
			e.serve(t, rootID, "GET", "/api/sessions/open", "")
		}()
	}
	wg.Wait()
	s, _ := e.get(id)
	if s.TimeListening != 8 {
		t.Fatalf("timeListening = %v, want the 8 syncs added", s.TimeListening)
	}
}

func TestEpisodeSessionWritesEpisodeProgress(t *testing.T) {
	e := newEnv(t)
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+podcast+"/play/"+episode2, `{"forceDirectPlay":true}`)
	id := decode(t, w)["id"].(string)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":2,"timeListened":2,"duration":4}`)
	var progress struct {
		Current  float64 `db:"currentTime"`
		Duration float64 `db:"duration"`
		Finished bool    `db:"isFinished"`
	}
	query := `SELECT currentTime, duration, isFinished FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?`
	e.row(t, &progress, query, listenerID, episode2)                           // the fixture's finished row, restarted by the sync
	if progress.Current != 2 || progress.Duration != 4 || !progress.Finished { // 2 s left, under the library's 10 s
		t.Fatalf("after sync %+v", progress)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", `{"currentTime":17,"timeListened":1}`)
	e.row(t, &progress, query, listenerID, episode2)
	if progress.Current != 17 || progress.Duration != 18.076735 || !progress.Finished { // the close has no duration: the session's 18.07 s applies
		t.Fatalf("after close %+v", progress)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE id = ? AND mediaItemType = 'podcastEpisode' AND mediaItemId = ? AND timeListening = 3`, id, episode2); n != 1 {
		t.Fatal("episode session row missing")
	}
}

func TestPodcastEpisodeTrackZeroFallback(t *testing.T) {
	e := newEnv(t)
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+podcast+"/play/"+episode1, `{"forceDirectPlay":true}`)
	id := decode(t, w)["id"].(string)
	e.expect(t, 200, "", "GET", "/public/session/"+id+"/track/0", "")
	e.expect(t, 404, "", "GET", "/public/session/"+id+"/track/2", "")
	e.expect(t, 400, "", "GET", "/public/session/"+id+"/track/x", "")
	e.expect(t, 405, "", "POST", "/public/session/"+id+"/track/1", "")
}

func TestDeviceRowIsWrittenFromClientDeviceInfo(t *testing.T) {
	e := newEnv(t)
	play := func(info string) {
		t.Helper()
		e.expect(t, 200, listenerID, "POST", "/api/items/"+novelItem+"/play", `{"forceDirectPlay":true,"deviceInfo":`+info+`}`)
	}
	type device struct {
		ClientName    string  `db:"clientName"`
		ClientVersion string  `db:"clientVersion"`
		DeviceName    *string `db:"deviceName"`
		DeviceVersion *string `db:"deviceVersion"`
		ExtraData     string  `db:"extraData"`
	}
	read := func(deviceID string) (d device) {
		t.Helper()
		e.row(t, &d, `SELECT clientName, clientVersion, deviceName, deviceVersion, extraData FROM devices WHERE deviceId = ?`, deviceID)
		return d
	}
	play(`{"deviceId":"dev-1","manufacturer":"Google","model":"Pixel 8","sdkVersion":34,"clientName":"Abs Android","clientVersion":"0.9.79","bogus":"x"}`)
	d := read("dev-1")
	if d.ClientName != "Abs Android" || d.ClientVersion != "0.9.79" || db.Val(d.DeviceName) != "Google Pixel 8" || db.Val(d.DeviceVersion) != "34" || d.ExtraData != `{"manufacturer":"Google","model":"Pixel 8"}` {
		t.Errorf("android device = %+v", d)
	}
	play(`{"deviceId":"dev-2","clientName":"Abs iOS","model":"iPhone"}`)
	d = read("dev-2")
	if d.ClientVersion != "2.37.2" || db.Val(d.DeviceName) != "Unknown iPhone" || d.DeviceVersion != nil {
		t.Errorf("ios device = %+v", d)
	}
	play(`{"deviceId":"dev-3"}`)
	if d = read("dev-3"); d.ExtraData != `{}` || d.ClientName != "Unknown" {
		t.Errorf("empty device = %+v", d)
	}
	before := e.count(t, `SELECT COUNT(*) FROM devices`)
	play(`{"deviceId":"dev-1","clientName":"Abs Android","sdkVersion":34}`)
	play(`{"clientName":"Abs Web"}`) // no deviceId: a new row known by its own id
	if after := e.count(t, `SELECT COUNT(*) FROM devices`); after != before+1 {
		t.Errorf("device rows %d -> %d, want one new row", before, after)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM devices WHERE clientName = 'Abs Web' AND id = deviceId`); n != 1 {
		t.Errorf("a client without deviceId should be known by its row id")
	}
}

func TestPersistedMetadataIsTheLegacySubset(t *testing.T) {
	e := newEnv(t)
	id := e.startDirect(t, sagaItem)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":1,"timeListened":1}`)
	var text string
	e.row(t, &text, `SELECT mediaMetadata FROM playbackSessions WHERE id = ?`, id)
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"titleIgnorePrefix", "authorName", "authorNameLF", "narratorName", "seriesName", "descriptionPlain"} {
		if _, found := got[key]; found {
			t.Errorf("persisted metadata has expanded key %q", key)
		}
	}
	for _, key := range []string{"title", "subtitle", "authors", "narrators", "series", "genres", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language", "explicit", "abridged"} {
		if _, found := got[key]; !found {
			t.Errorf("persisted metadata lacks %q", key)
		}
	}
}

func TestLocalSyncStoresOnceAndRespectsNewerProgress(t *testing.T) {
	e := newEnv(t)
	local := func(current, listened float64, updated int64, extra string) string {
		return fmt.Sprintf(`{"id":"play_local_a","libraryItemId":%q,"currentTime":%v,"timeListening":%v,"startedAt":%d,"updatedAt":%d,"mediaPlayer":"offline"%s}`, sagaItem, current, listened, updated-60000, updated, extra)
	}
	now := time.Now().UnixMilli() + 3600000 // newer than the fixture's progress
	e.expect(t, 200, listenerID, "POST", "/api/session/local", local(20, 5, now, `,"date":"2026-01-05","dayOfWeek":"Monday","displayTitle":"Mine","mediaMetadata":{"title":"Client title"}`))
	var s struct {
		Date, Title, Metadata string
	}
	e.row(t, &s.Date, `SELECT date FROM playbackSessions WHERE displayTitle = 'Mine'`)
	if s.Date != "2026-01-05" {
		t.Fatalf("a new local session keeps the client's date, got %q", s.Date)
	}
	e.row(t, &s.Metadata, `SELECT mediaMetadata FROM playbackSessions WHERE displayTitle = 'Mine'`)
	if !strings.Contains(s.Metadata, `"title":"Client title"`) || !strings.Contains(s.Metadata, `"authors"`) {
		t.Fatalf("metadata = %s, want the client's title over the item's other keys", s.Metadata)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/local", local(30, 9, now+10000, `,"displayTitle":"Replaced"`))
	var id string
	e.row(t, &id, `SELECT id FROM playbackSessions WHERE displayTitle = 'Mine'`)
	if id == "play_local_a" || e.count(t, `SELECT COUNT(*) FROM playbackSessions WHERE timeListening = 9 AND currentTime = 30 AND displayTitle = 'Mine'`) != 1 {
		t.Fatalf("a repeat should update only the progress fields of the row stored under a server id (%q)", id)
	}
	e.row(t, &s.Date, `SELECT date FROM playbackSessions WHERE id = ?`, id)
	if s.Date != time.UnixMilli(now+10000).Format("2006-01-02") {
		t.Fatalf("an update recomputes the day from updatedAt, got %q", s.Date)
	}
	var position float64
	progress := `SELECT currentTime FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?`
	e.row(t, &position, progress, listenerID, sagaBook)
	if position != 30 {
		t.Fatalf("progress = %v, want 30", position)
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/local", local(5, 1, 1000, ``)) // older than the stored progress
	e.row(t, &position, progress, listenerID, sagaBook)
	if position != 30 {
		t.Fatalf("a stale upload moved the progress to %v", position)
	}
	if w := e.expect(t, 500, listenerID, "POST", "/api/session/local", `{"id":"x","libraryItemId":"nope"}`); !strings.Contains(w.Body.String(), "Media item not found") {
		t.Fatalf("body %q", w.Body.String())
	}
	e.expect(t, 400, listenerID, "POST", "/api/session/local", `[`)
}

func TestLocalAllReportsEachSessionAndAcceptsStringTimestamps(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UnixMilli() + 3600000
	body := fmt.Sprintf(`{"sessions":[
		"junk",
		{"id":"play_local_one","libraryItemId":%[1]q,"currentTime":7,"timeListening":3,"startedAt":"%[2]d","updatedAt":"%[3]d"},
		{"id":"fixed","libraryItemId":"nope"},
		{"id":"play_local_one","libraryItemId":%[1]q,"currentTime":8,"timeListening":4,"updatedAt":%[4]d},
		{"id":"play_local_old","libraryItemId":%[1]q,"currentTime":1,"timeListening":1,"updatedAt":1000}
	]}`, sagaItem, now-1000, now, now+60000)
	var res struct {
		Results []struct {
			ID             *string `json:"id"`
			Success        bool    `json:"success"`
			ProgressSynced *bool   `json:"progressSynced"`
			Error          string  `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(e.expect(t, 200, listenerID, "POST", "/api/session/local-all", body).Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	r := res.Results
	if len(r) != 5 {
		t.Fatalf("results %+v", r)
	}
	if r[0].Success || r[0].Error != "Media item not found" || r[0].ID != nil {
		t.Errorf("junk entry %+v", r[0])
	}
	if !r[1].Success || r[1].ID == nil || strings.HasPrefix(*r[1].ID, "play_local_") || !*r[1].ProgressSynced {
		t.Errorf("first %+v", r[1])
	}
	if r[2].Success || r[2].ID == nil || *r[2].ID != "fixed" {
		t.Errorf("unknown item %+v", r[2])
	}
	if !r[3].Success || *r[3].ID != *r[1].ID {
		t.Errorf("repeat %+v should reuse the server id", r[3])
	}
	if !r[4].Success || *r[4].ProgressSynced {
		t.Errorf("stale %+v should be stored without moving progress", r[4])
	}
	var created string
	e.row(t, &created, `SELECT createdAt FROM playbackSessions WHERE id = ?`, *r[1].ID)
	if want := time.UnixMilli(now - 1000).UTC().Format("2006-01-02 15:04:05"); !strings.HasPrefix(created, want) {
		t.Errorf("createdAt = %q, want %q: string timestamps are epoch milliseconds", created, want)
	}
}

func TestHistoryListingSortsFiltersAndDeletes(t *testing.T) {
	e := newEnv(t)
	e.expect(t, 404, listenerID, "GET", "/api/sessions", "")
	var page struct {
		Total, NumPages, Page, ItemsPerPage int
		UserID                              string
		Sessions                            []map[string]any
	}
	get := func(q string) {
		t.Helper()
		page = struct {
			Total, NumPages, Page, ItemsPerPage int
			UserID                              string
			Sessions                            []map[string]any
		}{}
		if err := json.Unmarshal(e.expect(t, 200, rootID, "GET", "/api/sessions"+q, "").Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
	}
	get("?sort=createdAt&desc=1&itemsPerPage=3&page=1")
	if page.Total != 7 || page.NumPages != 3 || page.Page != 1 || page.ItemsPerPage != 3 || len(page.Sessions) != 3 || page.UserID != "" {
		t.Fatalf("page %+v", page)
	}
	get("?user=" + rootID)
	if page.Total != 0 || page.UserID != rootID || len(page.Sessions) != 0 {
		t.Fatalf("filtered page %+v", page)
	}
	get("?itemsPerPage=0&page=-3&sort=bogus")
	if page.ItemsPerPage != 10 || page.Page != 0 || len(page.Sessions) != 7 {
		t.Fatalf("defaults %+v", page)
	}
	first := page.Sessions[0]
	if _, present := first["audioTracks"]; !present || first["user"] == nil || first["deviceInfo"] == nil {
		t.Fatalf("session %v lacks the user fields", first)
	}
	e.expect(t, 403, listenerID, "DELETE", "/api/sessions/legacy-null-0001", "")
	e.expect(t, 404, rootID, "DELETE", "/api/sessions/nope", "")
	e.expect(t, 200, rootID, "DELETE", "/api/sessions/legacy-null-0001", "")
	e.expect(t, 403, listenerID, "POST", "/api/sessions/batch/delete", `{"sessions":["legacy-null-0002"]}`)
	for _, bad := range []string{`{"sessions":[]}`, `{"sessions":["x y"]}`, `{`} {
		if w := e.expect(t, 400, rootID, "POST", "/api/sessions/batch/delete", bad); !strings.Contains(w.Body.String(), `"sessions" array`) {
			t.Errorf("message %q", w.Body.String())
		}
	}
	e.expect(t, 200, rootID, "POST", "/api/sessions/batch/delete", `{"sessions":["f166755e-ad8c-4334-adb9-eea51bc07c18","ab27584f-2573-49d1-99d9-5baf7ce82c80"]}`)
	if n := e.count(t, `SELECT COUNT(*) FROM playbackSessions`); n != 4 {
		t.Fatalf("%d sessions left, want 4", n)
	}
	id := e.startDirect(t, novelItem)
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/sync", `{"currentTime":1,"timeListened":1}`)
	e.expect(t, 200, rootID, "DELETE", "/api/sessions/"+id, "")
	if got := e.openIDs(t); len(got) != 0 {
		t.Fatalf("deleting a stored session should close it, open: %v", got)
	}
}

// HLS ------------------------------------------------------------------

func (e *env) startTranscoded(t *testing.T) (id, dir string) {
	t.Helper()
	if !media.FFmpegAvailable() {
		t.Fatal("fake ffmpeg unavailable")
	}
	w := e.expect(t, 200, listenerID, "POST", "/api/items/"+sagaItem+"/play", `{"forceTranscode":true}`)
	id = decode(t, w)["id"].(string)
	dir = filepath.Join(e.streams, id)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stream directory missing: %v", err)
	}
	return id, dir
}

func (e *env) streamOf(id string) *stream {
	s, _ := e.get(id)
	return s.stream
}

func waitIdle(t *testing.T, st *stream, segmentStart int) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		st.mu.Lock()
		idle, at := !st.resetting, st.segmentStart
		st.mu.Unlock()
		if idle && at == segmentStart {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart did not finish: resetting=%v segmentStart=%d, want %d", !idle, at, segmentStart)
		}
	}
}

func TestTranscodedSessionServesPlaylistAndRemovesStreamOnClose(t *testing.T) {
	e := newEnv(t)
	id, dir := e.startTranscoded(t)
	w := e.expect(t, 200, "", "GET", "/hls/"+id+"/output.m3u8", "")
	if !strings.Contains(w.Body.String(), "#EXTM3U") || !strings.Contains(w.Body.String(), "output-10.ts") || w.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("playlist %q", w.Body.String())
	}
	e.expect(t, 400, "", "GET", "/hls/"+id+"/secret.json", "")
	e.expect(t, 405, "", "POST", "/hls/"+id+"/output.m3u8", "")
	e.expect(t, 302, "", "GET", "/public/session/"+id+"/track/1", "")
	session := decode(t, e.expect(t, 200, listenerID, "GET", "/api/session/"+id, ""))
	if tracks, _ := session["audioTracks"].([]any); len(tracks) != 1 || tracks[0].(map[string]any)["mimeType"] != "application/vnd.apple.mpegurl" {
		t.Fatalf("tracks = %v", session["audioTracks"])
	}
	e.expect(t, 200, listenerID, "POST", "/api/session/"+id+"/close", `{}`)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stream directory survived close: %v", err)
	}
	e.expect(t, 404, "", "GET", "/hls/"+id+"/output.m3u8", "")
}

func TestHandlerCloseRemovesStreams(t *testing.T) {
	e := newEnv(t)
	_, a := e.startTranscoded(t)
	_, b := e.startTranscoded(t)
	e.Close()
	for _, dir := range []string{a, b} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s survived Close: %v", dir, err)
		}
	}
}

func TestMissingSegmentsResetOnlyWhenFarFromTheTranscode(t *testing.T) {
	e := newEnv(t)
	id, dir := e.startTranscoded(t)
	st := e.streamOf(id)
	if err := os.WriteFile(filepath.Join(dir, "output-1.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	e.expect(t, 404, "", "GET", "/hls/"+id+"/output-11.ts", "") // 10 beyond the frontier: not yet
	if n := e.events("stream_reset"); n != 0 {
		t.Fatalf("%d resets for a segment exactly 10 ahead", n)
	}
	e.expect(t, 404, "", "GET", "/hls/"+id+"/output-12.ts", "")
	e.expect(t, 404, "", "GET", "/hls/"+id+"/output-12.ts", "") // already in flight: announced once
	if n := e.events("stream_reset"); n != 1 {
		t.Fatalf("%d resets, want 1", n)
	}
	payload, _ := e.payload["stream_reset"].(map[string]any)
	if payload["startTime"] != float64(72) || payload["streamId"] != id {
		t.Fatalf("payload %#v", payload)
	}
	waitIdle(t, st, 2) // sent to 72 s, started 30 s earlier: segment 2
	if playlist, err := os.ReadFile(filepath.Join(dir, "output.m3u8")); err != nil || !strings.Contains(string(playlist), "output-10.ts") {
		t.Fatalf("the full playlist must survive a reset: %v", err)
	}
}

func TestBackwardResetAtSegmentZeroIsSilent(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "files.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	st := &stream{root: root, startTime: 42, segmentStart: 2, complete: true}
	e.add(&session{Session: Session{ID: "zero", UserID: listenerID}, stream: st})
	e.expect(t, 404, "", "GET", "/hls/zero/output-0.ts", "")
	waitIdle(t, st, 0)
	if n := e.events("stream_reset"); n != 0 {
		t.Fatalf("stream_reset sent %d times for segment zero (upstream suppresses a zero start)", n)
	}
	st.stop("zero")
}

func TestCleanupOrphanStreams(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "1c7c9087-d713-4417-80d6-c82d14ca98ac")
	other := filepath.Join(root, "keep-me")
	for _, dir := range []string{orphan, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupOrphanStreams(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan stream kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a directory that is not a session id was removed: %v", err)
	}
}
