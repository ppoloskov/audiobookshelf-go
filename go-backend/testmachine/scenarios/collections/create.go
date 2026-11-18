package collections

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r080", Invariant: "INV-R080-SAMPLED", Case: "normal", State: "initial", Assertion: "create-persists-and-returns-expanded",
			Sources: []string{"server/routers/ApiRouter.js:146"},
			Claim:   "POST /api/collections answers 200 with the new expanded collection (tags stripped from the name, books in request order) and persists the row and links numbered from 1.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				started := time.Now().UnixMilli()
				resp := call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", map[string]any{
					"libraryId": e.IDs["library"], "name": "<b>Road</b> &amp; Trip Ü", "description": "Weekend picks",
					"books": []string{ids.novel, ids.saga1, ids.saga2},
				})
				c := decode(t, resp)
				if c.ID == "" || c.LibraryID != e.IDs["library"] || c.Name != "Road & Trip Ü" || c.Description == nil || *c.Description != "Weekend picks" {
					t.Fatalf("unexpected collection: %s", resp.Body)
				}
				sameStrings(t, "response books", c.itemIDs(), []string{ids.novel, ids.saga1, ids.saga2})
				if c.Books[0].MediaType != "book" || c.Books[0].Media.Metadata.Title != "Single File Novel" {
					t.Fatalf("books are not expanded library items: %+v", c.Books[0])
				}
				if c.CreatedAt < started-1000 || c.LastUpdate < started-1000 {
					t.Fatalf("timestamps %d/%d are before the request %d", c.CreatedAt, c.LastUpdate, started)
				}
				row := collectionRow(t, e, c.ID)
				if row["name"] != "Road & Trip Ü" || row["description"] != "Weekend picks" || row["libraryId"] != e.IDs["library"] {
					t.Fatalf("row wrong: %v", row)
				}
				if millis(t, row["createdAt"]) != c.CreatedAt || millis(t, row["updatedAt"]) != c.LastUpdate {
					t.Fatalf("row timestamps %v/%v differ from response %d/%d", row["createdAt"], row["updatedAt"], c.CreatedAt, c.LastUpdate)
				}
				wantLinks(t, e, c.ID, ids.novel, ids.saga1, ids.saga2)
				rows := e.Rows(t, "SELECT cb.bookId, li.mediaId FROM collectionBooks cb JOIN libraryItems li ON li.mediaId = cb.bookId WHERE cb.collectionId = ?", c.ID)
				if len(rows) != 3 {
					t.Fatalf("links must reference book ids (library item mediaId): %v", rows)
				}
			},
		},
		{
			Requirement: "route-r080", Invariant: "INV-R080-SAMPLED", Case: "normal", State: "repeat", Assertion: "create-repeat-makes-second-collection",
			Sources: []string{"server/routers/ApiRouter.js:146"},
			Claim:   "Posting the same body twice creates two collections with different ids, the same name and independent links.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				first := create(t, e, "Twin", ids.saga2)
				second := create(t, e, "Twin", ids.saga2)
				if first.ID == second.ID || first.Name != second.Name {
					t.Fatalf("repeat must create a distinct collection: %s %s", first.ID, second.ID)
				}
				wantLinks(t, e, first.ID, ids.saga2)
				wantLinks(t, e, second.ID, ids.saga2)
				if n := e.Count(t, "SELECT COUNT(*) FROM collections WHERE name = 'Twin'"); n != 2 {
					t.Fatalf("rows named Twin = %d, want 2", n)
				}
			},
		},
		{
			Requirement: "route-r080", Invariant: "INV-R080-SAMPLED", Case: "edge", State: "initial", Assertion: "create-optional-description-and-falsy-books",
			Sources: []string{"server/routers/ApiRouter.js:146"},
			Claim:   "An absent or empty description is stored as null, and null or empty entries in books are dropped before validation.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				for name, description := range map[string]any{"No description": nil, "Empty description": ""} {
					body := map[string]any{"libraryId": e.IDs["library"], "name": name, "books": []any{nil, "", ids.ebook}}
					if description != nil {
						body["description"] = description
					}
					c := decode(t, call(t, e, root, http.MethodPost, "/api/collections", body))
					if c.Description != nil {
						t.Fatalf("%s: description = %q, want null", name, *c.Description)
					}
					if row := collectionRow(t, e, c.ID); row["description"] != nil {
						t.Fatalf("%s: row description = %v, want NULL", name, row["description"])
					}
					sameStrings(t, name+" books", c.itemIDs(), []string{ids.ebook})
					wantLinks(t, e, c.ID, ids.ebook)
				}
			},
		},
		{
			Requirement: "route-r080", Invariant: "INV-R080-SAMPLED", Case: "normal", State: "changed", Assertion: "create-visible-to-every-read-and-user",
			Sources: []string{"server/routers/ApiRouter.js:146"},
			Claim:   "A created collection appears in GET /api/collections, GET /api/collections/:id and the library list for root and ordinary users, next to the fixture collection.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Visible everywhere", ids.saga1, ids.ebook)
				for _, who := range []string{"root", "listener", "finisher"} {
					token := e.Token(t, who)
					got := decode(t, e.Get(t, token, "/api/collections/"+c.ID))
					sameStrings(t, who+" books", got.itemIDs(), []string{ids.saga1, ids.ebook})
					var all struct {
						Collections []collection `json:"collections"`
					}
					e.Get(t, token, "/api/collections").JSON(t, &all)
					if !containsCollection(all.Collections, c.ID) || !containsCollection(all.Collections, e.IDs["collection"]) {
						t.Fatalf("%s: list lacks the new or the fixture collection", who)
					}
					_, page := libraryCollections(t, e, token, e.IDs["library"], "")
					if !containsCollection(page.Results, c.ID) || page.Total != len(page.Results) || page.Total != e.Count(t, "SELECT COUNT(*) FROM collections WHERE libraryId = ?", e.IDs["library"]) {
						t.Fatalf("%s: library list wrong, total %d", who, page.Total)
					}
				}
			},
		},
		{
			Requirement: "route-r080", Invariant: "INV-R080-VALIDATION-001", Case: "error", State: "failure", Assertion: "create-invalid-bodies-rejected-unchanged",
			Sources: []string{"server/controllers/CollectionController.js:32-64"},
			Claim:   "A missing or tag-only name, missing library, non-string description, empty or all-invalid books, a duplicate id, an unknown id, a podcast item or an unknown library each answer 400 and change no rows.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				lib := e.IDs["library"]
				bodies := map[string]map[string]any{
					"no name":            {"libraryId": lib, "books": []string{ids.novel}},
					"tags only name":     {"libraryId": lib, "name": "<b></b>", "books": []string{ids.novel}},
					"numeric name":       {"libraryId": lib, "name": 5, "books": []string{ids.novel}},
					"no library":         {"name": "x", "books": []string{ids.novel}},
					"object description": {"libraryId": lib, "name": "x", "description": map[string]any{"a": 1}, "books": []string{ids.novel}},
					"no books":           {"libraryId": lib, "name": "x"},
					"empty books":        {"libraryId": lib, "name": "x", "books": []string{}},
					"only non-strings":   {"libraryId": lib, "name": "x", "books": []any{nil, 5, true}},
					"unknown book":       {"libraryId": lib, "name": "x", "books": []string{ids.novel, "no-such-item"}},
					"duplicate book":     {"libraryId": lib, "name": "x", "books": []string{ids.novel, ids.novel}},
					"podcast item":       {"libraryId": lib, "name": "x", "books": []string{ids.podcast}},
					"unknown library":    {"libraryId": "no-such-library", "name": "x", "books": []string{ids.novel}},
				}
				unchanged(t, e, func() {
					for name, body := range bodies {
						if resp := call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", body); resp.Status != http.StatusBadRequest {
							t.Fatalf("%s: status %d, want 400: %.200s", name, resp.Status, resp.Body)
						}
					}
				})
			},
		},
		{
			Requirement: "route-r080", Invariant: "INV-R080-ACCESS-001", Case: "error", State: "initial", Assertion: "create-denied-without-token-or-permission",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:44-47", "server/controllers/CollectionController.js:450-453"},
			Claim:   "POST /api/collections answers 401 without a token, 403 for a user without update permission (even with an invalid body), 403 for an updater without access to the library, and 400 before 403 when that updater sends an invalid name (but 403 before the empty-books 400); nothing is created.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				body := map[string]any{"libraryId": e.IDs["library"], "name": "Denied", "books": []string{ids.novel}}
				unchanged(t, e, func() {
					wantStatus(t, call(t, e, "", http.MethodPost, "/api/collections", body), http.StatusUnauthorized)
					wantStatus(t, call(t, e, e.Token(t, "listener"), http.MethodPost, "/api/collections", body), http.StatusForbidden)
					wantStatus(t, call(t, e, podcastOnly(t, e), http.MethodPost, "/api/collections", body), http.StatusForbidden)
					// check order: permission middleware, then body validation, then library access
					invalid := map[string]any{"libraryId": e.IDs["library"], "name": "", "books": []string{ids.novel}}
					wantStatus(t, call(t, e, e.Token(t, "listener"), http.MethodPost, "/api/collections", invalid), http.StatusForbidden)
					wantStatus(t, call(t, e, podcastOnly(t, e), http.MethodPost, "/api/collections", invalid), http.StatusBadRequest)
					wantStatus(t, call(t, e, podcastOnly(t, e), http.MethodPost, "/api/collections", map[string]any{"libraryId": e.IDs["library"], "name": "x", "books": []string{}}), http.StatusForbidden) // the library check precedes the books check
				})
			},
		},
	}
}

func containsCollection(list []collection, id string) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}
