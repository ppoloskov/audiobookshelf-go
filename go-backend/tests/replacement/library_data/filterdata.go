package library_data

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	filterRoute  = "server/routers/ApiRouter.js:83"
	filterSrc    = "server/controllers/LibraryController.js:878-881"
	filterLoader = "server/utils/queries/libraryFilters.js:449-646"
	middleware   = "server/controllers/LibraryController.js:1475-1497"
)

// dbRefs returns {id, name} rows of a query ordered by name (natural order of the fixture names).
func dbRefs(t *testing.T, e *harness.Env, query string, args ...any) []idName {
	t.Helper()
	var out []idName
	for _, row := range e.Rows(t, query, args...) {
		out = append(out, idName{ID: row["id"].(string), Name: row["name"].(string)})
	}
	return out
}

func filterScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-CONTRACT", Case: "normal", State: "stateless", Assertion: "filterdata-book-library-fixture",
			Sources: []string{filterRoute, filterSrc, filterLoader},
			Claim:   "GET /api/libraries/:id/filterdata of the fixture book library answers its authors and series with ids, genres, narrators and languages, empty lists as arrays, the book, author and series counts, numIssues 0 and a loadedAt of now.",
			Run: func(t *testing.T, e *harness.Env) {
				before := time.Now()
				got := getFilterData(t, e, e.Token(t, "root"), e.IDs[bookLib])
				want := filterData{
					Authors:   dbRefs(t, e, "SELECT id, name FROM authors WHERE libraryId = ? ORDER BY name", e.IDs[bookLib]),
					Series:    dbRefs(t, e, "SELECT id, name FROM series WHERE libraryId = ? ORDER BY name", e.IDs[bookLib]),
					Genres:    []string{"Fiction"},
					Tags:      []string{},
					Narrators: []string{"Nora Narrator"}, Languages: []string{"en"}, Publishers: []string{}, PublishedDecades: []string{"2020"},
					BookCount: 5, AuthorCount: 3, SeriesCount: 1,
					LoadedAt: got.LoadedAt,
				}
				literal := []idName{{"225b0a63-df12-4a28-9f49-8d86c4796d69", "Ada Example"}, {"75e57a0b-a39d-4073-8501-e3b745ebd79d", "Terry Fixture"}, {"c4528461-64d4-4cc8-988a-713cde5b4f37", "Zed Disposable"}}
				if !reflect.DeepEqual(got.Authors, literal) || len(got.Series) != 1 || got.Series[0] != (idName{"e987a322-9111-4f79-8cb9-c25ee714e0d6", "Long Saga"}) {
					t.Fatalf("fixture authors and series: %+v %+v", got.Authors, got.Series)
				}
				if len(want.Authors) != 3 || len(want.Series) != 1 || want.Authors[0].Name != "Ada Example" || want.Series[0].Name != "Long Saga" {
					t.Fatalf("fixture authors/series changed: %+v %+v", want.Authors, want.Series)
				}
				if got.Tags == nil {
					got.Tags = []string{}
				}
				if got.Publishers == nil {
					got.Publishers = []string{}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("filterdata:\n got %+v\nwant %+v", got, want)
				}
				if loaded := time.UnixMilli(got.LoadedAt); loaded.Before(before.Add(-time.Minute)) || loaded.After(time.Now().Add(time.Second)) {
					t.Fatalf("loadedAt %v is not around now", loaded)
				}
			},
		},
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-PODCAST-001", Case: "edge", State: "stateless", Assertion: "filterdata-podcast-library-fixture",
			Sources: []string{filterRoute, filterSrc, "server/utils/queries/libraryFilters.js:478-541"},
			Claim:   "GET /api/libraries/:id/filterdata of a podcast library lists only the podcast genres, tags and languages, keeps the book-only lists empty arrays, and reports podcastCount with zero book, author and series counts.",
			Run: func(t *testing.T, e *harness.Env) {
				got := getFilterData(t, e, e.Token(t, "root"), e.IDs[podcastLib])
				want := filterData{
					Authors: []idName{}, Genres: []string{"Technology"}, Tags: []string{}, Series: []idName{}, Narrators: []string{},
					Languages: []string{}, Publishers: []string{}, PublishedDecades: []string{}, PodcastCount: 1, LoadedAt: got.LoadedAt,
				}
				normalizeFilter(&got)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("podcast filterdata:\n got %+v\nwant %+v", got, want)
				}
				if got.LoadedAt <= 0 {
					t.Fatal("loadedAt must be set")
				}
			},
		},
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-CACHE-001", Case: "normal", State: "stateless", Assertion: "filterdata-repeat-returns-cached-document",
			Sources: []string{filterRoute, filterLoader},
			Claim:   "A second GET of the filter data without writes in between answers the identical document, including the same loadedAt, as the filter data is cached for 30 minutes.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				first := e.Get(t, root, libPath(e.IDs[bookLib], "/filterdata"))
				second := e.Get(t, root, libPath(e.IDs[bookLib], "/filterdata"))
				requireStatus(t, first, http.StatusOK, "first")
				requireStatus(t, second, http.StatusOK, "second")
				var a, b filterData
				first.JSON(t, &a)
				second.JSON(t, &b)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("repeat differs:\n%+v\n%+v", a, b)
				}
			},
		},
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-AUTH-001", Case: "error", State: "stateless", Assertion: "filterdata-denied-unknown-and-bad-paging",
			Sources: []string{filterRoute, middleware},
			Claim:   "GET filterdata answers 401 without a token, 403 for a user without access to the library (also for an unknown id: access is checked before existence), 404 for an unknown id of an all-access user, and 400 for a negative limit before anything else.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.podcastOnly(t, e)
				book := libPath(e.IDs[bookLib], "/filterdata")
				requireStatus(t, e.Get(t, "", book), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, limited.Token, book), http.StatusForbidden, "limited user")
				requireStatus(t, e.Get(t, limited.Token, libPath(missingID, "/filterdata")), http.StatusForbidden, "limited user, unknown id")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/filterdata")), http.StatusNotFound, "unknown id")
				requireStatus(t, e.Get(t, e.Token(t, "root"), book+"?limit=-1"), http.StatusBadRequest, "negative limit")
				requireStatus(t, e.Get(t, e.Token(t, "root"), book+"?page=1.5"), http.StatusBadRequest, "fractional page")
				requireStatus(t, e.Get(t, limited.Token, libPath(e.IDs[podcastLib], "/filterdata")), http.StatusOK, "limited user on its library")
			},
		},
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-ENRICHED-001", Case: "normal", State: "stateless", Assertion: "filterdata-enriched-values-for-every-user",
			Sources: []string{filterRoute, filterSrc, filterLoader, "server/controllers/LibraryItemController.js:170-300"},
			Claim:   "After items are updated through the API and the server restarted, the filter data lists every distinct genre, tag, narrator, publisher, language and published decade sorted naturally, all series and authors with ids, the counts, and is identical for an explicit-content-restricted user and a tag-restricted user.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				got := getFilterData(t, e, root, e.IDs[bookLib])
				want := filterData{
					Authors:   dbRefs(t, e, "SELECT id, name FROM authors WHERE libraryId = ? ORDER BY name", e.IDs[bookLib]),
					Series:    nil,
					Genres:    []string{"Fantasy", "Fiction", "Mystery"},
					Tags:      []string{"alpha", "beta", "gamma"},
					Narrators: []string{"Nora Narrator", "Vera Voice"}, Languages: []string{"en", "English"},
					Publishers: []string{"Pub One", "Pub Two"}, PublishedDecades: []string{"1980", "1990", "2000", "2010"},
					BookCount: 5, AuthorCount: 3, SeriesCount: 3, LoadedAt: got.LoadedAt,
				}
				for _, name := range []string{"Gone Series", "Long Saga", "Solo Series"} {
					rows := dbRefs(t, e, "SELECT id, name FROM series WHERE libraryId = ? AND name = ?", e.IDs[bookLib], name)
					if len(rows) != 1 {
						t.Fatalf("series %q rows: %v", name, rows)
					}
					want.Series = append(want.Series, rows[0])
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("enriched filterdata:\n got %+v\nwant %+v", got, want)
				}
				tagged := s.createUser(t, e, "data-tagged", `"permissions":{"accessAllTags":false,"accessExplicitContent":false},"itemTagsSelected":["alpha"]`)
				for name, token := range map[string]string{"finisher": e.Token(t, "finisher"), "listener": e.Token(t, "listener"), "tag-restricted": tagged.Token} {
					other := getFilterData(t, e, token, e.IDs[bookLib])
					if !reflect.DeepEqual(other, got) {
						t.Fatalf("%s sees a different filterdata:\n%+v\n%+v", name, other, got)
					}
				}
			},
		},
		{
			Requirement: "route-r030", Invariant: "INV-ROUTE-R030-CACHE-001", Case: "edge", State: "stateless", Assertion: "filterdata-series-and-author-added-by-item-update",
			Sources: []string{filterRoute, filterLoader, "server/controllers/LibraryItemController.js:236-270", "server/Database.js:551-575"},
			Claim:   "Updating an item through the API with a new series and a new author adds both, with their new ids, to the cached filter data without a restart, and the filter data keeps its loadedAt.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				root := e.Token(t, "root")
				before := getFilterData(t, e, root, e.IDs[bookLib])
				patchMedia(t, e, e.IDs["saga1"], `{"metadata":{"series":[{"name":"Fresh Series","sequence":"3"}],"authors":[{"name":"Fresh Author"}]}}`)
				series := dbRefs(t, e, "SELECT id, name FROM series WHERE libraryId = ? AND name = 'Fresh Series'", e.IDs[bookLib])
				authors := dbRefs(t, e, "SELECT id, name FROM authors WHERE libraryId = ? AND name = 'Fresh Author'", e.IDs[bookLib])
				if len(series) != 1 || len(authors) != 1 {
					t.Fatalf("rows not created: %v %v", series, authors)
				}
				after := getFilterData(t, e, root, e.IDs[bookLib])
				if after.LoadedAt != before.LoadedAt {
					t.Fatalf("cached filter data must keep loadedAt: %d -> %d", before.LoadedAt, after.LoadedAt)
				}
				if !containsRef(after.Series, series[0]) || !containsRef(after.Authors, authors[0]) {
					t.Fatalf("new series %v and author %v missing from %+v", series[0], authors[0], after)
				}
				if len(after.Series) != len(before.Series)+1 || len(after.Authors) != len(before.Authors)+1 {
					t.Fatalf("series %d -> %d, authors %d -> %d", len(before.Series), len(after.Series), len(before.Authors), len(after.Authors))
				}
			},
		},
	}
}

func containsRef(list []idName, ref idName) bool {
	for _, item := range list {
		if item == ref {
			return true
		}
	}
	return false
}

// normalizeFilter turns nil lists into empty ones so empty and absent compare equal after the array check.
func normalizeFilter(d *filterData) {
	if d.Authors == nil {
		d.Authors = []idName{}
	}
	if d.Series == nil {
		d.Series = []idName{}
	}
	for _, list := range []*[]string{&d.Genres, &d.Tags, &d.Narrators, &d.Languages, &d.Publishers, &d.PublishedDecades} {
		if *list == nil {
			*list = []string{}
		}
	}
}
