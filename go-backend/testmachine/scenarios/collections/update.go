package collections

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func patch(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return call(t, e, token, http.MethodPatch, "/api/collections/"+id, body)
}

func updateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r083", Invariant: "INV-R083-SAMPLED", Case: "normal", State: "initial", Assertion: "patch-name-and-description",
			Sources: []string{"server/routers/ApiRouter.js:149"},
			Claim:   "PATCH /api/collections/:id with name and description answers 200 with the updated expanded collection, strips tags from the name, persists both and advances updatedAt but not createdAt.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Before", ids.saga1, ids.novel)
				afterMillis(t, c.LastUpdate)
				got := decode(t, patch(t, e, e.Token(t, "root"), c.ID, map[string]any{"name": "<i>After</i>", "description": "Now described"}))
				if got.Name != "After" || got.Description == nil || *got.Description != "Now described" || got.ID != c.ID {
					t.Fatalf("unexpected response: %+v", got)
				}
				sameStrings(t, "books", got.itemIDs(), []string{ids.saga1, ids.novel})
				row := collectionRow(t, e, c.ID)
				if row["name"] != "After" || row["description"] != "Now described" {
					t.Fatalf("row not updated: %v", row)
				}
				if got.CreatedAt != c.CreatedAt || got.LastUpdate <= c.LastUpdate || millis(t, row["updatedAt"]) != got.LastUpdate || millis(t, row["createdAt"]) != c.CreatedAt {
					t.Fatalf("timestamps wrong: created %d/%d updated %d->%d row %v", c.CreatedAt, got.CreatedAt, c.LastUpdate, got.LastUpdate, row)
				}
				wantLinks(t, e, c.ID, ids.saga1, ids.novel)
			},
		},
		{
			Requirement: "route-r083", Invariant: "INV-R083-BOOKS-001", Case: "normal", State: "changed", Assertion: "patch-books-reorders-links",
			Sources: []string{"server/controllers/CollectionController.js:150-202"},
			Claim:   "PATCH with a books array of library item ids renumbers the links to that order, returns the books in that order, advances updatedAt and leaves name and description alone.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Reorder", ids.saga1, ids.novel, ids.saga2)
				want := []string{ids.saga2, ids.saga1, ids.novel}
				afterMillis(t, c.LastUpdate)
				got := decode(t, patch(t, e, e.Token(t, "root"), c.ID, map[string]any{"books": want}))
				sameStrings(t, "response books", got.itemIDs(), want)
				if got.Name != "Reorder" || got.LastUpdate <= c.LastUpdate {
					t.Fatalf("name or timestamp wrong: %+v", got)
				}
				wantLinks(t, e, c.ID, want...)
				if millis(t, collectionRow(t, e, c.ID)["updatedAt"]) != got.LastUpdate {
					t.Fatal("updatedAt row differs from lastUpdate")
				}
				sameStrings(t, "GET books", fetch(t, e, c.ID).itemIDs(), want)
			},
		},
		{
			Requirement: "route-r083", Invariant: "INV-R083-SAMPLED", Case: "edge", State: "repeat", Assertion: "patch-repeat-and-empty-change-keep-row",
			Sources: []string{"server/routers/ApiRouter.js:149"},
			Claim:   "An applied PATCH moves lastUpdate strictly forward; repeating it, an empty body, an empty books array and a non-string name answer 200 with the collection and leave every row, including updatedAt, untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Stable", ids.saga1, ids.novel)
				afterMillis(t, c.LastUpdate)
				applied := decode(t, patch(t, e, root, c.ID, map[string]any{"name": "Stable 2", "description": "d", "books": []string{ids.novel, ids.saga1}}))
				if applied.LastUpdate <= c.LastUpdate {
					t.Fatalf("an applied change must move lastUpdate forward: %d -> %d", c.LastUpdate, applied.LastUpdate)
				}
				afterMillis(t, applied.LastUpdate)
				unchanged(t, e, func() {
					for _, body := range []any{
						map[string]any{"name": "Stable 2", "description": "d", "books": []string{ids.novel, ids.saga1}},
						map[string]any{},
						map[string]any{"books": []string{}},
						map[string]any{"name": 7},
					} {
						got := decode(t, patch(t, e, root, c.ID, body))
						if got.Name != "Stable 2" || got.LastUpdate != applied.LastUpdate {
							t.Fatalf("body %v changed the collection: %+v", body, got)
						}
						sameStrings(t, "books", got.itemIDs(), []string{ids.novel, ids.saga1})
					}
				})
			},
		},
		{
			Requirement: "route-r083", Invariant: "INV-R083-SAMPLED", Case: "edge", State: "changed", Assertion: "patch-null-description-clears-and-partial-keeps",
			Sources: []string{"server/routers/ApiRouter.js:149"},
			Claim:   "PATCH with description null clears the stored description, while a body without description or name keeps them.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Keep name", ids.saga1)
				decode(t, patch(t, e, root, c.ID, map[string]any{"description": "to clear"}))
				kept := decode(t, patch(t, e, root, c.ID, map[string]any{"books": []string{ids.saga1}}))
				if kept.Description == nil || *kept.Description != "to clear" || kept.Name != "Keep name" {
					t.Fatalf("omitted keys must be kept: %+v", kept)
				}
				cleared := decode(t, patch(t, e, root, c.ID, map[string]any{"description": nil}))
				if cleared.Description != nil || cleared.Name != "Keep name" {
					t.Fatalf("null description must clear it: %+v", cleared)
				}
				if row := collectionRow(t, e, c.ID); row["description"] != nil || row["name"] != "Keep name" {
					t.Fatalf("row wrong: %v", row)
				}
			},
		},
		{
			Requirement: "route-r083", Invariant: "INV-R083-ACCESS-001", Case: "error", State: "failure", Assertion: "patch-rejected-unchanged",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:433-453"},
			Claim:   "PATCH answers 401 without a token, 403 without update permission, 404 for an unknown collection and 404 for a collection in a library the user cannot access, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Guarded", ids.saga1)
				body := map[string]any{"name": "Hijacked", "description": "x", "books": []string{ids.saga1}}
				unchanged(t, e, func() {
					wantStatus(t, patch(t, e, "", c.ID, body), http.StatusUnauthorized)
					wantStatus(t, patch(t, e, e.Token(t, "listener"), c.ID, body), http.StatusForbidden)
					wantStatus(t, patch(t, e, e.Token(t, "root"), "no-such-collection", body), http.StatusNotFound)
					// the collection lookup runs before the permission check
					wantStatus(t, patch(t, e, e.Token(t, "listener"), "no-such-collection", body), http.StatusNotFound)
					wantStatus(t, patch(t, e, podcastOnly(t, e), c.ID, body), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r084", Invariant: "INV-R084-SAMPLED", Case: "normal", State: "initial", Assertion: "delete-removes-collection-links-and-feed",
			Sources: []string{"server/routers/ApiRouter.js:150"},
			Claim:   "DELETE /api/collections/:id answers 200, removes the collection row, its links and its open RSS feed, and keeps the library items.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Doomed", ids.saga1, ids.novel)
				openFeed(t, e, c.ID, "doomed-feed")
				if e.Count(t, "SELECT COUNT(*) FROM feeds WHERE entityId = ?", c.ID) != 1 {
					t.Fatal("feed was not persisted")
				}
				itemsBefore := e.Count(t, "SELECT COUNT(*) FROM libraryItems")
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/"+c.ID), http.StatusOK)
				for _, query := range []string{
					"SELECT COUNT(*) FROM collections WHERE id = ?",
					"SELECT COUNT(*) FROM collectionBooks WHERE collectionId = ?",
					"SELECT COUNT(*) FROM feeds WHERE entityId = ?",
				} {
					if n := e.Count(t, query, c.ID); n != 0 {
						t.Fatalf("%s: %d rows remain", query, n)
					}
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems") != itemsBefore || e.Count(t, "SELECT COUNT(*) FROM books") < 5 {
					t.Fatal("deleting a collection must keep library items and books")
				}
				wantStatus(t, e.Get(t, e.Token(t, "root"), "/api/collections/"+c.ID), http.StatusNotFound)
			},
		},
		{
			Requirement: "route-r084", Invariant: "INV-R084-SAMPLED", Case: "edge", State: "repeat", Assertion: "delete-repeat-is-not-found",
			Sources: []string{"server/routers/ApiRouter.js:150"},
			Claim:   "Deleting the same collection a second time answers 404 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				c := create(t, e, "Twice", fixtureItems(e).saga1)
				root := e.Token(t, "root")
				wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusOK)
				unchanged(t, e, func() {
					wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusNotFound)
				})
			},
		},
		{
			Requirement: "route-r084", Invariant: "INV-R084-SAMPLED", Case: "edge", State: "changed", Assertion: "delete-emptied-collection",
			Sources: []string{"server/routers/ApiRouter.js:150"},
			Claim:   "A collection whose books were all removed is still readable with an empty books array and can be deleted.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Emptied", ids.saga1, ids.novel)
				empty := decode(t, call(t, e, root, http.MethodPost, "/api/collections/"+c.ID+"/batch/remove", map[string]any{"books": []string{ids.saga1, ids.novel}}))
				if empty.Books == nil || len(empty.Books) != 0 {
					t.Fatalf("books must be an empty array: %+v", empty.Books)
				}
				for _, who := range []string{"root", "listener"} {
					if got := decode(t, e.Get(t, e.Token(t, who), "/api/collections/"+c.ID)); len(got.Books) != 0 {
						t.Fatalf("%s sees books of an emptied collection", who)
					}
				}
				wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusOK)
				if e.Count(t, "SELECT COUNT(*) FROM collections WHERE id = ?", c.ID) != 0 {
					t.Fatal("collection row remains")
				}
			},
		},
		{
			Requirement: "route-r084", Invariant: "INV-R084-ACCESS-001", Case: "error", State: "failure", Assertion: "delete-rejected-unchanged",
			Sources: []string{"server/Server.js:354", "server/Auth.js:61-63", "server/controllers/CollectionController.js:433-453"},
			Claim:   "DELETE answers 401 without a token, 403 for users without delete permission, 404 for an unknown collection or one in an inaccessible library, and removes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				c := create(t, e, "Protected", fixtureItems(e).saga1)
				path := "/api/collections/" + c.ID
				unchanged(t, e, func() {
					wantStatus(t, e.Delete(t, "", path), http.StatusUnauthorized)
					wantStatus(t, e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden)
					wantStatus(t, e.Delete(t, editor(t, e), path), http.StatusForbidden)
					wantStatus(t, e.Delete(t, podcastOnly(t, e), path), http.StatusNotFound)
					wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/no-such-collection"), http.StatusNotFound)
					// the collection lookup runs before the permission check
					wantStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/collections/no-such-collection"), http.StatusNotFound)
					wantStatus(t, e.Delete(t, editor(t, e), "/api/collections/no-such-collection"), http.StatusNotFound)
				})
				fetch(t, e, c.ID)
			},
		},
	}
}
