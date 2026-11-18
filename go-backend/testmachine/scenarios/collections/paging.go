package collections

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// listIDs returns the collection ids of a library list.
func listIDs(page libraryPage) []string {
	ids := make([]string, 0, len(page.Results))
	for _, c := range page.Results {
		ids = append(ids, c.ID)
	}
	return ids
}

// setTags sets the tags of a book and clears them when the scenario ends.
func setTags(t *testing.T, e *harness.Env, itemID string, tags ...string) {
	t.Helper()
	root := e.Token(t, "root")
	wantStatus(t, call(t, e, root, http.MethodPatch, "/api/items/"+itemID+"/media", map[string]any{"tags": tags}), http.StatusOK)
	t.Cleanup(func() {
		call(t, e, root, http.MethodPatch, "/api/items/"+itemID+"/media", map[string]any{"tags": []string{}})
	}) // best effort restore of the shared fixture
}

// tagUser is a user restricted by the selected tag "pick"; deny selects the
// mode in which books carrying a selected tag are hidden.
func tagUser(t *testing.T, e *harness.Env, name string, deny bool) string {
	return extraUser(t, e, name, map[string]any{
		"download": true, "update": false, "delete": false, "accessAllLibraries": true, "accessAllTags": false,
		"selectedTagsNotAccessible": deny, "itemTagsSelected": []string{"pick"}, "accessExplicitContent": true,
	})
}

func pagingScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-limit-and-page-slicing",
			Sources: []string{"server/controllers/LibraryController.js:835-843", "server/controllers/LibraryController.js:1487-1495"},
			Claim:   "With limit=2 page 0, 1 and 2 return the consecutive slices of the full list while total stays the full count; page without limit returns everything; limit=0 returns everything.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				for _, name := range []string{"Page A", "Page B", "Page C"} {
					create(t, e, name, ids.saga1)
				}
				root := e.Token(t, "root")
				lib := e.IDs["library"]
				_, full := libraryCollections(t, e, root, lib, "")
				all := listIDs(full)
				n := len(all)
				if n < 4 || full.Total != n || n != e.Count(t, "SELECT COUNT(*) FROM collections WHERE libraryId = ?", lib) {
					t.Fatalf("setup: %d collections, total %d", n, full.Total)
				}
				// the order of collections is not stable between requests, so compare sets
				seen := map[string]bool{}
				for page := 0; page <= (n+1)/2; page++ {
					_, got := libraryCollections(t, e, root, lib, "limit=2&page="+strconv.Itoa(page))
					wantLen := max(0, min(2, n-page*2))
					if len(got.Results) != wantLen || got.Total != n || got.Limit != 2 || got.Page != page {
						t.Fatalf("page %d: %d results (want %d), total %d limit %d page %d", page, len(got.Results), wantLen, got.Total, got.Limit, got.Page)
					}
					for _, id := range listIDs(got) {
						if seen[id] || !slices.Contains(all, id) {
							t.Fatalf("page %d repeats or invents collection %s", page, id)
						}
						seen[id] = true
					}
				}
				if len(seen) != n {
					t.Fatalf("pages cover %d of %d collections", len(seen), n)
				}
				for _, query := range []string{"page=2", "limit=0&page=3"} {
					_, got := libraryCollections(t, e, root, lib, query)
					wantSameSet(t, query, listIDs(got), all)
					if got.Total != n || got.Limit != 0 {
						t.Fatalf("%s: total %d limit %d", query, got.Total, got.Limit)
					}
				}
			},
		},
		{
			Requirement: "route-r082", Invariant: "INV-R082-ACCESS-001", Case: "edge", State: "stateless", Assertion: "get-tag-restricted-user",
			Sources: []string{"server/models/Collection.js:229-243", "server/models/User.js:659-668", "server/controllers/CollectionController.js:131-135"},
			Claim:   "A user restricted to selected tags (allow mode) sees only books carrying one, a user in deny mode only books without one, and GET /api/collections/:id answers 404 when no book is left; root sees all.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := create(t, e, "Tag A", ids.novel)
				b := create(t, e, "Tag B", ids.novel, ids.saga1)
				c := create(t, e, "Tag C", ids.saga1)
				setTags(t, e, ids.novel, "pick")
				setTags(t, e, ids.saga1, "other")
				allow, deny := tagUser(t, e, "tag-allow", false), tagUser(t, e, "tag-deny", true)
				sameStrings(t, "allow A", decode(t, e.Get(t, allow, "/api/collections/"+a.ID)).itemIDs(), []string{ids.novel})
				sameStrings(t, "allow B", decode(t, e.Get(t, allow, "/api/collections/"+b.ID)).itemIDs(), []string{ids.novel})
				wantStatus(t, e.Get(t, allow, "/api/collections/"+c.ID), http.StatusNotFound)
				wantStatus(t, e.Get(t, deny, "/api/collections/"+a.ID), http.StatusNotFound)
				sameStrings(t, "deny B", decode(t, e.Get(t, deny, "/api/collections/"+b.ID)).itemIDs(), []string{ids.saga1})
				sameStrings(t, "deny C", decode(t, e.Get(t, deny, "/api/collections/"+c.ID)).itemIDs(), []string{ids.saga1})
				sameStrings(t, "root B", fetch(t, e, b.ID).itemIDs(), []string{ids.novel, ids.saga1})
			},
		},
		{
			Requirement: "route-r081", Invariant: "INV-R081-ACCESS-001", Case: "edge", State: "stateless", Assertion: "list-tag-restricted-user",
			Sources: []string{"server/models/Collection.js:76-94", "server/models/User.js:659-668"},
			Claim:   "GET /api/collections omits for a tag-restricted user (allow and deny mode) the collections whose books are all hidden and keeps those with a visible book.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := create(t, e, "List tag A", ids.novel)
				b := create(t, e, "List tag B", ids.novel, ids.saga1)
				c := create(t, e, "List tag C", ids.saga1)
				setTags(t, e, ids.novel, "pick")
				setTags(t, e, ids.saga1, "other")
				list := func(token string) []collection {
					var body struct {
						Collections []collection `json:"collections"`
					}
					e.Get(t, token, "/api/collections").JSON(t, &body)
					return body.Collections
				}
				allow, deny := list(tagUser(t, e, "tag-allow", false)), list(tagUser(t, e, "tag-deny", true))
				if !containsCollection(allow, a.ID) || !containsCollection(allow, b.ID) || containsCollection(allow, c.ID) || !containsCollection(allow, e.IDs["collection"]) {
					t.Fatal("allow-mode list wrong")
				}
				if containsCollection(deny, a.ID) || !containsCollection(deny, b.ID) || !containsCollection(deny, c.ID) || !containsCollection(deny, e.IDs["collection"]) {
					t.Fatal("deny-mode list wrong")
				}
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-tag-restricted-user",
			Sources: []string{"server/models/Collection.js:76-94", "server/controllers/LibraryController.js:833-835"},
			Claim:   "The library collection list hides, for a tag-restricted user in allow and deny mode, collections whose books are all hidden, and total counts the listed ones.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := create(t, e, "Library tag A", ids.novel)
				c := create(t, e, "Library tag C", ids.saga1)
				setTags(t, e, ids.novel, "pick")
				setTags(t, e, ids.saga1, "other")
				lib := e.IDs["library"]
				_, allow := libraryCollections(t, e, tagUser(t, e, "tag-allow", false), lib, "")
				_, deny := libraryCollections(t, e, tagUser(t, e, "tag-deny", true), lib, "")
				if !containsCollection(allow.Results, a.ID) || containsCollection(allow.Results, c.ID) || allow.Total != len(allow.Results) {
					t.Fatalf("allow-mode library list wrong, total %d", allow.Total)
				}
				if containsCollection(deny.Results, a.ID) || !containsCollection(deny.Results, c.ID) || deny.Total != len(deny.Results) {
					t.Fatalf("deny-mode library list wrong, total %d", deny.Total)
				}
			},
		},
	}
}
