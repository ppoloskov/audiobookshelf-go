package item

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/metadata"
)

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

const override = `{"overrideDetails":true,"overrideCover":false}`

// matchResult is MatchResult as a client reads it.
type matchResult struct {
	Warning     *string
	Updated     *bool
	LibraryItem json.RawMessage
}

func (r *rig) match(id, body string) matchResult {
	r.t.Helper()
	rec := r.do(rootUser, "POST", "/api/items/"+id+"/match", body)
	if rec.Code != 200 {
		r.t.Fatalf("match: %d %s", rec.Code, rec.Body)
	}
	return decode[matchResult](r.t, rec)
}

func TestMatchValidationAndWarning(t *testing.T) {
	r := newRig(t)
	if rec := r.do(rootUser, "POST", "/api/items/nope/match", "{}"); rec.Code != 404 {
		t.Fatalf("unknown item %d", rec.Code)
	}
	if rec := r.do(readerUser, "POST", "/api/items/i1/match", "{}"); rec.Code != 403 {
		t.Fatalf("no update permission %d", rec.Code)
	}
	if rec := r.do(podUser, "POST", "/api/items/i1/match", "{}"); rec.Code != 403 {
		t.Fatalf("library not accessible %d", rec.Code)
	}
	rec := r.do(rootUser, "POST", "/api/items/i1/match", `{"provider":"audible","title":"Query","author":"Someone"}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"warning":"No audible match found"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if q := r.finder.queries[0]; q.Provider != "audible" || q.Title != "Query" || q.Author != "Someone" || q.MaxFuzzy != 2 {
		t.Fatalf("query %+v", q)
	}
	// Defaults are google, the book's title and its author names; an empty body is fine.
	req := r.do(rootUser, "POST", "/api/items/i1/match", "")
	if req.Code != 200 {
		t.Fatalf("empty body %d", req.Code)
	}
	if q := r.finder.queries[1]; q.Provider != "google" || q.Title != "The Book" || q.Author != "Ada Author" || q.Duration != 12.5 {
		t.Fatalf("defaults %+v", q)
	}
	if rec := r.do(rootUser, "POST", "/api/items/i1/match", `{"overrideCover":"yes"}`); rec.Code != 400 {
		t.Fatalf("mistyped body %d", rec.Code)
	}
}

func TestMatchAppliesDetails(t *testing.T) {
	r := newRig(t)
	r.finder.books = []metadata.BookResult{{
		Title: "Matched Title", Subtitle: "Sub", Author: "Ben Writer, New Person", Narrator: "N1, N2",
		Publisher: "Press", PublishedYear: "2001", Genres: []string{"G"}, Tags: []string{"a", "b"},
		Series:   []metadata.Series{{Series: "First Series", Sequence: "5"}, {Series: "Fresh Series", Sequence: "1"}},
		Explicit: db.Ptr(true),
	}}
	// Without overrides only empty fields are filled: the title stays, the
	// subtitle, publisher, narrators and genres arrive; the book keeps its
	// authors and series because it has some.
	res := r.match("i1", `{"provider":"custom-1"}`)
	if res.Updated == nil || !*res.Updated || len(res.LibraryItem) == 0 {
		t.Fatalf("result %+v", res)
	}
	if got := r.values(`SELECT title||'|'||subtitle||'|'||publisher||'|'||publishedYear||'|'||narrators||'|'||genres||'|'||tags||'|'||explicit||'|'||COALESCE(isbn,'NULL') FROM books WHERE id='b1'`); got != `The Book|Sub|Press|2024|["Reader"]|["fiction"]|["tag"]|1|NULL` {
		t.Fatalf("book %s", got)
	}
	if got := r.takeEventNames(); got != "item_updated" {
		t.Fatalf("events %s", got)
	}
	// With overrideDetails everything is replaced; authors and series follow the
	// match: Ben stays linked, Ada goes (and is swept), a new author and series appear.
	r.match("i1", `{"provider":"custom-1","overrideDetails":true,"overrideCover":false}`)
	if got := r.values(`SELECT title||'|'||titleIgnorePrefix||'|'||narrators||'|'||publishedYear||'|'||genres||'|'||tags FROM books WHERE id='b1'`); got != `Matched Title|Matched Title|["N1","N2"]|2001|["G"]|["a","b"]` {
		t.Fatalf("overridden book %s", got)
	}
	if got := r.values(`SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId='b1' ORDER BY ba.createdAt,ba.rowid`); got != "Ben Writer,New Person" {
		t.Fatalf("authors %s", got)
	}
	if got := r.values(`SELECT s.name||'#'||bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1' ORDER BY bs.createdAt,bs.rowid`); got != "First Series#5,Fresh Series#1" {
		t.Fatalf("series %s", got)
	}
	events := r.takeEventNames()
	for _, want := range []string{"author_added", "series_added", "author_removed", "item_updated"} {
		if !strings.Contains(events, want) {
			t.Fatalf("events %s lack %s", events, want)
		}
	}
	// A match equal to the stored values changes nothing.
	r.finder.books = []metadata.BookResult{{Title: "Matched Title"}}
	if again := r.match("i1", override); again.Updated == nil || *again.Updated {
		t.Fatalf("second match updated: %v", again.Updated)
	}
}

func TestMatchPodcast(t *testing.T) {
	r := newRig(t)
	r.finder.podcasts = []metadata.PodcastResult{{ID: raw(12345), ArtistID: raw("99"), Title: db.Ptr("Matched Cast"), ArtistName: db.Ptr("Artist"), DescriptionPlain: "about",
		ReleaseDate: db.Ptr("2020-01-01"), Genres: []string{"Tech"}, FeedURL: db.Ptr("https://other.test/f.xml"), PageURL: db.Ptr("https://itunes/p")}}
	var episodes []string
	r.h.deps.PodcastEpisodes = func(_ context.Context, id string, override bool) (int, error) {
		episodes = append(episodes, fmt.Sprint(id, override))
		return 2, nil
	}
	r.match("ip", `{}`)
	if got := r.values(`SELECT title||'|'||author||'|'||itunesId||'|'||itunesArtistId||'|'||releaseDate||'|'||genres||'|'||feedURL||'|'||description FROM podcasts`); got != `Science Now|Host|12345|99|2020-01-01|["Tech"]|https://example.test/feed|about` {
		t.Fatalf("podcast %s", got)
	}
	r.match("ip", `{"overrideDetails":true}`)
	if got := r.values(`SELECT title||'|'||titleIgnorePrefix||'|'||author||'|'||feedURL FROM podcasts`); got != `Matched Cast|Matched Cast|Artist|https://other.test/f.xml` {
		t.Fatalf("overridden podcast %s", got)
	}
	if strings.Join(episodes, ",") != "ipfalse,iptrue" {
		t.Fatalf("episode matches %v", episodes)
	}
	r.finder.podcasts = nil
	if res := r.match("ip", `{}`); res.Warning == nil || *res.Warning != "No google match found" {
		t.Fatalf("no match: %+v", res)
	}
}

func (r *rig) relations() string {
	r.t.Helper()
	return fmt.Sprintf("%s|%s", r.values(`SELECT group_concat(authorId) FROM bookAuthors WHERE bookId='b1'`), r.values(`SELECT group_concat(seriesId||'#'||COALESCE(sequence,'NULL')) FROM bookSeries WHERE bookId='b1'`))
}

func TestMatchEmptyAndDuplicateRelationsChangeNothing(t *testing.T) {
	for name, match := range map[string]metadata.BookResult{
		"empty": {Author: "", Series: []metadata.Series{}},
		"duplicates": {
			Author: "ADA AUTHOR, ada author",
			Series: []metadata.Series{{Series: "FIRST SERIES", Sequence: "1"}, {Series: "first series", Sequence: "1"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.finder.books = []metadata.BookResult{match}
			if res := r.match("i1", override); res.Updated == nil || *res.Updated {
				t.Fatalf("unchanged relations reported an update: %+v", res)
			}
			if got := r.relations(); got != "a1|s1#1" {
				t.Fatalf("relations %s", got)
			}
			if events := r.takeEventNames(); events != "" {
				t.Fatalf("events %s", events)
			}
		})
	}
}

func TestMatchLinksExistingRelationsWithoutCreationEvents(t *testing.T) {
	r := newRig(t)
	r.exec(`INSERT INTO series(id,name,libraryId,createdAt,updatedAt) VALUES('s2','Known Series','l1',?,?)`, ts, ts)
	r.finder.books = []metadata.BookResult{{
		Author: "BEN WRITER, ben writer",
		Series: []metadata.Series{{Series: "KNOWN SERIES", Sequence: "2"}, {Series: "known series", Sequence: "3"}},
	}}
	r.match("i1", override)
	if got := r.relations(); got != "a2|s2#3" {
		t.Fatalf("relations %s", got)
	}
	for _, event := range strings.Split(r.takeEventNames(), ",") {
		if strings.HasSuffix(event, "_added") || event == "author_updated" {
			t.Fatalf("linking existing entities emitted %q", event)
		}
	}
}

func TestMatchDuplicateSeries(t *testing.T) {
	r := newRig(t)
	r.finder.books = []metadata.BookResult{{Series: []metadata.Series{{Series: "First Series", Sequence: "2"}, {Series: "first series", Sequence: "1"}}}}
	// The intermediate sequence change is reported; the last one wins.
	if res := r.match("i1", override); res.Updated == nil || !*res.Updated {
		t.Fatalf("result %+v", res)
	}
	if got := r.relations(); got != "a1|s1#1" || r.takeEventNames() != "item_updated" {
		t.Fatalf("relations %s", got)
	}

	r = newRig(t)
	r.finder.books = []metadata.BookResult{{Series: []metadata.Series{{Series: "New Series", Sequence: "2"}, {Series: "new series", Sequence: "3"}}}}
	r.match("i1", override)
	if got := r.values(`SELECT s.name||'#'||bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1'`); got != "New Series#3" {
		t.Fatalf("series %s", got)
	}
	if events := r.takeEventNames(); events != "series_removed,series_added,item_updated" {
		t.Fatalf("events %s", events)
	}
}

func TestMatchMissingSequencePreservesExistingAndDefaultsNull(t *testing.T) {
	r := newRig(t)
	r.finder.books = []metadata.BookResult{{Series: []metadata.Series{{Series: "First Series"}}}}
	if res := r.match("i1", override); res.Updated == nil || *res.Updated || r.relations() != "a1|s1#1" || r.takeEventNames() != "" {
		t.Fatalf("existing series: %+v %s", res, r.relations())
	}
	r = newRig(t)
	r.finder.books = []metadata.BookResult{{Series: []metadata.Series{{Series: "New Series"}}}}
	r.match("i1", override)
	if got := r.values(`SELECT COALESCE(bs.sequence,'NULL') FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE s.name='New Series'`); got != "NULL" {
		t.Fatalf("new series sequence %s", got)
	}
}

func TestMatchSeriesSequenceNullEmptyAndNumbers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sequence string
		want     string
	}{{"absent keeps the stored one", "", "1"}, {"zero", "0", "0"}, {"number", "2", "2"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.finder.books = []metadata.BookResult{{Series: []metadata.Series{{Series: "First Series", Sequence: tc.sequence}}}}
			r.match("i1", override)
			if got := r.values(`SELECT COALESCE(sequence,'NULL')||'.' FROM bookSeries WHERE bookId='b1'`); got != strings.ReplaceAll(tc.want, ".", "")+"." {
				t.Fatalf("sequence %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatchRelationFailureRollsBackDatabaseAndEvents(t *testing.T) {
	r := newRig(t)
	r.finder.books = []metadata.BookResult{{Author: "New Author", Series: []metadata.Series{{Series: "New Series", Sequence: "2"}}}}
	r.exec(`CREATE TRIGGER fail_series BEFORE INSERT ON bookSeries BEGIN SELECT RAISE(ABORT, 'quickmatch test failure'); END`)
	if rec := r.do(rootUser, "POST", "/api/items/i1/match", override); rec.Code != 500 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT name FROM authors ORDER BY name`); got != "Ada Author,Ben Writer" {
		t.Errorf("authors %s", got)
	}
	if got := r.values(`SELECT name FROM series`); got != "First Series" {
		t.Errorf("series %s", got)
	}
	if got := r.relations(); got != "a1|s1#1" {
		t.Errorf("relations %s", got)
	}
	if events := r.takeEventNames(); events != "" {
		t.Errorf("events after rolled back match: %s", events)
	}
}

func TestMatchSavesCoverAndKeepsGoingWhenItFails(t *testing.T) {
	r := newRig(t)
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		downloads++
		if req.URL.Path == "/bad.jpg" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0 jpeg bytes"))
	}))
	defer server.Close()
	r.h.client = server.Client() // the real client refuses loopback addresses
	r.finder.books = []metadata.BookResult{{Cover: server.URL + "/c.jpg"}}
	r.match("i1", `{}`)
	want := filepath.Join(r.metaDir, "i1", "cover.jpg")
	if got := r.values(`SELECT coverPath FROM books WHERE id='b1'`); got != want || downloads != 1 {
		t.Fatalf("cover %q after %d downloads, want %q", got, downloads, want)
	}
	// With a cover and no override the cover is kept; with overrideCover it is replaced.
	r.match("i1", `{}`)
	r.match("i1", `{"overrideCover":true}`)
	if downloads != 2 {
		t.Fatalf("downloads %d", downloads)
	}
	r.finder.books = []metadata.BookResult{{Cover: server.URL + "/bad.jpg"}}
	r.exec(`UPDATE books SET coverPath=NULL WHERE id='b1'`)
	if res := r.match("i1", `{}`); res.Updated == nil || *res.Updated {
		t.Fatalf("a failed cover download must not fail or update: %+v", res)
	}
}

// relationSnapshot is everything one item mutation leaves behind: links, parents,
// events, filter data patches and metadata-file writes.
func (r *rig) relationSnapshot() string {
	r.t.Helper()
	var names []string
	for _, e := range r.takeEvents() {
		names = append(names, e.name)
	}
	data, err := r.filters.Get(context.Background(), r.d, "l1", "book")
	if err != nil {
		r.t.Fatal(err)
	}
	var a, s []string
	for _, n := range data.Authors {
		a = append(a, n.Name)
	}
	for _, n := range data.Series {
		s = append(s, n.Name)
	}
	sort.Strings(a)
	sort.Strings(s)
	return fmt.Sprintf("links=%s authors=%s series=%s seq=%s events=%s filter=%s|%s touched=%s",
		r.values(`SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId='b1' ORDER BY ba.createdAt,ba.rowid`),
		r.values(`SELECT name FROM authors ORDER BY name`), r.values(`SELECT name FROM series ORDER BY name`),
		r.values(`SELECT s.name||'#'||COALESCE(bs.sequence,'') FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1'`),
		strings.Join(names, ","), strings.Join(a, ","), strings.Join(s, ","), strings.Join(r.touched, ","))
}

// TestRelationsAcrossRoutes pins what PATCH media, batch update and quick match
// do to the author and series links of one book. The routes differ on purpose:
// quick match announces neither author_updated nor the reuse of an author, and
// adds series_added.
func TestRelationsAcrossRoutes(t *testing.T) {
	type scenario struct {
		name, prep string
		authors    []string
		series     [][2]string
		want       [3]string // patch, batch, quick match
	}
	const (
		adaZed = "links=Ada Author,Zed Newbie authors=Ada Author,Ben Writer,Zed Newbie series=First Series seq=First Series#1 events=author_added,item_updated filter=Ada Author,Ben Writer,Zed Newbie|First Series touched=i1"
		ben    = "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=%s filter=Ben Writer|First Series touched=i1"
		benAsi = "links=Ben Writer authors=Ada Author,Ben Writer series=First Series seq=First Series#1 events=%s filter=Ada Author,Ben Writer|First Series touched=i1"
		ada    = "links=Ada Author authors=Ada Author,Ben Writer series=%[1]s seq=%[1]s#%[2]s events=%[3]s filter=Ada Author,Ben Writer|%[1]s touched=i1"
	)
	scenarios := []scenario{
		{name: "new author", authors: []string{"Ada Author", "Zed Newbie"}, series: [][2]string{{"First Series", "1"}}, want: [3]string{adaZed, adaZed, adaZed}},
		{name: "existing author other case", authors: []string{"ben writer"}, series: [][2]string{{"First Series", "1"}},
			want: [3]string{fmt.Sprintf(ben, "author_updated,author_removed,item_updated"), fmt.Sprintf(ben, "author_updated,author_removed,item_updated"), fmt.Sprintf(ben, "author_removed,item_updated")}},
		{name: "author keeping asin survives", prep: `UPDATE authors SET asin='B000' WHERE id='a1'`, authors: []string{"Ben Writer"}, series: [][2]string{{"First Series", "1"}},
			want: [3]string{fmt.Sprintf(benAsi, "author_updated,item_updated"), fmt.Sprintf(benAsi, "author_updated,item_updated"), fmt.Sprintf(benAsi, "item_updated")}},
		{name: "sequence change", authors: []string{"Ada Author"}, series: [][2]string{{"First Series", "2"}},
			want: [3]string{fmt.Sprintf(ada, "First Series", "2", "item_updated"), fmt.Sprintf(ada, "First Series", "2", "item_updated"), fmt.Sprintf(ada, "First Series", "2", "item_updated")}},
		{name: "series replaced", authors: []string{"Ada Author"}, series: [][2]string{{"Other Series", "1"}},
			want: [3]string{fmt.Sprintf(ada, "Other Series", "1", "series_removed,item_updated"), fmt.Sprintf(ada, "Other Series", "1", "series_removed,item_updated"), fmt.Sprintf(ada, "Other Series", "1", "series_removed,series_added,item_updated")}},
	}
	for _, sc := range scenarios {
		for i, route := range []string{"patch", "batch", "quickmatch"} {
			t.Run(sc.name+"/"+route, func(t *testing.T) {
				r := newRig(t)
				if sc.prep != "" {
					r.exec(sc.prep)
				}
				// Load the library's filter data so the mutation must invalidate it.
				if _, err := r.filters.Get(context.Background(), r.d, "l1", "book"); err != nil {
					t.Fatal(err)
				}
				var authors []map[string]string
				var series []map[string]string
				var seriesMatch []metadata.Series
				for _, a := range sc.authors {
					authors = append(authors, map[string]string{"name": a})
				}
				for _, s := range sc.series {
					series = append(series, map[string]string{"name": s[0], "sequence": s[1]})
					seriesMatch = append(seriesMatch, metadata.Series{Series: s[0], Sequence: s[1]})
				}
				media := string(raw(map[string]any{"metadata": map[string]any{"authors": authors, "series": series}}))
				var code int
				switch route {
				case "patch":
					code = r.do(rootUser, "PATCH", "/api/items/i1/media", media).Code
				case "batch":
					code = r.do(rootUser, "POST", "/api/items/batch/update", `[{"id":"i1","mediaPayload":`+media+`}]`).Code
				default:
					r.finder.books = []metadata.BookResult{{Author: strings.Join(sc.authors, ", "), Series: seriesMatch}}
					code = r.do(rootUser, "POST", "/api/items/i1/match", override).Code
				}
				if code != 200 {
					t.Fatalf("status %d", code)
				}
				if got := r.relationSnapshot(); got != sc.want[i] {
					t.Fatalf("\n got %s\nwant %s", got, sc.want[i])
				}
			})
		}
	}
}

func TestBatchQuickMatch(t *testing.T) {
	r := newRig(t)
	r.finder.books = []metadata.BookResult{{Subtitle: "S", Publisher: "P"}}
	q := "/api/items/batch/quickmatch"
	if rec := r.do(adminUser, "POST", q, `{"libraryItemIds":["i1"]}`); rec.Code != 200 {
		t.Fatalf("admin %d", rec.Code)
	}
	r.waitFor("batch_quickmatch_complete")
	r.takeEvents()
	if rec := r.do(readerUser, "POST", q, `{"libraryItemIds":["i1"]}`); rec.Code != 403 {
		t.Fatalf("non-admin %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"libraryItemIds":[]}`, `{"libraryItemIds":["nope"]}`} {
		if rec := r.do(rootUser, "POST", q, body); rec.Code != 400 {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	r.finder.books = []metadata.BookResult{{Title: "T", Publisher: "P"}}
	if rec := r.do(rootUser, "POST", q, `{"libraryItemIds":["i1","i2","ip"],"options":{"provider":"x"}}`); rec.Code != 200 || rec.Body.String() != "OK" {
		t.Fatalf("run %d %q", rec.Code, rec.Body)
	}
	done := r.waitFor("batch_quickmatch_complete")
	result := done.payload.(batchQuickMatchResult)
	// i1 was filled by the first run, i2 gets its publisher; the podcast has no match.
	if done.audience != "u1" || !result.Success || result.Updates != 1 || result.Unmatched != 1 {
		t.Fatalf("result %+v to %s", result, done.audience)
	}
	if r.finder.queries[len(r.finder.queries)-1].Provider != "x" {
		t.Errorf("provider not passed: %+v", r.finder.queries)
	}
}

func (r *rig) waitFor(name string) sentEvent {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		r.mu.Lock()
		for _, e := range r.events {
			if e.name == name {
				r.mu.Unlock()
				return e
			}
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no %s event", name)
	return sentEvent{}
}

func TestCoverDownloadIsCapped(t *testing.T) {
	r := newRig(t)
	body := make([]byte, maxCoverDownload+1)
	copy(body, "\xff\xd8\xff\xe0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		if req.URL.Path == "/declared.jpg" {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		_, _ = w.Write(body) // without a declared length the body is chunked
	}))
	defer server.Close()
	r.h.client = server.Client() // the real client refuses loopback addresses
	for _, name := range []string{"declared.jpg", "chunked.jpg"} {
		rec := r.request(uploaderUser, "POST", "/api/items/i1/cover", "application/json", `{"url":"`+server.URL+"/"+name+`"}`)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Failed to download image from url") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(r.metaDir, "i1")); len(entries) != 0 {
		t.Errorf("oversized download left %d files", len(entries))
	}
}
