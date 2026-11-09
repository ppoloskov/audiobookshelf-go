package playback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/store"
)

// These tests drive the playback routes as a client does and assert what a
// client, an administrator, the database and the event stream observe. The
// expected values are derived by hand from the request bodies and from the
// TS sources named in the comments; the session JSON shape itself is compared
// with the TS server's by internal/contracttest (journey "play.*" steps).

var adminUser = store.User{ID: "admin-one", Username: "admin", Type: "admin", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return out
}

func startDirect(t *testing.T, h *env, user store.User, extra string) string {
	t.Helper()
	w := request(h, http.MethodPost, "/api/items/item-one/play", user, `{"forceDirectPlay":true,"mediaPlayer":"test"`+extra+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	id, _ := decode(t, w)["id"].(string)
	if id == "" {
		t.Fatalf("start returned no session id: %s", w.Body.String())
	}
	return id
}

func openIDs(t *testing.T, h *env) []string {
	t.Helper()
	w := request(h, http.MethodGet, "/api/sessions/open", adminUser, "")
	if w.Code != http.StatusOK {
		t.Fatalf("open sessions: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, s := range body.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}

func sessionRow(t *testing.T, h *env, id string) (listening float64, current float64, found bool) {
	t.Helper()
	err := h.db.QueryRow(`SELECT timeListening, currentTime FROM playbackSessions WHERE id=?`, id).Scan(&listening, &current)
	return listening, current, err == nil
}

// PlaybackSessionManager.js startSession/syncSession/closeSession: a session is
// open from start until close, syncs add to the listened time, and close
// persists and forgets it.
func TestSessionOpenSyncCloseLifecycle(t *testing.T) {
	h, user, _, _ := fixture(t)
	id := startDirect(t, h, user, "")
	if got := openIDs(t, h); len(got) != 1 || got[0] != id {
		t.Fatalf("open sessions after start = %v, want [%s]", got, id)
	}

	other := store.User{ID: "user-2", Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	for _, tc := range []struct {
		who  store.User
		want int
	}{{user, 200}, {adminUser, 200}, {other, 403}} {
		if w := request(h, http.MethodGet, "/api/session/"+id, tc.who, ""); w.Code != tc.want {
			t.Errorf("GET session as %s = %d, want %d", tc.who.ID, w.Code, tc.want)
		}
	}
	if w := request(h, http.MethodPost, "/api/session/"+id+"/sync", other, `{"currentTime":1,"timeListened":1}`); w.Code != http.StatusForbidden {
		t.Errorf("foreign sync = %d, want 403", w.Code)
	}
	if w := request(h, http.MethodGet, "/api/sessions/open", user, ""); w.Code != http.StatusNotFound {
		t.Errorf("non-admin open-session list = %d, want 404", w.Code)
	}

	// Invalid syncs are rejected and leave nothing behind.
	for _, body := range []string{`{"currentTime":-1,"timeListened":1}`, `{"currentTime":1}`, `{"timeListened":1}`, `{"currentTime":1,"timeListened":-2}`} {
		if w := request(h, http.MethodPost, "/api/session/"+id+"/sync", user, body); w.Code != http.StatusBadRequest {
			t.Errorf("sync %s = %d, want 400", body, w.Code)
		}
	}
	if _, _, found := sessionRow(t, h, id); found {
		t.Fatal("a rejected sync persisted the session")
	}

	// Two syncs accumulate listened time (2 + 3) and replace the position.
	for _, body := range []string{`{"currentTime":1.5,"timeListened":2,"duration":3}`, `{"currentTime":2,"timeListened":3,"duration":3}`} {
		if w := request(h, http.MethodPost, "/api/session/"+id+"/sync", user, body); w.Code != http.StatusOK {
			t.Fatalf("sync %s = %d %s", body, w.Code, w.Body.String())
		}
	}
	if listening, current, found := sessionRow(t, h, id); !found || listening != 5 || current != 2 {
		t.Fatalf("after syncs: found=%v timeListening=%v currentTime=%v, want true 5 2", found, listening, current)
	}

	// Close with a final sync adds to the total, ends the session and tells the user once.
	if w := request(h, http.MethodPost, "/api/session/"+id+"/close", user, `{"currentTime":2.5,"timeListened":1}`); w.Code != http.StatusOK {
		t.Fatalf("close = %d %s", w.Code, w.Body.String())
	}
	if listening, current, found := sessionRow(t, h, id); !found || listening != 6 || current != 2.5 {
		t.Fatalf("after close: found=%v timeListening=%v currentTime=%v, want true 6 2.5", found, listening, current)
	}
	if got := openIDs(t, h); len(got) != 0 {
		t.Fatalf("open sessions after close = %v", got)
	}
	for _, step := range []struct{ method, path string }{{"GET", "/api/session/" + id}, {"POST", "/api/session/" + id + "/sync"}, {"POST", "/api/session/" + id + "/close"}} {
		if w := request(h, step.method, step.path, user, `{"currentTime":1,"timeListened":1}`); w.Code != http.StatusNotFound {
			t.Errorf("%s %s after close = %d, want 404", step.method, step.path, w.Code)
		}
	}
	closed := 0
	for _, e := range h.emissions() {
		if e == (emission{user.ID, "user_session_closed"}) {
			closed++
		}
	}
	if closed != 1 {
		t.Fatalf("user_session_closed emitted %d times, want 1", closed)
	}
}

// closeSession without sync data only saves (PlaybackSessionManager.js:422-424),
// and saveSession skips a session with no listening time (:436).
func TestCloseWithoutSyncDataSavesOnlyListenedSessions(t *testing.T) {
	h, user, _, _ := fixture(t)
	silent := startDirect(t, h, user, "")
	if w := request(h, http.MethodPost, "/api/session/"+silent+"/close", user, `{}`); w.Code != http.StatusOK {
		t.Fatalf("close = %d", w.Code)
	}
	if _, _, found := sessionRow(t, h, silent); found {
		t.Fatal("a session with no listening time was saved")
	}
	listened := startDirect(t, h, user, "")
	request(h, http.MethodPost, "/api/session/"+listened+"/sync", user, `{"currentTime":1,"timeListened":2}`)
	if w := request(h, http.MethodPost, "/api/session/"+listened+"/close", user, `{}`); w.Code != http.StatusOK {
		t.Fatalf("close = %d", w.Code)
	}
	if listening, _, found := sessionRow(t, h, listened); !found || listening != 2 {
		t.Fatalf("listened session found=%v timeListening=%v, want a row with 2", found, listening)
	}
}

// Sessions are in memory: Close (server shutdown / backup restore) forgets
// them all.
func TestHandlerCloseForgetsOpenSessions(t *testing.T) {
	h, user, _, _ := fixture(t)
	a, b := startDirect(t, h, user, ""), startDirect(t, h, user, "")
	if len(openIDs(t, h)) != 2 {
		t.Fatal("expected two open sessions")
	}
	h.Close()
	if got := openIDs(t, h); len(got) != 0 {
		t.Fatalf("open sessions after Close = %v", got)
	}
	for _, id := range []string{a, b} {
		if w := request(h, http.MethodPost, "/api/session/"+id+"/sync", user, `{"currentTime":1,"timeListened":1}`); w.Code != http.StatusNotFound {
			t.Errorf("sync after Close = %d, want 404", w.Code)
		}
	}
}

// PlaybackSessionManager.syncLocalSession: an offline session is stored once
// per client id, later uploads update it, and its progress only applies when it
// is not older than the stored progress.
func TestLocalSyncStoresOnceAndRespectsNewerProgress(t *testing.T) {
	h, user, _, _ := fixture(t)
	// The first upload creates the progress, stamped with the server clock
	// (User.createUpdateMediaProgressFromPayload ignores lastUpdate on create);
	// later uploads apply only when not older than that (:232).
	now := time.Now().UnixMilli()
	post := func(path, body string) *httptest.ResponseRecorder {
		return request(h, http.MethodPost, path, user, body)
	}
	local := func(current float64, listening float64, updated int64, extra string) string {
		return `{"id":"play_local_a","libraryItemId":"item-one","currentTime":` + strconv.FormatFloat(current, 'f', -1, 64) +
			`,"timeListening":` + strconv.FormatFloat(listening, 'f', -1, 64) + `,"startedAt":` + strconv.FormatInt(updated-60000, 10) +
			`,"updatedAt":` + strconv.FormatInt(updated, 10) + `,"mediaPlayer":"offline"` + extra + `}`
	}
	if w := post("/api/session/local", local(20, 5, now, `,"date":"2026-01-05","dayOfWeek":"Monday"`)); w.Code != http.StatusOK {
		t.Fatalf("local = %d %s", w.Code, w.Body.String())
	}
	if w := post("/api/session/local", local(30, 9, now+10000, ``)); w.Code != http.StatusOK {
		t.Fatalf("local again = %d %s", w.Code, w.Body.String())
	}
	var rows int
	var id string
	var listening, current float64
	if err := h.db.QueryRow(`SELECT COUNT(*), MIN(id), MIN(timeListening), MIN(currentTime) FROM playbackSessions`).Scan(&rows, &id, &listening, &current); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || id == "play_local_a" || listening != 9 || current != 30 {
		t.Fatalf("rows=%d id=%q timeListening=%v currentTime=%v, want one row under a server id with 9 and 30", rows, id, listening, current)
	}
	// The second upload recomputes the day from its updatedAt (:212-216).
	var date string
	if err := h.db.QueryRow(`SELECT date FROM playbackSessions`).Scan(&date); err != nil || date != time.UnixMilli(now+10000).Format("2006-01-02") {
		t.Fatalf("date = %q err=%v", date, err)
	}
	var progress float64
	if err := h.db.QueryRow(`SELECT currentTime FROM mediaProgresses WHERE userId=? AND mediaItemId='book-one'`, user.ID).Scan(&progress); err != nil || progress != 30 {
		t.Fatalf("progress = %v err=%v, want 30", progress, err)
	}
	// An upload older than the stored progress is stored but does not move it.
	if w := post("/api/session/local", local(5, 1, now-3600000, ``)); w.Code != http.StatusOK {
		t.Fatalf("old local = %d", w.Code)
	}
	if err := h.db.QueryRow(`SELECT currentTime FROM mediaProgresses WHERE userId=? AND mediaItemId='book-one'`, user.ID).Scan(&progress); err != nil || progress != 30 {
		t.Fatalf("progress after stale upload = %v err=%v, want 30", progress, err)
	}
	// Unknown item and malformed body.
	if w := post("/api/session/local", `{"id":"x","libraryItemId":"nope"}`); w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "Media item not found") {
		t.Fatalf("unknown item = %d %s, want 500 Media item not found", w.Code, w.Body.String())
	}
	if w := post("/api/session/local", `[`); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", w.Code)
	}
}

func TestLocalAllReportsEachSessionIndependently(t *testing.T) {
	h, user, _, _ := fixture(t)
	now := time.Now().UnixMilli()
	body := `{"sessions":[
		{"id":"play_local_one","libraryItemId":"item-one","currentTime":7,"timeListening":3,"startedAt":` + strconv.FormatInt(now-1000, 10) + `,"updatedAt":` + strconv.FormatInt(now, 10) + `},
		{"id":"fixed-id","libraryItemId":"nope","displayTitle":"gone"},
		{"id":"play_local_one","libraryItemId":"item-one","currentTime":8,"timeListening":4,"updatedAt":` + strconv.FormatInt(now+60000, 10) + `},
		{"id":"play_local_old","libraryItemId":"item-one","currentTime":1,"timeListening":1,"updatedAt":1000}
	]}`
	w := request(h, http.MethodPost, "/api/session/local-all", user, body)
	if w.Code != http.StatusOK {
		t.Fatalf("local-all = %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Results []struct {
			ID             string `json:"id"`
			Success        bool   `json:"success"`
			ProgressSynced *bool  `json:"progressSynced"`
			Error          string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	r := res.Results
	if len(r) != 4 {
		t.Fatalf("results = %+v, want 4 in request order", r)
	}
	if !r[0].Success || r[0].ID == "" || r[0].ID == "play_local_one" || r[0].ProgressSynced == nil || !*r[0].ProgressSynced {
		t.Errorf("first = %+v, want success under a server id with progress synced", r[0])
	}
	if r[1].Success || r[1].Error != "Media item not found" || r[1].ID != "fixed-id" {
		t.Errorf("unknown item = %+v, want failure echoing its id", r[1])
	}
	if !r[2].Success || r[2].ID != r[0].ID {
		t.Errorf("repeat of play_local_one = %+v, want the same server id as %q", r[2], r[0].ID)
	}
	if !r[3].Success || r[3].ProgressSynced == nil || *r[3].ProgressSynced {
		t.Errorf("stale session = %+v, want stored without moving progress", r[3])
	}
	var rows int
	var current float64
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM playbackSessions`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("stored sessions = %d err=%v, want 2 (one shared by the repeat, one stale)", rows, err)
	}
	if err := h.db.QueryRow(`SELECT currentTime FROM mediaProgresses WHERE userId=?`, user.ID).Scan(&current); err != nil || current != 8 {
		t.Fatalf("progress = %v err=%v, want 8 from the newest upload", current, err)
	}
}

// Stream.js: a transcoded session owns a stream directory that lives exactly
// as long as the session. ffmpeg runs out of process, so the first segment is
// awaited by polling with a deadline.
func startTranscoded(t *testing.T, h *env, user store.User) (id, dir string) {
	t.Helper()
	if !mediatools.FFmpegAvailable() {
		t.Skip("ffmpeg is unavailable")
	}
	w := request(h, http.MethodPost, "/api/items/item-one/play", user, `{"forceTranscode":true,"mediaPlayer":"test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start transcode: %d %s", w.Code, w.Body.String())
	}
	id, _ = decode(t, w)["id"].(string)
	dir = filepath.Join(h.meta, "streams", id)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stream directory missing after start: %v", err)
	}
	return id, dir
}

func hls(h *env, id, name string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	serve(h, w, httptest.NewRequest(http.MethodGet, "/hls/"+id+"/"+name, nil), store.User{})
	return w
}

func TestTranscodeCloseRemovesStreamAndRefusesFurtherRequests(t *testing.T) {
	h, user, _, _ := fixture(t)
	id, dir := startTranscoded(t, h, user)
	if w := hls(h, id, "output.m3u8"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "#EXTM3U") {
		t.Fatalf("playlist while open = %d %s", w.Code, w.Body.String())
	}
	if w := request(h, http.MethodPost, "/api/session/"+id+"/close", user, `{}`); w.Code != http.StatusOK {
		t.Fatalf("close = %d", w.Code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stream directory survived close: %v", err)
	}
	if w := hls(h, id, "output.m3u8"); w.Code != http.StatusNotFound {
		t.Fatalf("playlist after close = %d, want 404", w.Code)
	}
}

func TestHandlerCloseStopsTranscodesAndRemovesStreams(t *testing.T) {
	h, user, _, _ := fixture(t)
	_, dirA := startTranscoded(t, h, user)
	_, dirB := startTranscoded(t, h, user)
	h.Close()
	for _, dir := range []string{dirA, dirB} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("stream directory %s survived Handler.Close: %v", dir, err)
		}
	}
}

// Stream.js checkSegmentNumberRequest: a segment before the transcode's first
// one restarts ffmpeg there and tells clients; a segment that is merely not
// written yet is just a 404.
func TestSegmentRequestsResetOnlyWhenBehindTheTranscodeStart(t *testing.T) {
	h, user, _, _ := fixture(t)
	if !mediatools.FFmpegAvailable() {
		t.Skip("ffmpeg is unavailable")
	}
	// Resuming at 40s starts the transcode at segment floor((40-30)/6) = 1.
	if _, err := h.db.Exec(`INSERT INTO mediaProgresses (id,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,extraData,userId) VALUES ('p','book-one','book',3,40,0,0,'{}','user-one')`); err != nil {
		t.Fatal(err)
	}
	id, _ := startTranscoded(t, h, user)
	defer request(h, http.MethodPost, "/api/session/"+id+"/close", user, `{}`)
	resets := func() int {
		n := 0
		for _, e := range h.emissions() {
			if e.event == "stream_reset" {
				n++
			}
		}
		return n
	}
	if w := hls(h, id, "output-5.ts"); w.Code != http.StatusNotFound || resets() != 0 {
		t.Fatalf("segment ahead of the start: status %d, resets %d; want 404 and no reset", w.Code, resets())
	}
	if w := hls(h, id, "output-0.ts"); w.Code != http.StatusNotFound || resets() != 1 {
		t.Fatalf("segment behind the start: status %d, resets %d; want 404 and one reset", w.Code, resets())
	}
}
