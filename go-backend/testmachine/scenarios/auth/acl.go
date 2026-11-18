package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// aclItemScenarios are the item-visibility scenarios of sub-authorization-ownership-w5.
func aclItemScenarios() []harness.Scenario {
	const (
		req  = "sub-authorization-ownership-w5"
		item = "INV-W5-ACL-ITEM-001"
	)
	return []harness.Scenario{
		{
			Requirement: req, Invariant: item, Case: "error", State: "initial", Assertion: "library-restriction-denies-items",
			Sources: []string{"server/models/User.js:646-653", "server/controllers/LibraryItemController.js:1220-1227", "server/controllers/LibraryController.js:188-195", "server/controllers/LibraryController.js:1475-1479", "server/controllers/CollectionController.js:434-445"},
			Claim:   "A user limited to the podcast library gets 403 for items and the item list of the other library, 404 for its collections, and sees only the podcast library in the library list.",
			Run:     checkLibraryRestriction,
		},
		{
			Requirement: req, Invariant: item, Case: "normal", State: "initial", Assertion: "tag-allowlist-limits-items",
			Sources: []string{"server/models/User.js:658-669", "server/controllers/LibraryItemController.js:1225"},
			Claim:   "A user restricted to a selected tag reaches only items with that tag (403 otherwise, and only those in the item list); with selectedTagsNotAccessible the selection is inverted and untagged items stay visible.",
			Run:     checkTagAllowlist,
		},
		{
			Requirement: req, Invariant: item, Case: "edge", State: "initial", Assertion: "empty-tag-allowlist-sees-nothing",
			Sources: []string{"server/models/User.js:658-669"},
			Claim:   "A user with accessAllTags false and no selected tags gets 403 for every item it requests directly.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllTags": false}})
				token := tokenFor(t, e, acc)
				for _, id := range []string{e.IDs["novel"], e.IDs["saga1"], e.IDs["ebook"]} {
					requireStatus(t, "item "+id, e.Get(t, token, "/api/items/"+id), http.StatusForbidden)
				}
				// Not asserted: upstream's list filters ignore an empty selection
				// (libraryItemsBookFilters.js:27) and still list every item.
			},
		},
		{
			Requirement: req, Invariant: item, Case: "normal", State: "initial", Assertion: "explicit-content-gate",
			Sources: []string{"server/models/User.js:675-683", "server/controllers/LibraryItemController.js:1225"},
			Claim:   "An item marked explicit is denied with 403 and hidden from the list for a user without explicit-content access and visible for one with it; unmarking makes it visible again.",
			Run:     checkExplicitGate,
		},
		{
			Requirement: req, Invariant: item, Case: "normal", State: "initial", Assertion: "hidden-items-absent-from-search-personalized-series-authors",
			Sources: []string{"server/routers/ApiRouter.js:75-86", "server/controllers/LibraryController.js:1475-1479", "server/models/User.js:646-683"},
			Claim:   "An item hidden by a tag rule or by the explicit-content rule is missing from the library search, personalized shelves, series list and author counts of that user while the admin still sees it, and a user without library access gets 403 on all of them.",
			Run:     checkHiddenFromListings,
		},
		{
			Requirement: req, Invariant: item, Case: "normal", State: "changed", Assertion: "permission-edit-applies-to-live-token",
			Sources: []string{"server/models/User.js:658-669", "server/controllers/UserController.js:254-320"},
			Claim:   "Changing a user's tag selection through PATCH /api/users/:id takes effect for their existing access token.",
			Run:     checkLivePermissionEdit,
		},
		{
			Requirement: req, Invariant: item, Case: "error", State: "failure", Assertion: "denied-item-write-changes-nothing",
			Sources: []string{"server/controllers/LibraryItemController.js:1220-1227"},
			Claim:   "A user who may update but cannot access the item's library gets 403 for PATCH /api/items/:id/media and the item's tags and updatedAt are unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, map[string]any{
					"permissions":         map[string]any{"update": true, "accessAllLibraries": false},
					"librariesAccessible": []string{e.IDs["podcastLibrary"]},
				})
				before := e.Rows(t, "SELECT tags, updatedAt FROM books WHERE id = (SELECT mediaId FROM libraryItems WHERE id = ?)", e.IDs["novel"])
				resp := e.Patch(t, tokenFor(t, e, acc), "/api/items/"+e.IDs["novel"]+"/media", map[string]any{"tags": []string{"intruder"}})
				requireStatus(t, "denied update", resp, http.StatusForbidden)
				after := e.Rows(t, "SELECT tags, updatedAt FROM books WHERE id = (SELECT mediaId FROM libraryItems WHERE id = ?)", e.IDs["novel"])
				if len(after) != 1 || after[0]["tags"] != before[0]["tags"] || after[0]["updatedAt"] != before[0]["updatedAt"] {
					t.Fatalf("denied update changed the item: %v -> %v", before, after)
				}
			},
		},
	}
}

// listTotal returns the total of GET /api/libraries/:id/items for the main library.
func listTotal(t *testing.T, e *harness.Env, token string) int {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+e.IDs["library"]+"/items?limit=100")
	requireStatus(t, "item list", resp, http.StatusOK)
	var list struct {
		Total int `json:"total"`
	}
	resp.JSON(t, &list)
	return list.Total
}

// setItemMedia patches an item's media as root and requires 200.
func setItemMedia(t *testing.T, e *harness.Env, itemID string, body map[string]any) {
	t.Helper()
	requireStatus(t, "patch item media", e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", body), http.StatusOK)
}

func checkLibraryRestriction(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, map[string]any{
		"permissions":         map[string]any{"accessAllLibraries": false},
		"librariesAccessible": []string{e.IDs["podcastLibrary"]},
	})
	token := tokenFor(t, e, acc)
	requireStatus(t, "item of the other library", e.Get(t, token, "/api/items/"+e.IDs["novel"]), http.StatusForbidden)
	requireStatus(t, "item of the allowed library", e.Get(t, token, "/api/items/"+e.IDs["podcast"]), http.StatusOK)
	requireStatus(t, "list of the other library", e.Get(t, token, "/api/libraries/"+e.IDs["library"]+"/items"), http.StatusForbidden)
	requireStatus(t, "collection of the other library", e.Get(t, token, "/api/collections/"+e.IDs["collection"]), http.StatusNotFound)
	resp := e.Get(t, token, "/api/libraries")
	requireStatus(t, "libraries", resp, http.StatusOK)
	var libs struct {
		Libraries []struct {
			ID string `json:"id"`
		} `json:"libraries"`
	}
	resp.JSON(t, &libs)
	if len(libs.Libraries) != 1 || libs.Libraries[0].ID != e.IDs["podcastLibrary"] {
		t.Fatalf("libraries = %s", resp.Body)
	}
}

func checkTagAllowlist(t *testing.T, e *harness.Env) {
	setItemMedia(t, e, e.IDs["novel"], map[string]any{"tags": []string{"w5-tag"}})
	defer setItemMedia(t, e, e.IDs["novel"], map[string]any{"tags": []string{}})
	total := listTotal(t, e, e.Token(t, "root"))
	selected := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllTags": false}, "itemTagsSelected": []string{"w5-tag"}}))
	requireStatus(t, "tagged item", e.Get(t, selected, "/api/items/"+e.IDs["novel"]), http.StatusOK)
	requireStatus(t, "untagged item", e.Get(t, selected, "/api/items/"+e.IDs["saga1"]), http.StatusForbidden)
	if got := listTotal(t, e, selected); got != 1 {
		t.Fatalf("selected-tag list total = %d, want 1", got)
	}
	inverted := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}, "itemTagsSelected": []string{"w5-tag"}}))
	requireStatus(t, "tagged item (inverted)", e.Get(t, inverted, "/api/items/"+e.IDs["novel"]), http.StatusForbidden)
	requireStatus(t, "untagged item (inverted)", e.Get(t, inverted, "/api/items/"+e.IDs["saga1"]), http.StatusOK)
	if got := listTotal(t, e, inverted); got != total-1 {
		t.Fatalf("inverted list total = %d, want %d", got, total-1)
	}
}

func checkExplicitGate(t *testing.T, e *harness.Env) {
	item := e.IDs["disposable"]
	setItemMedia(t, e, item, map[string]any{"metadata": map[string]any{"explicit": true}})
	defer setItemMedia(t, e, item, map[string]any{"metadata": map[string]any{"explicit": false}})
	allowed, denied := e.Token(t, "listener"), e.Token(t, "finisher") // finisher: accessExplicitContent=false
	requireStatus(t, "allowed user", e.Get(t, allowed, "/api/items/"+item), http.StatusOK)
	requireStatus(t, "denied user", e.Get(t, denied, "/api/items/"+item), http.StatusForbidden)
	if listTotal(t, e, denied) != listTotal(t, e, allowed)-1 {
		t.Fatal("the explicit item must be missing from the denied user's list only")
	}
	setItemMedia(t, e, item, map[string]any{"metadata": map[string]any{"explicit": false}})
	requireStatus(t, "after unmarking", e.Get(t, denied, "/api/items/"+item), http.StatusOK)
}

func checkLivePermissionEdit(t *testing.T, e *harness.Env) {
	setItemMedia(t, e, e.IDs["novel"], map[string]any{"tags": []string{"w5-live"}})
	defer setItemMedia(t, e, e.IDs["novel"], map[string]any{"tags": []string{}})
	acc := newAccount(t, e, nil)
	token := tokenFor(t, e, acc)
	requireStatus(t, "before", e.Get(t, token, "/api/items/"+e.IDs["novel"]), http.StatusOK)
	patchUser(t, e, acc.ID, map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}, "itemTagsSelected": []string{"w5-live"}})
	requireStatus(t, "after restriction", e.Get(t, token, "/api/items/"+e.IDs["novel"]), http.StatusForbidden)
	patchUser(t, e, acc.ID, map[string]any{"permissions": map[string]any{"accessAllTags": true}})
	requireStatus(t, "after lifting", e.Get(t, token, "/api/items/"+e.IDs["novel"]), http.StatusOK)
}

func checkHiddenFromListings(t *testing.T, e *harness.Env) {
	setItemMedia(t, e, e.IDs["saga1"], map[string]any{"tags": []string{"hide-me"}})
	setItemMedia(t, e, e.IDs["disposable"], map[string]any{"metadata": map[string]any{"explicit": true}})
	defer setItemMedia(t, e, e.IDs["saga1"], map[string]any{"tags": []string{}})
	defer setItemMedia(t, e, e.IDs["disposable"], map[string]any{"metadata": map[string]any{"explicit": false}})
	library := "/api/libraries/" + e.IDs["library"]
	root := e.Token(t, "root")
	tagUser := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}, "itemTagsSelected": []string{"hide-me"}}))
	explicitUser := e.Token(t, "finisher") // accessExplicitContent=false
	body := func(token, path string) []byte {
		resp := e.Get(t, token, library+path)
		requireStatus(t, path, resp, http.StatusOK)
		return resp.Body
	}
	has := func(raw []byte, id string) bool { return bytes.Contains(raw, []byte(id)) }
	authors := func(token string) map[string]int {
		var list struct {
			Authors []struct {
				Name     string `json:"name"`
				NumBooks int    `json:"numBooks"`
			} `json:"authors"`
		}
		json.Unmarshal(body(token, "/authors"), &list) // a decode failure leaves an empty map and fails below
		counts := map[string]int{}
		for _, a := range list.Authors {
			counts[a.Name] = a.NumBooks
		}
		return counts
	}
	// the admin sees everything
	for path, id := range map[string]string{"/search?q=Beginning": e.IDs["saga1"], "/personalized": e.IDs["saga1"], "/series?limit=50": e.IDs["saga1"], "/search?q=Hard": e.IDs["disposable"]} {
		if !has(body(root, path), id) {
			t.Fatalf("admin %s must list %s", path, id)
		}
	}
	rootAuthors := authors(root)
	if rootAuthors["Terry Fixture"] != 2 || rootAuthors["Zed Disposable"] != 1 {
		t.Fatalf("admin author counts = %v", rootAuthors)
	}
	// tag rule
	for _, path := range []string{"/search?q=Beginning", "/personalized", "/series?limit=50"} {
		if has(body(tagUser, path), e.IDs["saga1"]) {
			t.Fatalf("tag-restricted user must not see the hidden item in %s", path)
		}
	}
	if !has(body(tagUser, "/series?limit=50"), e.IDs["saga2"]) || !has(body(tagUser, "/personalized"), e.IDs["novel"]) {
		t.Fatal("visible items must stay listed")
	}
	if got := authors(tagUser); got["Terry Fixture"] != 1 || got["Ada Example"] != 2 {
		t.Fatalf("tag-restricted author counts = %v", got)
	}
	// explicit-content rule
	for _, path := range []string{"/search?q=Hard", "/personalized"} {
		if has(body(explicitUser, path), e.IDs["disposable"]) {
			t.Fatalf("user without explicit access must not see the explicit item in %s", path)
		}
	}
	if got := authors(explicitUser); got["Zed Disposable"] != 0 || got["Terry Fixture"] != 2 {
		t.Fatalf("explicit-restricted author counts = %v", got)
	}
	// library rule
	outsider := tokenFor(t, e, newAccount(t, e, map[string]any{"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"]}}))
	for _, path := range []string{"/search?q=Beginning", "/personalized", "/series", "/authors", "/stats"} {
		requireStatus(t, path, e.Get(t, outsider, library+path), http.StatusForbidden)
	}
}
