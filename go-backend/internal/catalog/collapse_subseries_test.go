package catalog

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// TestSeriesSequenceListMatchesUpstream pins libraryHelpers.js:236-257. The
// expectations are the output of that same naturalSort + reduce code run in
// Node 22 on each input.
func TestSeriesSequenceListMatchesUpstream(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"1", "2", "3"}, "1-3"},
		{[]string{"3", "1", "2", "5"}, "1-3, 5"},
		{[]string{"1", "3"}, "1, 3"},
		{[]string{"1", "1.5", "2"}, "1, 1.5, 2"},
		{[]string{"2", "10", "9", "1"}, "1-2, 9-10"},
		{[]string{"1a", "1", "2", "b", "A"}, "1, 1a, 2, A, b"},
		{[]string{"01", "2", "03"}, "1-3"},
		{[]string{"1", "1"}, "1, 1"},
		{[]string{".5", "1.5", "2.5"}, "0.5-2.5"},
		{[]string{"1.", "2"}, "1-2"},
		{[]string{"Book 2", "Book 10", "Book 1"}, "Book 1, Book 2, Book 10"},
		{[]string{"0", "1", "2", "x", "4", "5"}, "0-2, 4-5, x"},
	} {
		c := &subseriesCollapsed{filterSeqs: map[string]*string{}}
		for i, s := range tc.in {
			s := s
			b := &subseriesBook{itemID: "i" + strconv.Itoa(i)}
			c.books = append(c.books, b)
			c.filterSeqs[b.itemID] = &s
		}
		if got := seriesSequenceList(c); got != tc.want {
			t.Errorf("seriesSequenceList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNodeBase64DecodeMatchesBufferFrom(t *testing.T) {
	for in, want := range map[string]string{
		"bm8tc2VyaWVz": "no-series", "bm8tc2VyaWVz==": "no-series", "YWJj": "abc", "YWI=": "ab", "YW": "a", "Y": "", "": "",
		"YW-_": "ao\xbf", "Y W\nJj": "abc",
	} {
		if got := nodeBase64Decode(in); got != want {
			t.Errorf("nodeBase64Decode(%q) = %q, want %q", in, got, want)
		}
	}
}

// catalogDB opens an in-memory database with the pinned upstream schema.
func catalogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:catalog-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schemaassets.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	return db
}

// subseriesFixture builds, on the real upstream schema, series "Mega" with
// books A (1), B (2), C (3) and D (no sequence), where A and B also belong to
// sub-series "Sub" (1, 2).
func subseriesFixture(t *testing.T) (*Handler, store.User) {
	t.Helper()
	db := catalogDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	const now = "2024-01-01 00:00:00.000 +00:00"
	exec(`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('lib1','Lib',0,'database','book','google','{}','{}',?,?)`, now, now)
	exec(`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('f1','/books','lib1',?,?)`, now, now)
	exec(`INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES('mega','Mega','Mega','lib1',?,?),('sub','The Sub','Sub, The','lib1',?,?)`, now, now, now, now)
	for i, b := range []struct{ id, title string }{{"A", "Alpha"}, {"B", "Bravo"}, {"C", "Charlie"}, {"D", "Delta"}} {
		exec(`INSERT INTO books(id,title,titleIgnorePrefix,explicit,abridged,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES(?,?,?,0,0,?,'[]','[]','null','[]','[]','[]',?,?)`, b.id, b.title, b.title, float64(100-i*10)+0.5, now, now)
		exec(`INSERT INTO libraryItems(id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,libraryFiles,extraData,size,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,createdAt,updatedAt,libraryId,libraryFolderId) VALUES(?,?,?,?,?, 'book',0,0,0,'[]','{}',?,?,?,'','',?,?,'lib1','f1')`, "li"+b.id, "ino"+b.id, "/books/"+b.id, b.id, b.id, 10*(i+1), b.title, b.title, now, now)
	}
	exec(`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('m1','1','A','mega',?),('m2','2','B','mega',?),('m3','3','C','mega',?),('m4',NULL,'D','mega',?),('s1','1','A','sub',?),('s2','2','B','sub',?)`, now, now, now, now, now, now)
	user := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	return New(db, config.Config{}, nil), user
}

func getSubseriesPage(t *testing.T, h *Handler, user store.User, query string) (page struct {
	Results []struct {
		ID              string `json:"id"`
		CollapsedSeries *struct {
			Name               string   `json:"name"`
			NameIgnorePrefix   string   `json:"nameIgnorePrefix"`
			LibraryItemIDs     []string `json:"libraryItemIds"`
			NumBooks           int      `json:"numBooks"`
			SeriesSequenceList string   `json:"seriesSequenceList"`
		} `json:"collapsedSeries"`
		Media struct {
			Metadata struct {
				Series *struct {
					ID       string  `json:"id"`
					Name     string  `json:"name"`
					Sequence *string `json:"sequence"`
				} `json:"series"`
			} `json:"metadata"`
		} `json:"media"`
	} `json:"results"`
	Total  int     `json:"total"`
	SortBy *string `json:"sortBy"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
}, raw string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/libraries/lib1/items?minified=1&collapseseries=1&filter=series.bWVnYQ=="+query, nil)
	if !h.Handle(w, r, user) || w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", r.URL, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page, w.Body.String()
}

func ids(page []string) string { return strings.Join(page, ",") }

// TestCollapseSubseriesFilteredPage covers handleCollapseSubseries on the
// real schema: A and B collapse behind the sub-series entry at A's place,
// default order is sequence (a missing one compares as "null"), invalid sorts
// are dropped from the payload, and pagination slices the sorted list.
func TestCollapseSubseriesFilteredPage(t *testing.T) {
	h, user := subseriesFixture(t)
	order := func(query string) (string, int, *string) {
		p, _ := getSubseriesPage(t, h, user, query)
		var out []string
		for _, r := range p.Results {
			out = append(out, r.ID)
		}
		return ids(out), p.Total, p.SortBy
	}
	if got, total, _ := order(""); got != "liA,liC,liD" || total != 3 {
		// A (collapsed, seq 1), C (seq 3), D (seq null sorts after digits).
		t.Errorf("default order = %s total %d", got, total)
	}
	p, raw := getSubseriesPage(t, h, user, "")
	first := p.Results[0]
	if first.CollapsedSeries == nil || first.CollapsedSeries.Name != "The Sub" || first.CollapsedSeries.NameIgnorePrefix != "Sub, The" ||
		first.CollapsedSeries.NumBooks != 2 || !slices.Equal(first.CollapsedSeries.LibraryItemIDs, []string{"liA", "liB"}) ||
		first.CollapsedSeries.SeriesSequenceList != "1-2" {
		t.Errorf("collapsedSeries = %+v", first.CollapsedSeries)
	}
	if s := first.Media.Metadata.Series; s == nil || s.ID != "mega" || s.Name != "Mega" || s.Sequence == nil || *s.Sequence != "1" {
		t.Errorf("metadata.series = %+v", s)
	}
	if !strings.HasPrefix(raw, `{"results":[`) || !strings.Contains(raw, `"collapseseries":true,"include":"","offset":0}`) {
		t.Errorf("payload key order: %.60s ... %s", raw, raw[len(raw)-160:])
	}
	if got, _, sortBy := order("&sort=media.metadata.title&desc=1"); got != "liA,liD,liC" || sortBy == nil {
		t.Errorf("title desc = %s", got)
	}
	// Non-title sorts put collapsed series after plain books, alphabetically.
	if got, _, _ := order("&sort=media.duration&desc=1"); got != "liC,liD,liA" {
		t.Errorf("duration desc = %s", got)
	}
	if got, _, sortBy := order("&sort=bogus"); got != "liA,liC,liD" || sortBy != nil {
		t.Errorf("invalid sort = %s sortBy=%v", got, sortBy)
	}
	if got, _, _ := order("&limit=2&page=1"); got != "liD" {
		t.Errorf("page 1 = %s", got)
	}
	// Filtering by the sub-series itself: its books belong to "Mega" too, and
	// a single collapsed entry is left uncollapsed with the uncollapsed total.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/libraries/lib1/items?collapseseries=1&filter=series.c3Vi", nil)
	h.Handle(w, r, user)
	if !strings.Contains(w.Body.String(), `"total":2`) || strings.Contains(w.Body.String(), `"collapsedSeries"`) {
		t.Errorf("sub-series alone = %s", w.Body.String())
	}
}
