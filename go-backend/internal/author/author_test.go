package author

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/metadata"
	"audiobookshelf-compat/internal/settings"
)

type event struct {
	Name    string
	Payload any
}

// finder is a fake provider: lookups by asin or by name.
type finder struct {
	byASIN, byName map[string]*metadata.AuthorResult
	calls          []string
}

func (f *finder) FindAuthorByASIN(_ context.Context, asin, region string) *metadata.AuthorResult {
	f.calls = append(f.calls, "asin:"+asin+":"+region)
	return f.byASIN[strings.ToUpper(asin)]
}

func (f *finder) FindAuthorByName(_ context.Context, name, region string) *metadata.AuthorResult {
	f.calls = append(f.calls, "name:"+name+":"+region)
	return f.byName[name]
}

// rig serves the author routes over a copy of the upstream fixture: library
// "Audiobooks" with Ada Example (two single books), Terry Fixture (two books
// of the series Long Saga) and Zed Disposable (one book).
type rig struct {
	t       *testing.T
	d       *db.DB
	mux     *http.ServeMux
	meta    string
	finder  *finder
	mu      sync.Mutex
	events  []event
	touched []string
}

const (
	ada, terry, zed = "225b0a63-df12-4a28-9f49-8d86c4796d69", "75e57a0b-a39d-4073-8501-e3b745ebd79d", "c4528461-64d4-4cc8-988a-713cde5b4f37"
	zedItem         = "da2e4ecd-d9cc-44be-bac7-6b30992d1c0e"
)

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	r := &rig{t: t, d: dbtest.Fixture(t), meta: t.TempDir(), finder: &finder{}, mux: http.NewServeMux()}
	st, err := settings.NewStore(context.Background(), r.d, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	emit := func(_, name string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, event{name, payload})
	}
	New(r.d, st, emit, Deps{
		Filters: filterdata.NewCache(), Authors: r.finder,
		WriteMetadata: func(_ context.Context, id string) error { r.touched = append(r.touched, id); return nil },
		ImageDir:      filepath.Join(r.meta, "authors"), ImageCacheDir: filepath.Join(r.meta, "cache", "images"),
	}).Routes(r.mux)
	return r
}

func (r *rig) user(where string, edit func(*db.Permissions)) *auth.Principal {
	r.t.Helper()
	var u db.User
	if err := r.d.Get(context.Background(), &u, `SELECT * FROM users WHERE `+where); err != nil {
		r.t.Fatal(err)
	}
	if edit != nil {
		edit(&u.Permissions.V)
	}
	return auth.NewPrincipal(u)
}

func (r *rig) root() *auth.Principal   { return r.user(`type='root'`, nil) }
func (r *rig) reader() *auth.Principal { return r.user(`username='trial-listener'`, nil) }

func (r *rig) do(p *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if p != nil {
		req = req.WithContext(auth.WithUser(req.Context(), p))
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *rig) take() []event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func (r *rig) names() string {
	var names []string
	for _, e := range r.take() {
		names = append(names, e.Name)
	}
	return strings.Join(names, ",")
}

func (r *rig) author(id string) db.Author {
	r.t.Helper()
	var a db.Author
	if err := r.d.Get(context.Background(), &a, `SELECT * FROM authors WHERE id = ?`, id); err != nil {
		r.t.Fatal(err)
	}
	return a
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.d.Get(context.Background(), &n, query, args...); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	return v
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestGetDetail(t *testing.T) {
	r := newRig(t)
	plain := decode[map[string]any](t, r.do(r.root(), "GET", "/api/authors/"+terry, ""))
	if plain["name"] != "Terry Fixture" || plain["numBooks"] != nil || plain["libraryItems"] != nil || plain["series"] != nil {
		t.Fatalf("plain detail %v", plain)
	}
	if got := decode[map[string]any](t, r.do(r.root(), "GET", "/api/authors/"+terry+"?include=series", "")); got["series"] != nil {
		t.Fatal("include=series alone adds series")
	}
	full := decode[struct {
		LibraryItems []struct {
			ID string `json:"id"`
		} `json:"libraryItems"`
		Series []struct {
			Name  string `json:"name"`
			Items []struct {
				Media struct {
					Metadata struct {
						Series struct {
							Sequence string `json:"sequence"`
						} `json:"series"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"items"`
		} `json:"series"`
	}](t, r.do(r.root(), "GET", "/api/authors/"+terry+"?include=items,series", ""))
	if len(full.LibraryItems) != 2 || len(full.Series) != 1 || full.Series[0].Name != "Long Saga" || len(full.Series[0].Items) != 2 {
		t.Fatalf("detail %+v", full)
	}
	if full.Series[0].Items[0].Media.Metadata.Series.Sequence != "1" || full.Series[0].Items[1].Media.Metadata.Series.Sequence != "2" {
		t.Fatalf("series items are not sorted by sequence: %+v", full.Series[0].Items)
	}
	none := decode[struct {
		Series *[]any `json:"series"`
	}](t, r.do(r.root(), "GET", "/api/authors/"+ada+"?include=items,series", ""))
	if none.Series == nil || len(*none.Series) != 0 {
		t.Fatalf("an author without series answers series: [], got %v", none.Series)
	}
}

func TestGetAccess(t *testing.T) {
	r := newRig(t)
	if w := r.do(r.root(), "GET", "/api/authors/nope", ""); w.Code != 404 || w.Body.String() != "Not Found" {
		t.Fatalf("missing author: %d %q", w.Code, w.Body)
	}
	elsewhere := r.user(`type='root'`, func(p *db.Permissions) { p.AccessAllLibraries, p.LibrariesAccessible = false, []string{"other"} })
	if w := r.do(elsewhere, "GET", "/api/authors/"+ada, ""); w.Code != 404 {
		t.Fatalf("inaccessible library: %d", w.Code)
	}
	if w := r.do(nil, "GET", "/api/authors/"+ada, ""); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if _, err := r.d.Exec(context.Background(), `UPDATE books SET explicit=1`); err != nil {
		t.Fatal(err)
	}
	noExplicit := r.user(`type='root'`, func(p *db.Permissions) { p.AccessExplicitContent = false })
	got := decode[struct {
		LibraryItems []any `json:"libraryItems"`
	}](t, r.do(noExplicit, "GET", "/api/authors/"+ada+"?include=items", ""))
	if len(got.LibraryItems) != 0 {
		t.Fatalf("explicit items visible: %d", len(got.LibraryItems))
	}
}

func TestUpdateValidationAndPermissions(t *testing.T) {
	r := newRig(t)
	if w := r.do(r.reader(), "PATCH", "/api/authors/"+ada, `{"description":"x"}`); w.Code != 403 {
		t.Fatalf("no update permission: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"name":5,"description":{},"other":"x"}`} {
		w := r.do(r.root(), "PATCH", "/api/authors/"+ada, body)
		if w.Code != 400 || w.Body.String() != "Invalid request payload. No valid keys found" {
			t.Fatalf("body %s: %d %q", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`[1]`, `{`, ``} {
		if w := r.do(r.root(), "PATCH", "/api/authors/"+ada, body); w.Code != 400 {
			t.Fatalf("body %q: %d", body, w.Code)
		}
	}
}

func TestUpdateSetClearAndNoChange(t *testing.T) {
	r := newRig(t)
	body := `{"description":"Bio","asin":"B000000001"}`
	first := decode[struct {
		Author  View  `json:"author"`
		Updated *bool `json:"updated"`
		Merged  *bool `json:"merged"`
	}](t, r.do(r.root(), "PATCH", "/api/authors/"+ada, body))
	if first.Updated == nil || !*first.Updated || first.Merged != nil || db.Val(first.Author.Description) != "Bio" || first.Author.NumBooks != nil {
		t.Fatalf("first update %+v", first)
	}
	events := r.take()
	if len(events) != 1 || events[0].Name != "author_updated" {
		t.Fatalf("events %+v", events)
	}
	if v, ok := events[0].Payload.(View); !ok || v.NumBooks == nil || *v.NumBooks != 2 {
		t.Fatalf("payload %+v", events[0].Payload)
	}
	if second := decode[struct{ Updated bool }](t, r.do(r.root(), "PATCH", "/api/authors/"+ada, body)); second.Updated || len(r.take()) != 0 {
		t.Fatalf("a repeated update reports a change: %+v", second)
	}
	// null clears a column; "" stores an empty text (upstream does not turn it into NULL).
	r.do(r.root(), "PATCH", "/api/authors/"+ada, `{"asin":null,"description":""}`)
	if a := r.author(ada); a.ASIN != nil || a.Description == nil || *a.Description != "" {
		t.Fatalf("asin %v description %v", a.ASIN, a.Description)
	}
}

func TestRenameWritesLastFirstAndNotifies(t *testing.T) {
	r := newRig(t)
	if w := r.do(r.root(), "PATCH", "/api/authors/"+zed, `{"name":"ludwig van beethoven"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := db.Val(r.author(zed).LastFirst); got != "van Beethoven, Ludwig" {
		t.Fatalf("lastFirst %q", got)
	}
	if !slices.Equal(r.touched, []string{zedItem}) {
		t.Fatalf("metadata files written for %v", r.touched)
	}
	events := r.take()
	if len(events) != 2 || events[0].Name != "items_updated" || events[1].Name != "author_updated" {
		t.Fatalf("events %+v", events)
	}
}

func TestRenameOntoExistingAuthorMerges(t *testing.T) {
	r := newRig(t)
	w := r.do(r.root(), "PATCH", "/api/authors/"+zed, `{"name":"Ada Example"}`)
	body := decode[map[string]any](t, w)
	author, _ := body["author"].(map[string]any)
	if w.Code != 200 || body["merged"] != true || body["updated"] != nil || author["id"] != ada || author["numBooks"] != nil {
		t.Fatalf("merge response %d %s", w.Code, w.Body)
	}
	if r.count(`SELECT COUNT(*) FROM authors WHERE id = ?`, zed) != 0 || r.count(`SELECT COUNT(*) FROM bookAuthors WHERE authorId = ?`, ada) != 3 {
		t.Fatal("books did not move to the existing author")
	}
	if !slices.Equal(r.touched, []string{zedItem}) {
		t.Fatalf("metadata files %v", r.touched)
	}
	events := r.take()
	names := []string{events[0].Name, events[1].Name, events[2].Name}
	if len(events) != 3 || !slices.Equal(names, []string{"items_updated", "author_removed", "author_updated"}) {
		t.Fatalf("events %+v", events)
	}
	if removed := events[1].Payload.(View); removed.ID != zed || removed.NumBooks != nil {
		t.Fatalf("author_removed %+v", removed)
	}
	if updated := events[2].Payload.(View); updated.ID != ada || *updated.NumBooks != 3 {
		t.Fatalf("author_updated %+v", updated)
	}
}

func TestDeletePurgesCacheAndKeepsImageFile(t *testing.T) {
	r := newRig(t)
	cache := filepath.Join(r.meta, "cache", "images")
	image := filepath.Join(r.meta, "authors", zed+".jpg")
	for _, f := range []string{image, filepath.Join(cache, zed+"_400.jpeg"), filepath.Join(cache, zed+"_100x50.png"), filepath.Join(cache, ada+"_400.jpeg")} {
		write(t, f, "x")
	}
	if _, err := r.d.Exec(context.Background(), `UPDATE authors SET imagePath=? WHERE id=?`, image, zed); err != nil {
		t.Fatal(err)
	}
	if w := r.do(r.reader(), "DELETE", "/api/authors/"+zed, ""); w.Code != 403 {
		t.Fatalf("no delete permission: %d", w.Code)
	}
	if w := r.do(r.root(), "DELETE", "/api/authors/"+zed, ""); w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if exists(filepath.Join(cache, zed+"_400.jpeg")) || !exists(filepath.Join(cache, ada+"_400.jpeg")) || !exists(image) {
		t.Fatal("cache purge or image file kept wrong")
	}
	if names := r.names(); names != "author_removed" || !slices.Equal(r.touched, []string{zedItem}) {
		t.Fatalf("events %s, metadata %v", names, r.touched)
	}
	if w := r.do(r.root(), "DELETE", "/api/authors/"+zed, ""); w.Code != 404 {
		t.Fatalf("second delete %d", w.Code)
	}
}

func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfake"))
		case "/a.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpegbytes"))
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "/icon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestImageUploadAndDelete(t *testing.T) {
	r := newRig(t)
	server := imageServer(t)
	post := func(p *auth.Principal, body string) *httptest.ResponseRecorder {
		return r.do(p, "POST", "/api/authors/"+zed+"/image", body)
	}
	good := `{"url":"` + server.URL + `/a.jpg"}`
	if w := post(r.reader(), good); w.Code != 403 {
		t.Fatalf("no update: %d", w.Code)
	}
	if w := post(r.user(`type='root'`, func(p *db.Permissions) { p.Upload = false }), good); w.Code != 403 {
		t.Fatalf("no upload permission: %d", w.Code)
	}
	for body, message := range map[string]string{
		`{}`:                                    "Invalid request payload. 'url' not in request body",
		`{"url":""}`:                            "Invalid request payload. 'url' not in request body",
		`{"url":"ftp://x/a.jpg"}`:               `Invalid request payload. Invalid url "ftp://x/a.jpg"`,
		`{"url":5}`:                             `Invalid request payload. Invalid url "5"`,
		`{"url":"` + server.URL + `/page"}`:     `Invalid content type "text/html; charset=utf-8"`,
		`{"url":"` + server.URL + `/icon.svg"}`: `Invalid content type "image/svg+xml"`,
		`{"url":"` + server.URL + `/no.jpg"}`:   "Request failed with status code 404",
	} {
		if w := post(r.root(), body); w.Code != 400 || w.Body.String() != message {
			t.Errorf("body %s: %d %q, want 400 %q", body, w.Code, w.Body, message)
		}
	}
	if len(r.take()) != 0 {
		t.Fatal("failed uploads emitted events")
	}
	cached := filepath.Join(r.meta, "cache", "images", zed+"_400.jpeg")
	write(t, cached, "cached")
	if _, err := r.d.Exec(context.Background(), `UPDATE authors SET imagePath='/old/image.jpg' WHERE id=?`, zed); err != nil {
		t.Fatal(err)
	}
	w := post(r.root(), `{"url":"`+server.URL+`/a.png"}`)
	want := filepath.Join(r.meta, "authors", zed+".png")
	if got := decode[struct{ Author View }](t, w); w.Code != 200 || db.Val(got.Author.ImagePath) != want {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if data, _ := os.ReadFile(want); !strings.HasPrefix(string(data), "\x89PNG") || exists(cached) {
		t.Fatalf("file %q, cache kept %v", data, exists(cached))
	}
	if r.names() != "author_updated" {
		t.Fatal("upload did not announce the author")
	}
	// A url that does not end in .png is stored as .jpg.
	if w := post(r.root(), `{"url":"`+server.URL+`/a.jpg?x=png"}`); w.Code != 200 || !exists(filepath.Join(r.meta, "authors", zed+".jpg")) {
		t.Fatalf("jpg upload %d %s", w.Code, w.Body)
	}
	if w := r.do(r.reader(), "DELETE", "/api/authors/"+zed+"/image", ""); w.Code != 403 {
		t.Fatalf("delete image permission %d", w.Code)
	}
	w = r.do(r.root(), "DELETE", "/api/authors/"+zed+"/image", "")
	if got := decode[struct{ Author View }](t, w); w.Code != 200 || got.Author.ImagePath != nil || exists(filepath.Join(r.meta, "authors", zed+".jpg")) {
		t.Fatalf("delete image: %d %s", w.Code, w.Body)
	}
	if w := r.do(r.root(), "DELETE", "/api/authors/"+zed+"/image", ""); w.Code != 400 || w.Body.String() != "Author has no image path set" {
		t.Fatalf("second delete: %d %q", w.Code, w.Body)
	}
}

func TestImageRead(t *testing.T) {
	r := newRig(t)
	get := func(target string) *httptest.ResponseRecorder { return r.do(nil, "GET", target, "") }
	for target, code := range map[string]int{
		"/api/authors/" + ada + "/image":                          404, // no image
		"/api/authors/22222222-2222-4333-8444-555555555555/image": 404,
		"/api/authors/" + ada + "/image?raw=1":                    404,
		"/api/authors/" + ada + "/image?format=gif":               400,
		"/api/authors/not-a-uuid/image":                           400, // fails the cache name validation
		"/api/authors/not-a-uuid/image?raw=1":                     404, // just an unknown author
	} {
		if w := get(target); w.Code != code {
			t.Errorf("%s: %d, want %d", target, w.Code, code)
		}
	}
	image := filepath.Join(r.meta, "authors", ada+".jpg")
	write(t, image, "raw image bytes")
	if _, err := r.d.Exec(context.Background(), `UPDATE authors SET imagePath=? WHERE id=?`, image, ada); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/authors/" + ada + "/image?raw=1"); w.Code != 200 || w.Body.String() != "raw image bytes" {
		t.Fatalf("raw: %d %q", w.Code, w.Body)
	}
	// A cached resize is served as is, without ffmpeg.
	write(t, filepath.Join(r.meta, "cache", "images", ada+"_200x80.jpeg"), "resized")
	w := get("/api/authors/" + ada + "/image?width=200&height=80&format=jpeg")
	if w.Code != 200 || w.Body.String() != "resized" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("cache hit: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
}

func TestMatch(t *testing.T) {
	server := imageServer(t)
	r := newRig(t)
	img := server.URL + "/a.png"
	r.finder.byASIN = map[string]*metadata.AuthorResult{"B000APZR3Q": {ASIN: "B000APZR3Q", Name: "Ada", Description: "From Audnexus", Image: &img}}
	r.finder.byName = map[string]*metadata.AuthorResult{"Ada": {ASIN: "B000APZR3Q", Description: "By name"}}
	url := "/api/authors/" + ada + "/match"
	if w := r.do(r.reader(), "POST", url, `{"q":"Ada"}`); w.Code != 403 {
		t.Fatalf("no update: %d", w.Code)
	}
	if w := r.do(r.root(), "POST", url, `{}`); w.Code != 404 || w.Body.String() != "Author not found" {
		t.Fatalf("empty: %d %q", w.Code, w.Body)
	}
	w := r.do(r.root(), "POST", url, `{"asin":"b000apzr3q","region":"uk"}`)
	got := decode[struct {
		Updated bool `json:"updated"`
		Author  View `json:"author"`
	}](t, w)
	want := filepath.Join(r.meta, "authors", ada+".png")
	if !slices.Equal(r.finder.calls, []string{"name::us", "asin:b000apzr3q:uk"}) || !got.Updated || db.Val(got.Author.ASIN) != "B000APZR3Q" || db.Val(got.Author.ImagePath) != want || db.Val(got.Author.Description) != "From Audnexus" {
		t.Fatalf("calls %v, response %s", r.finder.calls, w.Body)
	}
	if r.names() != "author_updated" {
		t.Fatal("match did not announce the author")
	}
	// Matching again changes nothing; an invalid asin falls back to the name.
	r.finder.calls = nil
	if again := decode[struct{ Updated bool }](t, r.do(r.root(), "POST", url, `{"asin":"B000APZR3Q"}`)); again.Updated || len(r.take()) != 0 {
		t.Fatal("second match updated")
	}
	r.do(r.root(), "POST", url, `{"asin":"bad","q":"Ada"}`)
	if !slices.Equal(r.finder.calls, []string{"asin:B000APZR3Q:us", "name:Ada:us"}) {
		t.Fatalf("calls %v", r.finder.calls)
	}
}

func TestList(t *testing.T) {
	r := newRig(t)
	library := r.author(ada).LibraryID
	if _, err := r.d.Exec(context.Background(), `INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES('bare','No Books','Books, No',?,'2024-01-01 00:00:00.000 +00:00','2024-01-01 00:00:00.000 +00:00')`, library); err != nil {
		t.Fatal(err)
	}
	counts := func(p *auth.Principal) map[string]int {
		listed, err := List(context.Background(), r.d, p, *library)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, a := range listed {
			out[db.Val(a.Name)] = *a.NumBooks
		}
		return out
	}
	if got := counts(r.root()); len(got) != 4 || got["Terry Fixture"] != 2 || got["No Books"] != 0 {
		t.Fatalf("admin list %v", got)
	}
	if got := counts(r.reader()); len(got) != 3 || got["Ada Example"] != 2 {
		t.Fatalf("user list %v", got)
	}
}
