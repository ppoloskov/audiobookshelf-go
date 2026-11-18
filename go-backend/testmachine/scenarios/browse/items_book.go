package browse

import (
	"net/http"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	srcItems     = "server/controllers/LibraryController.js:604-639"
	srcMiddle    = "server/controllers/LibraryController.js:1475-1498"
	srcBookQuery = "server/utils/queries/libraryItemsBookFilters.js:408-686"
	srcBookSort  = "server/utils/queries/libraryItemsBookFilters.js:244-286"
	srcBookGroup = "server/utils/queries/libraryItemsBookFilters.js:79-243"
	srcLibFilter = "server/utils/queries/libraryFilters.js:23-40"
)

// keysOf maps result ids back to fixture keys for readable failures; unknown
// ids stay as they are.
func keysOf(e *harness.Env, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		for key, value := range e.IDs {
			if value == id {
				name = key
			}
		}
		out = append(out, name)
	}
	return out
}

// expectKeys fails unless the page lists exactly the fixture keys in order.
func expectKeys(t *testing.T, e *harness.Env, what string, page itemPage, want ...string) {
	t.Helper()
	expectOrder(t, what, keysOf(e, page.ids()), want)
}

// expectKeySet fails unless the page lists exactly the fixture keys, any order.
func expectKeySet(t *testing.T, e *harness.Env, what string, page itemPage, want ...string) {
	t.Helper()
	got := keysOf(e, page.ids())
	if !sameSet(got, want) {
		t.Fatalf("%s: got %v, want the set %v", what, got, want)
	}
}

// titleRank is the plain title order of the five fixture books.
var titleRank = map[string]int{"ebook": 0, "disposable": 1, "plain": 1, "novel": 2, "saga1": 3, "saga2": 4, "podcast": 5}

// inTitleOrder returns keys sorted the way a title-sorted list shows them.
func inTitleOrder(keys []string) []string {
	out := slices.Clone(keys)
	slices.SortFunc(out, func(a, b string) int { return titleRank[a] - titleRank[b] })
	return out
}

func (s *suite) itemScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-SAMPLED", Case: "normal", State: "stateless", Assertion: "book-items-envelope-and-item-shape",
			Sources: []string{srcItems, "server/models/LibraryItem.js:294-328"},
			Claim:   "GET items answers the list envelope (echoed query, total, offset) with minified items whose fields match the fixture book, whatever the minified flag.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				page := getItems(t, e, token, bookLibrary, nil)
				expectKeySet(t, e, "default list", page, "saga1", "saga2", "novel", "ebook", "disposable")
				expectInt(t, "total", page.Total, 5)
				expectInt(t, "limit", page.Limit, 0)
				expectInt(t, "page", page.Page, 0)
				expectInt(t, "offset", page.Offset, 0)
				if page.MediaType != "book" || page.Minified || page.Collapse || page.Include != "" || page.SortDesc || page.SortBy != nil || page.FilterBy != nil {
					t.Fatalf("envelope echo wrong: %+v", page)
				}
				minified := getItems(t, e, token, bookLibrary, query("minified", "1"))
				if !minified.Minified {
					t.Fatalf("minified=1 must be echoed")
				}
				expectOrder(t, "minified flag leaves results alone", minified.ids(), page.ids())
				var saga1 listItem
				for _, item := range page.Results {
					if item.ID == e.IDs["saga1"] {
						saga1 = item
					}
				}
				meta := saga1.Media.Metadata
				if saga1.LibraryID != e.IDs[bookLibrary] || saga1.MediaType != "book" || saga1.IsMissing || saga1.IsInvalid || saga1.NumFiles != 4 || saga1.Size != 244064 {
					t.Fatalf("saga1 item fields wrong: %+v", saga1)
				}
				if meta.Title != "The Beginning" || meta.TitleIgnore != "Beginning, The" || meta.AuthorName != "Terry Fixture" || meta.AuthorNameLF != "Fixture, Terry" || meta.SeriesName != "Long Saga #1" || meta.Explicit || meta.Abridged || meta.Subtitle != nil {
					t.Fatalf("saga1 metadata wrong: %+v", meta)
				}
				media := saga1.Media
				if media.ID != "5e33de11-3e94-4c51-87d8-d06d2928ac38" || media.NumTracks == nil || *media.NumTracks != 3 || *media.NumAudio != 3 || *media.NumChapters != 3 || media.Duration == nil || int(*media.Duration*1e6) != 60186123 || media.Tags == nil || len(media.Tags) != 0 {
					t.Fatalf("saga1 media wrong: %+v", media)
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-AUTH-001", Case: "error", State: "stateless", Assertion: "items-unauthenticated-and-unknown-library",
			Sources: []string{srcMiddle, "server/Auth.js:322"},
			Claim:   "GET items without a token is 401, and an unknown library id is 404 for an unrestricted user.",
			Run: func(t *testing.T, e *harness.Env) {
				expectStatus(t, "no token", e.Get(t, "", libraryPath(e, bookLibrary, "/items")), http.StatusUnauthorized)
				expectStatus(t, "unknown library", e.Get(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000/items"), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-AUTH-001", Case: "error", State: "stateless", Assertion: "items-invalid-pagination-rejected",
			Sources: []string{"server/controllers/LibraryController.js:1486-1495"},
			Claim:   "A negative or fractional limit or page is rejected with 400 before any query runs.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				for _, values := range []map[string]string{{"limit": "-1"}, {"page": "-1"}, {"limit": "1.5"}, {"page": "0.5"}} {
					q := query()
					for key, value := range values {
						q.Set(key, value)
					}
					resp := e.Do(t, harness.Request{Path: libraryPath(e, bookLibrary, "/items"), Token: root, Query: q})
					expectStatus(t, "pagination "+q.Encode(), resp, http.StatusBadRequest)
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-AUTH-001", Case: "error", State: "stateless", Assertion: "items-library-access-denied",
			Sources: []string{srcMiddle, "server/models/User.js:646-650"},
			Claim:   "A user limited to the podcast library gets 403 on the book library items, even for an unknown id, and 200 on its own library.",
			Run: func(t *testing.T, e *harness.Env) {
				token := s.restrictedToken(t, e, "browse-podonly")
				expectStatus(t, "book library", e.Get(t, token, libraryPath(e, bookLibrary, "/items")), http.StatusForbidden)
				expectStatus(t, "unknown library", e.Get(t, token, "/api/libraries/00000000-0000-4000-8000-000000000000/items"), http.StatusForbidden)
				// Rejections are ordered: library access (403), library exists (404), pagination (400).
				bad := query("limit", "-1")
				expectStatus(t, "403 before 400", e.Do(t, harness.Request{Path: libraryPath(e, bookLibrary, "/items"), Token: token, Query: bad}), http.StatusForbidden)
				expectStatus(t, "404 before 400", e.Do(t, harness.Request{Path: "/api/libraries/00000000-0000-4000-8000-000000000000/items", Token: e.Token(t, "root"), Query: bad}), http.StatusNotFound)
				page := getItems(t, e, token, podcastLibrary, nil)
				expectKeys(t, e, "own library", page, "podcast")
			},
		},
	}
}

// sortCase is one sort request of the book library and its expected order.
type sortCase struct {
	sort string
	desc bool
	want []string
}

func (s *suite) sortScenarios() []harness.Scenario {
	cases := []sortCase{
		{"media.metadata.title", false, []string{"ebook", "disposable", "novel", "saga1", "saga2"}},
		{"media.metadata.title", true, []string{"saga2", "saga1", "novel", "disposable", "ebook"}},
		{"addedAt", false, []string{"disposable", "novel", "ebook", "saga2", "saga1"}},
		{"addedAt", true, []string{"saga1", "saga2", "ebook", "novel", "disposable"}},
		{"size", false, []string{"ebook", "disposable", "novel", "saga2", "saga1"}},
		{"size", true, []string{"saga1", "saga2", "novel", "disposable", "ebook"}},
		{"birthtimeMs", false, []string{"saga1", "saga2", "novel", "ebook", "disposable"}},
		{"mtimeMs", false, []string{"saga1", "saga2", "novel", "ebook", "disposable"}},
		{"media.duration", false, []string{"ebook", "disposable", "saga2", "novel", "saga1"}},
		{"media.duration", true, []string{"saga1", "novel", "saga2", "disposable", "ebook"}},
		{"media.metadata.publishedYear", false, []string{"disposable", "saga1", "novel", "ebook", "saga2"}},
		{"media.metadata.publishedYear", true, []string{"saga2", "ebook", "novel", "saga1", "disposable"}},
		{"media.metadata.authorName", false, []string{"ebook", "novel", "saga1", "saga2", "disposable"}},
		{"media.metadata.authorName", true, []string{"disposable", "saga2", "saga1", "novel", "ebook"}},
		{"media.metadata.authorNameLF", false, []string{"disposable", "ebook", "novel", "saga1", "saga2"}},
		{"media.metadata.authorNameLF", true, []string{"saga2", "saga1", "novel", "ebook", "disposable"}},
		{"birthtimeMs", true, []string{"disposable", "ebook", "novel", "saga2", "saga1"}},
		{"mtimeMs", true, []string{"disposable", "ebook", "novel", "saga2", "saga1"}},
		{"progress.createdAt", true, []string{"saga2", "ebook", "novel", "saga1", "disposable"}},
		{"progress", false, []string{"saga1", "novel", "ebook", "saga2", "disposable"}},
		{"progress", true, []string{"saga2", "ebook", "novel", "saga1", "disposable"}},
		{"progress.createdAt", false, []string{"saga1", "novel", "ebook", "saga2", "disposable"}},
		// Without a series filter the "sequence" sort falls back to the title (libraryItemsBookFilters.js:403).
		{"sequence", false, []string{"ebook", "disposable", "novel", "saga1", "saga2"}},
	}
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-SORT-001", Case: "normal", State: "stateless", Assertion: "book-items-sort-keys",
			Sources: []string{srcBookSort, "client/components/controls/LibrarySortSelect.vue:96-160"},
			Claim:   "Every sort key of the client's book sort menu, ascending and descending, yields the exact fixture order, and sort/desc are echoed.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				for _, c := range cases {
					q := query("sort", c.sort, "minified", "1")
					if c.desc {
						q.Set("desc", "1")
					}
					page := getItems(t, e, token, bookLibrary, q)
					expectKeys(t, e, "sort "+c.sort+" desc="+q.Get("desc"), page, c.want...)
					if page.SortBy == nil || *page.SortBy != c.sort || page.SortDesc != c.desc {
						t.Fatalf("sort echo wrong for %s: %+v", c.sort, page)
					}
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-SORT-001", Case: "edge", State: "stateless", Assertion: "book-items-sort-ties-random-and-unknown",
			Sources: []string{srcBookSort, "server/managers/ApiCacheManager.js:79-82"},
			Claim:   "Sorting by finishedAt puts the only finished book first, random keeps all five items, and an unknown or podcast-only sort key still lists all five items.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				page := getItems(t, e, token, bookLibrary, query("sort", "progress.finishedAt"))
				if len(page.Results) != 5 || page.Results[0].ID != e.IDs["novel"] {
					t.Fatalf("finishedAt order: %v", keysOf(e, page.ids()))
				}
				page = getItems(t, e, token, bookLibrary, query("sort", "progress.finishedAt", "desc", "1"))
				if len(page.Results) != 5 || page.Results[0].ID != e.IDs["novel"] {
					t.Fatalf("finishedAt desc: the only finished book must lead: %v", keysOf(e, page.ids()))
				}
				all := []string{"saga1", "saga2", "novel", "ebook", "disposable"}
				for _, sortKey := range []string{"random", "media.numTracks", "nonsense"} {
					page := getItems(t, e, token, bookLibrary, query("sort", sortKey))
					expectKeySet(t, e, "sort "+sortKey, page, all...)
					expectInt(t, "total "+sortKey, page.Total, 5)
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-SORT-001", Case: "edge", State: "stateless", Assertion: "book-items-sort-title-ignores-prefix-setting",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:250-256", "server/controllers/MiscController.js:247"},
			Claim:   "With the server setting sortingIgnorePrefix on, title sort orders by the title without its leading article, and off again restores plain order.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				token := e.Token(t, "listener")
				mustOK(t, "enable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page := getItems(t, e, token, bookLibrary, query("sort", "media.metadata.title", "limit", "10"))
				expectKeys(t, e, "ignore prefix", page, "saga1", "ebook", "disposable", "saga2", "novel")
				mustOK(t, "disable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
				page = getItems(t, e, token, bookLibrary, query("sort", "media.metadata.title", "limit", "11"))
				expectKeys(t, e, "plain", page, "ebook", "disposable", "novel", "saga1", "saga2")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-PAGING-001", Case: "normal", State: "stateless", Assertion: "book-items-pagination-pages",
			Sources: []string{srcItems, "server/utils/queries/libraryItemsBookFilters.js:344-368"},
			Claim:   "limit and page slice the sorted list, total stays the full count, offset is page*limit, and a page past the end is empty.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				for _, c := range []struct {
					page   string
					want   []string
					offset int
				}{
					{"0", []string{"disposable", "novel"}, 0},
					{"1", []string{"ebook", "saga2"}, 2},
					{"2", []string{"saga1"}, 4},
					{"9", nil, 18},
				} {
					page := getItems(t, e, token, bookLibrary, query("sort", "addedAt", "limit", "2", "page", c.page))
					expectKeys(t, e, "page "+c.page, page, c.want...)
					expectInt(t, "total", page.Total, 5)
					expectInt(t, "limit", page.Limit, 2)
					expectInt(t, "offset", page.Offset, c.offset)
				}
				page := getItems(t, e, token, bookLibrary, query("sort", "addedAt", "desc", "1", "limit", "3", "page", "1"))
				expectKeys(t, e, "desc page", page, "novel", "disposable")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-PAGING-001", Case: "edge", State: "stateless", Assertion: "book-items-limit-zero-lists-all",
			Sources: []string{srcItems, "server/utils/queries/libraryItemsBookFilters.js:344-346"},
			Claim:   "limit=0 or no limit lists every item and a page without a limit has offset 0.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				page := getItems(t, e, token, bookLibrary, query("sort", "addedAt", "limit", "0", "page", "3"))
				expectKeys(t, e, "limit 0", page, "disposable", "novel", "ebook", "saga2", "saga1")
				expectInt(t, "offset", page.Offset, 0)
			},
		},
	}
}
