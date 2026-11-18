package collections

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// markExplicit sets the explicit flag of a book and restores it when the scenario ends.
func markExplicit(t *testing.T, e *harness.Env, itemID string) {
	t.Helper()
	root := e.Token(t, "root")
	set := func(value bool) harness.Response {
		return call(t, e, root, http.MethodPatch, "/api/items/"+itemID+"/media", map[string]any{"metadata": map[string]any{"explicit": value}})
	}
	wantStatus(t, set(true), http.StatusOK)
	t.Cleanup(func() { set(false) }) // best effort restore of the shared fixture
}

func restrictedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r082", Invariant: "INV-R082-ACCESS-001", Case: "edge", State: "stateless", Assertion: "get-hides-restricted-books",
			Sources: []string{"server/models/Collection.js:197-216", "server/controllers/CollectionController.js:131-135"},
			Claim:   "For a user who may not see explicit content, GET /api/collections/:id omits explicit books and answers 404 when every book is hidden; root still sees all books.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				all := create(t, e, "Explicit mix", ids.novel, ids.saga1)
				only := create(t, e, "Explicit only", ids.novel)
				markExplicit(t, e, ids.novel)
				finisher := e.Token(t, "finisher")
				got := decode(t, e.Get(t, finisher, "/api/collections/"+all.ID))
				sameStrings(t, "finisher books", got.itemIDs(), []string{ids.saga1})
				wantStatus(t, e.Get(t, finisher, "/api/collections/"+only.ID), http.StatusNotFound)
				sameStrings(t, "root books", fetch(t, e, all.ID).itemIDs(), []string{ids.novel, ids.saga1})
				sameStrings(t, "root only", fetch(t, e, only.ID).itemIDs(), []string{ids.novel})
			},
		},
		{
			Requirement: "route-r081", Invariant: "INV-R081-ACCESS-001", Case: "edge", State: "stateless", Assertion: "list-hides-collection-with-only-restricted-books",
			Sources: []string{"server/models/Collection.js:62-82"},
			Claim:   "GET /api/collections omits, for a user who may not see explicit content, a collection whose books are all explicit, while root still lists it.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				mixed := create(t, e, "List explicit mix", ids.novel, ids.saga1)
				only := create(t, e, "List explicit only", ids.novel)
				markExplicit(t, e, ids.novel)
				list := func(who string) []collection {
					var body struct {
						Collections []collection `json:"collections"`
					}
					e.Get(t, e.Token(t, who), "/api/collections").JSON(t, &body)
					return body.Collections
				}
				if f := list("finisher"); containsCollection(f, only.ID) || !containsCollection(f, mixed.ID) || !containsCollection(f, e.IDs["collection"]) {
					t.Fatalf("finisher list wrong: %d collections", len(f))
				}
				if r := list("root"); !containsCollection(r, only.ID) || !containsCollection(r, mixed.ID) {
					t.Fatal("root must list both")
				}
			},
		},
		{
			Requirement: "route-r027", Invariant: "INV-ROUTE-R027-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-hides-restricted-collection",
			Sources: []string{"server/models/Collection.js:62-82", "server/controllers/LibraryController.js:833-835"},
			Claim:   "The library collection list omits for a user without explicit access a collection whose books are all explicit, and its total counts only the listed collections.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				only := create(t, e, "Library explicit only", ids.novel)
				_, before := libraryCollections(t, e, e.Token(t, "finisher"), e.IDs["library"], "")
				markExplicit(t, e, ids.novel)
				_, after := libraryCollections(t, e, e.Token(t, "finisher"), e.IDs["library"], "")
				if containsCollection(after.Results, only.ID) || after.Total != len(after.Results) || after.Total >= before.Total {
					t.Fatalf("restricted collection must be hidden: before %d after %d", before.Total, after.Total)
				}
				_, root := libraryCollections(t, e, e.Token(t, "root"), e.IDs["library"], "")
				if !containsCollection(root.Results, only.ID) {
					t.Fatal("root must still list it")
				}
			},
		},
	}
}
