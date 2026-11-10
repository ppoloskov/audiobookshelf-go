package people

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

type recordedEvent struct {
	event   string
	payload any
}

type authorRig struct {
	t       *testing.T
	db      *sql.DB
	h       *Handler
	mux     *http.ServeMux
	meta    string
	mu      sync.Mutex
	events  []recordedEvent
	touched []string
}

type fakeFinder struct {
	byASIN map[string]*wire.AuthorMatch
	byName map[string]*wire.AuthorMatch
	calls  []string
}

func (f *fakeFinder) FindAuthorByASIN(_ context.Context, asin, region string) *wire.AuthorMatch {
	f.calls = append(f.calls, "asin:"+asin+":"+region)
	return f.byASIN[strings.ToUpper(asin)] // Audnexus.authorRequest uppercases it
}

func (f *fakeFinder) FindAuthorByName(_ context.Context, name, region string) *wire.AuthorMatch {
	f.calls = append(f.calls, "name:"+name+":"+region)
	return f.byName[name]
}

var adminPermissions = json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true,"upload":true}`)

func newAuthorRig(t *testing.T, finder AuthorFinder) *authorRig {
	t.Helper()
	db := peopleFixture(t)
	t.Cleanup(func() { db.Close() })
	rig := &authorRig{t: t, db: db, meta: t.TempDir()}
	emit := func(_, event string, payload any) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.events = append(rig.events, recordedEvent{event, payload})
	}
	writeMetadata := func(_ context.Context, itemID string) error {
		rig.touched = append(rig.touched, itemID)
		return nil
	}
	rig.h = New(db, config.Config{MetadataPath: rig.meta}, emit, Deps{
		Filters: filterdata.NewCache(), Authors: finder, WriteMetadata: writeMetadata,
	})
	rig.mux = http.NewServeMux()
	rig.h.Routes(rig.mux)
	for _, q := range []string{
		`INSERT INTO authors VALUES ('a2','Ben Writer','Writer, Ben',NULL,NULL,NULL,'l1','2024-01-03T00:00:00Z','2024-01-03T00:00:00Z')`,
		`INSERT INTO books VALUES ('b2','Second',NULL,NULL,'2025',NULL,NULL,NULL,NULL,NULL,'en',0,0,NULL,5,'[]','[]','null','[]','[]','[]','2024-01-02T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('i2','ino-2','/media/books/Second','Second','b2','book',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',10,NULL,NULL,'[]','{}','Second','Second','Ben Writer','Writer, Ben','l1','f1','2024-01-02T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO bookAuthors VALUES ('ba2','b2','a2','2024-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (r *authorRig) do(method, target, body string, user store.User) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(store.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *authorRig) takeEvents() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func rootUser() store.User {
	return store.User{ID: "u1", Username: "root", Type: "root", IsActive: true, Permissions: adminPermissions}
}

func readOnlyUser() store.User {
	return store.User{ID: "u2", Username: "reader", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
}

func decodeAuthorBody(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
}

func TestAuthorFindOneItemsAndSeries(t *testing.T) {
	rig := newAuthorRig(t, nil)
	w := rig.do("GET", "/api/authors/a1?include=items,series", "", rootUser())
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	var body struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		AddedAt      int64  `json:"addedAt"`
		NumBooks     *int   `json:"numBooks"`
		LibraryItems []struct {
			ID    string `json:"id"`
			Media struct {
				Metadata struct {
					Series struct {
						ID       string  `json:"id"`
						Sequence *string `json:"sequence"`
						Prefix   *string `json:"nameIgnorePrefix"`
					} `json:"series"`
				} `json:"metadata"`
			} `json:"media"`
		} `json:"libraryItems"`
		Series []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Items []struct {
				ID    string `json:"id"`
				Media struct {
					Metadata struct {
						Series struct {
							ID       string  `json:"id"`
							Sequence *string `json:"sequence"`
							Prefix   *string `json:"nameIgnorePrefix"`
						} `json:"series"`
					} `json:"metadata"`
				} `json:"media"`
			} `json:"items"`
		} `json:"series"`
	}
	decodeAuthorBody(t, w, &body)
	if body.ID != "a1" || body.Name != "Ada Author" || body.AddedAt != 1704067200000 || body.NumBooks != nil {
		t.Fatalf("author %+v", body)
	}
	if len(body.LibraryItems) != 1 || body.LibraryItems[0].ID != "i1" {
		t.Fatalf("items %+v", body.LibraryItems)
	}
	if body.LibraryItems[0].Media.Metadata.Series.ID != "" {
		t.Fatalf("plain library items carry no metadata.series: %+v", body.LibraryItems[0].Media.Metadata.Series)
	}
	if len(body.Series) != 1 || body.Series[0].ID != "s1" || len(body.Series[0].Items) != 1 {
		t.Fatalf("series %+v", body.Series)
	}
	got := body.Series[0].Items[0].Media.Metadata.Series
	if got.ID != "s1" || got.Sequence == nil || *got.Sequence != "1" || got.Prefix == nil || *got.Prefix != "First Series" {
		t.Fatalf("series ref %+v", got)
	}

	// No include: neither key. include=series alone does not add series.
	for _, query := range []string{"", "?include=series"} {
		var plain map[string]json.RawMessage
		decodeAuthorBody(t, rig.do("GET", "/api/authors/a1"+query, "", rootUser()), &plain)
		if _, ok := plain["libraryItems"]; ok {
			t.Fatalf("query %q: libraryItems present", query)
		}
		if _, ok := plain["series"]; ok {
			t.Fatalf("query %q: series present", query)
		}
	}
	// include=items without series has libraryItems and no series key.
	var itemsOnly map[string]json.RawMessage
	decodeAuthorBody(t, rig.do("GET", "/api/authors/a2?include=items", "", rootUser()), &itemsOnly)
	if _, ok := itemsOnly["libraryItems"]; !ok {
		t.Fatal("libraryItems missing")
	}
	if _, ok := itemsOnly["series"]; ok {
		t.Fatal("series present without include=series")
	}
	// An author without series groups still answers series: [].
	var empty struct {
		Series *[]json.RawMessage `json:"series"`
	}
	decodeAuthorBody(t, rig.do("GET", "/api/authors/a2?include=items,series", "", rootUser()), &empty)
	if empty.Series == nil || len(*empty.Series) != 0 {
		t.Fatalf("series %v", empty.Series)
	}
}

func TestAuthorFindOneAccess(t *testing.T) {
	rig := newAuthorRig(t, nil)
	if w := rig.do("GET", "/api/authors/nope", "", rootUser()); w.Code != 404 || w.Body.String() != "Not Found" {
		t.Fatalf("missing author: %d %q", w.Code, w.Body)
	}
	noLibrary := store.User{ID: "u3", IsActive: true, Permissions: json.RawMessage(`{"librariesAccessible":["other"]}`)}
	if w := rig.do("GET", "/api/authors/a1", "", noLibrary); w.Code != 404 {
		t.Fatalf("inaccessible library: %d", w.Code)
	}
	// Explicit books are hidden from a user without explicit access.
	if _, err := rig.db.Exec(`UPDATE books SET explicit=1 WHERE id='b1'`); err != nil {
		t.Fatal(err)
	}
	cleanUser := store.User{ID: "u4", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true}`)}
	var body struct {
		LibraryItems []json.RawMessage `json:"libraryItems"`
	}
	decodeAuthorBody(t, rig.do("GET", "/api/authors/a1?include=items", "", cleanUser), &body)
	if len(body.LibraryItems) != 0 {
		t.Fatalf("explicit item visible: %d", len(body.LibraryItems))
	}
}

func TestAuthorUpdateValidationAndPermissions(t *testing.T) {
	rig := newAuthorRig(t, nil)
	if w := rig.do("PATCH", "/api/authors/a1", `{"description":"x"}`, readOnlyUser()); w.Code != 403 {
		t.Fatalf("no update permission: %d", w.Code)
	}
	for _, body := range []string{`{}`, `{"name":5,"description":{},"other":"x"}`, `[1]`} {
		w := rig.do("PATCH", "/api/authors/a1", body, rootUser())
		if w.Code != 400 || w.Body.String() != "Invalid request payload. No valid keys found" {
			t.Fatalf("body %s: %d %q", body, w.Code, w.Body)
		}
	}
	// Not JSON: express leaves the body empty.
	req := httptest.NewRequest("PATCH", "/api/authors/a1", strings.NewReader(`{"name":"x"}`))
	req = req.WithContext(store.ContextWithUser(req.Context(), rootUser()))
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("form-less body: %d", w.Code)
	}
}

func TestAuthorUpdateDescriptionAndNoChange(t *testing.T) {
	rig := newAuthorRig(t, nil)
	body := `{"description":"Bio","asin":"B000000001"}`
	var first struct {
		Author  AuthorView `json:"author"`
		Updated *bool      `json:"updated"`
		Merged  *bool      `json:"merged"`
	}
	decodeAuthorBody(t, rig.do("PATCH", "/api/authors/a1", body, rootUser()), &first)
	if first.Updated == nil || !*first.Updated || first.Merged != nil {
		t.Fatalf("first update %+v", first)
	}
	if first.Author.Description == nil || *first.Author.Description != "Bio" || first.Author.UpdatedAt <= first.Author.AddedAt {
		t.Fatalf("author %+v", first.Author)
	}
	events := rig.takeEvents()
	if len(events) != 1 || events[0].event != "author_updated" {
		t.Fatalf("events %+v", events)
	}
	expanded, ok := events[0].payload.(AuthorWithBooks)
	if !ok || expanded.NumBooks != 1 {
		t.Fatalf("payload %+v", events[0].payload)
	}
	var second struct {
		Updated *bool `json:"updated"`
	}
	decodeAuthorBody(t, rig.do("PATCH", "/api/authors/a1", body, rootUser()), &second)
	if second.Updated == nil || *second.Updated {
		t.Fatalf("repeat update %+v", second)
	}
	if events := rig.takeEvents(); len(events) != 0 {
		t.Fatalf("no-op emitted %+v", events)
	}
	// null clears a nullable field.
	var cleared struct {
		Author AuthorView `json:"author"`
	}
	decodeAuthorBody(t, rig.do("PATCH", "/api/authors/a1", `{"asin":null}`, rootUser()), &cleared)
	if cleared.Author.ASIN != nil {
		t.Fatalf("asin %v", *cleared.Author.ASIN)
	}
}

func TestAuthorRenameWritesLastFirstAndNotifies(t *testing.T) {
	rig := newAuthorRig(t, nil)
	w := rig.do("PATCH", "/api/authors/a1", `{"name":"ludwig van beethoven"}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var lastFirst string
	if err := rig.db.QueryRow(`SELECT lastFirst FROM authors WHERE id='a1'`).Scan(&lastFirst); err != nil {
		t.Fatal(err)
	}
	if lastFirst != "van Beethoven, Ludwig" {
		t.Fatalf("lastFirst %q", lastFirst)
	}
	if len(rig.touched) != 1 || rig.touched[0] != "i1" {
		t.Fatalf("metadata files written for %v", rig.touched)
	}
	events := rig.takeEvents()
	if len(events) != 2 || events[0].event != "items_updated" || events[1].event != "author_updated" {
		t.Fatalf("events %+v", events)
	}
	if items, ok := events[0].payload.([]any); !ok || len(items) != 1 {
		t.Fatalf("items_updated payload %T", events[0].payload)
	}
}

func TestAuthorRenameToExistingNameMerges(t *testing.T) {
	rig := newAuthorRig(t, nil)
	// Ben already wrote the second book; give him the first too (a1's book).
	w := rig.do("PATCH", "/api/authors/a1", `{"name":"Ben Writer"}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		Author AuthorView `json:"author"`
		Merged *bool      `json:"merged"`
		Update *bool      `json:"updated"`
	}
	decodeAuthorBody(t, w, &body)
	if body.Merged == nil || !*body.Merged || body.Update != nil || body.Author.ID != "a2" {
		t.Fatalf("merge response %+v", body)
	}
	var authors int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM authors WHERE id='a1'`).Scan(&authors); err != nil || authors != 0 {
		t.Fatalf("merged author remains: %d %v", authors, err)
	}
	var linked int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM bookAuthors WHERE authorId='a2'`).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("a2 links %d %v", linked, err)
	}
	events := rig.takeEvents()
	var names []string
	for _, e := range events {
		names = append(names, e.event)
	}
	if strings.Join(names, ",") != "items_updated,author_removed,author_updated" {
		t.Fatalf("events %v", names)
	}
	if removed, ok := events[1].payload.(AuthorView); !ok || removed.ID != "a1" {
		t.Fatalf("author_removed %+v", events[1].payload)
	}
	if updated, ok := events[2].payload.(AuthorWithBooks); !ok || updated.ID != "a2" || updated.NumBooks != 2 {
		t.Fatalf("author_updated %+v", events[2].payload)
	}
	if len(rig.touched) != 1 || rig.touched[0] != "i1" {
		t.Fatalf("metadata files %v", rig.touched)
	}
}

func TestAuthorDeleteRemovesAndPurgesCache(t *testing.T) {
	rig := newAuthorRig(t, nil)
	cache := filepath.Join(rig.meta, "cache", "images")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(rig.meta, "authors", "a1.jpg")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{imagePath, filepath.Join(cache, "a1_400.jpeg"), filepath.Join(cache, "a1_100x50.png"), filepath.Join(cache, "a2_400.jpeg")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rig.db.Exec(`UPDATE authors SET imagePath=? WHERE id='a1'`, imagePath); err != nil {
		t.Fatal(err)
	}
	if w := rig.do("DELETE", "/api/authors/a1", "", readOnlyUser()); w.Code != 403 {
		t.Fatalf("no delete permission: %d", w.Code)
	}
	w := rig.do("DELETE", "/api/authors/a1", "", rootUser())
	if w.Code != 200 || w.Body.String() != "OK" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(cache, "a1_400.jpeg")); err == nil {
		t.Fatal("cached image not purged")
	}
	if _, err := os.Stat(filepath.Join(cache, "a2_400.jpeg")); err != nil {
		t.Fatal("another author's cache purged")
	}
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatal("upstream keeps the image file on delete")
	}
	if events := rig.takeEvents(); len(events) != 1 || events[0].event != "author_removed" {
		t.Fatalf("events %+v", events)
	}
	if len(rig.touched) != 1 {
		t.Fatalf("metadata files %v", rig.touched)
	}
	if w := rig.do("DELETE", "/api/authors/a1", "", rootUser()); w.Code != 404 {
		t.Fatalf("second delete %d", w.Code)
	}
}

func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfake"))
		case "/a.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpegbytes"))
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html/>"))
		case "/icon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	return server
}

func TestAuthorImageUploadAndDelete(t *testing.T) {
	rig := newAuthorRig(t, nil)
	server := imageServer(t)
	post := func(body string, user store.User) *httptest.ResponseRecorder {
		return rig.do("POST", "/api/authors/a1/image", body, user)
	}
	if w := post(`{"url":"`+server.URL+`/a.jpg"}`, readOnlyUser()); w.Code != 403 {
		t.Fatalf("no update: %d", w.Code)
	}
	updateOnly := store.User{ID: "u5", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"update":true}`)}
	if w := post(`{"url":"`+server.URL+`/a.jpg"}`, updateOnly); w.Code != 403 {
		t.Fatalf("no upload permission: %d", w.Code)
	}
	cases := []struct {
		body, message string
	}{
		{`{}`, "Invalid request payload. 'url' not in request body"},
		{`{"url":""}`, "Invalid request payload. 'url' not in request body"},
		{`{"url":"ftp://x/a.jpg"}`, `Invalid request payload. Invalid url "ftp://x/a.jpg"`},
		{`{"url":5}`, `Invalid request payload. Invalid url "5"`},
		{`{"url":"` + server.URL + `/page"}`, `Invalid content type "text/html; charset=utf-8"`},
		{`{"url":"` + server.URL + `/icon.svg"}`, `Invalid content type "image/svg+xml"`},
		{`{"url":"` + server.URL + `/missing.jpg"}`, "Request failed with status code 404"},
	}
	for _, c := range cases {
		w := post(c.body, rootUser())
		if w.Code != 400 || w.Body.String() != c.message {
			t.Errorf("body %s: %d %q, want 400 %q", c.body, w.Code, w.Body, c.message)
		}
	}
	if events := rig.takeEvents(); len(events) != 0 {
		t.Fatalf("failed uploads emitted %+v", events)
	}

	cache := filepath.Join(rig.meta, "cache", "images")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE authors SET imagePath='/old/image.jpg' WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "a1_400.jpeg"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := post(`{"url":"`+server.URL+`/a.png"}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	want := filepath.Join(rig.meta, "authors", "a1.png")
	var body struct {
		Author AuthorView `json:"author"`
	}
	decodeAuthorBody(t, w, &body)
	if body.Author.ImagePath == nil || *body.Author.ImagePath != want {
		t.Fatalf("imagePath %v want %s", body.Author.ImagePath, want)
	}
	if data, err := os.ReadFile(want); err != nil || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("file %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "a1_400.jpeg")); err == nil {
		t.Fatal("cache not purged on upload")
	}
	if events := rig.takeEvents(); len(events) != 1 || events[0].event != "author_updated" {
		t.Fatalf("events %+v", events)
	}
	// A url that does not end in png is stored as .jpg (AuthorFinder.js:50-52).
	if w := post(`{"url":"`+server.URL+`/a.jpg?x=png"}`, rootUser()); w.Code != 200 {
		t.Fatalf("jpg upload %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(rig.meta, "authors", "a1.jpg")); err != nil {
		t.Fatal(err)
	}

	// Delete: removes the file, clears the column; a second delete is a 400.
	if w := rig.do("DELETE", "/api/authors/a1/image", "", readOnlyUser()); w.Code != 403 {
		t.Fatalf("delete image permission %d", w.Code)
	}
	rig.takeEvents()
	w = rig.do("DELETE", "/api/authors/a1/image", "", rootUser())
	if w.Code != 200 {
		t.Fatalf("delete image: %d %s", w.Code, w.Body)
	}
	decodeAuthorBody(t, w, &body)
	if body.Author.ImagePath != nil {
		t.Fatalf("imagePath %v", *body.Author.ImagePath)
	}
	if _, err := os.Stat(filepath.Join(rig.meta, "authors", "a1.jpg")); err == nil {
		t.Fatal("image file kept")
	}
	if w := rig.do("DELETE", "/api/authors/a1/image", "", rootUser()); w.Code != 400 || w.Body.String() != "Author has no image path set" {
		t.Fatalf("second delete: %d %q", w.Code, w.Body)
	}
}

func TestAuthorImageRead(t *testing.T) {
	rig := newAuthorRig(t, nil)
	get := func(target string) *httptest.ResponseRecorder {
		return rig.do("GET", target, "", store.User{})
	}
	const id = "11111111-2222-4333-8444-555555555555"
	if _, err := rig.db.Exec(`INSERT INTO authors VALUES (?,'Image Author','Author, Image',NULL,NULL,NULL,'l1','2024-01-03T00:00:00Z','2024-01-03T00:00:00Z')`, id); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/authors/" + id + "/image"); w.Code != 404 {
		t.Fatalf("no image: %d", w.Code)
	}
	if w := get("/api/authors/22222222-2222-4333-8444-555555555555/image"); w.Code != 404 {
		t.Fatalf("missing uuid author: %d", w.Code)
	}
	if w := get("/api/authors/" + id + "/image?raw=1"); w.Code != 404 {
		t.Fatalf("raw without image: %d", w.Code)
	}
	if w := get("/api/authors/" + id + "/image?format=gif"); w.Code != 400 {
		t.Fatalf("bad format: %d", w.Code)
	}
	// A non-UUID id fails the cache lookup validation (CacheManager.js:160)
	// but is just an unknown author for the raw read.
	if w := get("/api/authors/a1/image?format=jpeg"); w.Code != 400 {
		t.Fatalf("non-uuid id: %d", w.Code)
	}
	if w := get("/api/authors/a1/image?raw=1"); w.Code != 404 {
		t.Fatalf("non-uuid raw: %d", w.Code)
	}
	image := filepath.Join(rig.meta, "authors", id+".jpg")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("raw image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE authors SET imagePath=? WHERE id=?`, image, id); err != nil {
		t.Fatal(err)
	}
	w := get("/api/authors/" + id + "/image?raw=1")
	if w.Code != 200 || w.Body.String() != "raw image bytes" {
		t.Fatalf("raw: %d %q", w.Code, w.Body)
	}
	// A cached resize is served as is, without needing the source or ffmpeg.
	cache := filepath.Join(rig.meta, "cache", "images")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, id+"_200x80.jpeg"), []byte("resized"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = get("/api/authors/" + id + "/image?width=200&height=80&format=jpeg")
	if w.Code != 200 || w.Body.String() != "resized" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("cache hit: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
}

func TestAuthorMatch(t *testing.T) {
	image := imageServer(t)
	finder := &fakeFinder{
		byASIN: map[string]*wire.AuthorMatch{"B000APZR3Q": {ASIN: "B000APZR3Q", Name: "Ada Author", Description: "From Audnexus", Image: ptr(image.URL + "/a.png")}},
		byName: map[string]*wire.AuthorMatch{"Ada": {ASIN: "B000APZR3Q", Name: "Ada Author", Description: "By name"}},
	}
	rig := newAuthorRig(t, finder)
	if w := rig.do("POST", "/api/authors/a1/match", `{"q":"Ada"}`, readOnlyUser()); w.Code != 403 {
		t.Fatalf("no update: %d", w.Code)
	}
	if w := rig.do("POST", "/api/authors/a1/match", `{}`, rootUser()); w.Code != 404 || w.Body.String() != "Author not found" {
		t.Fatalf("empty: %d %q", w.Code, w.Body)
	}
	if got := strings.Join(finder.calls, ","); got != "name::us" {
		t.Fatalf("calls %q", got)
	}
	finder.calls = nil
	// A valid ASIN (any case) is looked up by ASIN with the requested region.
	w := rig.do("POST", "/api/authors/a1/match", `{"asin":"b000apzr3q","region":"uk"}`, rootUser())
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := strings.Join(finder.calls, ","); got != "asin:b000apzr3q:uk" {
		t.Fatalf("calls %q", got)
	}
	var body struct {
		Updated bool       `json:"updated"`
		Author  AuthorView `json:"author"`
	}
	decodeAuthorBody(t, w, &body)
	want := filepath.Join(rig.meta, "authors", "a1.png")
	if !body.Updated || body.Author.ASIN == nil || *body.Author.ASIN != "B000APZR3Q" || body.Author.ImagePath == nil || *body.Author.ImagePath != want || body.Author.Description == nil || *body.Author.Description != "From Audnexus" {
		t.Fatalf("author %+v", body)
	}
	if events := rig.takeEvents(); len(events) != 1 || events[0].event != "author_updated" {
		t.Fatalf("events %+v", events)
	}
	// Matching again changes nothing: updated false, no event.
	var again struct {
		Updated bool `json:"updated"`
	}
	decodeAuthorBody(t, rig.do("POST", "/api/authors/a1/match", `{"asin":"B000APZR3Q"}`, rootUser()), &again)
	if again.Updated || len(rig.takeEvents()) != 0 {
		t.Fatalf("second match updated=%v", again.Updated)
	}
	// An invalid ASIN falls back to the name search.
	finder.calls = nil
	rig.do("POST", "/api/authors/a1/match", `{"asin":"bad","q":"Ada"}`, rootUser())
	if got := strings.Join(finder.calls, ","); got != "name:Ada:us" {
		t.Fatalf("calls %q", got)
	}
}

func ptr[T any](v T) *T { return &v }

func peopleFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(peopleSchema); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO libraries VALUES ('l1','Shelf',1,'books','book','google',NULL,NULL,'{}','{}','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraries VALUES ('l2','Podcast Shelf',2,'podcast','podcast','podcastIndex',NULL,NULL,'{}','{}','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryFolders VALUES ('f1','/media/books','l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO authors VALUES ('a1','Ada Author','Author, Ada',NULL,NULL,NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO series VALUES ('s1','First Series','First Series',NULL,'l1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookAuthors VALUES ('ba1','b1','a1','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookSeries VALUES ('bs1','1','b1','s1','2024-01-01T00:00:00Z')`,
		`INSERT INTO books VALUES ('b1','The Book',NULL,NULL,'2024',NULL,NULL,'<p>A story</p>',NULL,NULL,'en',0,0,'/metadata/covers/b1.jpg',12.5,'["Reader"]','[{"ino":"audio-1","metadata":{"filename":"01.mp3","ext":".mp3","path":"/media/books/01.mp3","relPath":"01.mp3","size":100,"mtimeMs":1704067200000,"ctimeMs":1704067200000,"birthtimeMs":1704067200000},"duration":12.5,"mimeType":"audio/mpeg","exclude":false}]','null','[{"id":0,"start":0,"end":12.5,"title":"Chapter 1"}]','["tag"]','["fiction"]','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('i1','ino-1','/media/books/The Book','The Book','b1','book',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',100,NULL,NULL,'[]','{}','The Book','Book, The','Ada Author','Author, Ada','l1','f1','2024-01-01T00:00:00Z','2024-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

const peopleSchema = `
CREATE TABLE libraries (id TEXT PRIMARY KEY,name TEXT,displayOrder INTEGER,icon TEXT,mediaType TEXT,provider TEXT,lastScan DATETIME,lastScanVersion TEXT,settings JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE libraryFolders (id TEXT PRIMARY KEY,path TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE books (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,subtitle TEXT,publishedYear TEXT,publishedDate TEXT,publisher TEXT,description TEXT,isbn TEXT,asin TEXT,language TEXT,explicit BOOLEAN,abridged BOOLEAN,coverPath TEXT,duration FLOAT,narrators JSON,audioFiles JSON,ebookFile JSON,chapters JSON,tags JSON,genres JSON,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE libraryItems (id TEXT PRIMARY KEY,ino TEXT,path TEXT,relPath TEXT,mediaId TEXT,mediaType TEXT,isFile BOOLEAN,isMissing BOOLEAN,isInvalid BOOLEAN,mtime DATETIME,ctime DATETIME,birthtime DATETIME,size BIGINT,lastScan DATETIME,lastScanVersion TEXT,libraryFiles JSON,extraData JSON,title TEXT,titleIgnorePrefix TEXT,authorNamesFirstLast TEXT,authorNamesLastFirst TEXT,libraryId TEXT,libraryFolderId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE authors (id TEXT PRIMARY KEY,name TEXT,lastFirst TEXT,asin TEXT,description TEXT,imagePath TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE series (id TEXT PRIMARY KEY,name TEXT,nameIgnorePrefix TEXT,description TEXT,libraryId TEXT,createdAt DATETIME,updatedAt DATETIME);
CREATE TABLE bookAuthors (id TEXT PRIMARY KEY,bookId TEXT,authorId TEXT,createdAt DATETIME);
CREATE TABLE bookSeries (id TEXT PRIMARY KEY,sequence TEXT,bookId TEXT,seriesId TEXT,createdAt DATETIME);
`
