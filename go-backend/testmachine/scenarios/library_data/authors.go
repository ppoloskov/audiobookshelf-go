package library_data

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	authorsRoute = "server/routers/ApiRouter.js:86"
	authorsSrc   = "server/controllers/LibraryController.js:1021-1105"
)

// authorJSON is an author of GET /api/libraries/:id/authors.
type authorJSON struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	NumBooks    int     `json:"numBooks"`
	LastFirst   string  `json:"lastFirst"`
}

// authorPage is the paginated answer; Authors is the unpaginated one.
type authorPage struct {
	Results  []authorJSON `json:"results"`
	Authors  []authorJSON `json:"authors"`
	Total    *int         `json:"total"`
	Limit    *int         `json:"limit"`
	Page     *int         `json:"page"`
	SortBy   *string      `json:"sortBy"`
	SortDesc *bool        `json:"sortDesc"`
	FilterBy *string      `json:"filterBy"`
	Minified *bool        `json:"minified"`
	Include  *string      `json:"include"`
}

func getAuthors(t *testing.T, e *harness.Env, token, libraryID, query string) authorPage {
	t.Helper()
	resp := e.Get(t, token, libPath(libraryID, "/authors")+query)
	requireStatus(t, resp, http.StatusOK, "authors "+query)
	var page authorPage
	resp.JSON(t, &page)
	return page
}

func authorNames(list []authorJSON) []string {
	names := make([]string, len(list))
	for i, a := range list {
		names[i] = a.Name
	}
	return names
}

func authorCounts(list []authorJSON) map[string]int {
	counts := map[string]int{}
	for _, a := range list {
		counts[a.Name] = a.NumBooks
	}
	return counts
}

// fixtureAuthors are the literal rows of the seeded authors.
var fixtureAuthors = map[string]authorJSON{
	"Ada Example":    {ID: "225b0a63-df12-4a28-9f49-8d86c4796d69", Name: "Ada Example", AddedAt: 1793709985752, UpdatedAt: 1793709985752, NumBooks: 2, LastFirst: "Example, Ada"},
	"Terry Fixture":  {ID: "75e57a0b-a39d-4073-8501-e3b745ebd79d", Name: "Terry Fixture", AddedAt: 1793709985981, UpdatedAt: 1793709985981, NumBooks: 2, LastFirst: "Fixture, Terry"},
	"Zed Disposable": {ID: "c4528461-64d4-4cc8-988a-713cde5b4f37", Name: "Zed Disposable", AddedAt: 1793709985265, UpdatedAt: 1793709985265, NumBooks: 1, LastFirst: "Disposable, Zed"},
}

func authorScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-CONTRACT", Case: "normal", State: "stateless", Assertion: "authors-unpaginated-fixture",
			Sources: []string{authorsRoute, authorsSrc, "server/models/Author.js:177-199"},
			Claim:   "GET /api/libraries/:id/authors without limit and page answers {authors} with every fixture author: id, asin, name, description, imagePath, libraryId, addedAt, updatedAt, numBooks and lastFirst.",
			Run: func(t *testing.T, e *harness.Env) {
				page := getAuthors(t, e, e.Token(t, "root"), e.IDs[bookLib], "")
				if page.Results != nil || page.Total != nil || len(page.Authors) != 3 {
					t.Fatalf("unpaginated answer: %+v", page)
				}
				for _, got := range page.Authors {
					want := fixtureAuthors[got.Name]
					want.LibraryID = e.IDs[bookLib]
					if got != want {
						t.Fatalf("author %s:\n got %+v\nwant %+v", got.Name, got, want)
					}
				}
				for _, query := range []string{"?limit=2", "?limit=0&page=0", "?page=1"} {
					other := getAuthors(t, e, e.Token(t, "listener"), e.IDs[bookLib], query)
					if other.Results != nil || len(other.Authors) != 3 {
						t.Fatalf("%s must not paginate: %+v", query, other)
					}
				}
			},
		},
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-PAGING-001", Case: "normal", State: "stateless", Assertion: "authors-paginated-sorted-pages",
			Sources: []string{authorsRoute, authorsSrc},
			Claim:   "With limit and page the answer is {results, total, limit, page, sortBy, sortDesc, filterBy, minified, include}; two pages of two cover the three authors in the requested order and total stays 3.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				id := e.IDs[bookLib]
				cases := []struct {
					query       string
					first, next []string
				}{
					{"sort=name", []string{"Ada Example", "Terry Fixture"}, []string{"Zed Disposable"}},
					{"sort=name&desc=1", []string{"Zed Disposable", "Terry Fixture"}, []string{"Ada Example"}},
					{"sort=lastFirst", []string{"Zed Disposable", "Ada Example"}, []string{"Terry Fixture"}},
					{"sort=addedAt", []string{"Zed Disposable", "Ada Example"}, []string{"Terry Fixture"}},
					{"sort=updatedAt&desc=1", []string{"Terry Fixture", "Ada Example"}, []string{"Zed Disposable"}},
				}
				for _, c := range cases {
					p0 := getAuthors(t, e, root, id, "?limit=2&page=0&"+c.query)
					p1 := getAuthors(t, e, root, id, "?limit=2&page=1&"+c.query)
					requireEqual(t, c.query+" page 0", authorNames(p0.Results), c.first)
					requireEqual(t, c.query+" page 1", authorNames(p1.Results), c.next)
					if p0.Total == nil || *p0.Total != 3 || p1.Total == nil || *p1.Total != 3 || p0.Authors != nil {
						t.Fatalf("%s: total must be 3 on both pages: %+v %+v", c.query, p0.Total, p1.Total)
					}
					if *p0.Limit != 2 || *p0.Page != 0 || *p1.Page != 1 {
						t.Fatalf("%s: limit/page echo wrong", c.query)
					}
				}
				p := getAuthors(t, e, root, id, "?limit=2&page=0&sort=name&desc=1&filter=abc&minified=1&include=books")
				if *p.SortBy != "name" || !*p.SortDesc || *p.FilterBy != "abc" || !*p.Minified || *p.Include != "books" {
					t.Fatalf("echoed parameters: %+v", p)
				}
				plain := getAuthors(t, e, root, id, "?limit=2&page=0")
				if plain.SortBy != nil || plain.FilterBy != nil || plain.Include != nil || *plain.SortDesc || *plain.Minified {
					t.Fatalf("defaults: %+v", plain)
				}
				// numBooks is sorted after the query and paged afterwards
				asc := getAuthors(t, e, root, id, "?limit=2&page=0&sort=numBooks")
				asc1 := getAuthors(t, e, root, id, "?limit=2&page=1&sort=numBooks")
				desc := getAuthors(t, e, root, id, "?limit=2&page=0&sort=numBooks&desc=1")
				if asc.Results[0].Name != "Zed Disposable" || len(asc.Results) != 2 || len(asc1.Results) != 1 || asc1.Results[0].NumBooks != 2 {
					t.Fatalf("numBooks asc: %v %v", authorNames(asc.Results), authorNames(asc1.Results))
				}
				if desc.Results[0].NumBooks != 2 || desc.Results[1].NumBooks != 2 || desc.Total == nil || *desc.Total != 3 {
					t.Fatalf("numBooks desc: %v", authorNames(desc.Results))
				}
			},
		},
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-VISIBILITY-001", Case: "edge", State: "stateless", Assertion: "authors-zero-book-author-only-for-admins",
			Sources: []string{authorsRoute, "server/controllers/LibraryController.js:1054-1073", "server/controllers/AuthorController.js"},
			Claim:   "An author with a description and no books is listed with numBooks 0 for an admin and left out for a non-admin user.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newScratchLibrary(t, e, "authors-zero", nil, nil)
				source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me")
				copyDir(t, source, filepath.Join(lib.Root, "One", "Book One"))
				copyDir(t, source, filepath.Join(lib.Root, "Two", "Book Two"))
				lib.scan(t, e, false, 2)
				var one, two string
				for _, item := range itemRows(t, e, lib.ID) {
					if strings.HasSuffix(item.Path, "Book One") {
						one = item.ID
					} else {
						two = item.ID
					}
				}
				patchMedia(t, e, one, `{"metadata":{"authors":[{"name":"Keeper"}]}}`)
				patchMedia(t, e, two, `{"metadata":{"authors":[{"name":"Described"}]}}`)
				rows := e.Rows(t, "SELECT id FROM authors WHERE libraryId = ? AND name = 'Described'", lib.ID)
				if len(rows) != 1 {
					t.Fatalf("author Described: %v", rows)
				}
				root := e.Token(t, "root")
				resp := e.Patch(t, root, "/api/authors/"+rows[0]["id"].(string), map[string]any{"description": "stays without books", "asin": "B000ASIN001"})
				requireStatus(t, resp, http.StatusOK, "describe author")
				patchMedia(t, e, two, `{"metadata":{"authors":[{"name":"Keeper"}]}}`)
				admin := getAuthors(t, e, root, lib.ID, "")
				requireEqual(t, "admin view", authorCounts(admin.Authors), map[string]int{"Keeper": 2, "Described": 0})
				for _, author := range admin.Authors {
					if author.Name == "Described" && (author.Description == nil || *author.Description != "stays without books" || author.ASIN == nil || *author.ASIN != "B000ASIN001") {
						t.Fatalf("description and asin must be listed: %+v", author)
					}
				}
				user := getAuthors(t, e, e.Token(t, "listener"), lib.ID, "")
				requireEqual(t, "listener view", authorCounts(user.Authors), map[string]int{"Keeper": 2})
				sorted := getAuthors(t, e, root, lib.ID, "?limit=1&page=0&sort=numBooks")
				if len(sorted.Results) != 1 || sorted.Results[0].Name != "Described" || *sorted.Total != 2 {
					t.Fatalf("numBooks asc first page: %+v", sorted)
				}
			},
		},
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-VISIBILITY-001", Case: "edge", State: "stateless", Assertion: "authors-explicit-and-tag-rules-per-user",
			Sources: []string{authorsRoute, "server/controllers/LibraryController.js:1054-1073", "server/utils/queries/libraryItemsBookFilters.js:17-43"},
			Claim:   "A user without explicit-content access does not count explicit books, a tag user in allow mode counts only books with a selected tag and one in deny mode only books without; authors left with no visible book are not listed, while an admin sees every count.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := e.IDs[bookLib]
				admin := getAuthors(t, e, e.Token(t, "root"), id, "")
				requireEqual(t, "admin counts", authorCounts(admin.Authors), map[string]int{"Ada Example": 2, "Terry Fixture": 2, "Zed Disposable": 1})
				// enrichment: ebook (Ada) is explicit; tags alpha: novel (Ada), saga1 (Terry); gamma: disposable (Zed)
				requireEqual(t, "finisher (no explicit content)", authorCounts(getAuthors(t, e, e.Token(t, "finisher"), id, "").Authors), map[string]int{"Ada Example": 1, "Terry Fixture": 2, "Zed Disposable": 1})
				allow := s.createUser(t, e, "data-tag-allow", `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":false,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`)
				requireEqual(t, "tag allow mode", authorCounts(getAuthors(t, e, allow.Token, id, "").Authors), map[string]int{"Ada Example": 1, "Terry Fixture": 1})
				deny := s.createUser(t, e, "data-tag-deny", `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":true,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`)
				requireEqual(t, "tag deny mode", authorCounts(getAuthors(t, e, deny.Token, id, "").Authors), map[string]int{"Ada Example": 1, "Terry Fixture": 1, "Zed Disposable": 1})
				paged := getAuthors(t, e, allow.Token, id, "?limit=1&page=1&sort=name")
				if len(paged.Results) != 1 || paged.Results[0].Name != "Terry Fixture" || *paged.Total != 2 {
					t.Fatalf("restricted pagination: %+v", paged)
				}
			},
		},
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-PODCAST-001", Case: "edge", State: "stateless", Assertion: "authors-podcast-library-is-empty",
			Sources: []string{authorsRoute, authorsSrc},
			Claim:   "GET authors of a podcast library answers an empty authors list, and an empty page with total 0 when paginated.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				plain := getAuthors(t, e, root, e.IDs[podcastLib], "")
				paged := getAuthors(t, e, root, e.IDs[podcastLib], "?limit=5&page=0")
				raw := e.Get(t, root, libPath(e.IDs[podcastLib], "/authors"))
				var body map[string]json.RawMessage
				raw.JSON(t, &body)
				if string(body["authors"]) != "[]" || len(plain.Authors) != 0 || len(paged.Results) != 0 || paged.Total == nil || *paged.Total != 0 {
					t.Fatalf("podcast authors: %s %+v", raw.Body, paged)
				}
			},
		},
		{
			Requirement: "route-r033", Invariant: "INV-ROUTE-R033-AUTH-001", Case: "error", State: "stateless", Assertion: "authors-denied-unknown-and-bad-paging",
			Sources: []string{authorsRoute, middleware},
			Claim:   "GET authors answers 401 without a token, 403 without library access (also for an unknown id), 404 for an unknown id, and 400 for a negative limit or a fractional page.",
			Run: func(t *testing.T, e *harness.Env) {
				limited := s.podcastOnly(t, e)
				path := libPath(e.IDs[bookLib], "/authors")
				requireStatus(t, e.Get(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Get(t, limited.Token, path), http.StatusForbidden, "limited")
				requireStatus(t, e.Get(t, limited.Token, libPath(missingID, "/authors")), http.StatusForbidden, "limited unknown")
				requireStatus(t, e.Get(t, e.Token(t, "root"), libPath(missingID, "/authors")), http.StatusNotFound, "unknown")
				for _, query := range []string{"?limit=-1&page=0", "?limit=2&page=1.5", "?page=-1"} {
					requireStatus(t, e.Get(t, e.Token(t, "root"), path+query), http.StatusBadRequest, query)
				}
				requireStatus(t, e.Get(t, e.Token(t, "listener"), path+"?limit=abc&page=abc"), http.StatusOK, "non numeric paging is treated as 0")
			},
		},
	}
}
