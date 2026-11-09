package catalog

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/store"
)

// INDEPENDENT EVIDENCE. testdata/query_golden.json and personalized_golden.json
// were recorded from this Go code, so they only lock it against regressions.
// The expectations below are derived by hand from the fixture rows in
// queryFixture / seedShelfGolden and the upstream filter and sort definitions
// (server/utils/queries/libraryItemsBookFilters.js, libraryItemsPodcastFilters.js,
// libraryFilters.js): each case names the rule it follows.

func queryIDs(t *testing.T, h *Handler, user store.User, target string) ([]string, int) {
	t.Helper()
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", target, nil), user) || w.Code != 200 {
		t.Fatalf("%s: status %d body %s", target, w.Code, w.Body.String())
	}
	var body struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range body.Results {
		ids = append(ids, r.ID)
	}
	return ids, body.Total
}

func TestLibraryFiltersSelectExactlyTheMatchingItems(t *testing.T) {
	root := store.User{ID: "u1", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	h := queryFixture(t, false)
	// Fixture: i1..i6 = b1..b6. Order is not asserted for filters, only the set.
	cases := []struct {
		name, filter string
		want         []string
	}{
		{"genre fiction", "genres." + b64("fiction"), []string{"i1", "i3", "i6"}},
		{"genre without match", "genres." + b64("none"), nil},
		{"tag red", "tags." + b64("red"), []string{"i1", "i2"}},
		{"narrator Nora", "narrators." + b64("Nora"), []string{"i1", "i2"}},
		{"language en", "languages." + b64("en"), []string{"i1", "i3", "i4"}},
		{"publisher Pub A", "publishers." + b64("Pub A"), []string{"i1", "i3"}},
		{"author a1", "authors." + b64("a1"), []string{"i1", "i3", "i5"}},
		{"series s1", "series." + b64("s1"), []string{"i1", "i2"}},
		{"no series", "series." + b64("no-series"), []string{"i6"}},
		{"explicit flag", "explicit", []string{"i2"}},
		{"abridged flag", "abridged", []string{"i2"}},
		{"missing items are issues", "issues", []string{"i3"}},
		{"decade 1990", "publishedDecades." + b64("1990"), []string{"i1", "i2"}},
		{"ebook present", "ebooks." + b64("ebook"), []string{"i2", "i3"}},
		{"supplementary ebook files", "ebooks." + b64("supplementary"), []string{"i2"}},
		{"no audio tracks", "tracks." + b64("none"), []string{"i3", "i5"}},
		{"multiple tracks", "tracks." + b64("multi"), []string{"i2"}},
		{"single track", "tracks." + b64("single"), []string{"i1", "i4", "i6"}},
		{"missing authors", "missing." + b64("authors"), []string{"i6"}},
		{"missing asin", "missing." + b64("asin"), []string{"i2", "i4", "i6"}},
		// u1's progress rows: b1 5s, b2 finished, b3 ebook 50%, b4 zero, b5 finished (u2's row ignored).
		{"finished", "progress." + b64("finished"), []string{"i2", "i5"}},
		{"in progress", "progress." + b64("in-progress"), []string{"i1", "i3"}},
		// not-started: currentTime null or 0 and not finished (an ebook-only progress row still counts).
		{"not started", "progress." + b64("not-started"), []string{"i3", "i4", "i6"}},
		{"on the shelf of an open feed", "feed-open", []string{"i2"}},
		{"shared", "share-open", []string{"i3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ids, total := queryIDs(t, h, root, "/api/libraries/l1/items?filter="+c.filter)
			slices.Sort(ids)
			if !slices.Equal(ids, append([]string{}, c.want...)) && !(len(ids) == 0 && len(c.want) == 0) {
				t.Fatalf("ids %v, want %v", ids, c.want)
			}
			if total != len(c.want) {
				t.Fatalf("total %d, want %d", total, len(c.want))
			}
		})
	}
}

func TestLibrarySortsOrderItemsByTheirKey(t *testing.T) {
	root := store.User{ID: "u1", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	for _, tc := range []struct {
		name, query string
		ignore      bool
		want        []string
	}{
		{"added asc", "sort=addedAt&desc=0", false, []string{"i1", "i2", "i3", "i4", "i5", "i6"}},
		{"added desc", "sort=addedAt&desc=1", false, []string{"i6", "i5", "i4", "i3", "i2", "i1"}},
		// sizes: b1 100, b2 300, b3 200, b4 50, b5 400, b6 10
		{"size asc", "sort=size&desc=0", false, []string{"i6", "i4", "i1", "i3", "i2", "i5"}},
		// durations: b1 10, b2 20, b3 5, b4 30, b5 0, b6 15
		{"duration desc", "sort=media.duration&desc=1", false, []string{"i4", "i2", "i6", "i1", "i3", "i5"}},
		// Titles compare case-insensitively on the title column...
		{"title", "sort=media.metadata.title&desc=0", false, []string{"i3", "i6", "i2", "i4", "i5", "i1"}},
		// ...or on the title without its prefix when the server ignores prefixes.
		{"title ignoring prefix", "sort=media.metadata.title&desc=0", true, []string{"i1", "i6", "i2", "i4", "i5", "i3"}},
		// Series sequence within s2: b4 #1, b3 #3, b5 #2.5 -> 1, 2.5, 3.
		{"sequence in series", "sort=sequence&desc=0&filter=series." + b64("s2"), false, []string{"i4", "i5", "i3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := queryFixture(t, tc.ignore)
			ids, _ := queryIDs(t, h, root, "/api/libraries/l1/items?"+tc.query)
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("order %v, want %v", ids, tc.want)
			}
		})
	}
}

// Whatever the filter, an item the user may not see never appears.
func TestLibraryQueriesNeverLeakInaccessibleItems(t *testing.T) {
	restricted := store.User{ID: "u1", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["l1","l2"],"accessAllTags":false,"accessExplicitContent":false,"itemTagsSelected":["red","sci"],"selectedTagsNotAccessible":false}`)}
	h := queryFixture(t, false)
	// Tag allow-list red/sci and no explicit content: only b1 (red) in books, p1 (sci) in podcasts.
	for _, c := range []struct {
		target string
		want   []string
	}{
		{"/api/libraries/l1/items", []string{"i1"}},
		{"/api/libraries/l1/items?filter=" + "explicit", nil},
		{"/api/libraries/l1/items?sort=size&desc=1&collapseseries=1", []string{"i1"}},
		{"/api/libraries/l2/items", []string{"p1"}},
	} {
		ids, total := queryIDs(t, h, restricted, c.target)
		if !slices.Equal(ids, append([]string{}, c.want...)) && !(len(ids) == 0 && len(c.want) == 0) || total != len(c.want) {
			t.Errorf("%s: ids %v total %d, want %v", c.target, ids, total, c.want)
		}
	}
	denied := store.User{ID: "u2", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":false,"librariesAccessible":["l2"],"accessAllTags":true,"accessExplicitContent":true}`)}
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest("GET", "/api/libraries/l1/items", nil), denied)
	if w.Code != 403 && w.Code != 404 {
		t.Fatalf("a library the user cannot access answered %d", w.Code)
	}
}
