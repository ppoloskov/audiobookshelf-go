package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

// harness is a Handler over an empty database and the fake upstream.
type harness struct {
	h      *Handler
	mux    *http.ServeMux
	fake   *fakeUpstream
	mu     sync.Mutex
	events []string
}

func (hn *harness) emit(audience, event string, _ any) {
	hn.mu.Lock()
	defer hn.mu.Unlock()
	hn.events = append(hn.events, audience+":"+event)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	hn := &harness{fake: newFake(t), mux: http.NewServeMux()}
	hn.h = New(dbtest.New(t), hn.emit)
	f := hn.fake.URL
	hn.h.svc = newServices(getter{&http.Client{}}, Endpoints{
		Google: f + "/google/books/v1/volumes", ITunes: f + "/itunes/search", OpenLibrary: f + "/openlibrary", FantLab: f + "/fantlab",
		Audible: f + "/audible/1.0/catalog/products", Audnexus: f + "/nexus", AudiobookCovers: f + "/abc/cover/bytext/",
	})
	hn.h.custom = getter{&http.Client{}} // the fake is on loopback; see TestCustomProvidersAreSSRFGuarded
	hn.h.Routes(hn.mux)
	insertCustom(t, hn.h.d, "c1", "Local", "book", f+"/custom", "Bearer secret")
	return hn
}

func (hn *harness) do(user, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(auth.WithUser(r.Context(), auth.NewPrincipal(db.User{ID: "u", Type: &user, IsActive: true})))
	w := httptest.NewRecorder()
	hn.mux.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, w.Body)
	}
	return v
}

// insertCustom seeds a custom provider row.
func insertCustom(t *testing.T, d *db.DB, id, name, mediaType, url, authHeader string) {
	t.Helper()
	now := db.Now()
	if _, err := d.Exec(context.Background(), `INSERT INTO customMetadataProviders(id,name,mediaType,url,authHeaderValue,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?)`, id, name, mediaType, url, authHeader, now, now); err != nil {
		t.Fatal(err)
	}
}

// TestProviderOutputs checks every provider's output for the recorded
// upstream bodies in fakeUpstream. The expected values are written out by hand
// from those bodies and the TS providers (server/providers/*.js).
func TestProviderOutputs(t *testing.T) {
	hn := newHarness(t)
	for _, c := range []struct{ name, path, want string }{
		{"books/google", "/api/search/books?provider=google&title=Title&author=Writer", `[{"author":"A One, B Two","cover":"https://i.test/l.jpg","description":"<b>Desc</b> text","descriptionPlain":"Desc text","genres":["Fiction","Fantasy"],"id":"g1","isbn":"9780000000001","publishedYear":"2020","publisher":"Pub","subtitle":"Sub","title":"Full Book"},{"cover":"https://i.test/z.jpg","id":"g2","isbn":"0000000002","title":"Sparse"}]`},
		{"books/itunes", "/api/search/books?provider=itunes&title=Title&author=Writer", `[{"artistId":222,"author":"X, Y","cover":"https://a.test/600x600bb.jpg","description":"<p>Hello</p>","descriptionPlain":"Hello","genres":["Mystery"],"id":111,"publishedYear":"2019","title":"iTunes Book"},{"author":"Solo","cover":"https://a.test/600.jpg","id":112,"title":"Plain"}]`},
		{"books/openlibrary", "/api/search/books?provider=openlibrary&title=Title&author=Writer", `[{"author":"Tolkien, Other","cover":"https://covers.openlibrary.org/b/OLID/OL1M-L.jpg","covers":["https://covers.openlibrary.org/b/id/12-L.jpg","https://covers.openlibrary.org/b/id/34-L.jpg"],"description":"Obj <b>desc</b>","descriptionPlain":"Obj desc","edition":"OL1M","first_publish_date":"1937-09-21","id":"OL1W","key":"/works/OL1W","publishedYear":"1937","title":"The Hobbit"},{"description":"Plain desc","descriptionPlain":"Plain desc","first_publish_date":"1999-01","id":"OL2W","key":"/works/OL2W","publishedYear":"1999","title":"Second"}]`},
		{"books/audiobookcovers", "/api/search/books?provider=audiobookcovers&title=Title&author=Writer", `[{"cover":"https://c.test/1.png"},{"cover":"https://c.test/2.png"}]`},
		{"books/fantlab", "/api/search/books?provider=fantlab&title=Title&author=Writer", `[{"author":"Writer One, Writer Two","cover":"https://fantlab.ru/img/e.jpg","description":"Fant desc","descriptionPlain":"Fant desc","genres":["Fantasy","Epic","Heroic"],"id":12,"isbn":"978-5","publishedYear":"2021","subtitle":"Alt A","title":"Fant Title"},{"id":13,"publishedYear":"2005","title":"Second Fant"}]`},
		{"books/audible", "/api/search/books?provider=audible&title=Title&author=Writer", `[{"abridged":true,"asin":"B000000001","author":"Auth A, Auth B","cover":"https://img.test/a.jpg","description":"<p>Sum</p>","descriptionPlain":"Sum","duration":125,"explicit":true,"genres":["Mystery"],"isbn":"9781","language":"English","narrator":"Nar","publishedYear":"2022","publisher":"PubCo","rating":"4.5","region":"us","series":[{"sequence":"1.5","series":"Saga"},{"series":"Other"}],"subtitle":"Sub","tags":["Classic"],"title":"Audible One"},{"abridged":false,"asin":"B000000002","duration":30.5,"explicit":false,"region":"uk","title":"Audible Two"}]`},
		{"books/audible-asin", "/api/search/books?provider=audible&title=x&asin=b000000002&region=uk", `[{"abridged":false,"asin":"B000000002","duration":30.5,"explicit":false,"region":"uk","title":"Audible Two"}]`},
		{"books/custom-c1", "/api/search/books?provider=custom-c1&title=Title&author=Writer", `[{"asin":"B,C","author":"A,B","cover":"https://img.test/cover.jpg","description":"<p>Safe <a>link</a></p>","descriptionPlain":"Safe link","duration":12.5,"genres":["Mystery","Thriller"],"isbn":"123","language":"en","narrator":"N","publishedYear":"2001","publisher":"7","series":[{"sequence":"2","series":"Saga"},{"sequence":"3","series":"Other"}],"tags":["Classic"],"title":"42"},{"title":"Second"},{"duration":7,"title":"Third"}]`},
		{"books/empty-title", "/api/search/books?provider=google", `[]`},
		{"books/unknown-provider-is-google", "/api/search/books?provider=bogus&title=Title", `[{"author":"A One, B Two","cover":"https://i.test/l.jpg","description":"<b>Desc</b> text","descriptionPlain":"Desc text","genres":["Fiction","Fantasy"],"id":"g1","isbn":"9780000000001","publishedYear":"2020","publisher":"Pub","subtitle":"Sub","title":"Full Book"},{"cover":"https://i.test/z.jpg","id":"g2","isbn":"0000000002","title":"Sparse"}]`},
		{"podcast", "/api/search/podcast?term=pod&country=us", `[{"artistId":6,"artistName":"Host","cover":"https://a.test/p600.jpg","description":"<i>About</i> it","descriptionPlain":"About it","explicit":true,"feedUrl":"https://feed.test/rss","genres":["Tech","News"],"id":5,"pageUrl":"https://pod.test/5","releaseDate":"2021-01-02T00:00:00Z","title":"Pod","trackCount":42},{"artistId":null,"artistName":null,"cover":"https://a.test/600x600bb.jpg","description":"","descriptionPlain":"","explicit":false,"feedUrl":null,"genres":[],"id":7,"pageUrl":null,"releaseDate":null,"title":"Bare","trackCount":null}]`},
		{"covers/all", "/api/search/covers?title=Title&author=Writer&provider=all", `{"results":["https://i.test/l.jpg","https://i.test/z.jpg","https://a.test/600x600bb.jpg","https://a.test/600.jpg","https://covers.openlibrary.org/b/id/12-L.jpg","https://covers.openlibrary.org/b/id/34-L.jpg","https://covers.openlibrary.org/b/OLID/OL1M-L.jpg","https://fantlab.ru/img/e.jpg","https://c.test/1.png","https://c.test/2.png","https://img.test/a.jpg"]}`},
		{"covers/podcast", "/api/search/covers?title=Pod&podcast=1", `{"results":["https://a.test/p600.jpg","https://a.test/600x600bb.jpg"]}`},
		{"covers/custom", "/api/search/covers?title=Title&provider=custom-c1", `{"results":["https://img.test/cover.jpg"]}`},
		{"covers/empty-title", "/api/search/covers?title=", `{"results":null}`},
		{"authors", "/api/search/authors?q=Jane+Doe", `{"asin":"B0AUTHOR01","description":"Bio","image":"https://img.test/jane.jpg","name":"Jane Doe"}`},
		{"authors/none", "/api/search/authors?q=Zzzzzz", `null`},
		{"authors/empty", "/api/search/authors?q=", `null`},
		{"chapters", "/api/search/chapters?asin=b000000009", `{"asin":"B000000009","chapters":[{"lengthMs":1000,"startOffsetMs":0,"title":"One"}]}`},
		{"chapters/invalid", "/api/search/chapters?asin=bad", `{"error":"Invalid ASIN","stringKey":"MessageInvalidAsin"}`},
		{"chapters/unknown", "/api/search/chapters?asin=B000000008", `{"error":"Chapters not found","stringKey":"MessageChaptersNotFound"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := hn.do("user", http.MethodGet, c.path, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body)
			}
			var want any
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			if got := decode(t, w); !reflect.DeepEqual(want, got) {
				gotJSON, _ := json.MarshalIndent(got, "", " ")
				t.Errorf("body differs\nwant %s\ngot  %s", c.want, gotJSON)
			}
		})
	}
}

func TestSearchRouteErrors(t *testing.T) {
	hn := newHarness(t)
	itemID := "li-1"
	if _, err := hn.h.d.Exec(context.Background(), `INSERT INTO libraryItems(id,createdAt,updatedAt) VALUES(?,?,?)`, itemID, db.Now(), db.Now()); err != nil {
		t.Skipf("cannot seed a library item: %v", err)
	}
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"/api/search/podcast", 400, `Query parameter \"term\" is required`},
		{"/api/search/podcast?term=a&term=b", 400, `is an array`},
		{"/api/search/podcast?term=" + strings.Repeat("x", 1001), 400, `is too long`},
		{"/api/search/podcast?term=", 200, `null`},
		{"/api/search/authors", 400, `Query parameter \"q\" is required`},
		{"/api/search/covers", 400, `Missing required query parameter: title`},
		{"/api/search/books?title=Title&id=missing", 404, `library item \"missing\" not found`},
		{"/api/search/books?title=Title&id=" + itemID, 200, `Full Book`},
		{"/api/search/podcasts?term=a", 404, ``},
	} {
		w := hn.do("user", http.MethodGet, c.path, "")
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.body) {
			t.Errorf("%s = %d %s, want %d containing %q", c.path, w.Code, w.Body, c.status, c.body)
		}
	}
}

func TestCustomProviderCRUDPermissionsAndFallback(t *testing.T) {
	hn := newHarness(t)
	if _, err := hn.h.d.Exec(context.Background(), `DELETE FROM customMetadataProviders`); err != nil {
		t.Fatal(err)
	}
	if w := hn.do("user", "GET", "/api/custom-metadata-providers", ""); w.Code != http.StatusForbidden {
		t.Fatalf("user list status = %d", w.Code)
	}
	for body, want := range map[string]string{
		`{"name":"P","mediaType":"book","url":"not a url"}`:    "Invalid url",
		`{"name":"P","mediaType":"book"}`:                      "Invalid request body",
		`{"name":7,"mediaType":"book","url":"https://x.test"}`: "Invalid value",
	} {
		if w := hn.do("admin", "POST", "/api/custom-metadata-providers", body); w.Code != 400 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("POST %s = %d %q, want 400 %q", body, w.Code, w.Body, want)
		}
	}
	created := hn.do("root", "POST", "/api/custom-metadata-providers", `{"name":"Archive","mediaType":"book","url":"https://provider.test","authHeaderValue":"Bearer token","extraData":{"x":1}}`)
	if created.Code != 200 {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	provider := decode(t, created).(map[string]any)["provider"].(map[string]any)
	id, _ := provider["id"].(string)
	if id == "" || provider["url"] != "https://provider.test/" || provider["extraData"] == nil {
		t.Fatalf("created provider = %v", provider)
	}
	if _, err := hn.h.d.Exec(context.Background(), `INSERT INTO libraries(id,name,provider,createdAt,updatedAt) VALUES('lib-1','L',?,?,?)`, "custom-"+id, db.Now(), db.Now()); err != nil {
		t.Skipf("cannot seed a library: %v", err)
	}
	listed := decode(t, hn.do("admin", "GET", "/api/custom-metadata-providers", "")).(map[string]any)["providers"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["authHeaderValue"] != "Bearer token" {
		t.Fatalf("list = %v", listed)
	}
	if w := hn.do("admin", "DELETE", "/api/custom-metadata-providers/missing", ""); w.Code != 404 {
		t.Fatalf("delete missing = %d", w.Code)
	}
	if w := hn.do("admin", "DELETE", "/api/custom-metadata-providers/"+id, ""); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("delete = %d %q", w.Code, w.Body)
	}
	var fallback string
	if err := hn.h.d.Get(context.Background(), &fallback, `SELECT provider FROM libraries WHERE id = 'lib-1'`); err != nil || fallback != "google" {
		t.Fatalf("library provider = %q (%v), want google", fallback, err)
	}
	if want := []string{"*:custom_metadata_provider_added", "*:custom_metadata_provider_removed"}; !slices.Equal(hn.events, want) {
		t.Fatalf("events = %v, want %v", hn.events, want)
	}
}

// Stored SQLite datetimes reach clients as RFC 3339 UTC (upstream's toJSON).
func TestCustomProviderListReportsRFC3339Timestamps(t *testing.T) {
	hn := newHarness(t)
	for stored, want := range map[string]string{
		"2024-01-01 00:00:00.123 +00:00": "2024-01-01T00:00:00.123Z",
		"2024-01-01 02:00:00.5 +02:00":   "2024-01-01T00:00:00.5Z",
		"2024-01-01T00:00:00Z":           "2024-01-01T00:00:00Z",
		"2024-01-01 00:00:00 +00:00":     "2024-01-01T00:00:00Z",
	} {
		if _, err := hn.h.d.Exec(context.Background(), `DELETE FROM customMetadataProviders`); err != nil {
			t.Fatal(err)
		}
		if _, err := hn.h.d.Exec(context.Background(), `INSERT INTO customMetadataProviders(id,name,mediaType,url,createdAt,updatedAt) VALUES('a','n','book','http://x.test',?,?)`, stored, stored); err != nil {
			t.Fatal(err)
		}
		list := decode(t, hn.do("admin", "GET", "/api/custom-metadata-providers", "")).(map[string]any)["providers"].([]any)
		row := list[0].(map[string]any)
		if row["createdAt"] != want || row["updatedAt"] != want {
			t.Errorf("stored %q -> %v / %v, want %q", stored, row["createdAt"], row["updatedAt"], want)
		}
	}
}

func TestCustomSearchMalformedIsAnError(t *testing.T) {
	for _, body := range []string{`{}`, `{"matches":null}`, `not-json`, `{"matches":["junk"]}`} {
		t.Run(body, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer remote.Close()
			hn := newHarness(t)
			insertCustom(t, hn.h.d, "m1", "M", "book", remote.URL, "")
			if results, err := hn.h.searchCustom(context.Background(), "custom-m1", "book", BookQuery{Title: "t"}); err == nil {
				t.Fatalf("malformed response %q accepted: %#v", body, results)
			}
		})
	}
	hn := newHarness(t)
	if _, err := hn.h.searchCustom(context.Background(), "custom-nope", "book", BookQuery{Title: "t"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestCustomSearchSendsQueryAndAuthorization(t *testing.T) {
	hn := newHarness(t)
	results, err := hn.h.searchCustom(context.Background(), "custom-c1", "book", BookQuery{Title: "The Book", Author: "A Writer", ISBN: "123"})
	if err != nil || len(results) != 3 {
		t.Fatalf("results = %d, err = %v", len(results), err)
	}
	want := "GET /custom/search?author=A+Writer&isbn=123&mediaType=book&query=The+Book"
	if got := hn.fake.requests(); !slices.Equal(got, []string{want}) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	// Without the Authorization header the fake answers 401: no matches.
	insertCustom(t, hn.h.d, "c3", "NoAuth", "book", hn.fake.URL+"/custom", "")
	if results, err := hn.h.searchCustom(context.Background(), "custom-c3", "book", BookQuery{Title: "t"}); err != nil || len(results) != 0 {
		t.Fatalf("unauthorized provider: %d results, err %v", len(results), err)
	}
}

// A custom provider URL is admin-set, so its requests go through the
// SSRF-guarded client: a loopback provider is never reached.
func TestCustomProvidersAreSSRFGuarded(t *testing.T) {
	for _, name := range []string{"DISABLE_SSRF_REQUEST_FILTER", "SSRF_REQUEST_FILTER_WHITELIST", "EXP_PROXY_SUPPORT"} {
		t.Setenv(name, "")
	}
	fake := newFake(t)
	h := New(dbtest.New(t), nil)
	insertCustom(t, h.d, "c1", "Local", "book", fake.URL+"/custom", "Bearer secret")
	results, err := h.searchCustom(context.Background(), "custom-c1", "book", BookQuery{Title: "t"})
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %d, err = %v; want an empty answer", len(results), err)
	}
	if got := fake.requests(); len(got) != 0 {
		t.Fatalf("the loopback provider was reached: %q", got)
	}
}

func TestSearchBooksFuzzyFallback(t *testing.T) {
	hn := newHarness(t)
	ctx := context.Background()
	q := BookQuery{Provider: "google", Title: "The Hobbit: An Unexpected Journey [mp3]", Author: "Tolkien", MaxFuzzy: 3}
	got := hn.h.SearchBooks(ctx, q)
	if len(got) != 1 || got[0].Title != "The Hobbit" || got[0].Description != "<b>b</b>" || got[0].DescriptionPlain != "b" {
		t.Fatalf("results = %+v", got)
	}
	var first string
	for _, request := range hn.fake.requests() {
		if strings.Contains(request, "/google/") && strings.Contains(request, "mp3") {
			first = request
		}
	}
	if !strings.Contains(first, "intitle%3AThe+Hobbit%3A+An+Unexpected+Journey+%5Bmp3%5D+inauthor%3ATolkien") {
		t.Errorf("the verbatim search was not made first: %q", hn.fake.requests())
	}
	if none := hn.h.SearchBooks(ctx, BookQuery{Provider: "google", Title: ""}); len(none) != 0 {
		t.Errorf("empty title found %v", none)
	}
	if none := hn.h.SearchBooks(ctx, BookQuery{Provider: "google", Title: "zzz hobbit", MaxFuzzy: 0}); len(none) != 0 {
		t.Errorf("no fuzzy searches allowed: %v", none)
	}
}

func TestSearchBooksOrdersAudibleByDuration(t *testing.T) {
	hn := newHarness(t)
	asins := func(duration float64) []string {
		var out []string
		for _, book := range hn.h.SearchBooks(context.Background(), BookQuery{Provider: "audible", Title: "Title", Duration: duration}) {
			out = append(out, book.ASIN)
		}
		return out
	}
	if got, want := asins(31*60), []string{"B000000002", "B000000001"}; !slices.Equal(got, want) {
		t.Errorf("near 31 minutes: %v, want %v", got, want)
	}
	if got, want := asins(0), []string{"B000000001", "B000000002"}; !slices.Equal(got, want) {
		t.Errorf("no duration keeps the provider order: %v, want %v", got, want)
	}
}

func TestSearchBooksFiltersOpenLibraryByDistance(t *testing.T) {
	hn := newHarness(t)
	got := hn.h.SearchBooks(context.Background(), BookQuery{Provider: "openlibrary", Title: "The Hobbit", Author: "Tolkien"})
	if len(got) != 1 || got[0].Title != "The Hobbit" { // "Second" has no author, so the author filter drops it
		t.Fatalf("results = %+v", got)
	}
}

func TestFindersReturnTypedResults(t *testing.T) {
	hn := newHarness(t)
	ctx := context.Background()
	if p := hn.h.SearchPodcasts(ctx, "pod"); len(p) != 2 || *p[0].FeedURL != "https://feed.test/rss" {
		t.Errorf("podcasts = %+v", p)
	}
	if a := hn.h.FindAuthorByName(ctx, "Jane Doe", ""); a == nil || a.ASIN != "B0AUTHOR01" {
		t.Errorf("author by name = %+v", a)
	}
	if a := hn.h.FindAuthorByASIN(ctx, "b0author01", ""); a == nil || a.Name != "Jane Doe" {
		t.Errorf("author by ASIN = %+v", a)
	}
	if a := hn.h.FindAuthorByASIN(ctx, "bad", ""); a != nil {
		t.Errorf("invalid ASIN found %+v", a)
	}
}

// collect runs a socket cover search and returns its events, sorted.
func collect(t *testing.T, h *Handler, req CoverRequest) []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	h.StartCoverSearch(req, func(event string, payload any) {
		raw, _ := json.Marshal(payload)
		mu.Lock()
		got = append(got, event+" "+string(raw))
		mu.Unlock()
		if event == "cover_search_complete" {
			close(done)
		}
	})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cover search did not complete")
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(got)
	return got
}

func TestCoverSearchSocket(t *testing.T) {
	hn := newHarness(t)
	insertCustom(t, hn.h.d, "bad", "Bad", "book", hn.fake.URL+"/badcustom", "")
	got := collect(t, hn.h, CoverRequest{RequestID: "r1", Title: "Title", Provider: "best"})
	want := []string{
		`cover_search_complete {"requestId":"r1"}`,
		`cover_search_result {"requestId":"r1","provider":"audible","covers":["https://img.test/a.jpg"],"total":1}`,
		`cover_search_result {"requestId":"r1","provider":"fantlab","covers":["https://fantlab.ru/img/e.jpg"],"total":1}`,
		`cover_search_result {"requestId":"r1","provider":"google","covers":["https://i.test/l.jpg","https://i.test/z.jpg"],"total":2}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("best:\n got %q\nwant %q", got, want)
	}
	got = collect(t, hn.h, CoverRequest{RequestID: "r2", Title: "Title", Provider: "custom-bad"})
	want = []string{
		`cover_search_complete {"requestId":"r2"}`,
		`cover_search_provider_error {"requestId":"r2","provider":"custom-bad","error":"Custom provider returned malformed response"}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("custom error:\n got %q\nwant %q", got, want)
	}
	got = collect(t, hn.h, CoverRequest{RequestID: "r3", Title: "Pod", Podcast: true})
	if len(got) != 2 || !strings.HasPrefix(got[1], `cover_search_result {"requestId":"r3","provider":"itunes"`) {
		t.Errorf("podcast: %q", got)
	}
}

func TestCancelCoverSearch(t *testing.T) {
	hn := newHarness(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	insertCustom(t, hn.h.d, "slow", "Slow", "book", slow.URL, "")
	var mu sync.Mutex
	var events []string
	reply := func(event string, _ any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	hn.h.StartCoverSearch(CoverRequest{RequestID: "r1", Title: "t", Provider: "custom-slow"}, reply)
	time.Sleep(100 * time.Millisecond)
	hn.h.CancelCoverSearch("r1", reply)
	hn.h.CancelCoverSearch("r1", reply) // already stopped: silent
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(events, []string{"cover_search_cancelled"}) {
		t.Errorf("events = %v, want only cover_search_cancelled", events)
	}
}

func TestCleanForCompares(t *testing.T) {
	for in, want := range map[string]string{
		"Ender's Game (Ender's Saga): A Novel": "enders game",
		"  Café   Society ":                    "cafe society",
		"Title - Subtitle":                     "title",
	} {
		if got := cleanTitleForCompares(in); got != want {
			t.Errorf("title %q = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"J.R.R. Tolkien":          "j. r.r. tolkien",
		"John Q. Smith":           "john smith",
		"Jane Doe et al.":         "jane doe",
		"Ursula K. Le Guin":       "ursula le guin",
		"Isaac Asimov":            "isaac asimov",
		"Al B. Cee Dee":           "al cee dee",
		"Ab C. De F. Gh":          "ab de gh",
		"Robert A. Heinlein, Jr.": "robert heinlein, jr.",
	} {
		if got := cleanAuthorForCompares(in); got != want {
			t.Errorf("author %q = %q, want %q", in, got, want)
		}
	}
}

func TestTitleCandidatesOrdering(t *testing.T) {
	got := titleCandidates("", []string{"the hobbit: an unexpected journey", "12"})
	if len(got) != 2 || got[0] != "the hobbit" || got[1] != "12" {
		t.Fatalf("candidates %v: want the hobbit first and the digits-only candidate last", got)
	}
}
