package contracttest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/contracttest/shape"
)

// The journey's recorded responses (contracts/shapes/journey.json) come from TS
// v2.37.2 run on a copy of the fixture. To regenerate them:
//
//	node go-backend/tools/contractfixture/serve-upstream.cjs   # prints ROOT and READY
//	cd go-backend && go test ./internal/contracttest -run TestJourney -record \
//	    -upstream=<READY URL> -upstream-root=<ROOT>
//
// then stop the serve script. Record mode is in journey_record_test.go. A plain
// `go test ./internal/contracttest` checks Go against the file.

// journeyShapesFile holds the status, media type and shape each journey step got
// from TS v2.37.2 (see the note above).
const journeyShapesFile = "../../contracts/shapes/journey.json"

// journeyStep mirrors one entry of journeySteps in tools/parity/replacement-trial.cjs.
// A ctx-dependent field is a func of the journeyCtx instead of its plain value.
type journeyStep struct {
	id     string
	who    string // token to send: "listener", "root" or "finisher"; "" for none
	method string // GET when empty
	route  string
	// routeFn replaces route when the path depends on an earlier step.
	routeFn func(*journeyCtx) string
	// body is the JSON body; nil sends none.
	body   any
	bodyFn func(*journeyCtx) any
	// headers are extra request headers.
	headers   map[string]string
	headersFn func(*journeyCtx) map[string]string
	// capture keeps a 2xx JSON response under this name for later steps.
	capture string
	// loginAs stores the response's access token as the token of this user.
	loginAs string
	// binary names a non-JSON body kind ("bytes", "meta", "zip", "xml", "text").
	// Its media type came from a response header, so it is checked.
	binary string
	// tokenQuery sends the token as ?token= (media URLs), not as a bearer header.
	tokenQuery bool
	// retry is the number of extra attempts after a 404 (HLS playlists appear
	// once transcoding starts).
	retry int
	// events is how many Socket.IO events the step expects. The root client
	// waits for them (up to 3s) and the step's events are checked against
	// the recording (journey_events_test.go).
	events int
	// skip is why the step does not run here.
	skip string
	// settle marks a step whose handler answers before its work is done (the
	// trial waited for its events). The runner waits for the database to stop
	// changing before the next step.
	settle bool
	// taskStarted names a task action: after its request the step waits until
	// an unfinished task of that action is listed by GET /api/tasks.
	taskStarted string
	// taskFinished names a task action: after its request the step waits until
	// the tasks of that action have finished (see awaitTaskFinished).
	taskFinished string
	// touches is a file, relative to the install directory, that the task
	// rewrites when it succeeds. With taskFinished, the step fails if the file's
	// modification time did not change: a failed task leaves it alone.
	touches string
	// produces is a file, relative to the install directory, that the step's
	// task writes. The step fails unless the file exists and ffprobe reads a
	// duration above zero from it (checkProduced).
	produces string
}

// recordedStep is one entry of journey.json: the status, media type and, for a
// JSON body, the shape of what TS v2.37.2 answered.
type recordedStep struct {
	Status      int             `json:"status"`
	ContentType string          `json:"contentType"`
	Shape       *shape.Skeleton `json:"shape,omitempty"`
	// Events is set for a step that expects Socket.IO events: the events TS
	// sent, by name, with their payload skeletons.
	Events *eventShapes `json:"events,omitempty"`
}

// expectedEvents is what the recording says the step emits (nil when it
// recorded none).
func (r recordedStep) expectedEvents() eventShapes {
	if r.Events == nil {
		return nil
	}
	return *r.Events
}

// journeyResult is what one request of the journey got back.
type journeyResult struct {
	status int
	media  string // media type of the Content-Type header, without parameters
	raw    []byte
	events []journeyEvent
}

// TestJourney runs the replacement trial's client journey against one booted
// fixture, in order: the steps mutate state that later steps read. Each step
// is a subtest. A step checks its recorded status, media type and, for JSON,
// the recorded shape; a step that declares Socket.IO events also checks the
// events its root client received. With -record the same steps run against a
// TS server instead, and journey.json is written from them (journey_record_test.go).
func TestJourney(t *testing.T) {
	runJourney(t, journeyShapesFile, journeyDumps, func(_ *testing.T, in *Install, _ *journeyCtx) []journeyStep {
		return journeySteps(in.IDs)
	})
}

// runJourney runs one journey on its own install: steps builds the steps from
// the booted install and the journey's context. A plain run checks each step
// against shapesFile, then the dumps. With -record the same steps run against
// TS and shapesFile is written from them (journey_record_test.go).
func runJourney(t *testing.T, shapesFile string, dumps []string, steps func(*testing.T, *Install, *journeyCtx) []journeyStep) {
	t.Helper()
	// The image source is on 127.0.0.1, which the SSRF filter refuses unless
	// whitelisted. The trial needs the same setting (AGENTS.md).
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	in := journeyInstall(t)
	c := &journeyCtx{
		vals:       map[string]any{},
		tokens:     map[string]string{},
		fixtureURL: startJourneyImageSource(t),
		deadURL:    closedURL(t),
	}
	t.Cleanup(c.closeEvents)
	list := steps(t, in, c)
	// Only a journey with event steps connects the root client: its auth updates
	// the root user's lastSeen, which the other journeys' recordings do not show.
	for _, st := range list {
		if st.events > 0 {
			c.wantEvents = true
		}
	}
	if *record {
		recordJourneyFile(t, shapesFile, in, c, list, dumps)
		return
	}
	recorded := readJSON[struct {
		Steps map[string]recordedStep `json:"steps"`
	}](t, shapesFile).Steps
	for _, st := range list {
		t.Run(st.id, func(t *testing.T) {
			rec, ok := recorded[st.id]
			if !ok {
				t.Fatalf("no recorded shape for step %s in %s", st.id, shapesFile)
			}
			if st.events > 0 && rec.Events == nil {
				t.Fatalf("step %s expects events but %s has none: re-record", st.id, shapesFile)
			}
			checkJourneyStep(t, rec, runJourneyStep(t, in, c, st, rec.expectedEvents()))
		})
	}
	checkJourneyDumps(t, in, recorded, dumps)
}

// runJourneyStep sends one step's request and keeps what its response captures.
// It does not check the response; checkJourneyStep does.
func runJourneyStep(t *testing.T, in *Install, c *journeyCtx, st journeyStep, expect eventShapes) journeyResult {
	t.Helper()
	if st.skip != "" {
		t.Skip(st.skip)
	}

	c.missing = nil
	route := st.route
	if st.routeFn != nil {
		route = st.routeFn(c)
	}
	body := st.body
	if st.bodyFn != nil {
		body = st.bodyFn(c)
	}
	headers := map[string]string{"Accept": "*/*"} // what the trial's fetch sends by default
	for k, v := range st.headers {
		headers[k] = v
	}
	if st.headersFn != nil {
		for k, v := range st.headersFn(c) {
			headers[k] = v
		}
	}
	if len(c.missing) > 0 {
		t.Fatalf("needs %s: an earlier step did not capture it", strings.Join(c.missing, ", "))
	}

	token := ""
	if st.who != "" {
		token = c.tokens[st.who]
		if token == "" {
			t.Fatalf("no %s token: its login step did not succeed", st.who)
		}
	}
	bearer := token
	if st.tokenQuery {
		bearer = ""
		if token != "" && !tokenParam.MatchString(route) {
			sep := "?"
			if strings.Contains(route, "?") {
				sep = "&"
			}
			route += sep + "token=" + token
		}
	}

	method := st.method
	if method == "" {
		method = http.MethodGet
	}
	before := ""
	if st.settle {
		before = databaseFingerprint(t, in.DB)
	}
	if st.events > 0 {
		if c.events == nil {
			t.Fatal("step expects Socket.IO events but login.root did not connect a root client")
		}
		// The trial drained the buffer before the request, so the step sees
		// only its own events.
		time.Sleep(100 * time.Millisecond)
		c.events.drain()
	}
	touched := ""
	var touchedBefore time.Time
	if st.touches != "" {
		touched = filepath.Join(in.Dir, st.touches)
		touchedBefore = modTime(t, touched)
	}
	var resp *http.Response
	var raw []byte
	for attempt := 0; ; attempt++ {
		resp, raw = in.request(t, method, route, bearer, headers, body)
		if resp.StatusCode != http.StatusNotFound || attempt >= st.retry {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	var events []journeyEvent
	if st.events > 0 {
		// The step's completion event (batch_quickmatch_complete, task_finished)
		// is among the expected names, so this also waits for background work.
		events = c.events.take(expect, st.events)
	}
	if st.settle {
		waitForBackgroundWork(t, in.DB, before)
	}
	if st.taskStarted != "" {
		awaitTaskStarted(t, in, token, st.taskStarted)
	}
	if st.taskFinished != "" {
		awaitTaskFinished(t, in, token, st.taskFinished)
	}
	if touched != "" && !modTime(t, touched).After(touchedBefore) {
		t.Fatalf("%s was not rewritten by the %s task: it failed", st.touches, st.taskFinished)
	}
	if st.produces != "" {
		checkProduced(t, filepath.Join(in.Dir, st.produces))
	}

	if ok2xx(resp.StatusCode) {
		if st.loginAs != "" {
			captureLogin(t, c, st.loginAs, raw)
		}
		if st.loginAs == "root" && c.wantEvents && c.tokens["root"] != "" && c.events == nil {
			c.events = connectEventSocket(t, in.BaseURL, c.tokens["root"])
		}
		if st.capture != "" {
			c.vals[st.capture] = decodeAny(raw)
		}
	}
	return journeyResult{status: resp.StatusCode, media: mediaType(resp.Header.Get("Content-Type")), raw: raw, events: events}
}

// checkJourneyStep checks a response against its recorded status, media type
// and, for a JSON body, its recorded shape.
func checkJourneyStep(t *testing.T, rec recordedStep, got journeyResult) {
	t.Helper()
	if got.status != rec.Status {
		t.Fatalf("status %d, recorded %d: %s", got.status, rec.Status, snippet(got.raw))
	}
	// Content type is contract only where a client decodes the body: a JSON
	// response, or an image, audio or video the client plays or shows. Text
	// bodies (TS "OK", HTML errors), download containers (zip vs octet-stream)
	// and error or empty responses are free framing (ARCHITECTURE.md).
	if rec.Status < 300 && typedMedia(rec.ContentType) && got.media != rec.ContentType {
		t.Errorf("content type %q, recorded %q", got.media, rec.ContentType)
	}
	if rec.Events != nil {
		checkJourneyEvents(t, *rec.Events, got.events)
	}
	if rec.Shape == nil {
		return
	}
	var v any
	if len(bytes.TrimSpace(got.raw)) > 0 {
		if err := json.Unmarshal(got.raw, &v); err != nil {
			t.Fatalf("body is not JSON: %v: %s", err, snippet(got.raw))
		}
	}
	for _, problem := range shape.Conforms(rec.Shape, v) {
		t.Error(problem)
	}
}

// captureLogin keeps what the journey reads from a login: the access token and,
// for the listener, the refresh token and the user id.
func captureLogin(t *testing.T, c *journeyCtx, user string, raw []byte) {
	t.Helper()
	var out struct {
		User struct {
			ID           string `json:"id"`
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.User.AccessToken == "" {
		t.Fatalf("login as %s: no access token in %s", user, snippet(raw))
	}
	c.tokens[user] = out.User.AccessToken
	if user == "listener" {
		c.listenerID = out.User.ID
		c.listenerRefresh = out.User.RefreshToken
	}
}

var (
	tokenParam      = regexp.MustCompile(`[?&]token=`)
	providerNothing = regexp.MustCompile(`(?i)nothing`)
)

func ok2xx(status int) bool { return status >= 200 && status < 300 }

// typedMedia reports whether a recorded media type is contract for a successful
// response: JSON, or image, audio and video.
func typedMedia(media string) bool {
	return media == "application/json" || strings.HasPrefix(media, "image/") ||
		strings.HasPrefix(media, "audio/") || strings.HasPrefix(media, "video/")
}

func decodeAny(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func snippet(raw []byte) string {
	if len(raw) > 300 {
		return string(raw[:300]) + "..."
	}
	return string(raw)
}

// startJourneyImageSource serves the files the journey downloads from a URL:
// a JPEG and a PNG, a non-image page, an SVG, a redirect, and a custom metadata
// provider's search endpoint. Everything else is 404.
func startJourneyImageSource(t testing.TB) string {
	t.Helper()
	jpg := encodeJourneyImage(t, color.RGBA{R: 255, G: 165, A: 255}, 300, 500, false)
	pngBytes := encodeJourneyImage(t, color.RGBA{R: 128, A: 255}, 200, 200, true)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/author.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpg)
		case path == "/author.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		case strings.HasSuffix(path, "/search"):
			writeProviderMatches(w, r.URL.Query().Get("query"), srv.URL)
		case path == "/redirect":
			w.Header().Set("Location", "/author.jpg")
			w.WriteHeader(http.StatusFound)
		case path == "/page.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html></html>"))
		case path == "/icon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
		default:
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nope"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// writeProviderMatches answers a custom metadata provider's search: no matches
// for a query containing "nothing", otherwise two matches.
func writeProviderMatches(w http.ResponseWriter, query, base string) {
	var matches []map[string]any
	if !providerNothing.MatchString(query) {
		title := "Matched " + query
		if len(title) > 40 {
			title = title[:40]
		}
		matches = []map[string]any{
			{"title": title, "subtitle": "Matched Subtitle", "author": "Terry Fixture, Quick Match Author",
				"narrator": "Match Narrator One, Match Narrator Two", "publisher": "Match Press", "publishedYear": "2001",
				"description": "<b>Matched</b> description", "cover": base + "/author.jpg", "isbn": "9780000000001",
				"asin": "B0MATCH001", "genres": []string{"Matched Genre"}, "tags": []string{"matched-tag"},
				"series": []map[string]any{{"series": "Matched Series", "sequence": "3"}}, "language": "English"},
			{"title": "Second Result", "author": "Nobody"},
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"matches": matches})
}

// encodeJourneyImage makes a solid-colour image, as PNG or JPEG.
func encodeJourneyImage(t testing.TB, fill color.RGBA, width, height int, asPNG bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	var buf bytes.Buffer
	var err error
	if asPNG {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatalf("encode journey image: %v", err)
	}
	return buf.Bytes()
}

// databaseFingerprint summarises the tables background matching writes: the
// newest updatedAt of each and the row counts of the two that are created.
func databaseFingerprint(t testing.TB, db *sql.DB) string {
	t.Helper()
	var fp string
	err := db.QueryRow(`SELECT (SELECT COALESCE(MAX(updatedAt), '') FROM libraryItems) || '|' ||
		(SELECT COALESCE(MAX(updatedAt), '') FROM books) || '|' ||
		(SELECT COALESCE(MAX(updatedAt), '') FROM authors) || '|' ||
		(SELECT COALESCE(MAX(updatedAt), '') FROM series) || '|' ||
		(SELECT COUNT(*) FROM authors) || '|' || (SELECT COUNT(*) FROM series)`).Scan(&fp)
	if err != nil {
		t.Fatalf("database fingerprint: %v", err)
	}
	return fp
}

// waitForBackgroundWork waits for work a handler started and answered before
// (the trial waited for its Socket.IO events, which this runner cannot read).
// If the database changed since before, the wait ends once it has been quiet for
// 300ms. If it did not change within 2s, there was nothing to wait for.
func waitForBackgroundWork(t testing.TB, db *sql.DB, before string) {
	t.Helper()
	const quiet, noChange, limit = 300 * time.Millisecond, 2 * time.Second, 10 * time.Second
	start := time.Now()
	last, lastChange, changed := before, start, false
	for time.Since(start) < limit {
		time.Sleep(25 * time.Millisecond)
		fp := databaseFingerprint(t, db)
		if fp != last {
			last, lastChange, changed = fp, time.Now(), true
		}
		if changed && time.Since(lastChange) >= quiet {
			return
		}
		if !changed && time.Since(start) >= noChange {
			return
		}
	}
	t.Fatalf("background work still changing the database after %s", limit)
}

// Background tasks (embed, m4b merge) answer before their work is done. A step
// waits on the task list, not on a fixed sleep.
const (
	// taskWait bounds a wait for a task to start or finish.
	taskWait = 60 * time.Second
	// taskAppearWait is how long a wait for a task's end accepts that no such
	// task was ever listed: it finished before the first poll.
	taskAppearWait = 3 * time.Second
	taskPoll       = 50 * time.Millisecond
)

// listedTask is the part of a GET /api/tasks entry the waits read. The server
// drops a task from the list when it finishes (TS TaskManager.taskFinished and
// Go's task table alike), so a finished task's outcome is not listed.
type listedTask struct {
	Action     string `json:"action"`
	IsFinished bool   `json:"isFinished"`
}

// listTasks returns GET /api/tasks in the order the server lists them (oldest
// first).
func listTasks(t testing.TB, in *Install, token string) []listedTask {
	t.Helper()
	resp, raw := in.Do(t, http.MethodGet, "/api/tasks", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tasks: status %d: %s", resp.StatusCode, snippet(raw))
	}
	var out struct {
		Tasks []listedTask `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tasks: %v: %s", err, snippet(raw))
	}
	return out.Tasks
}

// taskUnfinished reports whether GET /api/tasks lists an unfinished task of action.
func taskUnfinished(t testing.TB, in *Install, token, action string) bool {
	t.Helper()
	for _, task := range listTasks(t, in, token) {
		if task.Action == action && !task.IsFinished {
			return true
		}
	}
	return false
}

// modTime is the modification time of a file the journey's tasks rewrite.
func modTime(t testing.TB, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// awaitTaskStarted waits until an unfinished task of action is listed.
func awaitTaskStarted(t testing.TB, in *Install, token, action string) {
	t.Helper()
	start := time.Now()
	for !taskUnfinished(t, in, token, action) {
		if time.Since(start) >= taskWait {
			t.Fatalf("no unfinished %s task listed after %s", action, taskWait)
		}
		time.Sleep(taskPoll)
	}
}

// awaitTaskFinished waits until the tasks of action have finished: one was
// listed unfinished and is no longer. A task that is never listed unfinished
// within taskAppearWait counts as finished before the first poll.
func awaitTaskFinished(t testing.TB, in *Install, token, action string) {
	t.Helper()
	start := time.Now()
	seen := false
	for {
		if taskUnfinished(t, in, token, action) {
			seen = true
		} else if seen || time.Since(start) >= taskAppearWait {
			return
		}
		if time.Since(start) >= taskWait {
			t.Fatalf("%s tasks still unfinished after %s", action, taskWait)
		}
		time.Sleep(taskPoll)
	}
}

// checkProduced fails unless path is a media file that ffprobe reads a duration
// above zero from: a step whose task writes a file checks what it wrote, not
// only that the request succeeded.
func checkProduced(t testing.TB, path string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	text := strings.TrimSpace(string(out))
	duration, err := strconv.ParseFloat(text, 64)
	if err != nil || duration <= 0 {
		t.Fatalf("ffprobe %s: duration %q, want above 0", path, text)
	}
}

// closedURL is an address nothing listens on: the port is bound, then released.
func closedURL(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("closed port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}
