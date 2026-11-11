package share

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

const (
	rootID   = "c2eedd44-2b7f-44cf-a18f-2687b798c386"
	listener = "f6af59cf-3988-45fe-b0ad-81420a953f9d"
	itemMid  = "7baea19e-3334-416f-b862-0cb7499e5006"
	bookMid  = "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af"
)

type event struct{ audience, name string }

type env struct {
	t      *testing.T
	d      *db.DB
	mux    *http.ServeMux
	events []event
	media  string
}

// newEnv mounts the handler on a copy of the fixture install whose book
// "The Middle" has a real audio file.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, d: dbtest.Fixture(t), mux: http.NewServeMux(), media: filepath.Join(t.TempDir(), "The Middle.flac")}
	if err := os.WriteFile(e.media, []byte("audio-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var files string
	if err := e.d.Get(t.Context(), &files, `SELECT audioFiles FROM books WHERE id = ?`, bookMid); err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	_ = json.Unmarshal([]byte(files), &parsed) // the fixture holds valid JSON
	parsed[0]["metadata"].(map[string]any)["path"] = e.media
	patched, _ := json.Marshal(parsed)
	e.exec(`UPDATE books SET audioFiles = ? WHERE id = ?`, string(patched), bookMid)
	e.exec(`UPDATE libraryItems SET path = ?, isFile = 1, relPath = 'The Middle.flac' WHERE id = ?`, e.media, itemMid)
	New(e.d, func(audience, name string, _ any) { e.events = append(e.events, event{audience, name}) }, "").Routes(e.mux)
	return e
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.d.Exec(e.t.Context(), query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(table string) (n int) {
	e.t.Helper()
	if err := e.d.Get(e.t.Context(), &n, `SELECT COUNT(*) FROM `+table); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// do sends a request as user ("" is anonymous), with an optional cookie.
func (e *env) do(user, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "abs.example"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	ctx := r.Context()
	if user != "" {
		var row db.User
		if err := e.d.Get(ctx, &row, `SELECT * FROM users WHERE id = ?`, user); err != nil {
			e.t.Fatal(err)
		}
		ctx = auth.WithUser(ctx, auth.NewPrincipal(row))
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r.WithContext(ctx))
	return w
}

func (e *env) share(slug string, downloadable bool) {
	e.t.Helper()
	body := `{"slug":"` + slug + `","expiresAt":0,"mediaItemType":"book","mediaItemId":"` + bookMid + `","isDownloadable":` + map[bool]string{true: "true", false: "false"}[downloadable] + `}`
	e.exec(`DELETE FROM mediaItemShares`)
	if w := e.do(rootID, "POST", "/api/share/mediaitem", body, nil); w.Code != 201 {
		e.t.Fatalf("create share: %d %s", w.Code, w.Body)
	}
}

func (e *env) closed() (n int) {
	for _, ev := range e.events {
		if ev.name == "share_closed" && ev.audience == "@admins" {
			n++
		}
	}
	return n
}

func TestFeedOpenListPublicRefresh(t *testing.T) {
	e := newEnv(t)
	body := `{"serverAddress":"https://abs.example","slug":"mid","metadataDetails":{"ownerName":"Owner","preventIndexing":false}}`
	if w := e.do(listener, "POST", "/api/feeds/item/"+itemMid+"/open", body, nil); w.Code != 403 {
		t.Fatalf("non-admin open = %d", w.Code)
	}
	if w := e.do(rootID, "POST", "/api/feeds/item/"+itemMid+"/open", body, nil); w.Code != 200 {
		t.Fatalf("open = %d %s", w.Code, w.Body)
	}
	if w := e.do(rootID, "POST", "/api/feeds/item/"+itemMid+"/open", body, nil); w.Code != 400 {
		t.Fatalf("duplicate slug = %d", w.Code)
	}
	if w := e.do(rootID, "GET", "/api/feeds", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"slug":"mid"`) {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	xml := e.do("", "GET", "/feed/mid", "", nil)
	if xml.Code != 200 || !strings.Contains(xml.Body.String(), "<title><![CDATA[The Middle]]></title>") || !strings.Contains(xml.Body.String(), "http://abs.example/feed/mid/item/") {
		t.Fatalf("feed = %d %s", xml.Code, xml.Body)
	}
	var episode string
	if err := e.d.Get(t.Context(), &episode, `SELECT id FROM feedEpisodes WHERE feedId = (SELECT id FROM feeds WHERE slug = 'mid')`); err != nil {
		t.Fatal(err)
	}
	if w := e.do("", "GET", "/feed/mid/item/"+episode+"/media.flac", "", nil); w.Code != 200 || w.Body.String() != "audio-data" {
		t.Fatalf("media = %d %q", w.Code, w.Body)
	}
	if w := e.do("", "GET", "/feed/mid/item/nope/media.flac", "", nil); w.Code != 404 {
		t.Fatalf("unknown episode = %d", w.Code)
	}
	// A changed book refreshes the feed and its episode keeps its id.
	later := db.FromMillis(time.Now().Add(time.Hour).UnixMilli())
	e.exec(`UPDATE books SET title = 'Renamed', updatedAt = ? WHERE id = ?`, later, bookMid)
	if w := e.do("", "GET", "/feed/mid", "", nil); !strings.Contains(w.Body.String(), "<title><![CDATA[Renamed]]></title>") {
		t.Fatalf("refreshed feed: %s", w.Body)
	}
	var kept string
	if err := e.d.Get(t.Context(), &kept, `SELECT id FROM feedEpisodes WHERE feedId = (SELECT id FROM feeds WHERE slug = 'mid')`); err != nil || kept != episode {
		t.Fatalf("episode id %q, want %q (%v)", kept, episode, err)
	}
	var feedID string
	_ = e.d.Get(t.Context(), &feedID, `SELECT id FROM feeds WHERE slug = 'mid'`)
	before := e.count("feedEpisodes")
	if w := e.do(rootID, "POST", "/api/feeds/"+feedID+"/close", "", nil); w.Code != 200 || e.count("feedEpisodes") != before-1 {
		t.Fatalf("close = %d, episodes %d -> %d", w.Code, before, e.count("feedEpisodes"))
	}
}

func TestShareSessionProgressTrackDownload(t *testing.T) {
	e := newEnv(t)
	e.share("pub", true)
	w := e.do("", "GET", "/public/share/pub?t=4", "", nil)
	if w.Code != 200 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	var got struct {
		PlaybackSession struct {
			CurrentTime float64 `json:"currentTime"`
			AudioTracks []struct {
				Metadata *struct{} `json:"metadata"`
			} `json:"audioTracks"`
		} `json:"playbackSession"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.PlaybackSession.CurrentTime != 4 {
		t.Fatalf("session %s", w.Body)
	}
	if w := e.do("", "PATCH", "/public/share/pub/progress", `{"currentTime":999}`, cookie); w.Code != 204 {
		t.Fatalf("progress = %d", w.Code)
	}
	if w := e.do("", "PATCH", "/public/share/pub/progress", `{"currentTime":1}`, nil); w.Code != 404 {
		t.Fatalf("progress without cookie = %d", w.Code)
	}
	again := e.do("", "GET", "/public/share/pub", "", cookie)
	if !strings.Contains(again.Body.String(), `"currentTime":30`) { // clamped to the 30 s duration
		t.Fatalf("clamped session %s", again.Body)
	}
	for _, path := range []string{"/track/1", "/download"} {
		if w := e.do("", "GET", "/public/share/pub"+path, "", cookie); w.Code != 200 || w.Body.String() != "audio-data" {
			t.Fatalf("%s = %d %q", path, w.Code, w.Body)
		}
	}
	if w := e.do("", "GET", "/public/share/pub/track/9", "", cookie); w.Code != 404 {
		t.Fatalf("unknown track = %d", w.Code)
	}
	e.share("nodl", false)
	cookie = e.do("", "GET", "/public/share/nodl", "", nil).Result().Cookies()[0]
	if w := e.do("", "GET", "/public/share/nodl/download", "", cookie); w.Code != 403 {
		t.Fatalf("download of a non-downloadable share = %d", w.Code)
	}
}

func TestShareExpiryClosesOnceOnEveryRoute(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "", ""}, {"GET", "/cover", ""}, {"GET", "/track/1", ""}, {"GET", "/download", ""}, {"PATCH", "/progress", `{"currentTime":3}`},
	} {
		e := newEnv(t)
		e.share("soon", true)
		cookie := e.do("", "GET", "/public/share/soon", "", nil).Result().Cookies()[0]
		e.exec(`UPDATE mediaItemShares SET expiresAt = ?`, db.FromMillis(time.Now().Add(-time.Minute).UnixMilli()))
		for range 2 {
			if w := e.do("", tc.method, "/public/share/soon"+tc.path, tc.body, cookie); w.Code != 404 {
				t.Fatalf("%s: expired = %d", tc.path, w.Code)
			}
		}
		if e.count("mediaItemShares") != 0 || e.closed() != 1 {
			t.Fatalf("%s: shares left %d, share_closed %d", tc.path, e.count("mediaItemShares"), e.closed())
		}
	}
}

func TestShareDeleteClosesSessions(t *testing.T) {
	e := newEnv(t)
	e.share("gone", true)
	cookie := e.do("", "GET", "/public/share/gone", "", nil).Result().Cookies()[0]
	var id string
	_ = e.d.Get(t.Context(), &id, `SELECT id FROM mediaItemShares`)
	if w := e.do(listener, "DELETE", "/api/share/mediaitem/"+id, "", nil); w.Code != 403 {
		t.Fatalf("non-admin delete = %d", w.Code)
	}
	if w := e.do(rootID, "DELETE", "/api/share/mediaitem/"+id, "", nil); w.Code != 204 || e.closed() != 1 {
		t.Fatalf("delete = %d, closed %d", w.Code, e.closed())
	}
	e.share("gone", true)
	if w := e.do("", "GET", "/public/share/gone/download", "", cookie); w.Code != 404 {
		t.Fatalf("old session on a re-created share = %d", w.Code)
	}
}

func TestShareCreateValidationAndDates(t *testing.T) {
	e := newEnv(t)
	create := func(body string) *httptest.ResponseRecorder {
		return e.do(rootID, "POST", "/api/share/mediaitem", body, nil)
	}
	for body, want := range map[string]int{
		`{"expiresAt":0,"mediaItemType":"book","mediaItemId":"` + bookMid + `"}`:                                       400,
		`{"slug":"a","mediaItemType":"book","mediaItemId":"` + bookMid + `"}`:                                          400,
		`{"slug":"a","expiresAt":0,"mediaItemType":"playlist","mediaItemId":"x"}`:                                      400,
		`{"slug":"a","expiresAt":0,"mediaItemType":"book","mediaItemId":"nope"}`:                                       404,
		`{"slug":"trial-share-novel","expiresAt":0,"mediaItemType":"book","mediaItemId":"` + bookMid + `"}`:            409,
		`{"slug":"a","expiresAt":4102444800000,"mediaItemType":"book","mediaItemId":"` + bookMid + `"}`:                409, // already shared
		`{"slug":"past","expiresAt":1000,"mediaItemType":"book","mediaItemId":"54374ca2-e3c2-4121-93eb-4b0d806b241f"}`: 201,
	} {
		if w := create(body); w.Code != want {
			t.Errorf("%s = %d, want %d (%s)", body, w.Code, want, w.Body)
		}
	}
	if e.count("mediaItemShares") != 2 { // the fixture's two; the expired one is never stored
		t.Fatalf("shares = %d", e.count("mediaItemShares"))
	}
	e.exec(`DELETE FROM mediaItemShares`)
	w := create(`{"slug":"iso","expiresAt":4102444800000,"mediaItemType":"book","mediaItemId":"` + bookMid + `"}`)
	iso := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
	var share map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &share)
	for _, key := range []string{"createdAt", "updatedAt", "expiresAt"} {
		if s, _ := share[key].(string); !iso.MatchString(s) {
			t.Errorf("%s = %v, want an ISO string", key, share[key])
		}
	}
	if share["expiresAt"] != "2100-01-01T00:00:00.000Z" {
		t.Errorf("expiresAt = %v", share["expiresAt"])
	}
}
