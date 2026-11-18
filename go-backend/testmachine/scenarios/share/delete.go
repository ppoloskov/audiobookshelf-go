package share

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// deleteScenarios covers route-r196: DELETE /api/share/mediaitem/:id.
func deleteScenarios() []harness.Scenario {
	const req = "route-r196"
	const inv = "INV-R196-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "delete-share-closes-access",
			Sources: []string{"server/controllers/ShareController.js:384-406", "server/managers/ShareManager.js:149-165"},
			Claim:   "Deleting an existing share as admin answers 204 with no body, removes the row, closes its public sessions (slug and track 404) and clears the item detail's mediaItemShare.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				created := mustCreateShare(t, e, "share-slug-delete", mediaIDForItem(t, e, fx.saga1), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				if resp := trackRequest(t, e, created.Slug, "1", cookie, nil); resp.Status != http.StatusOK {
					t.Fatalf("track before deletion = %d", resp.Status)
				}
				before := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				resp := removeShare(t, e, created.ID)
				wantStatus(t, resp, http.StatusNoContent, "delete share")
				if len(resp.Body) != 0 {
					t.Fatalf("204 must have no body, got %d bytes", len(resp.Body))
				}
				noShareRow(t, e, created.ID)
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != before-1 {
					t.Fatalf("mediaItemShares rows = %d, want %d", got, before-1)
				}
				wantStatus(t, publicGet(t, e, created.Slug, nil, "", nil), http.StatusNotFound, "slug after deletion")
				wantStatus(t, trackRequest(t, e, created.Slug, "1", cookie, nil), http.StatusNotFound, "track after deletion")
				if share, present := itemShareFromDetail(t, e, e.Token(t, "root"), fx.saga1); !present || share != nil {
					t.Fatalf("item detail must show no share after deletion: present=%v value=%v", present, share)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "delete-share-repeat-and-unknown",
			Sources: []string{"server/controllers/ShareController.js:386-395"},
			Claim:   "A second delete of the same share and a delete of an unknown id both answer 404, leave the row count unchanged and expose no public share.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-delete-twice", mediaIDForItem(t, e, fixtureOf(e).ebook), 0, false)
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "first delete")
				noShareRow(t, e, created.ID)
				rows := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNotFound, "repeat delete")
				wantStatus(t, removeShare(t, e, "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown delete")
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != rows {
					t.Fatalf("rejected deletes changed the row count: %d -> %d", rows, got)
				}
				wantStatus(t, publicGet(t, e, created.Slug, nil, "", nil), http.StatusNotFound, "deleted slug")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "delete-share-rejects-non-admin",
			Sources: []string{"server/controllers/ShareController.js:384-390"},
			Claim:   "A non-admin delete answers 403 and an unauthenticated delete answers 401 before any lookup, so an existing share row and its public session remain; an admin delete of an unknown id answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				created := mustCreateShare(t, e, "share-slug-protected", mediaIDForItem(t, e, fx.saga1), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				before := shareRows(t, e, created.ID)

				wantStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/share/mediaitem/"+created.ID), http.StatusForbidden, "listener delete")
				unauthenticated := e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/share/mediaitem/" + created.ID})
				wantStatus(t, unauthenticated, http.StatusUnauthorized, "unauthenticated delete")
				wantStatus(t, removeShare(t, e, "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown id")

				after := shareRows(t, e, created.ID)
				if len(after) != 1 || !rowsEqual(before[0], after[0]) {
					t.Fatalf("rejected deletes changed the share row: %v -> %v", before, after)
				}
				wantStatus(t, publicGet(t, e, created.Slug, nil, cookie, nil), http.StatusOK, "public share after rejected deletes")
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "cleanup protected share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "delete-share-recreated-slug-gets-new-session",
			Sources: []string{"server/controllers/ShareController.js:390-400", "server/controllers/ShareController.js:70-119"},
			Claim:   "After deleting a share, recreating the same slug creates a new share id; the old cookie's session is closed so its track denies, and re-opening the recreated slug binds the same cookie to a new session.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				mediaID := mediaIDForItem(t, e, fx.disposable)
				first := mustCreateShare(t, e, "share-slug-reborn", mediaID, 0, true)
				old, cookie := openPublicSession(t, e, first.Slug, nil, "")
				wantStatus(t, trackRequest(t, e, first.Slug, "1", cookie, nil), http.StatusOK, "track of the first share")
				wantStatus(t, removeShare(t, e, first.ID), http.StatusNoContent, "delete first share")
				wantStatus(t, trackRequest(t, e, first.Slug, "1", cookie, nil), http.StatusNotFound, "old cookie after deletion")

				second := mustCreateShare(t, e, first.Slug, mediaID, 0, true)
				if second.ID == first.ID {
					t.Fatalf("recreated share must have a new id, both %s", first.ID)
				}
				wantStatus(t, trackRequest(t, e, first.Slug, "1", cookie, nil), http.StatusNotFound, "old cookie before re-opening the slug")
				reopened, returned := openPublicSession(t, e, first.Slug, nil, cookie)
				if reopened.PlaybackSession.ID == old.PlaybackSession.ID {
					t.Fatal("re-opening the recreated slug must create a new session")
				}
				if returned != cookie || reopened.PlaybackSession.DeviceInfo.DeviceID != cookie {
					t.Fatalf("the recreated session must reuse the cookie device: cookie %q returned %q", cookie, returned)
				}
				wantStatus(t, trackRequest(t, e, first.Slug, "1", cookie, nil), http.StatusOK, "track of the recreated share")
				wantStatus(t, removeShare(t, e, second.ID), http.StatusNoContent, "cleanup recreated share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "delete-share-rejection-is-silent",
			Sources: []string{"server/controllers/ShareController.js:384-390", "server/managers/ShareManager.js:149-165", "server/SocketAuthority.js:81-90"},
			Claim:   "A refused delete (non-admin or unknown id) keeps the row and public session intact, emits no share_closed, and only the subsequent admin delete closes the share exactly once.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-delete-silent", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				admin := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				adminMark, listenerMark := admin.Mark(), listener.Mark()
				before := shareRows(t, e, created.ID)

				wantStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/share/mediaitem/"+created.ID), http.StatusForbidden, "listener delete")
				wantStatus(t, removeShare(t, e, "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown delete")
				if after := shareRows(t, e, created.ID); len(after) != 1 || !rowsEqual(before[0], after[0]) {
					t.Fatalf("refused deletes changed the row: %v -> %v", before, after)
				}
				wantStatus(t, publicGet(t, e, created.Slug, nil, cookie, nil), http.StatusOK, "public share after refused deletes")
				admin.None(t, adminMark, "share_closed", quietWindow, nil)
				listener.None(t, listenerMark, "share_closed", quietWindow, nil)

				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "admin delete")
				admin.Wait(t, adminMark, "share_closed", eventWait, matchShareID(created.ID))
				listener.None(t, listenerMark, "share_closed", quietWindow, nil)
				noShareRow(t, e, created.ID)
			},
		},
	}
}
