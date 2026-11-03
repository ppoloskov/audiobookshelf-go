package contracttest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	// events is how many Socket.IO events the trial waited for after the
	// request. They are not checked here (see the skip in TestJourney).
	events int
	// skip is why the step does not run here.
	skip string
	// settle marks a step whose handler answers before its work is done (the
	// trial waited for its events). The runner waits for the database to stop
	// changing before the next step.
	settle bool
}

// recordedStep is one entry of journey.json: the status, media type and, for a
// JSON body, the shape of what TS v2.37.2 answered.
type recordedStep struct {
	Status      int             `json:"status"`
	ContentType string          `json:"contentType"`
	Shape       *shape.Skeleton `json:"shape,omitempty"`
}

// journeyResult is what one request of the journey got back.
type journeyResult struct {
	status int
	media  string // media type of the Content-Type header, without parameters
	raw    []byte
}

// TestJourney runs the replacement trial's client journey against one booted
// fixture, in order: the steps mutate state that later steps read. Each step
// is a subtest. A step checks its recorded status, media type and, for JSON,
// the recorded shape. Socket.IO events are not checked. With -record the same
// steps run against a TS server instead, and journey.json is written from them
// (journey_record_test.go).
func TestJourney(t *testing.T) {
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
	steps := journeySteps(in.IDs)
	if *record {
		recordJourney(t, in, c, steps)
		return
	}
	recorded := readJSON[struct {
		Steps map[string]recordedStep `json:"steps"`
	}](t, journeyShapesFile).Steps
	for _, st := range steps {
		t.Run(st.id, func(t *testing.T) {
			rec, ok := recorded[st.id]
			if !ok {
				t.Fatalf("no recorded shape for step %s in %s", st.id, journeyShapesFile)
			}
			checkJourneyStep(t, rec, runJourneyStep(t, in, c, st))
			if st.events > 0 {
				t.Skip(fmt.Sprintf("%d Socket.IO event(s) not checked: no Socket.IO client in the module", st.events))
			}
		})
	}
	checkJourneyDumps(t, in, recorded)
}

// runJourneyStep sends one step's request and keeps what its response captures.
// It does not check the response; checkJourneyStep does.
func runJourneyStep(t *testing.T, in *Install, c *journeyCtx, st journeyStep) journeyResult {
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
	var resp *http.Response
	var raw []byte
	for attempt := 0; ; attempt++ {
		resp, raw = in.request(t, method, route, bearer, headers, body)
		if resp.StatusCode != http.StatusNotFound || attempt >= st.retry {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if st.settle {
		waitForBackgroundWork(t, in.DB, before)
	}

	if ok2xx(resp.StatusCode) {
		if st.loginAs != "" {
			captureLogin(t, c, st.loginAs, raw)
		}
		if st.capture != "" {
			c.vals[st.capture] = decodeAny(raw)
		}
	}
	return journeyResult{status: resp.StatusCode, media: mediaType(resp.Header.Get("Content-Type")), raw: raw}
}

// checkJourneyStep checks a response against its recorded status, media type
// and, for a JSON body, its recorded shape.
func checkJourneyStep(t *testing.T, rec recordedStep, got journeyResult) {
	t.Helper()
	if got.status != rec.Status {
		t.Fatalf("status %d, recorded %d: %s", got.status, rec.Status, snippet(got.raw))
	}
	if got.media != rec.ContentType {
		t.Errorf("content type %q, recorded %q", got.media, rec.ContentType)
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
