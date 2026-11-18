package author

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// The match route asks the fixed external host api.audnex.us. Scenarios here
// only assert what holds with and without internet access: authorisation, not
// found, and the no-match answer for requests that cannot match anything.

func matchScenarios() []harness.Scenario {
	const missingID = "00000000-0000-4000-8000-000000000000"
	return []harness.Scenario{
		{
			Requirement: "route-r134", Invariant: "INV-ROUTE-R134-AUTH", Case: "error", State: "failure", Assertion: "match-denied-changes-nothing",
			Sources: []string{"server/controllers/AuthorController.js:427-442", "server/Auth.js:322"},
			Claim:   "POST /api/authors/:id/match is 401 without a token, 404 for an unknown author (even for users who may not update) and 403 for a user without update permission, and 404 for a user with every permission but no access to the library; the author and events stay unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				body := map[string]any{"q": "Terry Fixture"}
				wantStatus(t, "no token", e.Post(t, "", authorURL(id, "/match"), body), http.StatusUnauthorized)
				wantStatus(t, "unknown as root", e.Post(t, e.Token(t, "root"), authorURL(missingID, "/match"), body), http.StatusNotFound)
				wantStatus(t, "unknown as listener", e.Post(t, e.Token(t, "listener"), authorURL(missingID, "/match"), body), http.StatusNotFound)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), authorURL(id, "/match"), body), http.StatusForbidden)
				wantStatus(t, "restricted library with every permission", e.Post(t, restrictedWriter(t, e).token, authorURL(id, "/match"), body), http.StatusNotFound)
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", idIs(id))
			},
		},
		{
			Requirement: "route-r134", Invariant: "INV-ROUTE-R134-CONTRACT", Case: "error", State: "initial", Assertion: "match-no-result-is-404",
			Sources: []string{"server/controllers/AuthorController.js:337-347", "server/finders/AuthorFinder.js:20-33", "server/providers/Audnexus.js:47-60,100-125"},
			Claim:   "A match request with no usable query (no q, empty q, an invalid asin) or a name the provider does not know answers 404 and changes nothing, with or without internet access.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				for _, body := range []any{
					map[string]any{}, map[string]any{"q": ""}, map[string]any{"asin": "not-an-asin"}, map[string]any{"asin": "nope", "region": "de"},
					map[string]any{"q": "Zzqx Vwrk Fixture Nobody 7391824"},
				} {
					wantStatus(t, "match", e.Post(t, e.Token(t, "root"), authorURL(id, "/match"), body), http.StatusNotFound)
				}
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", idIs(id))
			},
		},
		{
			Requirement: "route-r134", Invariant: "INV-ROUTE-R134-NOMATCH", Case: "edge", State: "repeat", Assertion: "match-no-result-repeat",
			Sources: []string{"server/controllers/AuthorController.js:345-347"},
			Claim:   "Repeating a no-result match request gives 404 again and the author keeps its asin and description.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				for range 2 {
					wantStatus(t, "match", e.Post(t, e.Token(t, "root"), authorURL(id, "/match"), map[string]any{"q": "Zzqx Vwrk Fixture Nobody 7391824"}), http.StatusNotFound)
				}
				assertRowEqual(t, before, mustRow(t, e, id))
			},
		},
	}
}

// assertRowEqual fails when two author rows differ in any column.
func assertRowEqual(t *testing.T, before, after map[string]any) {
	t.Helper()
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("author column %s changed: %v -> %v", key, value, after[key])
		}
	}
}
