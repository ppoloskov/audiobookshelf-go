package podcasts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/wire"
)

// useLocalFeeds lets the handler under test reach httptest servers on
// 127.0.0.1 through the production SafeClient, using the same
// SSRF_REQUEST_FILTER_WHITELIST switch an operator would. Call it before New:
// the client reads the environment once, when it is built.
func useLocalFeeds(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
}

type emitted struct {
	event   string
	payload any
}

// downloadRig is a handler with one podcast item, a controllable episode
// server and a stream of emitted events.
type downloadRig struct {
	*notifyRig
	srv    *httptest.Server
	events chan emitted

	mu        sync.Mutex
	active    int
	maxActive int
	requested []string
	gates     map[string]chan struct{} // closed to let the episode body through
	status    map[string]int           // non-zero: answer with this status
	cancelled chan string              // paths whose request was cancelled by the client
	hits      chan string              // paths as they are requested
}

func newDownloadRig(t *testing.T) *downloadRig {
	t.Helper()
	useLocalFeeds(t)
	d := &downloadRig{notifyRig: newNotifyRig(t), events: make(chan emitted, 256), gates: map[string]chan struct{}{},
		status: map[string]int{}, cancelled: make(chan string, 8), hits: make(chan string, 8)}
	d.h.emit = func(_ string, event string, payload any) {
		select {
		case d.events <- emitted{event, payload}:
		default:
		}
	}
	tone := testToneWAV(.25)
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		d.mu.Lock()
		d.active++
		d.maxActive = max(d.maxActive, d.active)
		d.requested = append(d.requested, p)
		gate, status := d.gates[p], d.status[p]
		d.mu.Unlock()
		defer func() { d.mu.Lock(); d.active--; d.mu.Unlock() }()
		d.hits <- p
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				d.cancelled <- p
				return
			}
		}
		if status != 0 {
			http.Error(w, "fixture failure", status)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tone)
	}))
	t.Cleanup(d.srv.Close)
	t.Cleanup(d.h.Close)
	return d
}

func (d *downloadRig) gate(path string) chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	g := make(chan struct{})
	d.gates[path] = g
	return g
}

func (d *downloadRig) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requested...)
}

// enqueue posts episodes (title -> server path) through the download route.
func (d *downloadRig) enqueue(t *testing.T, titlesToPaths ...string) {
	t.Helper()
	var eps []map[string]any
	for i := 0; i < len(titlesToPaths); i += 2 {
		eps = append(eps, map[string]any{"title": titlesToPaths[i], "enclosure": map[string]any{"url": d.srv.URL + titlesToPaths[i+1]}})
	}
	body, _ := json.Marshal(eps)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/podcasts/item/download-episodes", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	serveAdmin(d.h, w, r)
	if w.Code != 200 {
		t.Fatalf("download-episodes = %d %s", w.Code, w.Body)
	}
}

func (d *downloadRig) queued(t *testing.T) []wire.EpisodeDownload {
	t.Helper()
	w := httptest.NewRecorder()
	serveAdmin(d.h, w, httptest.NewRequest("GET", "/api/podcasts/item/downloads", nil))
	var out EpisodeDownloads
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("downloads = %d %s (%v)", w.Code, w.Body, err)
	}
	return out.Downloads
}

func (d *downloadRig) hit(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-d.hits:
		if got != want {
			t.Fatalf("server saw %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server never saw %s", want)
	}
}

// finished waits for the next episode_download_finished event.
func (d *downloadRig) finished(t *testing.T) wire.EpisodeDownload {
	t.Helper()
	for {
		select {
		case e := <-d.events:
			if e.event == "episode_download_finished" {
				return e.payload.(wire.EpisodeDownload)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no episode_download_finished event")
		}
	}
}

// idle waits until nothing is running or queued for the item.
func (d *downloadRig) idle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if q, cur := d.h.EpisodeDownloads("item"); len(q) == 0 && cur == nil {
			return
		}
		time.Sleep(2 * time.Millisecond) // polling a condition, not a delay
	}
	t.Fatal("downloads never went idle")
}

func (d *downloadRig) episodeRows(t *testing.T) int {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId='pod'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Downloads run one at a time in the order they were queued; the queued ones
// are listed by the route while the first runs.
func TestDownloadsRunOneAtATimeInQueueOrder(t *testing.T) {
	d := newDownloadRig(t)
	ga, gb, gc := d.gate("/a.wav"), d.gate("/b.wav"), d.gate("/c.wav")
	d.enqueue(t, "A", "/a.wav", "B", "/b.wav", "C", "/c.wav")
	d.hit(t, "/a.wav")
	if q := d.queued(t); len(q) != 2 || q[0].URL != d.srv.URL+"/b.wav" || q[1].URL != d.srv.URL+"/c.wav" {
		t.Fatalf("queued while A runs = %+v", q)
	}
	if _, cur := d.h.EpisodeDownloads("item"); cur == nil || cur.URL != d.srv.URL+"/a.wav" || cur.StartedAt == nil || cur.IsFinished {
		t.Fatalf("current = %+v", cur)
	}
	// A duplicate of a queued or running episode is not queued twice.
	d.enqueue(t, "A again", "/a.wav", "B again", "/b.wav")
	if q := d.queued(t); len(q) != 2 {
		t.Fatalf("duplicates were queued: %+v", q)
	}
	close(ga)
	d.hit(t, "/b.wav")
	if q := d.queued(t); len(q) != 1 {
		t.Fatalf("queued while B runs = %+v", q)
	}
	close(gb)
	d.hit(t, "/c.wav")
	close(gc)
	d.idle(t)

	d.mu.Lock()
	maxActive := d.maxActive
	d.mu.Unlock()
	if maxActive != 1 {
		t.Errorf("%d downloads ran at once, want 1", maxActive)
	}
	if got := strings.Join(d.requests(), ","); got != "/a.wav,/b.wav,/c.wav" {
		t.Errorf("request order = %s", got)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		if n := d.episodeRows(t); n != 2+3 { // the two seeded episodes plus three downloads
			t.Errorf("episode rows = %d, want 5", n)
		}
	}
}

// clear-queue drops what is waiting and tells clients; the running download
// finishes and nothing else starts.
func TestClearQueueDropsWaitingDownloadsOnly(t *testing.T) {
	d := newDownloadRig(t)
	ga := d.gate("/a.wav")
	d.enqueue(t, "A", "/a.wav", "B", "/b.wav", "C", "/c.wav")
	d.hit(t, "/a.wav")
	w := httptest.NewRecorder()
	serveAdmin(d.h, w, httptest.NewRequest("GET", "/api/podcasts/item/clear-queue", nil))
	if w.Code != 200 {
		t.Fatalf("clear-queue = %d", w.Code)
	}
	if q := d.queued(t); len(q) != 0 {
		t.Fatalf("queue after clear = %+v", q)
	}
	if _, cur := d.h.EpisodeDownloads("item"); cur == nil {
		t.Fatal("clear-queue stopped the running download")
	}
	cleared := false
	for len(d.events) > 0 {
		if e := <-d.events; e.event == "episode_download_queue_cleared" && e.payload == "item" {
			cleared = true
		}
	}
	if !cleared {
		t.Error("no episode_download_queue_cleared event for the item")
	}
	close(ga)
	d.idle(t)
	if got := strings.Join(d.requests(), ","); got != "/a.wav" {
		t.Errorf("requests after clear = %s, want only /a.wav", got)
	}
}

// A failed download is reported as failed and finished, leaves no episode or
// file behind, and does not stall the next queued download. Pollers read the
// status throughout, as the client does; run with -race.
func TestFailedDownloadIsReportedAndQueueContinues(t *testing.T) {
	d := newDownloadRig(t)
	d.status["/a.wav"] = 503
	ga := d.gate("/a.wav")
	stop := make(chan struct{})
	var pollers sync.WaitGroup
	for i := 0; i < 4; i++ {
		pollers.Add(1)
		go func() {
			defer pollers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.h.EpisodeDownloads("item")
				}
			}
		}()
	}
	d.enqueue(t, "A", "/a.wav", "B", "/b.wav")
	d.hit(t, "/a.wav")
	close(ga)
	first := d.finished(t)
	if !first.Failed || !first.IsFinished || first.FinishedAt == nil || first.URL != d.srv.URL+"/a.wav" {
		t.Fatalf("failed download status = %+v", first)
	}
	d.hit(t, "/b.wav")
	second := d.finished(t)
	d.idle(t)
	close(stop)
	pollers.Wait()
	if _, err := exec.LookPath("ffprobe"); err == nil {
		if second.Failed || !second.IsFinished {
			t.Errorf("second download = %+v", second)
		}
		if n := d.episodeRows(t); n != 3 {
			t.Errorf("episode rows = %d, want 3 (only B added)", n)
		}
	}
}

// Close cancels the running download (its request is abandoned and it is
// reported failed), drops the queue so nothing else starts, and may be called
// again.
func TestCloseCancelsRunningDownloadAndDropsQueue(t *testing.T) {
	d := newDownloadRig(t)
	d.gate("/a.wav") // never released: only cancellation ends the request
	d.enqueue(t, "A", "/a.wav", "B", "/b.wav")
	d.hit(t, "/a.wav")
	d.h.Close()
	d.h.Close()
	select {
	case p := <-d.cancelled:
		if p != "/a.wav" {
			t.Fatalf("cancelled %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close left the running download's request open")
	}
	if got := d.finished(t); !got.Failed || !got.IsFinished {
		t.Errorf("cancelled download = %+v", got)
	}
	d.idle(t)
	if got := strings.Join(d.requests(), ","); got != "/a.wav" {
		t.Errorf("requests after Close = %s, want only /a.wav", got)
	}
}

// The auto-download cron entry follows the podcast: present only while
// auto-download is on with a valid schedule, replaced (never duplicated) when
// the schedule changes, removed when it is turned off. Cron fires on minute
// boundaries, so the schedule is asserted through its next run time instead of
// waiting for it.
func TestRescheduleFollowsAutoDownloadSettings(t *testing.T) {
	r := newNotifyRig(t)
	defer r.h.Close()
	nextRun := func() []time.Time {
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
		var out []time.Time
		for _, e := range r.h.scheduler.Entries() {
			out = append(out, e.Schedule.Next(base))
		}
		return out
	}
	set := func(auto int, schedule any) {
		t.Helper()
		if _, err := r.db.Exec(`UPDATE podcasts SET autoDownloadEpisodes=?,autoDownloadSchedule=? WHERE id='pod'`, auto, schedule); err != nil {
			t.Fatal(err)
		}
		r.h.Reschedule(context.Background(), "item")
	}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }

	if got := nextRun(); len(got) != 0 {
		t.Fatalf("schedules before configuration: %v", got)
	}
	set(1, "30 2 * * *")
	if got := nextRun(); len(got) != 1 || !got[0].Equal(at(2, 30)) {
		t.Fatalf("after enabling 30 2 * * *: %v", got)
	}
	set(1, "0 5 * * *")
	if got := nextRun(); len(got) != 1 || !got[0].Equal(at(5, 0)) {
		t.Fatalf("after changing to 0 5 * * *: %v", got)
	}
	set(1, "not a cron")
	if got := nextRun(); len(got) != 0 {
		t.Fatalf("invalid schedule left entries: %v", got)
	}
	set(1, "15 1 * * *")
	set(0, "15 1 * * *")
	if got := nextRun(); len(got) != 0 {
		t.Fatalf("auto-download off left entries: %v", got)
	}
}

// Failed feed checks count consecutively: a successful check resets the
// count, so only maxFailedChecks failures in a row disable auto-download
// (PodcastManager.js:336-351).
func TestSuccessfulCheckResetsFailedCheckCount(t *testing.T) {
	useLocalFeeds(t)
	r := newNotifyRig(t)
	defer r.h.Close()
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
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><title>Pod</title><item><title>Old</title><guid>old</guid><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate><enclosure url="http://127.0.0.1:1/old.mp3"/></item></channel></rss>`))
	}))
	defer srv.Close()
	setHealthy := func(v bool) { mu.Lock(); healthy = v; mu.Unlock() }
	if _, err := r.db.Exec(`UPDATE podcasts SET feedURL=?`, srv.URL); err != nil {
		t.Fatal(err)
	}
	r.h.maxFailedChecks = 2
	expect := func(event string, numFailed int) {
		t.Helper()
		r.h.runScheduled("item")
		if n := r.note(t); n.event != event || n.data["numFailed"] != numFailed {
			t.Fatalf("notification = %+v, want %s #%d", n, event, numFailed)
		}
	}
	expect("onRSSFeedFailed", 1)
	setHealthy(true)
	r.h.runScheduled("item")
	r.noNote(t)
	setHealthy(false)
	expect("onRSSFeedFailed", 1) // not 2: the healthy check reset the count
	var auto bool
	if err := r.db.QueryRow(`SELECT autoDownloadEpisodes FROM podcasts`).Scan(&auto); err != nil || !auto {
		t.Fatalf("auto-download disabled too early: %v %v", auto, err)
	}
	expect("onRSSFeedDisabled", 2)
	if err := r.db.QueryRow(`SELECT autoDownloadEpisodes FROM podcasts`).Scan(&auto); err != nil || auto {
		t.Fatalf("auto-download still on after %d failures: %v %v", 2, auto, err)
	}
}
