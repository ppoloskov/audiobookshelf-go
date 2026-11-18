package library_manage

import (
	"net/http"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	orderRoute = "server/routers/ApiRouter.js:94"
	orderSrc   = "server/controllers/LibraryController.js:913-949"
)

type orderEntry = map[string]any

type orderAnswer struct {
	Libraries []libraryJSON `json:"libraries"`
}

func postOrder(t *testing.T, e *harness.Env, token string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/libraries/order", body)
}

// order returns library ids by displayOrder as stored.
func storedOrder(t *testing.T, e *harness.Env) []string {
	t.Helper()
	var ids []string
	for _, row := range e.Rows(t, "SELECT id FROM libraries ORDER BY displayOrder, id") {
		ids = append(ids, row["id"].(string))
	}
	return ids
}

func orderScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r041", Invariant: "INV-ROUTE-R041-CONTRACT", Case: "normal", State: "initial", Assertion: "order-swaps-libraries-and-answers-sorted-list",
			Sources: []string{orderRoute, orderSrc, "server/models/Library.js:92-97"},
			Claim:   "POST /api/libraries/order sets each listed library's displayOrder, answers {libraries} for all libraries sorted by displayOrder, persists the values and emits no library_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				first, second := newBookLibrary(t, e, "swap-a"), newBookLibrary(t, e, "swap-b")
				a, _ := everyone(t, e)
				defer a.close()
				resp := postOrder(t, e, root, []orderEntry{{"id": first.ID, "newOrder": second.DisplayOrder}, {"id": second.ID, "newOrder": first.DisplayOrder}})
				requireStatus(t, resp, http.StatusOK, "order")
				var got orderAnswer
				resp.JSON(t, &got)
				ids := libraryIDs(got.Libraries)
				if slices.Index(ids, second.ID) >= slices.Index(ids, first.ID) || len(ids) != len(listLibraries(t, e, root)) {
					t.Fatalf("answer must list every library with the swap applied: %v", ids)
				}
				for i := 1; i < len(got.Libraries); i++ {
					if got.Libraries[i].DisplayOrder < got.Libraries[i-1].DisplayOrder {
						t.Fatalf("answer is not sorted by displayOrder")
					}
				}
				for _, lib := range got.Libraries {
					switch lib.ID {
					case first.ID:
						if lib.DisplayOrder != second.DisplayOrder {
							t.Fatalf("first library order %v", lib.DisplayOrder)
						}
					case second.ID:
						if lib.DisplayOrder != first.DisplayOrder {
							t.Fatalf("second library order %v", lib.DisplayOrder)
						}
					}
					checkRow(t, e, lib)
				}
				if !slices.Equal(libraryIDs(listLibraries(t, e, root)), ids) {
					t.Fatal("GET /api/libraries must follow the new order")
				}
				a.silent(t, "library_updated")
			},
		},
		{
			Requirement: "route-r041", Invariant: "INV-ROUTE-R041-CONTRACT", Case: "normal", State: "repeat", Assertion: "order-repeat-leaves-rows-untouched",
			Sources: []string{orderRoute, orderSrc},
			Claim:   "Posting the same order again answers the same list and does not rewrite the library rows.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				first, second := newBookLibrary(t, e, "repeat-a"), newBookLibrary(t, e, "repeat-b")
				body := []orderEntry{{"id": first.ID, "newOrder": second.DisplayOrder + 10}, {"id": second.ID, "newOrder": first.DisplayOrder}}
				requireStatus(t, postOrder(t, e, root, body), http.StatusOK, "first order")
				rows := e.Rows(t, "SELECT * FROM libraries ORDER BY id")
				resp := postOrder(t, e, root, body)
				requireStatus(t, resp, http.StatusOK, "repeat order")
				var got orderAnswer
				resp.JSON(t, &got)
				if again := e.Rows(t, "SELECT * FROM libraries ORDER BY id"); !equalRows(rows, again) {
					t.Fatalf("repeat must not change rows:\n%v\n%v", rows, again)
				}
				if ids := libraryIDs(got.Libraries); slices.Index(ids, second.ID) >= slices.Index(ids, first.ID) {
					t.Fatalf("order not applied: %v", ids)
				}
			},
		},
		{
			Requirement: "route-r041", Invariant: "INV-ROUTE-R041-CONTRACT", Case: "edge", State: "changed", Assertion: "order-partial-and-empty-lists",
			Sources: []string{orderRoute, orderSrc},
			Claim:   "POST /api/libraries/order with one entry changes only that library and with [] changes nothing; both answer every library, and a later list reflects the new order.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				lib := newBookLibrary(t, e, "partial")
				others := e.Rows(t, "SELECT id, displayOrder FROM libraries WHERE id <> ? ORDER BY id", lib.ID)
				resp := postOrder(t, e, root, []orderEntry{{"id": lib.ID, "newOrder": -1}})
				requireStatus(t, resp, http.StatusOK, "partial")
				var got orderAnswer
				resp.JSON(t, &got)
				if got.Libraries[0].ID != lib.ID || got.Libraries[0].DisplayOrder != -1 {
					t.Fatalf("library with the lowest order must come first: %+v", got.Libraries[0])
				}
				if now := e.Rows(t, "SELECT id, displayOrder FROM libraries WHERE id <> ? ORDER BY id", lib.ID); !equalRows(others, now) {
					t.Fatal("libraries not named must keep their order")
				}
				before := storedOrder(t, e)
				resp = postOrder(t, e, root, []orderEntry{})
				requireStatus(t, resp, http.StatusOK, "empty")
				resp.JSON(t, &got)
				if len(got.Libraries) != len(before) || !slices.Equal(storedOrder(t, e), before) {
					t.Fatalf("empty list must change nothing")
				}
				if listed := libraryIDs(listLibraries(t, e, root)); listed[0] != lib.ID {
					t.Fatalf("list must reflect the new order: %v", listed)
				}
			},
		},
		{
			Requirement: "route-r041", Invariant: "INV-R041-AUTH-001", Case: "error", State: "initial", Assertion: "order-requires-admin",
			Sources: []string{orderRoute, "server/controllers/LibraryController.js:914-917", "server/Auth.js:322"},
			Claim:   "POST /api/libraries/order answers 401 without a token and 403 for a non-admin user and changes no order.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "order-denied")
				before := snapshot(t, e)
				body := []orderEntry{{"id": lib.ID, "newOrder": 99}}
				requireStatus(t, postOrder(t, e, "", body), http.StatusUnauthorized, "anonymous")
				requireStatus(t, postOrder(t, e, e.Token(t, "listener"), body), http.StatusForbidden, "non-admin")
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r041", Invariant: "INV-R041-VALIDATION-001", Case: "error", State: "failure", Assertion: "order-invalid-bodies-rejected-without-effects",
			Sources: []string{orderRoute, "server/controllers/LibraryController.js:919-932"},
			Claim:   "POST /api/libraries/order answers 400 for a body that is not an array, an entry without a string id or numeric newOrder, and an unknown library id in the first entry, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				lib := newBookLibrary(t, e, "order-invalid")
				before := snapshot(t, e)
				cases := map[string]any{
					"object":             map[string]any{"id": lib.ID, "newOrder": 9},
					"null":               nil,
					"string newOrder":    []orderEntry{{"id": lib.ID, "newOrder": "9"}},
					"missing newOrder":   []orderEntry{{"id": lib.ID}},
					"numeric id":         []orderEntry{{"id": 5, "newOrder": 9}},
					"null entry":         []any{nil},
					"valid then invalid": []any{orderEntry{"id": lib.ID, "newOrder": 9}, orderEntry{"id": lib.ID}},
					"unknown first":      []orderEntry{{"id": "00000000-0000-4000-8000-000000000000", "newOrder": 9}, {"id": lib.ID, "newOrder": 9}},
				}
				for name, body := range cases {
					if resp := postOrder(t, e, root, body); resp.Status != http.StatusBadRequest {
						t.Fatalf("%s: status %d, want 400 (%s)", name, resp.Status, resp.Body)
					}
				}
				requireUnchanged(t, e, before)
			},
		},
	}
}

func equalRows(a, b []map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for key, value := range a[i] {
			if b[i][key] != value {
				return false
			}
		}
	}
	return true
}
