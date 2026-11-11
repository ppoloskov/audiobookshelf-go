package item

import (
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
)

func TestUpdateMediaPersistsPresentFieldsAndLeavesAbsentFieldsUntouched(t *testing.T) {
	r := newRig(t)
	body := `{"metadata":{"title":"The Changed Book","subtitle":"Sub","publishedYear":"2030","description":"<script>bad()</script><p>Safe</p>","narrators":["N"],"explicit":true,
		"authors":[{"name":"Ada Author"},{"name":"New Author"}],"series":[{"name":"First Series","sequence":"2"},{"name":"Another Series","sequence":"1"}]},"tags":["new","tag"]}`
	rec := r.do(rootUser, "PATCH", "/api/items/i1/media", body)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	result := decode[struct {
		Updated     bool
		LibraryItem map[string]any
	}](t, rec)
	if !result.Updated || result.LibraryItem["media"].(map[string]any)["metadata"].(map[string]any)["title"] != "The Changed Book" {
		t.Fatalf("result = %s", rec.Body)
	}
	for _, c := range []struct{ query, want string }{
		{`SELECT title||'|'||titleIgnorePrefix||'|'||subtitle||'|'||publishedYear||'|'||explicit FROM books WHERE id='b1'`, "The Changed Book|Changed Book|Sub|2030|1"},
		{`SELECT narrators||tags FROM books WHERE id='b1'`, `["N"]["new","tag"]`},
		{`SELECT language||'|'||duration FROM books WHERE id='b1'`, "en|12.5"}, // absent keys stay
		{`SELECT title FROM libraryItems WHERE id='i1'`, "The Changed Book"},   // by trigger
		{`SELECT description FROM books WHERE id='b1'`, "<p>Safe</p>"},
		{`SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId='b1' ORDER BY ba.createdAt,ba.rowid`, "Ada Author,New Author"},
		{`SELECT s.name||'#'||bs.sequence FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1' ORDER BY bs.createdAt,bs.rowid`, "First Series#2,Another Series#1"},
		{`SELECT authorNamesFirstLast FROM libraryItems WHERE id='i1'`, "Ada Author, New Author"},
	} {
		if got := r.values(c.query); got != c.want {
			t.Errorf("%s = %q, want %q", c.query, got, c.want)
		}
	}
	if got := r.takeEventNames(); got != "author_added,item_updated" {
		t.Errorf("events = %s", got)
	}
	if strings.Join(r.touched, ",") != "i1" {
		t.Errorf("metadata files = %v", r.touched)
	}
	// An empty string clears a text column (NULL); a missing sequence clears the sequence.
	r.do(rootUser, "PATCH", "/api/items/i1/media", `{"metadata":{"subtitle":"","series":[{"name":"Another Series"}]}}`)
	if got := r.values(`SELECT COALESCE(subtitle,'NULL') FROM books WHERE id='b1'`); got != "NULL" {
		t.Errorf("subtitle = %s", got)
	}
	if got := r.values(`SELECT COALESCE(bs.sequence,'NULL') FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE s.name='Another Series'`); got != "NULL" {
		t.Errorf("sequence = %s", got)
	}
	r.do(rootUser, "PATCH", "/api/items/i1/media", `{"metadata":{"series":[{"name":"Another Series","sequence":""}]}}`)
	if got := r.values(`SELECT COALESCE(bs.sequence,'NULL')||'.' FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE s.name='Another Series'`); got != "." {
		t.Errorf("empty sequence = %q", got)
	}
}

// A client may send a numeric publishedYear; it is stored as its text.
func TestUpdateMediaReadsNumbersAsText(t *testing.T) {
	r := newRig(t)
	if rec := r.do(rootUser, "PATCH", "/api/items/i1/media", `{"metadata":{"publishedYear":1999}}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	if got := r.values(`SELECT publishedYear FROM books WHERE id='b1'`); got != "1999" {
		t.Errorf("publishedYear = %s", got)
	}
}

func TestUpdateMediaNoOpChangesNothing(t *testing.T) {
	r := newRig(t)
	before := r.values(`SELECT updatedAt FROM books WHERE id='b1'`)
	rec := r.do(rootUser, "PATCH", "/api/items/i1/media", `{"metadata":{"title":"The Book","genres":["fiction"],"authors":[{"name":"ada author"}],"series":[{"name":"first series","sequence":"1"}]},"tags":["tag"]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"updated":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if after := r.values(`SELECT updatedAt FROM books WHERE id='b1'`); after != before || len(r.takeEvents()) != 0 || len(r.touched) != 0 {
		t.Errorf("a no-op edit wrote something")
	}
}

func TestUpdateMediaRejections(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name string
		user *auth.Principal
		path string
		body string
		want int
	}{
		{"unknown item", rootUser, "missing", `{}`, 404},
		{"no update permission", readerUser, "i1", `{}`, 403},
		{"library not accessible", podUser, "i1", `{}`, 403},
		{"malformed body", rootUser, "i1", `{`, 400},
		{"wrong type", rootUser, "i1", `{"metadata":{"title":true}}`, 400},
		{"invalid cron", rootUser, "ip", `{"autoDownloadSchedule":"nope"}`, 400},
	} {
		if rec := r.do(tc.user, "PATCH", "/api/items/"+tc.path+"/media", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %q, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	if rec := r.do(nil, "PATCH", "/api/items/i1/media", `{}`); rec.Code != 401 {
		t.Errorf("no user: %d", rec.Code)
	}
}

func TestUpdateMediaPodcastReschedulesOnlyWhenTheScheduleChanged(t *testing.T) {
	r := newRig(t)
	patch := func(body string) {
		t.Helper()
		if rec := r.do(rootUser, "PATCH", "/api/items/ip/media", body); rec.Code != 200 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	patch(`{"metadata":{"title":"The Cast","author":"Me","type":"serial","genres":["News"]},"tags":["x"],"maxEpisodesToKeep":5,"maxNewEpisodesToDownload":2}`)
	if len(r.scheduled) != 0 {
		t.Errorf("rescheduled for unrelated changes: %v", r.scheduled)
	}
	patch(`{"autoDownloadEpisodes":true}`)
	patch(`{"autoDownloadEpisodes":true,"autoDownloadSchedule":"0 3 * * *"}`)
	patch(`{"autoDownloadSchedule":"0 3 * * *"}`)
	if strings.Join(r.scheduled, ",") != "ip,ip" {
		t.Errorf("rescheduled = %v", r.scheduled)
	}
	if got := r.values(`SELECT title||'|'||titleIgnorePrefix||'|'||author||'|'||podcastType||'|'||genres||'|'||tags||'|'||maxEpisodesToKeep||'|'||maxNewEpisodesToDownload||'|'||autoDownloadSchedule||'|'||autoDownloadEpisodes FROM podcasts WHERE id='p1'`); got != `The Cast|Cast|Me|serial|["News"]|["x"]|5|2|0 3 * * *|1` {
		t.Errorf("podcast row = %s", got)
	}
}

func TestBatchUpdateValidation(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, body string
		user       *auth.Principal
		want       int
	}{
		{"no update permission", `[{"id":"i1","mediaPayload":{}}]`, readerUser, 403},
		{"not an array", `{"id":"i1"}`, rootUser, 400},
		{"empty array", `[]`, rootUser, 400},
		{"entry without id", `[{"mediaPayload":{}}]`, rootUser, 400},
		{"duplicate ids", `[{"id":"i1"},{"id":"i1"}]`, rootUser, 400},
		{"unknown item", `[{"id":"i1","mediaPayload":{}},{"id":"nope","mediaPayload":{}}]`, rootUser, 404},
		{"library not accessible", `[{"id":"i1","mediaPayload":{}}]`, podUser, 403},
		{"mistyped payload", `[{"id":"i1","mediaPayload":{"tags":"x"}}]`, rootUser, 400},
	} {
		if rec := r.do(tc.user, "POST", "/api/items/batch/update", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %q, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
	// A user who cannot see one of the items gets 403 for the whole batch.
	r.exec(`UPDATE books SET explicit=1 WHERE id='b2'`)
	clean := user("u9", "user", `{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)
	if rec := r.do(clean, "POST", "/api/items/batch/update", `[{"id":"i1","mediaPayload":{}},{"id":"i2","mediaPayload":{}}]`); rec.Code != 403 {
		t.Errorf("explicit item: %d", rec.Code)
	}
}

func TestBatchUpdateCountsOnlyRealChanges(t *testing.T) {
	r := newRig(t)
	rec := r.do(rootUser, "POST", "/api/items/batch/update", `[
		{"id":"i1","mediaPayload":{"metadata":{"title":"The Book","genres":["fiction"]},"tags":["tag"]}},
		{"id":"i2","mediaPayload":{"metadata":{"title":"The New Second","subtitle":"Sub","publishedYear":"1999","authors":[{"name":"Ada Author"}]}}}]`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"success":true,"updates":1}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT title||'|'||titleIgnorePrefix||'|'||subtitle||'|'||publishedYear FROM books WHERE id='b2'`); got != "The New Second|New Second|Sub|1999" {
		t.Errorf("book = %s", got)
	}
	// b2 moved from Ben Writer to Ada Author: Ben has no book, asin or image left.
	if got := r.values(`SELECT id FROM authors ORDER BY id`); got != "a1" {
		t.Errorf("authors = %s", got)
	}
	// Author events, the empty-author removal, then item_updated for each saved item.
	if got := r.takeEventNames(); got != "author_updated,author_removed,item_updated" {
		t.Errorf("events = %s", got)
	}
	if strings.Join(r.touched, ",") != "i2" {
		t.Errorf("metadata files = %v", r.touched)
	}
	rec = r.do(rootUser, "POST", "/api/items/batch/update", `[{"id":"i2","mediaPayload":{"metadata":{"title":"The New Second"}}}]`)
	if strings.TrimSpace(rec.Body.String()) != `{"success":true,"updates":0}` || len(r.takeEvents()) != 0 {
		t.Errorf("repeat: %d %s", rec.Code, rec.Body)
	}
}

// Two books of one series both move to another: the series is swept once, after
// both, and only then.
func TestBatchUpdateSweepsOnceForTheWholeBatch(t *testing.T) {
	r := newRig(t)
	r.exec(`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs2','2','b2','s1',?)`, ts)
	rec := r.do(rootUser, "POST", "/api/items/batch/update", `[
		{"id":"i1","mediaPayload":{"metadata":{"series":[{"name":"Moved","sequence":"1"}]}}},
		{"id":"i2","mediaPayload":{"metadata":{"series":[{"name":"moved","sequence":"2"}]}}}]`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT name FROM series`); got != "Moved" {
		t.Errorf("series = %s", got)
	}
	if got := r.takeEventNames(); got != "series_removed,item_updated,item_updated" {
		t.Errorf("events = %s", got)
	}
}

func TestBatchUpdateSkipsInvalidPodcastSchedule(t *testing.T) {
	r := newRig(t)
	rec := r.do(rootUser, "POST", "/api/items/batch/update", `[{"id":"ip","mediaPayload":{"autoDownloadSchedule":"bad","metadata":{"title":"Nope"}}},{"id":"i2","mediaPayload":{"tags":["t"]}}]`)
	if strings.TrimSpace(rec.Body.String()) != `{"success":true,"updates":1}` {
		t.Fatalf("%s", rec.Body)
	}
	if got := r.values(`SELECT title FROM podcasts`); got != "Science Now" {
		t.Errorf("podcast = %s", got)
	}
}

func TestBatchGet(t *testing.T) {
	r := newRig(t)
	rec := r.do(rootUser, "POST", "/api/items/batch/get", `{"libraryItemIds":["i2","i1","missing","i1"]}`)
	items := decode[struct{ LibraryItems []map[string]any }](t, rec).LibraryItems
	if rec.Code != 200 || len(items) != 2 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, ok := items[0]["numFiles"]; !ok {
		t.Error("batch get returns expanded items")
	}
	for _, tc := range []struct {
		name, body string
		user       *auth.Principal
		want       int
	}{
		{"empty list", `{"libraryItemIds":[]}`, rootUser, 403},
		{"malformed", `{`, rootUser, 403},
		{"only unknown ids", `{"libraryItemIds":["missing"]}`, rootUser, 200},
		{"one item the user cannot see", `{"libraryItemIds":["ip","i1"]}`, podUser, 403},
	} {
		if rec := r.do(tc.user, "POST", "/api/items/batch/get", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// Every nullable text field: a value sets it; absent leaves it; null and ""
// clear it (NULL) and count as an update; a number stores its text.
func TestUpdateMediaTextFieldsSetAndClear(t *testing.T) {
	type field struct{ item, table, key, column string }
	var fields []field
	for _, k := range [][2]string{{"subtitle", "subtitle"}, {"publisher", "publisher"}, {"publishedYear", "publishedYear"}, {"publishedDate", "publishedDate"},
		{"description", "description"}, {"isbn", "isbn"}, {"asin", "asin"}, {"language", "language"}} {
		fields = append(fields, field{"i1", "books", k[0], k[1]})
	}
	for _, k := range [][2]string{{"author", "author"}, {"releaseDate", "releaseDate"}, {"feedUrl", "feedURL"}, {"imageUrl", "imageURL"}, {"description", "description"},
		{"itunesPageUrl", "itunesPageURL"}, {"itunesId", "itunesId"}, {"itunesArtistId", "itunesArtistId"}, {"language", "language"}, {"type", "podcastType"}} {
		fields = append(fields, field{"ip", "podcasts", k[0], k[1]})
	}
	for _, f := range fields {
		t.Run(f.table+"."+f.key, func(t *testing.T) {
			r := newRig(t)
			patch := func(value string) (updated bool) {
				t.Helper()
				rec := r.do(rootUser, "PATCH", "/api/items/"+f.item+"/media", `{"metadata":{"`+f.key+`":`+value+`}}`)
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", value, rec.Code, rec.Body)
				}
				return decode[struct{ Updated bool }](t, rec).Updated
			}
			stored := func() string {
				return r.values(`SELECT COALESCE(` + f.column + `,'<NULL>') FROM ` + f.table)
			}
			if f.table == "books" {
				stored = func() string { return r.values(`SELECT COALESCE(` + f.column + `,'<NULL>') FROM books WHERE id='b1'`) }
			}
			if !patch(`"v1"`) || stored() != "v1" {
				t.Fatalf("set: %s", stored())
			}
			if patch(`"v1"`) {
				t.Error("same value reported as updated")
			}
			r.do(rootUser, "PATCH", "/api/items/"+f.item+"/media", `{"tags":["other"]}`) // absent key
			if stored() != "v1" {
				t.Errorf("absent key changed the column: %s", stored())
			}
			if !patch(`null`) || stored() != "<NULL>" {
				t.Errorf("null did not clear: %s", stored())
			}
			if patch(`null`) {
				t.Error("null on a NULL column reported as updated")
			}
			patch(`"v2"`)
			if !patch(`""`) || stored() != "<NULL>" {
				t.Errorf(`"" did not clear: %s`, stored())
			}
			if !patch(`1999`) || stored() != "1999" {
				t.Errorf("number stores %s", stored())
			}
		})
	}
}
