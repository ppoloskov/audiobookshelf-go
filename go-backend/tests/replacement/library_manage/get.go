package library_manage

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const getRoute = "server/routers/ApiRouter.js:71"

func getScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r019", Invariant: "INV-R019-SAMPLED", Case: "normal", State: "stateless", Assertion: "get-library-document",
			Sources: []string{getRoute, "server/controllers/LibraryController.js:1475-1498", "server/controllers/LibraryController.js:220-232"},
			Claim:   "GET /api/libraries/:id answers the library document, identical to its entry in the list, to an admin and to an all-access user.",
			Run: func(t *testing.T, e *harness.Env) {
				listed := listLibraries(t, e, e.Token(t, "root"))
				for _, who := range []string{"root", "listener"} {
					for _, want := range listed {
						if got := getLibrary(t, e, e.Token(t, who), want.ID); !reflect.DeepEqual(got, want) {
							t.Fatalf("%s: GET %s differs from list entry:\n%+v\n%+v", who, want.ID, got, want)
						}
					}
				}
			},
		},
		{
			Requirement: "route-r019", Invariant: "INV-R019-FILTERDATA-001", Case: "edge", State: "stateless", Assertion: "get-include-filterdata",
			Sources: []string{getRoute, "server/controllers/LibraryController.js:220-232", "server/utils/queries/libraryFilters.js"},
			Claim:   "GET /api/libraries/:id?include=filterdata answers {filterdata, issues, numUserPlaylists, library} with the fixture's filter values, the requesting user's playlist count and the library document.",
			Run: func(t *testing.T, e *harness.Env) {
				type filterdata struct {
					Authors []struct {
						Name string `json:"name"`
					} `json:"authors"`
					Genres, Languages, Narrators, PublishedDecades []string
					Series                                         []struct {
						Name string `json:"name"`
					} `json:"series"`
					Tags        []string `json:"tags"`
					BookCount   int      `json:"bookCount"`
					AuthorCount int      `json:"authorCount"`
					SeriesCount int      `json:"seriesCount"`
					Podcasts    int      `json:"podcastCount"`
					NumIssues   int      `json:"numIssues"`
					LoadedAt    int64    `json:"loadedAt"`
				}
				type answer struct {
					Filterdata       filterdata  `json:"filterdata"`
					Issues           int         `json:"issues"`
					NumUserPlaylists int         `json:"numUserPlaylists"`
					Library          libraryJSON `json:"library"`
				}
				read := func(who, id string) answer {
					resp := e.Get(t, e.Token(t, who), "/api/libraries/"+id+"?include=name,filterdata")
					requireStatus(t, resp, http.StatusOK, "filterdata")
					var got answer
					resp.JSON(t, &got)
					return got
				}
				books := read("listener", e.IDs["library"])
				names := []string{}
				for _, a := range books.Filterdata.Authors {
					names = append(names, a.Name)
				}
				slices.Sort(names)
				fd := books.Filterdata
				if !slices.Equal(names, []string{"Ada Example", "Terry Fixture", "Zed Disposable"}) || fd.BookCount != 5 || fd.AuthorCount != 3 || fd.SeriesCount != 1 ||
					len(fd.Series) != 1 || fd.Series[0].Name != "Long Saga" || fd.Podcasts != 0 || fd.NumIssues != 0 || books.Issues != 0 || fd.LoadedAt == 0 ||
					!slices.Equal(fd.Genres, []string{"Fiction"}) || !slices.Equal(fd.Languages, []string{"en"}) || !slices.Equal(fd.Narrators, []string{"Nora Narrator"}) {
					t.Fatalf("unexpected book filterdata: %+v", books)
				}
				wantPlaylists := e.Count(t, "SELECT COUNT(*) FROM playlists WHERE userId = ? AND libraryId = ?", e.IDs["listener"], e.IDs["library"])
				if wantPlaylists == 0 || books.NumUserPlaylists != wantPlaylists {
					t.Fatalf("numUserPlaylists %d, want %d", books.NumUserPlaylists, wantPlaylists)
				}
				if root := read("root", e.IDs["library"]); root.NumUserPlaylists != 0 {
					t.Fatalf("playlist count must be the requesting user's own: %d", root.NumUserPlaylists)
				}
				if !reflect.DeepEqual(books.Library, getLibrary(t, e, e.Token(t, "root"), e.IDs["library"])) {
					t.Fatalf("embedded library differs from the plain document")
				}
				podcasts := read("root", e.IDs["podcastLibrary"])
				if podcasts.Filterdata.Podcasts != 1 || podcasts.Filterdata.BookCount != 0 {
					t.Fatalf("unexpected podcast filterdata: %+v", podcasts.Filterdata)
				}
			},
		},
		{
			Requirement: "route-r019", Invariant: "INV-R019-AUTH-001", Case: "error", State: "stateless", Assertion: "get-unauthenticated-unknown-and-forbidden",
			Sources: []string{getRoute, "server/controllers/LibraryController.js:1475-1484", "server/models/User.js:646-650"},
			Claim:   "GET /api/libraries/:id answers 401 without a token, 404 for an unknown id as admin, and 403 for a limited user on a library it may not access, whether or not that id exists.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Get(t, "", "/api/libraries/"+e.IDs["library"]), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown as admin")
				requireStatus(t, e.Get(t, e.Token(t, "listener"), "/api/libraries/not-a-library"), http.StatusNotFound, "unknown as all-access user")
				limited := restrictedUser(t, e)
				requireStatus(t, e.Get(t, limited.Token, "/api/libraries/"+e.IDs["library"]), http.StatusForbidden, "inaccessible library")
				requireStatus(t, e.Get(t, limited.Token, "/api/libraries/00000000-0000-4000-8000-000000000000"), http.StatusForbidden, "unknown id for limited user")
				requireStatus(t, e.Get(t, limited.Token, "/api/libraries/"+e.IDs["podcastLibrary"]), http.StatusOK, "accessible library")
			},
		},
		{
			Requirement: "route-r019", Invariant: "INV-R019-PAGING-001", Case: "edge", State: "stateless", Assertion: "get-pagination-query-validated-by-middleware",
			Sources: []string{getRoute, "server/controllers/LibraryController.js:1487-1495"},
			Claim:   "GET /api/libraries/:id ignores valid limit and page values (non-numeric counts as 0) and answers 400 for a negative or fractional one.",
			Run: func(t *testing.T, e *harness.Env) {
				path := "/api/libraries/" + e.IDs["library"]
				root := e.Token(t, "root")
				for _, ok := range []string{"?limit=0", "?limit=5&page=2", "?limit=abc", "?page=x"} {
					requireStatus(t, e.Get(t, root, path+ok), http.StatusOK, ok)
				}
				for _, bad := range []string{"?limit=-1", "?page=-3", "?limit=1.5", "?page=0.5"} {
					requireStatus(t, e.Get(t, root, path+bad), http.StatusBadRequest, bad)
				}
			},
		},
		{
			Requirement: "route-r019", Invariant: "INV-R019-SAMPLED", Case: "normal", State: "stateless", Assertion: "get-reflects-permission-change",
			Sources: []string{getRoute, "server/controllers/LibraryController.js:1475-1479", "server/models/User.js:646-650"},
			Claim:   "GET /api/libraries/:id for a limited user turns from 403 to 200 once an admin adds the library to librariesAccessible.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "grant")
				limited := restrictedUser(t, e)
				requireStatus(t, e.Get(t, limited.Token, "/api/libraries/"+lib.ID), http.StatusForbidden, "before grant")
				resp := e.Patch(t, e.Token(t, "root"), "/api/users/"+limited.ID, map[string]any{"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"], lib.ID}})
				requireStatus(t, resp, http.StatusOK, "grant access")
				if got := getLibrary(t, e, limited.Token, lib.ID); !sameLibrary(got, lib) {
					t.Fatalf("granted user reads a different document: %+v", got)
				}
			},
		},
	}
}
