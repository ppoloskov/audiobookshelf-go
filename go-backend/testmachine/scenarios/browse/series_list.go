package browse

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	third = "A Third Saga"
	long  = "Long Saga"
	solo  = "Solo Series"
)

func (s *suite) seriesListScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r025", Invariant: "INV-R025-SAMPLED", Case: "normal", State: "stateless", Assertion: "series-list-envelope-and-books",
			Sources: []string{srcSeriesList, srcSeriesQuery + ":153-222"},
			Claim:   "GET series answers the paged envelope with each series, its minified books ordered by numeric sequence, and totals.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				page := getSeriesPage(t, e, e.Token(t, "listener"), query("limit", "10", "sort", "name", "minified", "1"))
				expectOrder(t, "names", page.names(), []string{third, long, solo})
				expectInt(t, "total", page.Total, 3)
				expectInt(t, "limit", page.Limit, 10)
				expectInt(t, "page", page.Page, 0)
				if !page.Minified || page.SortBy == nil || *page.SortBy != "name" || page.SortDesc || page.Include != "" {
					t.Fatalf("envelope echo: %+v", page)
				}
				expectOrder(t, "third saga books (numeric sequence 2 before 10)", bookKeys(e, page.Results[0]), []string{"ebook", "disposable"})
				expectOrder(t, "long saga books", bookKeys(e, page.Results[1]), []string{"saga1", "saga2"})
				expectOrder(t, "solo books", bookKeys(e, page.Results[2]), []string{"novel"})
				long := page.Results[1]
				if long.LibraryID != e.IDs[bookLibrary] || long.NameIgnore != "Long Saga" || long.Description != nil || long.AddedAt == 0 || long.UpdatedAt == 0 {
					t.Fatalf("series fields: %+v", long)
				}
				if long.Books[0].Media.Metadata.Title != "The Beginning" || long.Books[0].Media.NumTracks == nil {
					t.Fatalf("series book is a minified item: %+v", long.Books[0])
				}
				if page.Results[2].Duration != nil {
					t.Fatalf("totalDuration only appears with that sort: %v", page.Results[2].Duration)
				}
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-SORT-001", Case: "normal", State: "stateless", Assertion: "series-list-sort-keys",
			Sources: []string{srcSeriesQuery + ":122-184", "client/components/app/BookShelfToolbar.vue:156-185"},
			Claim:   "Each series sort key of the client menu orders the three fixture series as expected, ascending and descending where the order has no ties.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				for _, c := range []struct {
					sort string
					desc bool
					want []string
				}{
					{"name", false, []string{third, long, solo}},
					{"name", true, []string{solo, long, third}},
					{"addedAt", false, []string{long, solo, third}},
					{"addedAt", true, []string{third, solo, long}},
					{"lastBookAdded", false, []string{solo, third, long}},
					{"lastBookAdded", true, []string{long, third, solo}},
					{"lastBookUpdated", false, []string{solo, long, third}},
					{"lastBookUpdated", true, []string{third, long, solo}},
					{"totalDuration", false, []string{third, solo, long}},
					{"totalDuration", true, []string{long, solo, third}},
				} {
					q := query("limit", "10", "sort", c.sort)
					if c.desc {
						q.Set("desc", "1")
					}
					page := getSeriesPage(t, e, token, q)
					expectOrder(t, "series sort "+q.Encode(), page.names(), c.want)
					if page.SortDesc != c.desc {
						t.Fatalf("sortDesc echo")
					}
				}
				asc := getSeriesPage(t, e, token, query("limit", "10", "sort", "numBooks")).names()
				desc := getSeriesPage(t, e, token, query("limit", "10", "sort", "numBooks", "desc", "1")).names()
				if asc[0] != solo || desc[2] != solo {
					t.Fatalf("numBooks: single-book series must be first ascending and last descending: %v %v", asc, desc)
				}
				page := getSeriesPage(t, e, token, query("limit", "10", "sort", "totalDuration"))
				if page.Results[2].Duration == nil || *page.Results[2].Duration < 90.18 || *page.Results[2].Duration > 90.19 {
					t.Fatalf("totalDuration of long saga: %v", page.Results[2].Duration)
				}
				random := getSeriesPage(t, e, token, query("limit", "10", "sort", "random"))
				expectSet(t, "random", random.names(), []string{third, long, solo})
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-SORT-001", Case: "edge", State: "stateless", Assertion: "series-list-name-sort-ignores-prefix-setting",
			Sources: []string{srcSeriesQuery + ":134-139"},
			Claim:   "With sortingIgnorePrefix on, name sort uses nameIgnorePrefix, so A Third Saga sorts after Solo Series.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				mustOK(t, "enable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":true}`))
				page := getSeriesPage(t, e, e.Token(t, "listener"), query("limit", "10", "sort", "name", "page", "0"))
				expectOrder(t, "ignore prefix", page.names(), []string{long, solo, "A Third Saga"})
				mustOK(t, "disable", patchJSON(t, e, root, "/api/settings", `{"sortingIgnorePrefix":false}`))
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-PAGING-001", Case: "edge", State: "stateless", Assertion: "series-list-paging",
			Sources: []string{srcSeriesList, srcSeriesQuery + ":153-158"},
			Claim:   "limit and page slice the series list with the full total, and a page past the end is empty.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				page := getSeriesPage(t, e, token, query("limit", "2", "page", "0", "sort", "name"))
				expectOrder(t, "page 0", page.names(), []string{third, long})
				page = getSeriesPage(t, e, token, query("limit", "2", "page", "1", "sort", "name"))
				expectOrder(t, "page 1", page.names(), []string{solo})
				expectInt(t, "total", page.Total, 3)
				page = getSeriesPage(t, e, token, query("limit", "2", "page", "5", "sort", "name"))
				expectOrder(t, "page 5", page.names(), []string{})
				expectInt(t, "total", page.Total, 3)
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-FILTER-001", Case: "normal", State: "stateless", Assertion: "series-list-filter-groups",
			Sources: []string{srcSeriesQuery + ":32-120"},
			Claim:   "The series filter groups genres, tags, authors, narrators, publishers, languages and progress select the series with a matching book, per user for progress; the all filter and unknown filters select all.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				all := []string{third, long, solo}
				for _, c := range []struct {
					group, value string
					want         []string
				}{
					{"genres", "Fantasy", []string{long}},
					{"genres", "Mystery", all},
					{"genres", "Absent", nil},
					{"tags", "gamma", []string{third}},
					{"tags", "alpha", []string{long, solo}},
					{"authors", "225b0a63-df12-4a28-9f49-8d86c4796d69", []string{third, solo}},
					{"narrators", "Vera Voice", []string{solo}},
					{"publishers", "Pub Two", []string{long}},
					{"languages", "English", []string{long, solo}},
					{"progress", "finished", []string{solo}},
					{"progress", "not-finished", []string{third, long}},
					{"progress", "not-started", []string{third}},
					{"progress", "in-progress", []string{third, long}},
				} {
					page := getSeriesPage(t, e, token, query("limit", "10", "sort", "name", "filter", filterParam(c.group, c.value)))
					expectOrder(t, "series filter "+c.group+"."+c.value, page.names(), append([]string{}, c.want...))
					expectInt(t, "total", page.Total, len(c.want))
				}
				for _, filter := range []string{"all", "nonsense"} {
					page := getSeriesPage(t, e, token, query("limit", "10", "sort", "name", "filter", filter))
					expectOrder(t, "filter "+filter, page.names(), all)
				}
				finisher := getSeriesPage(t, e, e.Token(t, "finisher"), query("limit", "10", "sort", "name", "filter", filterParam("progress", "in-progress")))
				expectOrder(t, "finisher in-progress", finisher.names(), []string{})
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-AUTH-001", Case: "error", State: "stateless", Assertion: "series-list-auth-and-pagination-errors",
			Sources: []string{srcMiddle},
			Claim:   "The series list is 401 without a token, 404 for an unknown library, 403 outside the user's libraries and 400 for a negative limit.",
			Run: func(t *testing.T, e *harness.Env) {
				path := libraryPath(e, bookLibrary, "/series")
				expectStatus(t, "no token", e.Get(t, "", path), http.StatusUnauthorized)
				expectStatus(t, "unknown", e.Get(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000/series"), http.StatusNotFound)
				expectStatus(t, "podonly", e.Get(t, s.restrictedToken(t, e, "browse-podonly"), path), http.StatusForbidden)
				resp := e.Do(t, harness.Request{Path: path, Token: e.Token(t, "root"), Query: query("limit", "-3")})
				expectStatus(t, "negative limit", resp, http.StatusBadRequest)
			},
		},
		{
			Requirement: "route-r025", Invariant: "INV-R025-RESTRICT-001", Case: "normal", State: "stateless", Assertion: "series-list-user-restrictions",
			Sources: []string{srcSeriesQuery + ":97-120", srcSeriesQuery + ":161-180,208-214"},
			Claim:   "Series lists show only series with a book the user may access and list only those books: explicit, tag-limited and tag-excluded users see different series and books.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				q := query("limit", "10", "sort", "name")
				finisher := getSeriesPage(t, e, e.Token(t, "finisher"), q)
				expectOrder(t, "finisher", finisher.names(), []string{third, long, solo})
				expectOrder(t, "finisher third books", bookKeys(e, finisher.Results[0]), []string{"disposable"})
				tagged := getSeriesPage(t, e, s.restrictedToken(t, e, "browse-tagged"), q)
				expectOrder(t, "tagged", tagged.names(), []string{long, solo})
				expectInt(t, "tagged total", tagged.Total, 2)
				expectOrder(t, "tagged long books", bookKeys(e, tagged.Results[0]), []string{"saga1"})
				notag := getSeriesPage(t, e, s.restrictedToken(t, e, "browse-notag"), q)
				expectOrder(t, "notag", notag.names(), []string{third, long})
				expectOrder(t, "notag third books", bookKeys(e, notag.Results[0]), []string{"disposable"})
			},
		},
	}
}
