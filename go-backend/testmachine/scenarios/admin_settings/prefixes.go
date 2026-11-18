package admin_settings

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const prefixSource = "server/controllers/MiscController.js:183-281"

// titleColumns reads every stored sort-title column the handler recomputes,
// keyed by "<table>:<title>".
func titleColumns(t *testing.T, e *harness.Env) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := []struct{ table, query string }{
		{"books", "SELECT title AS name, titleIgnorePrefix AS sort FROM books"},
		{"libraryItems", "SELECT title AS name, titleIgnorePrefix AS sort FROM libraryItems"},
		{"podcasts", "SELECT title AS name, titleIgnorePrefix AS sort FROM podcasts"},
		{"series", "SELECT name, nameIgnorePrefix AS sort FROM series"},
	}
	for _, q := range queries {
		for _, row := range e.Rows(t, q.query) {
			out[q.table+":"+row["name"].(string)] = row["sort"].(string)
		}
	}
	return out
}

func patchPrefixes(t *testing.T, e *harness.Env, body any) harness.Response {
	t.Helper()
	return e.Patch(t, e.Token(t, "root"), "/api/sorting-prefixes", body)
}

// wantPrefixResult checks status, rowsUpdated and the returned prefix list.
func wantPrefixResult(t *testing.T, resp harness.Response, rows int, prefixes []string) {
	t.Helper()
	wantStatus(t, "sorting-prefixes", resp, http.StatusOK)
	object := resp.Object(t)
	if !sameJSON(object["rowsUpdated"], rows) {
		t.Fatalf("rowsUpdated = %v, want %d", object["rowsUpdated"], rows)
	}
	got := responseSettings(t, resp)
	if !sameJSON(got["sortingPrefixes"], prefixes) {
		t.Fatalf("response sortingPrefixes = %v, want %v", got["sortingPrefixes"], prefixes)
	}
	wantNoSecrets(t, "serverSettings", got)
}

func prefixScenarios() []harness.Scenario {
	const req = "route-r206"
	changedColumns := map[string]string{
		"books:Single File Novel": "File Novel", "libraryItems:Single File Novel": "File Novel",
		"books:The Middle": "The Middle", "libraryItems:The Middle": "The Middle",
		"books:The Beginning": "The Beginning", "libraryItems:The Beginning": "The Beginning",
		"podcasts:Fixture Cast": "Cast", "libraryItems:Fixture Cast": "Cast", "series:Long Saga": "Saga",
	}
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R206-ROUTE", "error", "initial", "sorting-prefixes-admin-only",
			"PATCH /api/sorting-prefixes answers 401 without a token and 403 for a non-admin and changes nothing.",
			[]string{prefixSource},
			func(t *testing.T, e *harness.Env) {
				before, columns := storedSettings(t, e), titleColumns(t, e)
				adminOnly(t, e, http.MethodPatch, "/api/sorting-prefixes", map[string]any{"sortingPrefixes": []string{"single"}})
				if !sameJSON(before, storedSettings(t, e)) || !sameJSON(columns, titleColumns(t, e)) {
					t.Fatal("a refused request changed settings or title columns")
				}
			}),
		scenario(req, "INV-ROUTE-R206-ROUTE", "error", "failure", "sorting-prefixes-invalid-bodies-rejected",
			"A missing, non-array, empty or all-blank sortingPrefixes answers 400 and changes neither the setting nor any title column.",
			[]string{prefixSource},
			func(t *testing.T, e *harness.Env) {
				before, columns := storedSettings(t, e), titleColumns(t, e)
				bodies := []any{
					map[string]any{}, map[string]any{"sortingPrefixes": "the"}, map[string]any{"sortingPrefixes": []string{}},
					map[string]any{"sortingPrefixes": []any{" ", "", nil, 5}}, `{bad`,
				}
				for _, body := range bodies {
					wantStatus(t, "invalid body", patchPrefixes(t, e, body), http.StatusBadRequest)
				}
				if !sameJSON(before, storedSettings(t, e)) || !sameJSON(columns, titleColumns(t, e)) {
					t.Fatal("a rejected request changed settings or title columns")
				}
			}),
		scenario(req, "INV-ROUTE-R206-ROUTE", "normal", "initial", "sorting-prefixes-recompute-all-title-columns",
			"PATCH /api/sorting-prefixes trims, lowercases and de-duplicates the list, stores it, recomputes titleIgnorePrefix on books, podcasts and series and returns the number of changed rows.",
			[]string{prefixSource, "server/utils/index.js:176-198"},
			func(t *testing.T, e *harness.Env) {
				before := titleColumns(t, e)
				resp := patchPrefixes(t, e, map[string]any{"sortingPrefixes": []string{"Fixture", " LONG ", "single", "fixture"}})
				wantPrefixResult(t, resp, 5, []string{"fixture", "long", "single"})
				if !sameJSON(storedSettings(t, e)["sortingPrefixes"], []string{"fixture", "long", "single"}) {
					t.Fatalf("stored sortingPrefixes = %v", storedSettings(t, e)["sortingPrefixes"])
				}
				after := titleColumns(t, e)
				for key, value := range before {
					want := value
					if changed, ok := changedColumns[key]; ok {
						want = changed
					}
					if after[key] != want {
						t.Errorf("%s = %q, want %q", key, after[key], want)
					}
				}
			}),
		scenario(req, "INV-ROUTE-R206-REPEAT", "edge", "repeat", "sorting-prefixes-repeat-updates-nothing",
			"Sending the same prefixes again reports rowsUpdated 0 and leaves every title column as it was.",
			[]string{prefixSource},
			func(t *testing.T, e *harness.Env) {
				columns := titleColumns(t, e)
				wantPrefixResult(t, patchPrefixes(t, e, map[string]any{"sortingPrefixes": []string{"fixture", "long", "single"}}), 0, []string{"fixture", "long", "single"})
				if !sameJSON(columns, titleColumns(t, e)) {
					t.Fatal("repeat changed title columns")
				}
			}),
		scenario(req, "INV-ROUTE-R206-ROUTE", "edge", "changed", "sorting-prefixes-order-follows-prefixes",
			"After the prefixes change, the title sort of the library uses the recomputed titleIgnorePrefix when sortingIgnorePrefix is on.",
			[]string{prefixSource, "server/utils/queries/libraryItemsBookFilters.js:254-262"},
			func(t *testing.T, e *harness.Env) {
				ids := e.IDs
				settingsPatch(t, e, map[string]any{"sortingIgnorePrefix": true})
				wantOrder(t, e, []string{ids["ebook"], ids["novel"], ids["disposable"], ids["saga1"], ids["saga2"]})
				settingsPatch(t, e, map[string]any{"sortingIgnorePrefix": false})
				wantOrder(t, e, []string{ids["ebook"], ids["disposable"], ids["novel"], ids["saga1"], ids["saga2"]})
			}),
		scenario(req, "INV-ROUTE-R206-ROUTE", "edge", "changed", "sorting-prefixes-restore-and-drop-non-strings",
			"Non-string and blank members are dropped, and restoring the original prefixes recomputes the five columns back.",
			[]string{prefixSource},
			func(t *testing.T, e *harness.Env) {
				resp := patchPrefixes(t, e, map[string]any{"sortingPrefixes": []any{" The ", "A", "the", nil, 5, ""}})
				wantPrefixResult(t, resp, 5, []string{"the", "a"})
				columns := titleColumns(t, e)
				want := map[string]string{
					"books:Single File Novel": "Single File Novel", "books:The Middle": "Middle", "books:The Beginning": "Beginning",
					"libraryItems:The Middle": "Middle", "podcasts:Fixture Cast": "Fixture Cast", "libraryItems:Fixture Cast": "Fixture Cast", "libraryItems:Single File Novel": "Single File Novel", "series:Long Saga": "Long Saga",
				}
				for key, value := range want {
					if columns[key] != value {
						t.Errorf("%s = %q, want %q", key, columns[key], value)
					}
				}
			}),
	}
}
