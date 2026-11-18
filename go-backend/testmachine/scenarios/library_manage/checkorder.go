package library_manage

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const unknownLibrary = "00000000-0000-4000-8000-000000000000"

func checkOrderScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r017", Invariant: "INV-R017-AUTH-001", Case: "error", State: "initial", Assertion: "create-admin-check-precedes-validation",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:51-58"},
			Claim:   "POST /api/libraries by a non-admin with an invalid body answers 403, not 400, and creates nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshot(t, e)
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/libraries", map[string]any{"name": 5, "folders": "x"}), http.StatusForbidden, "non-admin invalid create")
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r041", Invariant: "INV-R041-AUTH-001", Case: "error", State: "initial", Assertion: "order-admin-check-precedes-validation",
			Sources: []string{orderRoute, "server/controllers/LibraryController.js:914-922"},
			Claim:   "POST /api/libraries/order by a non-admin with an invalid body answers 403, not 400, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshot(t, e)
				requireStatus(t, postOrder(t, e, e.Token(t, "listener"), map[string]any{"not": "an array"}), http.StatusForbidden, "non-admin invalid order")
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-AUTH-001", Case: "error", State: "initial", Assertion: "update-lookup-precedes-admin-check-precedes-validation",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:1475-1484", "server/controllers/LibraryController.js:239-262"},
			Claim:   "PATCH /api/libraries/:id by a non-admin all-access user answers 404 for an unknown id and 403 (not 400) for an existing library with an invalid body.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "precedence")
				before := snapshot(t, e)
				listener := e.Token(t, "listener")
				requireStatus(t, e.Patch(t, listener, "/api/libraries/"+unknownLibrary, map[string]any{"name": "x"}), http.StatusNotFound, "unknown id")
				requireStatus(t, e.Patch(t, listener, "/api/libraries/"+lib.ID, map[string]any{"name": 5, "displayOrder": "abc"}), http.StatusForbidden, "invalid body")
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r021", Invariant: "INV-R021-AUTH-001", Case: "error", State: "initial", Assertion: "delete-lookup-precedes-admin-check",
			Sources: []string{deleteRoute, "server/controllers/LibraryController.js:1475-1484", "server/controllers/LibraryController.js:497-505"},
			Claim:   "DELETE /api/libraries/:id by a non-admin all-access user answers 404 for an unknown id.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshot(t, e)
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/libraries/"+unknownLibrary), http.StatusNotFound, "unknown id")
				requireUnchanged(t, e, before)
			},
		},
	}
}
