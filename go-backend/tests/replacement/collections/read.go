package collections

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// libraryPage is the GET /api/libraries/:id/collections body.
type libraryPage struct {
	Results  []collection               `json:"results"`
	Total    int                        `json:"total"`
	Limit    int                        `json:"limit"`
	Page     int                        `json:"page"`
	SortDesc bool                       `json:"sortDesc"`
	Minified bool                       `json:"minified"`
	Include  string                     `json:"include"`
	SortBy   *string                    `json:"sortBy"`
	FilterBy *string                    `json:"filterBy"`
	Raw      map[string]json.RawMessage `json:"-"`
}

func libraryCollections(t *testing.T, e *harness.Env, token, libraryID, query string) (harness.Response, libraryPage) {
	t.Helper()
	path := "/api/libraries/" + libraryID + "/collections"
	if query != "" {
		path += "?" + query
	}
	resp := e.Get(t, token, path)
	var page libraryPage
	if resp.Status == http.StatusOK {
		resp.JSON(t, &page)
		resp.JSON(t, &page.Raw)
	}
	return resp, page
}

// wantFixtureCollection asserts c is the fixture collection "Favourites".
func wantFixtureCollection(t *testing.T, e *harness.Env, c collection) {
	t.Helper()
	ids := fixtureItems(e)
	row := collectionRow(t, e, e.IDs["collection"])
	if c.ID != e.IDs["collection"] || c.LibraryID != e.IDs["library"] || c.Name != "Favourites" || c.Description != nil {
		t.Fatalf("fixture collection fields wrong: %+v", c)
	}
	sameStrings(t, "book order", c.itemIDs(), []string{ids.saga1, ids.novel})
	titles := []string{c.Books[0].Media.Metadata.Title, c.Books[1].Media.Metadata.Title}
	sameStrings(t, "book titles", titles, []string{"The Beginning", "Single File Novel"})
	for _, book := range c.Books {
		if book.MediaType != "book" || book.LibraryID != e.IDs["library"] || book.Media.ID == "" {
			t.Fatalf("book is not an expanded library item: %+v", book)
		}
	}
	if c.CreatedAt != millis(t, row["createdAt"]) || c.LastUpdate != millis(t, row["updatedAt"]) {
		t.Fatalf("timestamps %d/%d do not match the row %v/%v", c.CreatedAt, c.LastUpdate, row["createdAt"], row["updatedAt"])
	}
}

func readScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r081", Invariant: "INV-R081-SAMPLED", Case: "normal", State: "stateless", Assertion: "list-fixture-collection",
			Sources: []string{"server/routers/ApiRouter.js:147"},
			Claim:   "GET /api/collections returns {collections:[...]} with the fixture collection, its expanded books in link order and timestamps equal to the row.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/collections")
				wantStatus(t, resp, http.StatusOK)
				var body struct {
					Collections []collection `json:"collections"`
				}
				resp.JSON(t, &body)
				if len(body.Collections) != 1 {
					t.Fatalf("want 1 collection, got %d", len(body.Collections))
				}
				wantFixtureCollection(t, e, body.Collections[0])
			},
		},
		{
			Requirement: "route-r081", Invariant: "INV-R081-ACCESS-001", Case: "edge", State: "stateless", Assertion: "list-ordinary-and-restricted-users",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:115-120"},
			Claim:   "An ordinary user sees the fixture collection and a user without access to its library gets an empty collections array.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, who := range []string{"listener", "finisher"} {
					resp := e.Get(t, e.Token(t, who), "/api/collections")
					wantStatus(t, resp, http.StatusOK)
					var body struct {
						Collections []collection `json:"collections"`
					}
					resp.JSON(t, &body)
					if len(body.Collections) != 1 {
						t.Fatalf("%s: want 1 collection, got %d", who, len(body.Collections))
					}
					wantFixtureCollection(t, e, body.Collections[0])
				}
				resp := e.Get(t, podcastOnly(t, e), "/api/collections")
				wantStatus(t, resp, http.StatusOK)
				var body struct {
					Collections []json.RawMessage `json:"collections"`
				}
				resp.JSON(t, &body)
				if body.Collections == nil || len(body.Collections) != 0 {
					t.Fatalf("restricted user must get an empty array, got %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r081", Invariant: "INV-R081-ACCESS-001", Case: "error", State: "stateless", Assertion: "list-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:115-120"},
			Claim:   "GET /api/collections without a valid token answers 401.",
			Run: func(t *testing.T, e *harness.Env) {
				wantStatus(t, e.Get(t, "", "/api/collections"), http.StatusUnauthorized)
				wantStatus(t, e.Get(t, "not-a-token", "/api/collections"), http.StatusUnauthorized)
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "normal", State: "stateless", Assertion: "library-list-defaults",
			Sources: []string{"server/controllers/LibraryController.js:814-844", "server/controllers/LibraryController.js:1475-1498"},
			Claim:   "GET /api/libraries/:id/collections without query returns results, total 1, limit 0, page 0, sortDesc false, minified false, include empty and no sortBy/filterBy keys.",
			Run: func(t *testing.T, e *harness.Env) {
				resp, page := libraryCollections(t, e, e.Token(t, "root"), e.IDs["library"], "")
				wantStatus(t, resp, http.StatusOK)
				if page.Total != 1 || len(page.Results) != 1 || page.Limit != 0 || page.Page != 0 || page.SortDesc || page.Minified || page.Include != "" {
					t.Fatalf("unexpected defaults: %s", resp.Body)
				}
				for _, key := range []string{"sortBy", "filterBy"} {
					if _, present := page.Raw[key]; present {
						t.Fatalf("%s must be absent without its query parameter: %s", key, resp.Body)
					}
				}
				wantFixtureCollection(t, e, page.Results[0])
				listener, _ := libraryCollections(t, e, e.Token(t, "listener"), e.IDs["library"], "")
				wantStatus(t, listener, http.StatusOK)
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-query-echo",
			Sources: []string{"server/controllers/LibraryController.js:815-830", "server/controllers/LibraryController.js:1487-1495"},
			Claim:   "The query parameters limit, page, sort, desc, filter, minified and include are echoed (numbers as numbers, include lower-cased and trimmed) without changing the results.",
			Run: func(t *testing.T, e *harness.Env) {
				query := url.Values{"limit": {"5"}, "page": {"0"}, "sort": {"name"}, "desc": {"1"}, "filter": {"anything"}, "minified": {"1"}, "include": {" RssFeed , ,Other"}}
				resp, page := libraryCollections(t, e, e.Token(t, "root"), e.IDs["library"], query.Encode())
				wantStatus(t, resp, http.StatusOK)
				if page.Limit != 5 || page.Page != 0 || !page.SortDesc || !page.Minified || page.Include != "rssfeed,other" || page.Total != 1 || len(page.Results) != 1 {
					t.Fatalf("unexpected echo: %s", resp.Body)
				}
				if page.SortBy == nil || *page.SortBy != "name" || page.FilterBy == nil || *page.FilterBy != "anything" {
					t.Fatalf("sortBy/filterBy not echoed: %s", resp.Body)
				}
				_, desc0 := libraryCollections(t, e, e.Token(t, "root"), e.IDs["library"], "desc=0&minified=0")
				if desc0.SortDesc || desc0.Minified {
					t.Fatalf("desc=0 and minified=0 must be false")
				}
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-rssfeed-include",
			Sources: []string{"server/controllers/LibraryController.js:815-833", "server/models/Collection.js:34-40", "server/models/Collection.js:90-93"},
			Claim:   "include=rssfeed adds the open feed as rssFeed to each collection of the library list, and the list without it has no rssFeed.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				feedID := openFeed(t, e, e.IDs["collection"], "library-list-feed")
				_, with := libraryCollections(t, e, root, e.IDs["library"], "include=rssfeed")
				if len(with.Results) != 1 || with.Results[0].RSSFeed == nil || with.Results[0].RSSFeed.ID != feedID || with.Results[0].RSSFeed.Slug != "library-list-feed" {
					t.Fatalf("rssFeed missing from list: %+v", with.Results)
				}
				_, without := libraryCollections(t, e, root, e.IDs["library"], "")
				if len(without.Results) != 1 || without.Results[0].RSSFeed != nil {
					t.Fatalf("rssFeed present without include: %+v", without.Results)
				}
				var all struct {
					Collections []collection `json:"collections"`
				}
				e.Get(t, root, "/api/collections").JSON(t, &all)
				if len(all.Collections) != 1 || all.Collections[0].RSSFeed != nil {
					t.Fatalf("GET /api/collections must not include rssFeed: %+v", all.Collections)
				}
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-other-library-empty",
			Sources: []string{"server/controllers/LibraryController.js:833-843"},
			Claim:   "A library without collections returns an empty results array and total 0.",
			Run: func(t *testing.T, e *harness.Env) {
				resp, page := libraryCollections(t, e, e.Token(t, "root"), e.IDs["podcastLibrary"], "")
				wantStatus(t, resp, http.StatusOK)
				if page.Results == nil || len(page.Results) != 0 || page.Total != 0 {
					t.Fatalf("want empty results, got %s", resp.Body)
				}
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "error", State: "stateless", Assertion: "library-list-rejections",
			Sources: []string{"server/controllers/LibraryController.js:1475-1498"},
			Claim:   "GET /api/libraries/:id/collections answers 401 without a token, 403 for a user without access to the library, 404 for an unknown library and 400 for a negative or fractional limit or page.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				lib := e.IDs["library"]
				wantStatus(t, e.Get(t, "", "/api/libraries/"+lib+"/collections"), http.StatusUnauthorized)
				resp, _ := libraryCollections(t, e, podcastOnly(t, e), lib, "")
				wantStatus(t, resp, http.StatusForbidden)
				resp, _ = libraryCollections(t, e, root, "no-such-library", "")
				wantStatus(t, resp, http.StatusNotFound)
				for _, query := range []string{"limit=-1", "limit=1.5", "page=-1", "page=0.5"} {
					resp, _ = libraryCollections(t, e, root, lib, query)
					if resp.Status != http.StatusBadRequest {
						t.Fatalf("%s: status %d, want 400", query, resp.Status)
					}
				}
			},
		},
		{
			Requirement: "route-r082", Invariant: "INV-R082-SAMPLED", Case: "normal", State: "stateless", Assertion: "get-fixture-collection",
			Sources: []string{"server/routers/ApiRouter.js:148"},
			Claim:   "GET /api/collections/:id returns the fixture collection with books in link order, expanded library items and timestamps equal to the row.",
			Run: func(t *testing.T, e *harness.Env) {
				wantFixtureCollection(t, e, fetch(t, e, e.IDs["collection"]))
			},
		},
		{
			Requirement: "route-r082", Invariant: "INV-R082-SAMPLED", Case: "edge", State: "stateless", Assertion: "get-ordinary-users-and-rssfeed-include",
			Sources: []string{"server/routers/ApiRouter.js:148"},
			Claim:   "Ordinary users read the collection, and rssFeed appears only with include=rssfeed and only while a feed is open for it.",
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs["collection"]
				for _, who := range []string{"listener", "finisher"} {
					wantFixtureCollection(t, e, decode(t, e.Get(t, e.Token(t, who), "/api/collections/"+id)))
				}
				root := e.Token(t, "root")
				if c := decode(t, e.Get(t, root, "/api/collections/"+id+"?include=rssfeed")); c.RSSFeed != nil {
					t.Fatalf("no feed is open yet, rssFeed = %+v", c.RSSFeed)
				}
				feedID := openFeed(t, e, id, "fixture-favourites")

				with := decode(t, e.Get(t, root, "/api/collections/"+id+"?include=name,rssfeed"))
				if with.RSSFeed == nil || with.RSSFeed.ID != feedID || with.RSSFeed.Slug != "fixture-favourites" || with.RSSFeed.EntityID != id || with.RSSFeed.EntityType != "collection" {
					t.Fatalf("rssFeed wrong: %+v", with.RSSFeed)
				}
				if without := decode(t, e.Get(t, root, "/api/collections/"+id)); without.RSSFeed != nil {
					t.Fatalf("rssFeed without include: %+v", without.RSSFeed)
				}
			},
		},
		{
			Requirement: "route-r082", Invariant: "INV-R082-ACCESS-001", Case: "error", State: "stateless", Assertion: "get-rejections",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:433-444"},
			Claim:   "GET /api/collections/:id answers 401 without a token and 404 for an unknown collection or one in a library the user cannot access, without leaking books.",
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs["collection"]
				wantStatus(t, e.Get(t, "", "/api/collections/"+id), http.StatusUnauthorized)
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/collections/no-such-collection"), http.StatusNotFound)
				hidden := e.Get(t, podcastOnly(t, e), "/api/collections/"+id)
				wantStatus(t, hidden, http.StatusNotFound)
				var leak collection
				_ = json.Unmarshal(hidden.Body, &leak) // plain text body is expected; only a JSON leak matters
				if len(leak.Books) != 0 {
					t.Fatalf("books leaked: %s", hidden.Body)
				}
			},
		},
	}
}
