package author

import (
	"net/http"
	"slices"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// setItemMedia PATCHes /api/items/:id/media as root.
func setItemMedia(t *testing.T, e *harness.Env, itemID string, body map[string]any) {
	t.Helper()
	wantStatus(t, "item media "+itemID, e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", body), http.StatusOK)
}

// itemIDsIn returns the sorted ids of the minified items.
func itemIDsIn(items []minifiedItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	slices.Sort(ids)
	return ids
}

// sortedIDs returns a sorted copy.
func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// authorItems reads GET /api/authors/:id?include=items,series as the user.
func authorItems(t *testing.T, e *harness.Env, token, id string) authorPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: authorURL(id, ""), Token: token, Query: map[string][]string{"include": {"items,series"}}})
	wantStatus(t, "get author with items", resp, http.StatusOK)
	return decode[authorPage](t, resp.Body)
}

func getRestrictedScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "edge", State: "stateless", Assertion: "get-author-items-item-tag-restrictions",
			Sources: []string{"server/controllers/AuthorController.js:47", "server/utils/queries/libraryItemsBookFilters.js:13-30"},
			Claim:   "include=items and the series groups show only the books the user may see under item-tag restrictions: in allow mode only books with a selected tag, in deny mode all books except those with a selected tag; the author itself is still returned.",
			Run: func(t *testing.T, e *harness.Env) {
				ada, terry := authorID(t, e, "Ada Example"), authorID(t, e, "Terry Fixture")
				setItemMedia(t, e, e.IDs["novel"], map[string]any{"tags": []string{"author-allowed"}})
				setItemMedia(t, e, e.IDs["ebook"], map[string]any{"tags": []string{"author-blocked"}})
				setItemMedia(t, e, e.IDs["saga1"], map[string]any{"tags": []string{"author-allowed"}})
				allow := ensureUser(t, e, "author-tag-allow", map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": false, "itemTagsSelected": []string{"author-allowed"}})
				deny := ensureUser(t, e, "author-tag-deny", map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true, "itemTagsSelected": []string{"author-blocked"}})
				root := e.Token(t, "root")

				if got := itemIDsIn(authorItems(t, e, root, ada).LibraryItems); !slices.Equal(got, sortedIDs(e.IDs["novel"], e.IDs["ebook"])) {
					t.Fatalf("root sees %v", got)
				}
				for name, want := range map[string]struct {
					user          user
					ada, terry    []string
					terrySeriesIn []string
				}{
					"allow mode": {allow, sortedIDs(e.IDs["novel"]), sortedIDs(e.IDs["saga1"]), sortedIDs(e.IDs["saga1"])},
					"deny mode":  {deny, sortedIDs(e.IDs["novel"]), sortedIDs(e.IDs["saga1"], e.IDs["saga2"]), sortedIDs(e.IDs["saga1"], e.IDs["saga2"])},
				} {
					adaPage, terryPage := authorItems(t, e, want.user.token, ada), authorItems(t, e, want.user.token, terry)
					if adaPage.ID != ada || !slices.Equal(itemIDsIn(adaPage.LibraryItems), want.ada) || !slices.Equal(itemIDsIn(terryPage.LibraryItems), want.terry) {
						t.Fatalf("%s: Ada items %v, Terry items %v", name, itemIDsIn(adaPage.LibraryItems), itemIDsIn(terryPage.LibraryItems))
					}
					if len(terryPage.Series) != 1 || !slices.Equal(itemIDsIn(terryPage.Series[0].Items), want.terrySeriesIn) {
						t.Fatalf("%s: series groups %+v", name, terryPage.Series)
					}
				}
			},
		},
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "edge", State: "stateless", Assertion: "get-author-items-explicit-restriction",
			Sources: []string{"server/controllers/AuthorController.js:47", "server/utils/queries/libraryItemsBookFilters.js:13-30"},
			Claim:   "include=items and the series groups hide explicit books from a user without explicit-content access, while a user with access sees them.",
			Run: func(t *testing.T, e *harness.Env) {
				terry := authorID(t, e, "Terry Fixture")
				setItemMedia(t, e, e.IDs["saga2"], map[string]any{"metadata": map[string]any{"explicit": true}})
				restricted := authorItems(t, e, e.Token(t, "finisher"), terry)
				open := authorItems(t, e, e.Token(t, "listener"), terry)
				if !slices.Equal(itemIDsIn(restricted.LibraryItems), sortedIDs(e.IDs["saga1"])) || len(restricted.Series) != 1 || !slices.Equal(itemIDsIn(restricted.Series[0].Items), sortedIDs(e.IDs["saga1"])) {
					t.Fatalf("restricted user sees %+v", restricted)
				}
				if !slices.Equal(itemIDsIn(open.LibraryItems), sortedIDs(e.IDs["saga1"], e.IDs["saga2"])) || len(open.Series) != 1 || len(open.Series[0].Items) != 2 {
					t.Fatalf("unrestricted user sees %+v", open)
				}
			},
		},
	}
}

func seriesSortScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r131", Invariant: "INV-ROUTE-R131-ITEMS", Case: "edge", State: "stateless", Assertion: "get-author-series-items-natural-sequence-order",
			Sources: []string{"server/controllers/AuthorController.js:75-78", "server/controllers/AuthorController.js:15-17"},
			Claim:   "Items inside a series group are ordered by sequence with natural numeric comparison: 1.5, 2, 10 (a plain string order would give 1.5, 10, 2).",
			Run: func(t *testing.T, e *harness.Env) {
				terry := authorID(t, e, "Terry Fixture")
				root := e.Token(t, "root")
				series := func(sequence string) map[string]any {
					return map[string]any{"metadata": map[string]any{"series": []map[string]string{{"name": "Long Saga", "sequence": sequence}}}}
				}
				setItemMedia(t, e, e.IDs["saga1"], series("10"))
				setItemMedia(t, e, e.IDs["saga2"], series("2"))
				wantStatus(t, "second author", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Renamed", "Terry Fixture"), http.StatusOK)
				setItemMedia(t, e, e.IDs["ebook"], series("1.5"))
				page := authorItems(t, e, e.Token(t, "listener"), terry)
				if len(page.Series) != 1 || len(page.Series[0].Items) != 3 {
					t.Fatalf("series groups %+v", page.Series)
				}
				var got []string
				for _, item := range page.Series[0].Items {
					got = append(got, item.ID+"="+item.Media.Metadata.Series.Sequence)
				}
				want := []string{e.IDs["ebook"] + "=1.5", e.IDs["saga2"] + "=2", e.IDs["saga1"] + "=10"}
				if !slices.Equal(got, want) {
					t.Fatalf("series order %v, want %v", got, want)
				}
				// Restore the fixture links for the scenarios that follow.
				setItemMedia(t, e, e.IDs["ebook"], map[string]any{"metadata": map[string]any{"series": []map[string]string{}}})
				wantStatus(t, "restore authors", setItemAuthors(t, e, root, e.IDs["ebook"], "Ada Renamed"), http.StatusOK)
				setItemMedia(t, e, e.IDs["saga1"], series("1"))
				setItemMedia(t, e, e.IDs["saga2"], series("2"))
			},
		},
	}
}
