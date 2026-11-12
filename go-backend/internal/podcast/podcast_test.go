package podcast

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

const (
	testItem    = "eeb6224f-1316-40c8-b8bf-00804cc90436"
	testPod     = "a477b353-4058-4b9e-8399-49781256fc24"
	testFolder  = "707e28b2-414a-4996-be70-fbfcdb687727"
	testLib     = "61d2b487-8714-4575-ad70-147fd4ee924b"
	testEpisode = "c2b5b947-590f-4fe7-a9cd-ef18855a1ddb"
)

type event struct {
	event   string
	payload any
}

type note struct {
	event string
	data  map[string]any
}

// rig is a Handler over a copy of the fixture install whose podcast folder is a
// temporary directory.
type rig struct {
	h       *Handler
	filters *filterdata.Cache
	d       *db.DB
	root    string
	events  chan event
	notes   chan note
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	r := &rig{d: dbtest.Fixture(t), filters: filterdata.NewCache(), root: t.TempDir(), events: make(chan event, 256), notes: make(chan note, 16)}
	for _, q := range []string{
		"UPDATE libraryItems SET path='" + r.root + "' WHERE mediaType='podcast'",
		"UPDATE libraryFolders SET path='" + r.root + "' WHERE id='" + testFolder + "'",
	} {
		if _, err := r.d.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	st, err := settings.NewStore(context.Background(), r.d, os.Getenv, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r.h = New(r.d, st, r.filters, config.Config{MaxFailedEpisodeChecks: 2}, func(_, name string, payload any) {
		select {
		case r.events <- event{name, payload}:
		default:
		}
	}, func(_ context.Context, name string, data map[string]any) error {
		r.notes <- note{name, data}
		return nil
	}, func(_ context.Context, changes []item.PlaylistChange) {
		for _, c := range changes {
			name := "playlist_updated"
			if c.Removed {
				name = "playlist_removed"
			}
			select {
			case r.events <- event{name, c.Playlist.ID}:
			default:
			}
		}
	})
	t.Cleanup(r.h.Close)
	return r
}

// serve sends a request through the routes as the named fixture user.
func (r *rig) serve(t *testing.T, username, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var row db.User
	if err := r.d.Get(context.Background(), &row, `SELECT * FROM users WHERE username=?`, username); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.h.Routes(mux)
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(auth.WithUser(context.Background(), auth.NewPrincipal(row)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func (r *rig) sql(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := r.d.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := r.d.Get(context.Background(), &n, query); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitEvent returns the next event with the name, skipping others.
func (r *rig) waitEvent(t *testing.T, name string) any {
	t.Helper()
	for {
		select {
		case e := <-r.events:
			if e.event == name {
				return e.payload
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s event", name)
		}
	}
}

func (r *rig) idle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if q, cur := r.h.EpisodeDownloads(testItem); len(q) == 0 && cur == nil {
			return
		}
		time.Sleep(2 * time.Millisecond) // polling a condition, not a delay
	}
	t.Fatal("downloads never went idle")
}

// episodeServer serves a tone per path; a gated path blocks until its channel
// closes or the client goes away.
type episodeServer struct {
	*httptest.Server
	mu        sync.Mutex
	gates     map[string]chan struct{}
	requested []string
	hits      chan string
	cancelled chan string
}

func newEpisodeServer(t *testing.T) *episodeServer {
	s := &episodeServer{gates: map[string]chan struct{}{}, hits: make(chan string, 16), cancelled: make(chan string, 4)}
	tone := testToneWAV(.25)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requested = append(s.requested, r.URL.Path)
		gate := s.gates[r.URL.Path]
		s.mu.Unlock()
		s.hits <- r.URL.Path
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				s.cancelled <- r.URL.Path
				return
			}
		}
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "fixture failure", 503)
			return
		}
		_, _ = w.Write(tone)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *episodeServer) gate(path string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := make(chan struct{})
	s.gates[path] = g
	return g
}

func (s *episodeServer) hit(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-s.hits:
		if got != want {
			t.Fatalf("server saw %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server never saw %s", want)
	}
}

func (s *episodeServer) requests() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.requested, ",")
}

func (r *rig) download(t *testing.T, s *episodeServer, titlesToPaths ...string) {
	t.Helper()
	var eps []map[string]any
	for i := 0; i < len(titlesToPaths); i += 2 {
		eps = append(eps, map[string]any{"title": titlesToPaths[i], "enclosure": map[string]any{"url": s.URL + titlesToPaths[i+1]}})
	}
	body, _ := json.Marshal(eps)
	if w := r.serve(t, "trial-root", "POST", "/api/podcasts/"+testItem+"/download-episodes", string(body)); w.Code != 200 {
		t.Fatalf("download-episodes = %d %s", w.Code, w.Body)
	}
}

func TestDownloadsRunOneAtATimeInQueueOrder(t *testing.T) {
	r, srv := newRig(t), newEpisodeServer(t)
	ga, gb, gc := srv.gate("/a.wav"), srv.gate("/b.wav"), srv.gate("/c.wav")
	r.download(t, srv, "A", "/a.wav", "B", "/b.wav", "C", "/c.wav")
	srv.hit(t, "/a.wav")
	queued, current := r.h.EpisodeDownloads(testItem)
	if len(queued) != 2 || queued[0].URL != srv.URL+"/b.wav" || current == nil || current.URL != srv.URL+"/a.wav" || current.StartedAt == nil {
		t.Fatalf("queued %+v current %+v", queued, current)
	}
	r.download(t, srv, "A again", "/a.wav", "B again", "/b.wav") // already running or queued
	if queued, _ = r.h.EpisodeDownloads(testItem); len(queued) != 2 {
		t.Fatalf("duplicates were queued: %+v", queued)
	}
	close(ga)
	srv.hit(t, "/b.wav")
	close(gb)
	srv.hit(t, "/c.wav")
	close(gc)
	r.idle(t)
	if got := srv.requests(); got != "/a.wav,/b.wav,/c.wav" {
		t.Errorf("request order = %s", got)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM podcastEpisodes`); n != 6 {
		t.Errorf("episode rows = %d, want 6", n)
	}
	if n := r.count(t, `SELECT numEpisodes FROM podcasts`); n != 6 {
		t.Errorf("numEpisodes = %d, want 6", n)
	}
}

func TestClearQueueDropsWaitingDownloadsOnly(t *testing.T) {
	r, srv := newRig(t), newEpisodeServer(t)
	ga := srv.gate("/a.wav")
	r.download(t, srv, "A", "/a.wav", "B", "/b.wav")
	srv.hit(t, "/a.wav")
	if w := r.serve(t, "trial-root", "GET", "/api/podcasts/"+testItem+"/clear-queue", ""); w.Code != 200 {
		t.Fatalf("clear-queue = %d", w.Code)
	}
	if got := r.waitEvent(t, "episode_download_queue_cleared"); got != testItem {
		t.Errorf("cleared payload = %v", got)
	}
	if q, cur := r.h.EpisodeDownloads(testItem); len(q) != 0 || cur == nil {
		t.Fatalf("after clear queued %+v current %+v", q, cur)
	}
	close(ga)
	r.idle(t)
	if got := srv.requests(); got != "/a.wav" {
		t.Errorf("requests = %s", got)
	}
}

func TestFailedDownloadIsReportedAndQueueContinues(t *testing.T) {
	r, srv := newRig(t), newEpisodeServer(t)
	r.download(t, srv, "Bad", "/bad.wav", "Good", "/good.wav")
	first := r.waitEvent(t, "episode_download_finished")
	if v, _ := json.Marshal(first); !strings.Contains(string(v), `"failed":true`) || !strings.Contains(string(v), `"isFinished":true`) {
		t.Fatalf("failed download = %s", v)
	}
	r.waitEvent(t, "episode_download_finished")
	r.idle(t)
	if n := r.count(t, `SELECT COUNT(*) FROM podcastEpisodes`); n != 4 {
		t.Errorf("episode rows = %d, want 4 (only Good added)", n)
	}
}

func TestCloseCancelsRunningDownloadAndDropsQueue(t *testing.T) {
	r, srv := newRig(t), newEpisodeServer(t)
	srv.gate("/a.wav") // never released: only cancellation ends the request
	r.download(t, srv, "A", "/a.wav", "B", "/b.wav")
	srv.hit(t, "/a.wav")
	r.h.Close()
	r.h.Close()
	select {
	case <-srv.cancelled:
	default:
		select {
		case <-srv.cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("Close left the running download's request open")
		}
	}
	if got := srv.requests(); got != "/a.wav" {
		t.Errorf("requests after Close = %s", got)
	}
}

func TestRescheduleFollowsAutoDownloadSettings(t *testing.T) {
	r := newRig(t)
	next := func() []time.Time {
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
		var out []time.Time
		for _, e := range r.h.cron.Entries() {
			out = append(out, e.Schedule.Next(base))
		}
		return out
	}
	set := func(auto int, schedule string) {
		r.sql(t, `UPDATE podcasts SET autoDownloadEpisodes=?,autoDownloadSchedule=? WHERE id=?`, auto, schedule, testPod)
		r.h.Reschedule(context.Background(), testItem)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	if got := next(); len(got) != 0 {
		t.Fatalf("entries before configuration: %v", got)
	}
	set(1, "30 2 * * *")
	if got := next(); len(got) != 1 || !got[0].Equal(at(2, 30)) {
		t.Fatalf("after enabling: %v", got)
	}
	set(1, "0 5 * * *")
	if got := next(); len(got) != 1 || !got[0].Equal(at(5, 0)) {
		t.Fatalf("after changing: %v", got)
	}
	set(1, "not a cron")
	set(0, "15 1 * * *")
	if got := next(); len(got) != 0 {
		t.Fatalf("entries left: %v", got)
	}
}

func (r *rig) wantNote(t *testing.T, event string, numFailed int) {
	t.Helper()
	select {
	case n := <-r.notes:
		if n.event != event || n.data["numFailed"] != numFailed {
			t.Fatalf("notification = %+v, want %s #%d", n, event, numFailed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s notification", event)
	}
}

// Failed checks count in a row; a good check resets the count, and the
// maxFailedChecks-th failure disables auto-download.
func TestFailedFeedChecksDisableAutoDownload(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	healthy := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		ok := healthy
		mu.Unlock()
		if !ok {
			http.Error(w, "down", 503)
			return
		}
		_, _ = w.Write([]byte(feedXML(feedItem("Old", "old", "Mon, 02 Jan 2006 15:04:05 GMT", "/old.wav"))))
	}))
	defer srv.Close()
	r.sql(t, `UPDATE podcasts SET feedURL=?,autoDownloadEpisodes=1 WHERE id=?`, srv.URL, testPod)
	setHealthy := func(v bool) { mu.Lock(); healthy = v; mu.Unlock() }
	r.h.runScheduled(testItem)
	r.wantNote(t, "onRSSFeedFailed", 1)
	setHealthy(true)
	r.h.runScheduled(testItem)
	setHealthy(false)
	r.h.runScheduled(testItem)
	r.wantNote(t, "onRSSFeedFailed", 1) // the healthy check reset the count
	if r.count(t, `SELECT autoDownloadEpisodes FROM podcasts`) != 1 {
		t.Fatal("auto-download disabled too early")
	}
	r.h.runScheduled(testItem)
	r.wantNote(t, "onRSSFeedDisabled", 2)
	if r.count(t, `SELECT autoDownloadEpisodes FROM podcasts`) != 0 {
		t.Fatal("auto-download still on")
	}
}

func TestScheduledRunDownloadsNewEpisodesAndNotifies(t *testing.T) {
	r, srv := newRig(t), newEpisodeServer(t)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.ReplaceAll(feedXML(feedItem("Fresh", "g1", "Mon, 02 Mar 2099 10:00:00 GMT", "/fresh.wav")), "EPIS", srv.URL)))
	}))
	defer feed.Close()
	r.sql(t, `UPDATE podcasts SET feedURL=?,autoDownloadEpisodes=1 WHERE id=?`, feed.URL, testPod)
	r.h.runScheduled(testItem)
	r.waitEvent(t, "episode_added")
	r.wantNoteEvent(t, "onPodcastEpisodeDownloaded")
	r.idle(t)
	var guid string
	if err := r.d.Get(context.Background(), &guid, `SELECT json_extract(extraData,'$.guid') FROM podcastEpisodes WHERE title='Fresh'`); err != nil || guid != "g1" {
		t.Fatalf("guid = %q, %v", guid, err)
	}
	r.h.runScheduled(testItem) // nothing new now
	time.Sleep(100 * time.Millisecond)
	if n := r.count(t, `SELECT COUNT(*) FROM podcastEpisodes WHERE title='Fresh'`); n != 1 {
		t.Fatalf("Fresh downloaded %d times", n)
	}
}

func (r *rig) wantNoteEvent(t *testing.T, event string) {
	t.Helper()
	select {
	case n := <-r.notes:
		if n.event != event {
			t.Fatalf("notification = %s, want %s", n.event, event)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %s notification", event)
	}
}

func TestEpisodePatchAndDelete(t *testing.T) {
	r := newRig(t)
	path := "/api/podcasts/" + testItem + "/episode/" + testEpisode
	title := func() string {
		var s string
		if err := r.d.Get(context.Background(), &s, `SELECT COALESCE(title,'<null>') FROM podcastEpisodes WHERE id=?`, testEpisode); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, tc := range []struct {
		body, title string
		events      int
	}{
		{`{}`, "Pilot Episode", 0},
		{`{"title":"Pilot Episode"}`, "Pilot Episode", 0},
		{`{"chapters":[{"title":"bad","start":0}]}`, "Pilot Episode", 0},
		{`{"title":"","description":"<p>x</p><script>bad</script>","chapters":[{"title":"part","start":1}]}`, "", 1},
	} {
		if w := r.serve(t, "trial-root", "PATCH", path, tc.body); w.Code != 200 || strings.Contains(w.Body.String(), "<script>") {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		if got := title(); got != tc.title && !(tc.title == "" && got == "") {
			t.Fatalf("%s: title %q", tc.body, got)
		}
		if got := len(r.events); got != tc.events {
			t.Fatalf("%s: %d events, want %d", tc.body, got, tc.events)
		}
		for len(r.events) > 0 {
			<-r.events
		}
	}
	if w := r.serve(t, "trial-root", "PATCH", path, `{"title":3}`); w.Code != 400 {
		t.Errorf("wrong type = %d", w.Code)
	}
	if w := r.serve(t, "trial-listener", "PATCH", path, `{"title":"x"}`); w.Code != 403 {
		t.Errorf("listener patch = %d", w.Code)
	}
	if w := r.serve(t, "trial-listener", "GET", path, ""); w.Code != 200 || strings.Contains(w.Body.String(), "audioTrack") {
		t.Errorf("listener get = %d %s", w.Code, w.Body)
	}
	if w := r.serve(t, "trial-root", "DELETE", path, ""); w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	if n := r.count(t, `SELECT COUNT(*) FROM podcastEpisodes`); n != 2 {
		t.Errorf("episodes after delete = %d", n)
	}
	if n := r.count(t, `SELECT numEpisodes FROM podcasts`); n != 2 {
		t.Errorf("numEpisodes = %d", n)
	}
	var files []db.LibraryFile
	var raw db.JSON[[]db.LibraryFile]
	if err := r.d.Get(context.Background(), &raw, `SELECT libraryFiles FROM libraryItems WHERE id=?`, testItem); err != nil {
		t.Fatal(err)
	}
	files = raw.V
	if len(files) != 3 {
		t.Errorf("libraryFiles after delete = %d, want 3", len(files))
	}
	if w := r.serve(t, "trial-root", "DELETE", path, ""); w.Code != 404 {
		t.Errorf("second delete = %d", w.Code)
	}
}

func TestCreatePodcast(t *testing.T) {
	r := newRig(t)
	body := func(path, extra string) string {
		return `{"libraryId":"` + testLib + `","folderId":"` + testFolder + `","path":"` + path + `",` + extra + `"media":{"metadata":{"title":"The New Show","description":"<p>x</p><script>bad</script>","itunesId":12345,"genres":["Arts"]}}}`
	}
	post := func(b string) *httptest.ResponseRecorder { return r.serve(t, "trial-root", "POST", "/api/podcasts", b) }
	w := post(body(r.root+"/New Show", `"autoDownloadEpisodes":true,"autoDownloadSchedule":"5 4 * * *",`))
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(r.root, "New Show")); err != nil {
		t.Errorf("folder not made: %v", err)
	}
	var id string
	if err := r.d.Get(context.Background(), &id, `SELECT itunesId FROM podcasts WHERE title='The New Show'`); err != nil || id != "12345" {
		t.Errorf("itunesId = %q, %v", id, err)
	}
	if len(r.h.cron.Entries()) != 1 {
		t.Errorf("cron entries = %d, want 1", len(r.h.cron.Entries()))
	}
	for name, tc := range map[string]struct {
		user, body string
		want       int
	}{
		"duplicate": {"trial-root", body(r.root+"/New Show", ""), 400},
		"outside":   {"trial-root", body(filepath.Dir(r.root)+"/elsewhere", ""), 400},
		"bad cron":  {"trial-root", body(r.root+"/Cron", `"autoDownloadSchedule":"bad",`), 400},
		"not admin": {"trial-listener", body(r.root+"/Nope", ""), 403},
		"no media":  {"trial-root", `{"libraryId":"x"}`, 400},
		"unknown":   {"trial-root", strings.Replace(body(r.root+"/F", ""), testFolder, "none", 1), 404},
		"malformed": {"trial-root", `{`, 400},
	} {
		if w := r.serve(t, tc.user, "POST", "/api/podcasts", tc.body); w.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", name, w.Code, tc.want, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(r.root, "Cron")); !os.IsNotExist(err) {
		t.Errorf("rejected podcast left a folder: %v", err)
	}
}

func TestFeedOPMLAndSearchRoutes(t *testing.T) {
	r := newRig(t)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(feedXML(feedItem("Long Road Home", "g", "Mon, 02 Mar 2026 10:00:00 GMT", "/a.wav"), feedItem("Cooking", "h", "Tue, 03 Mar 2026 10:00:00 GMT", "/b.wav"))))
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gz.Bytes())
	}))
	defer srv.Close()
	r.sql(t, `UPDATE podcasts SET feedURL=?`, srv.URL)
	w := r.serve(t, "trial-root", "POST", "/api/podcasts/feed", `{"rssFeed":"`+srv.URL+`"}`)
	var out struct{ Podcast feed }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Podcast.Metadata.Title != "Fixture Cast" || len(out.Podcast.Episodes) != 2 {
		t.Fatalf("feed = %d %s", w.Code, w.Body)
	}
	for body, want := range map[string]int{`{"rssFeed":"nope"}`: 400, `{"rssFeed":"http://127.0.0.1:1/x"}`: 404} {
		if w := r.serve(t, "trial-root", "POST", "/api/podcasts/feed", body); w.Code != want {
			t.Errorf("%s = %d, want %d", body, w.Code, want)
		}
	}
	w = r.serve(t, "trial-root", "GET", "/api/podcasts/"+testItem+"/search-episode?title=long+road+home", "")
	var found struct {
		Episodes []struct{ Episode feedEpisode }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &found); err != nil || len(found.Episodes) != 1 || found.Episodes[0].Episode.Title != "Long Road Home" {
		t.Fatalf("search = %d %s", w.Code, w.Body)
	}
	r.sql(t, `UPDATE podcasts SET feedURL='http://127.0.0.1:1/x'`)
	if w = r.serve(t, "trial-root", "GET", "/api/podcasts/"+testItem+"/search-episode?title=x", ""); strings.TrimSpace(w.Body.String()) != `{"episodes":null}` {
		t.Errorf("unreadable feed search = %s", w.Body)
	}
	if w = r.serve(t, "trial-root", "GET", "/api/podcasts/"+testItem+"/checknew", ""); strings.TrimSpace(w.Body.String()) != `{"episodes":[]}` {
		t.Errorf("unreadable feed checknew = %s", w.Body)
	}
}

func TestQuickMatchFillsUnmatchedEpisodes(t *testing.T) {
	r := newRig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(feedXML(feedItem("Pilot Episode", "g", "Mon, 02 Mar 2026 10:00:00 GMT", "/a.wav"), feedItem("Unrelated", "h", "Tue, 03 Mar 2026 10:00:00 GMT", "/b.wav"))))
	}))
	defer srv.Close()
	r.sql(t, `UPDATE podcasts SET feedURL=?`, srv.URL)
	n, err := r.h.QuickMatchEpisodes(context.Background(), testItem, false)
	if err != nil || n != 1 {
		t.Fatalf("matched %d, %v", n, err)
	}
	var row struct {
		URL       string `db:"enclosureURL"`
		Season    string `db:"season"`
		Subtitle  string `db:"subtitle"`
		Published string `db:"publishedAt"`
	}
	if err := r.d.Get(context.Background(), &row, `SELECT enclosureURL,season,COALESCE(subtitle,'') AS subtitle,publishedAt FROM podcastEpisodes WHERE id=?`, testEpisode); err != nil {
		t.Fatal(err)
	}
	url, season, subtitle := row.URL, row.Season, row.Subtitle
	if !strings.HasSuffix(url, "/a.wav") || season != "1" || subtitle != "sub Pilot Episode" {
		t.Errorf("row = %q %q %q", url, season, subtitle)
	}
	if n, err = r.h.QuickMatchEpisodes(context.Background(), testItem, false); err != nil || n != 0 {
		t.Errorf("second run matched %d, %v", n, err)
	}
}

func TestLibraryRoutes(t *testing.T) {
	r := newRig(t)
	get := func(user, path string) *httptest.ResponseRecorder { return r.serve(t, user, "GET", path, "") }
	if w := get("trial-root", "/api/libraries/"+testLib+"/recent-episodes?limit=2&page=0"); w.Code != 200 || strings.Count(w.Body.String(), `"libraryId"`) != 2 {
		t.Errorf("recent = %d %s", w.Code, w.Body)
	}
	if w := get("trial-root", "/api/libraries/51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41/recent-episodes?limit=2"); w.Code != 404 {
		t.Errorf("book library recent = %d", w.Code)
	}
	if w := get("trial-root", "/api/libraries/none/episode-downloads"); w.Code != 404 {
		t.Errorf("unknown library = %d", w.Code)
	}
	if w := get("trial-root", "/api/libraries/"+testLib+"/recent-episodes?limit=-1"); w.Code != 400 {
		t.Errorf("negative limit = %d", w.Code)
	}
	if w := get("trial-root", "/api/libraries/"+testLib+"/episode-downloads"); strings.TrimSpace(w.Body.String()) != `{"queue":[]}` {
		t.Errorf("idle downloads = %s", w.Body)
	}
}

func TestParseOPMLSelectsRSSOutlines(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want []opmlFeed
	}{
		"flat":   {`<opml><body><outline type="rss" text="One" xmlUrl="https://a.example/1.xml"/><outline type="rss" title="Two" text="x" xmlUrl=" https://a.example/2.xml "/></body></opml>`, []opmlFeed{{"One", "https://a.example/1.xml"}, {"Two", "https://a.example/2.xml"}}},
		"nested": {`<opml><body><outline text="F"><outline type="rss" text="In" xmlUrl="https://a.example/3.xml"/><outline><outline type="RSS" text="Deep" xmlUrl="https://a.example/4.xml"/></outline></outline></body></opml>`, []opmlFeed{{"In", "https://a.example/3.xml"}, {"Deep", "https://a.example/4.xml"}}},
		"skips":  {`<opml><body><outline type="link" xmlUrl="https://a.example/6.xml"/><outline type="rss" text="No url"/></body></opml>`, []opmlFeed{}},
		"latin1": {"<?xml version=\"1.0\" encoding=\"windows-1252\"?><opml><body><outline type=\"rss\" text=\"Caf\xe9\" xmlUrl=\"https://a.example/9.xml\"/></body></opml>", []opmlFeed{{"Café", "https://a.example/9.xml"}}},
		"broken": {`<opml><body><outline type="rss" xmlUrl="https://a.example/8.xml"></body>`, []opmlFeed{}},
		"text":   {`hello`, []opmlFeed{}},
	} {
		if got := parseOPML(tc.doc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", name, got, tc.want)
		}
	}
}

func TestFilterNewSelectsUnseenEpisodesAfterCutoff(t *testing.T) {
	guid, url := "g1", "http://x/seen.mp3"
	it := &item.Item{Podcast: &item.Podcast{Episodes: []item.Episode{
		{PodcastEpisode: db.PodcastEpisode{ExtraData: db.JSON[*db.EpisodeExtra]{V: &db.EpisodeExtra{GUID: &guid}}}},
		{PodcastEpisode: db.PodcastEpisode{EnclosureURL: &url}},
	}}}
	at := func(ms int64) *int64 { return &ms }
	other := "other"
	eps := []feedEpisode{
		{Title: "old", PublishedAt: at(50), Enclosure: enclosure{URL: "u1"}},
		{Title: "undated", Enclosure: enclosure{URL: "u2"}},
		{Title: "known guid", PublishedAt: at(200), GUID: &guid, Enclosure: enclosure{URL: "u3"}},
		{Title: "known url", PublishedAt: at(200), Enclosure: enclosure{URL: url}},
		{Title: "new a", PublishedAt: at(300), GUID: &other, Enclosure: enclosure{URL: "u4"}},
		{Title: "new b", PublishedAt: at(400), Enclosure: enclosure{URL: "u5"}},
		{Title: "new c", PublishedAt: at(500), Enclosure: enclosure{URL: "u6"}},
	}
	titles := func(limit int) (out []string) {
		for _, ep := range filterNew(eps, it, 100, limit) {
			out = append(out, ep.Title)
		}
		return out
	}
	if got := titles(0); !reflect.DeepEqual(got, []string{"new a", "new b", "new c"}) {
		t.Errorf("no limit: %v", got)
	}
	if got := titles(2); !reflect.DeepEqual(got, []string{"new a", "new b"}) {
		t.Errorf("limit 2: %v", got)
	}
}

func TestParseFeedShapesAndSanitizes(t *testing.T) {
	parsed, err := parseFeed([]byte(`<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><title>Cast</title><description><![CDATA[<p>Hi <script>x</script><strong>b</strong></p>]]></description><itunes:author>W</itunes:author><itunes:image href="https://e.test/c.jpg"/><itunes:category text="Arts"><itunes:category text="Books"/></itunes:category><item><title>E1</title><content:encoded><![CDATA[<p><a href="javascript:x">l</a></p>]]></content:encoded><itunes:duration>01:02:03</itunes:duration><guid>g</guid><enclosure url="https://e.test/1.mp3" type="audio/mpeg" length="12"/></item><item><title>no audio</title></item></channel></rss>`), "https://e.test/feed")
	if err != nil {
		t.Fatal(err)
	}
	m := parsed.Metadata
	if m.Title != "Cast" || m.Image != "https://e.test/c.jpg" || m.FeedURL != "https://e.test/feed" || !reflect.DeepEqual(m.Categories, []string{"Arts:Books"}) || strings.Contains(m.Description, "script") {
		t.Errorf("metadata = %+v", m)
	}
	if len(parsed.Episodes) != 1 || *parsed.Episodes[0].DurationSeconds != 3723 || strings.Contains(parsed.Episodes[0].Description, "javascript:") || parsed.Episodes[0].Enclosure.Length != "12" {
		t.Errorf("episodes = %+v", parsed.Episodes)
	}
	for _, doc := range []string{`<feed xmlns="http://www.w3.org/2005/Atom"/>`, `<rss version="2.0"><channel><title>x</title></channel></rss>`, `<rss version="2.0"><channel><item><title>a</title></item></channel></rss>`} {
		if _, err := parseFeed([]byte(doc), "u"); err == nil {
			t.Errorf("accepted %s", doc)
		}
	}
}

func TestCreateInvalidatesFilterData(t *testing.T) {
	r := newRig(t)
	genres := func() []string {
		data, err := r.filters.Get(context.Background(), r.d, testLib, "podcast")
		if err != nil {
			t.Fatal(err)
		}
		return data.Genres
	}
	if slices.Contains(genres(), "Zymurgy") { // also fills the cache
		t.Fatal("genre present before the podcast exists")
	}
	body := `{"libraryId":"` + testLib + `","folderId":"` + testFolder + `","path":"` + r.root + `/Brew","tags":["fermenting"],"media":{"metadata":{"title":"Brew","genres":["Zymurgy"]}}}`
	if w := r.serve(t, "trial-root", "POST", "/api/podcasts", body); w.Code != 200 {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	if !slices.Contains(genres(), "Zymurgy") {
		t.Error("the filter menu does not show the new podcast's genre")
	}
}
