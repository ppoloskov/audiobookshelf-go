package collections

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// wantPayload asserts a socket payload equals the collection the HTTP route returned.
func wantPayload(t *testing.T, got, want collection) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name || got.LibraryID != want.LibraryID || got.LastUpdate != want.LastUpdate || got.CreatedAt != want.CreatedAt {
		t.Fatalf("event payload %+v differs from response %+v", got, want)
	}
	sameStrings(t, "event books", got.itemIDs(), want.itemIDs())
	for _, book := range got.Books {
		if book.MediaType != "book" || book.Media.Metadata.Title == "" {
			t.Fatalf("event books must be expanded library items: %+v", book)
		}
	}
}

func eventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-collection-added", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-ADDED", Case: "normal", State: "initial", Assertion: "event-added-reaches-every-authenticated-socket",
			Sources: []string{"server/controllers/CollectionController.js:99-105", "server/SocketAuthority.js:57-66"},
			Claim:   "A successful create emits collection_added with the expanded collection to the sockets of root, ordinary users, an updater and a user without library access, and not to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := connect(t, e)
				c := create(t, e, "Announced", ids.saga2, ids.novel)
				got := a.receive(t, "collection_added", c.ID)
				wantPayload(t, got, c)
				a.none(t, "collection_updated")
				a.none(t, "collection_removed")
			},
		},
		{
			Requirement: "socket-outgoing-collection-added", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-ADDED", Case: "edge", State: "initial", Assertion: "event-added-not-replayed-to-late-socket",
			Sources: []string{"server/SocketAuthority.js:57-66"},
			Claim:   "A socket that authenticates after the create receives no collection_added for it.",
			Run: func(t *testing.T, e *harness.Env) {
				c := create(t, e, "Before the socket", fixtureItems(e).saga1)
				late := e.Socket(t, e.Token(t, "root"))
				late.None(t, 0, "collection_added", quietWindow, matchID(c.ID))
			},
		},
		{
			Requirement: "socket-outgoing-collection-added", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-ADDED", Case: "normal", State: "repeat", Assertion: "event-added-repeat-emits-new-collection",
			Sources: []string{"server/controllers/CollectionController.js:104"},
			Claim:   "Creating the same collection twice emits two collection_added events with different ids, one per create.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := connect(t, e)
				first := create(t, e, "Same again", ids.ebook)
				second := create(t, e, "Same again", ids.ebook)
				wantPayload(t, a.receive(t, "collection_added", first.ID), first)
				wantPayload(t, a.receive(t, "collection_added", second.ID), second)
				if got := a.payloads("listener", "collection_added"); len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
					t.Fatalf("listener must get exactly the two creates in order: %+v", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-collection-added", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-ADDED", Case: "normal", State: "changed", Assertion: "event-added-by-updater-user",
			Sources: []string{"server/controllers/CollectionController.js:104", "server/controllers/CollectionController.js:450-453"},
			Claim:   "A collection created by a non-admin user with update permission is announced to every authenticated socket, including root's.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connect(t, e)
				c := createAs(t, e, editor(t, e), "By an editor", fixtureItems(e).novel)
				wantPayload(t, a.receive(t, "collection_added", c.ID), c)
			},
		},
		{
			Requirement: "socket-outgoing-collection-added", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-ADDED", Case: "error", State: "failure", Assertion: "event-added-not-sent-for-rejected-create",
			Sources: []string{"server/controllers/CollectionController.js:38-64", "server/controllers/CollectionController.js:450-453"},
			Claim:   "Rejected creates (invalid body, unknown book, no permission, no token) emit no collection_added to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				a := connect(t, e)
				body := func(name string, books ...string) map[string]any {
					return map[string]any{"libraryId": e.IDs["library"], "name": name, "books": books}
				}
				wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", body("")), http.StatusBadRequest)
				wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodPost, "/api/collections", body("x", ids.novel, "no-such-item")), http.StatusBadRequest)
				wantStatus(t, call(t, e, e.Token(t, "listener"), http.MethodPost, "/api/collections", body("x", ids.novel)), http.StatusForbidden)
				wantStatus(t, call(t, e, "", http.MethodPost, "/api/collections", body("x", ids.novel)), http.StatusUnauthorized)
				a.none(t, "collection_added")
			},
		},
		{
			Requirement: "socket-outgoing-collection-removed", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-REMOVED", Case: "normal", State: "initial", Assertion: "event-removed-reaches-every-authenticated-socket",
			Sources: []string{"server/controllers/CollectionController.js:214-224", "server/SocketAuthority.js:57-66"},
			Claim:   "Deleting a collection emits collection_removed with the collection as it was (id, name, books) to every authenticated socket, including a user without library access, and not to an unauthenticated one.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Removed soon", ids.saga1, ids.novel)
				a := connect(t, e)
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/"+c.ID), http.StatusOK)
				got := a.receive(t, "collection_removed", c.ID)
				wantPayload(t, got, c)
				a.none(t, "collection_updated")
			},
		},
		{
			Requirement: "socket-outgoing-collection-removed", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-REMOVED", Case: "edge", State: "changed", Assertion: "event-removed-emptied-collection-has-no-books",
			Sources: []string{"server/controllers/CollectionController.js:215", "server/controllers/CollectionController.js:222"},
			Claim:   "Deleting a collection after its books were removed emits collection_removed with an empty books array.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Emptied then removed", ids.saga1)
				emptied := decode(t, batch(t, e, root, c.ID, "remove", []string{ids.saga1}))
				a := connect(t, e)
				wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusOK)
				got := a.receive(t, "collection_removed", c.ID)
				if got.Books == nil || len(got.Books) != 0 || got.Name != "Emptied then removed" || got.LastUpdate != emptied.LastUpdate {
					t.Fatalf("payload %+v", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-collection-removed", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-REMOVED", Case: "error", State: "repeat", Assertion: "event-removed-not-repeated-for-second-delete",
			Sources: []string{"server/controllers/CollectionController.js:433-438"},
			Claim:   "A second delete of the same collection answers 404 and emits no further collection_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				c := create(t, e, "Removed once", fixtureItems(e).saga1)
				root := e.Token(t, "root")
				a := connect(t, e)
				wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusOK)
				a.receive(t, "collection_removed", c.ID)
				a.mark()
				wantStatus(t, e.Delete(t, root, "/api/collections/"+c.ID), http.StatusNotFound)
				a.none(t, "collection_removed")
			},
		},
		{
			Requirement: "socket-outgoing-collection-removed", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-REMOVED", Case: "error", State: "failure", Assertion: "event-removed-not-sent-for-rejected-delete",
			Sources: []string{"server/controllers/CollectionController.js:433-453"},
			Claim:   "Rejected deletes (no token, no delete permission, unknown or inaccessible collection) emit no collection_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				c := create(t, e, "Survives", fixtureItems(e).saga1)
				a := connect(t, e)
				path := "/api/collections/" + c.ID
				wantStatus(t, e.Delete(t, "", path), http.StatusUnauthorized)
				wantStatus(t, e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden)
				wantStatus(t, e.Delete(t, editor(t, e), path), http.StatusForbidden)
				wantStatus(t, e.Delete(t, podcastOnly(t, e), path), http.StatusNotFound)
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/no-such-collection"), http.StatusNotFound)
				a.none(t, "collection_removed")
				fetch(t, e, c.ID)
			},
		},
		{
			Requirement: "socket-outgoing-collection-updated", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-UPDATED", Case: "normal", State: "initial", Assertion: "event-updated-by-patch",
			Sources: []string{"server/controllers/CollectionController.js:199-203", "server/SocketAuthority.js:57-66"},
			Claim:   "An accepted PATCH emits collection_updated with the expanded updated collection to every authenticated socket, including a user without library access, and not to an unauthenticated one.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				c := create(t, e, "Patched", ids.saga1, ids.novel)
				a := connect(t, e)
				resp := decode(t, patch(t, e, e.Token(t, "root"), c.ID, map[string]any{"name": "Patched twice"}))
				got := a.receive(t, "collection_updated", c.ID)
				wantPayload(t, got, resp)
				if got.Name != "Patched twice" {
					t.Fatalf("payload name %q", got.Name)
				}
				a.none(t, "collection_added")
			},
		},
		{
			Requirement: "socket-outgoing-collection-updated", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-UPDATED", Case: "normal", State: "changed", Assertion: "event-updated-at-every-call-site",
			Sources: []string{"server/controllers/CollectionController.js:201", "server/controllers/CollectionController.js:258", "server/controllers/CollectionController.js:303", "server/controllers/CollectionController.js:365", "server/controllers/CollectionController.js:422"},
			Claim:   "Reordering books by PATCH, adding a book, removing a book, batch add and batch remove each emit one collection_updated carrying the resulting books to every authenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Every route", ids.saga1, ids.novel)
				a := connect(t, e)
				steps := []struct {
					name string
					act  func() harness.Response
					want []string
				}{
					{"patch reorder", func() harness.Response {
						return patch(t, e, root, c.ID, map[string]any{"books": []string{ids.novel, ids.saga1}})
					}, []string{ids.novel, ids.saga1}},
					{"add book", func() harness.Response { return addBook(t, e, root, c.ID, map[string]any{"id": ids.saga2}) }, []string{ids.novel, ids.saga1, ids.saga2}},
					{"remove book", func() harness.Response { return removeBook(t, e, root, c.ID, ids.novel) }, []string{ids.saga1, ids.saga2}},
					{"batch add", func() harness.Response { return batch(t, e, root, c.ID, "add", []string{ids.ebook}) }, []string{ids.saga1, ids.saga2, ids.ebook}},
					{"batch remove", func() harness.Response { return batch(t, e, root, c.ID, "remove", []string{ids.saga1, ids.ebook}) }, []string{ids.saga2}},
				}
				for _, step := range steps {
					a.mark()
					resp := decode(t, step.act())
					sameStrings(t, step.name+" response books", resp.itemIDs(), step.want)
					got := a.receive(t, "collection_updated", c.ID)
					wantPayload(t, got, resp)
					if n := len(a.payloads("listener", "collection_updated")); n != 1 {
						t.Fatalf("%s: listener got %d collection_updated events, want 1", step.name, n)
					}
				}
			},
		},
		{
			Requirement: "socket-outgoing-collection-updated", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-UPDATED", Case: "edge", State: "repeat", Assertion: "event-updated-not-sent-for-noop-requests",
			Sources: []string{"server/controllers/CollectionController.js:164", "server/controllers/CollectionController.js:200", "server/controllers/CollectionController.js:286", "server/controllers/CollectionController.js:361", "server/controllers/CollectionController.js:421"},
			Claim:   "Requests that change nothing (same PATCH again, empty PATCH, removing a non-member, batch add of members, batch remove of non-members) answer 200 and emit no collection_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				c := create(t, e, "Quiet", ids.saga1, ids.novel)
				decode(t, patch(t, e, root, c.ID, map[string]any{"name": "Quiet 2"}))
				a := connect(t, e)
				decode(t, patch(t, e, root, c.ID, map[string]any{"name": "Quiet 2"}))
				decode(t, patch(t, e, root, c.ID, map[string]any{}))
				decode(t, removeBook(t, e, root, c.ID, ids.saga2))
				decode(t, batch(t, e, root, c.ID, "add", []string{ids.saga1, ids.novel}))
				decode(t, batch(t, e, root, c.ID, "remove", []string{ids.saga2}))
				a.none(t, "collection_updated")
			},
		},
		{
			Requirement: "socket-outgoing-collection-updated", Invariant: "INV-SOCKET-OUTGOING-COLLECTION-UPDATED", Case: "error", State: "failure", Assertion: "event-updated-not-sent-for-rejected-requests",
			Sources: []string{"server/controllers/CollectionController.js:238-249", "server/controllers/CollectionController.js:321-336", "server/controllers/CollectionController.js:433-453"},
			Claim:   "Rejected PATCH, add, remove and batch requests (no token, no permission, unknown collection, duplicate or invalid books) emit no collection_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := fixtureItems(e)
				root := e.Token(t, "root")
				listener := e.Token(t, "listener")
				c := create(t, e, "Rejections are silent", ids.novel)
				a := connect(t, e)
				wantStatus(t, patch(t, e, listener, c.ID, map[string]any{"name": "x"}), http.StatusForbidden)
				wantStatus(t, patch(t, e, root, "no-such-collection", map[string]any{"name": "x"}), http.StatusNotFound)
				wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.novel}), http.StatusBadRequest)
				wantStatus(t, addBook(t, e, root, c.ID, map[string]any{"id": ids.podcast}), http.StatusBadRequest)
				wantStatus(t, addBook(t, e, "", c.ID, map[string]any{"id": ids.saga1}), http.StatusUnauthorized)
				wantStatus(t, removeBook(t, e, root, c.ID, "no-such-item"), http.StatusNotFound)
				wantStatus(t, batch(t, e, root, c.ID, "add", []string{"no-such-item"}), http.StatusBadRequest)
				wantStatus(t, batch(t, e, listener, c.ID, "add", []string{ids.saga1}), http.StatusForbidden)
				wantStatus(t, batch(t, e, root, c.ID, "remove", []string{}), http.StatusInternalServerError)
				wantStatus(t, batch(t, e, podcastOnly(t, e), c.ID, "remove", []string{ids.novel}), http.StatusNotFound)
				a.none(t, "collection_updated")
			},
		},
	}
}
