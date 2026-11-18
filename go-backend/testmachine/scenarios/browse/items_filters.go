package browse

import (
	"encoding/json"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// filterCase is one filter request and the fixture keys it selects, in title order.
type filterCase struct {
	group, value string
	want         []string
}

// bookFilterCases follow the enriched fixture: the groups of the client's
// filter menu (LibraryFilterSelect.vue) with listener progress.
func bookFilterCases(e *harness.Env, series string) []filterCase {
	none := []string{}
	return []filterCase{
		{"genres", "Fiction", []string{"novel"}},
		{"genres", "Mystery", []string{"ebook", "novel", "saga2"}},
		{"genres", "Fantasy", []string{"saga1", "saga2"}},
		{"genres", "Absent", none},
		{"tags", "alpha", []string{"novel", "saga1"}},
		{"tags", "beta", []string{"ebook", "novel"}},
		{"tags", "gamma", []string{"disposable"}},
		{"authors", "225b0a63-df12-4a28-9f49-8d86c4796d69", []string{"ebook", "novel"}},
		{"authors", "75e57a0b-a39d-4073-8501-e3b745ebd79d", []string{"saga1", "saga2"}},
		{"series", series, []string{"saga1", "saga2"}},
		{"series", "no-series", none},
		{"narrators", "Nora Narrator", []string{"novel", "saga2"}},
		{"narrators", "Vera Voice", []string{"novel"}},
		{"publishers", "Pub One", []string{"novel", "saga2"}},
		{"publishers", "Pub Two", []string{"saga1"}},
		{"publishedDecades", "1990", []string{"novel"}},
		{"publishedDecades", "2000", []string{"ebook"}},
		{"publishedDecades", "1980", []string{"saga1"}},
		{"languages", "English", []string{"novel", "saga1"}},
		{"languages", "en", []string{"ebook"}},
		{"progress", "finished", []string{"novel"}},
		{"progress", "in-progress", []string{"ebook", "saga1", "saga2"}},
		{"progress", "not-started", []string{"ebook", "disposable"}},
		{"progress", "not-finished", []string{"disposable", "ebook", "saga1", "saga2"}},
		{"progress", "audio-in-progress", []string{"saga1", "saga2"}},
		{"progress", "ebook-in-progress", []string{"ebook"}},
		{"progress", "ebook-finished", none},
		{"missing", "asin", []string{"disposable", "ebook", "saga1"}},
		{"missing", "isbn", []string{"disposable", "ebook", "saga1", "saga2"}},
		{"missing", "subtitle", []string{"disposable", "saga1", "saga2"}},
		{"missing", "publishedYear", []string{"disposable"}},
		{"missing", "description", []string{"disposable", "ebook", "novel", "saga1"}},
		{"missing", "publisher", []string{"disposable", "ebook"}},
		{"missing", "language", []string{"disposable", "saga2"}},
		{"missing", "cover", []string{"disposable", "ebook", "saga2"}},
		{"missing", "genres", []string{"disposable"}},
		{"missing", "tags", []string{"saga2"}},
		{"missing", "narrators", []string{"disposable", "ebook", "saga1"}},
		{"missing", "chapters", []string{"disposable", "ebook", "saga2"}},
		{"missing", "authors", none},
		{"missing", "series", none},
		{"tracks", "none", []string{"ebook"}},
		{"tracks", "single", []string{"disposable", "novel", "saga2"}},
		{"tracks", "multi", []string{"saga1"}},
		{"ebooks", "ebook", []string{"ebook"}},
		{"ebooks", "no-ebook", []string{"disposable", "novel", "saga1", "saga2"}},
		{"ebooks", "supplementary", none},
		{"ebooks", "no-supplementary", []string{"disposable", "ebook", "novel", "saga1", "saga2"}},
		{"abridged", "", []string{"novel"}},
		{"explicit", "", []string{"ebook"}},
		{"issues", "", none},
		{"feed-open", "", []string{"novel", "saga1"}},
		{"share-open", "", []string{"novel", "saga2"}},
	}
}

// seriesIDByName looks the series up in the series list of the library.
func seriesIDByName(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: libraryPath(e, bookLibrary, "/series"), Token: e.Token(t, "root"), Query: query("limit", "50")})
	mustOK(t, "series list", resp)
	var list struct {
		Results []struct{ ID, Name string } `json:"results"`
	}
	resp.JSON(t, &list)
	for _, series := range list.Results {
		if series.Name == name {
			return series.ID
		}
	}
	t.Fatalf("series %q not found", name)
	return ""
}

func (s *suite) filterScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-filter-groups",
			Sources: []string{srcLibFilter, srcBookGroup, "client/components/controls/LibraryFilterSelect.vue:160-260"},
			Claim:   "Every filter group the client sends (genres, tags, authors, series, narrators, publishers, publishedDecades, languages, progress, missing, tracks, ebooks, abridged, explicit, issues, feed-open, share-open) selects exactly the expected books, with total equal to the match count.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				series := seriesIDByName(t, e, "Long Saga")
				for _, c := range bookFilterCases(e, series) {
					filter := filterParam(c.group, c.value)
					page := getItems(t, e, token, bookLibrary, query("filter", filter, "sort", "media.metadata.title", "minified", "1"))
					expectKeys(t, e, "filter "+c.group+"."+c.value, page, inTitleOrder(c.want)...)
					expectInt(t, "total "+c.group+"."+c.value, page.Total, len(c.want))
					if page.FilterBy == nil || *page.FilterBy != filter {
						t.Fatalf("filterBy echo: %v want %s", page.FilterBy, filter)
					}
				}
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-filter-recent",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:316-320"},
			Claim:   "filter=recent lists the books created within the last 60 days.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				page := getItems(t, e, e.Token(t, "listener"), bookLibrary, query("filter", "recent", "sort", "media.metadata.title"))
				expectKeys(t, e, "recent excludes the 90 day old book", page, "ebook", "novel", "saga1", "saga2")
				expectInt(t, "total", page.Total, 4)
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-filter-progress-per-user",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:77-110", "server/utils/queries/libraryItemsBookFilters.js:299-310"},
			Claim:   "Progress filters follow the requesting user: the finisher has only the first saga book finished, root has no progress at all.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				value := filterParam("progress", "finished")
				page := getItems(t, e, e.Token(t, "finisher"), bookLibrary, query("filter", value))
				expectKeys(t, e, "finisher finished", page, "saga1")
				page = getItems(t, e, e.Token(t, "root"), bookLibrary, query("filter", value))
				expectKeys(t, e, "root finished", page)
				page = getItems(t, e, e.Token(t, "root"), bookLibrary, query("filter", filterParam("progress", "not-started"), "sort", "media.metadata.title"))
				expectKeys(t, e, "root not started", page, "ebook", "disposable", "novel", "saga1", "saga2")
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "edge", State: "stateless", Assertion: "book-items-filter-unknown-and-series-edges",
			Sources: []string{srcLibFilter, "server/utils/queries/libraryItemsBookFilters.js:223-243"},
			Claim:   "An unknown filter group selects everything, a series filter naming an unknown series selects nothing, and series.no-series selects books without a series.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				page := getItems(t, e, token, bookLibrary, query("filter", "bogus", "sort", "media.metadata.title"))
				expectKeys(t, e, "unknown group", page, "ebook", "disposable", "novel", "saga1", "saga2")
				page = getItems(t, e, token, bookLibrary, query("filter", filterParam("series", "00000000-0000-4000-8000-000000000000")))
				expectKeys(t, e, "unknown series", page)
			},
		},
		{
			Requirement: "route-r022", Invariant: "INV-R022-FILTER-001", Case: "normal", State: "stateless", Assertion: "book-items-series-filter-sequence-order",
			Sources: []string{"server/utils/queries/libraryItemsBookFilters.js:303-312", "server/utils/queries/libraryItemsBookFilters.js:388-394"},
			Claim:   "Filtering by a series orders by sequence (secondary default, or the sequence sort, ascending or descending) and each item carries its series sequence.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				token := e.Token(t, "listener")
				series := seriesIDByName(t, e, "Long Saga")
				filter := filterParam("series", series)
				page := getItems(t, e, token, bookLibrary, query("filter", filter))
				expectKeys(t, e, "default", page, "saga1", "saga2")
				page = getItems(t, e, token, bookLibrary, query("filter", filter, "sort", "sequence", "desc", "1"))
				expectKeys(t, e, "sequence desc", page, "saga2", "saga1")
				var ref *seriesRef
				if err := json.Unmarshal(page.Results[0].Media.Metadata.Series, &ref); err != nil {
					t.Fatalf("series ref: %v", err)
				}
				if ref == nil || ref.ID != series || ref.Name != "Long Saga" || ref.Sequence == nil || *ref.Sequence != "2" {
					t.Fatalf("series ref of saga2: %+v", ref)
				}
			},
		},
	}
}
