package browse

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	srcSeriesPatch  = "server/controllers/SeriesController.js:59-90"
	srcSeriesMiddle = "server/controllers/SeriesController.js:95-120"
)

// patchSeries sends PATCH /api/series/:id.
func patchSeries(t *testing.T, e *harness.Env, token, id, body string) harness.Response {
	t.Helper()
	return patchJSON(t, e, token, "/api/series/"+id, body)
}

// expectRow fails unless the series row has the name and description ("" = NULL).
func expectRow(t *testing.T, e *harness.Env, id, name, description string) map[string]any {
	t.Helper()
	row := seriesRow(t, e, id)
	if row == nil {
		t.Fatalf("series row %s missing", id)
	}
	got, _ := row["description"].(string)
	if row["name"] != name || got != description {
		t.Fatalf("series row: name %v description %v, want %q %q", row["name"], row["description"], name, description)
	}
	return row
}

func (s *suite) seriesPatchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "normal", State: "initial", Assertion: "series-patch-updates-and-persists",
			Sources: []string{srcSeriesPatch},
			Claim:   "PATCH /api/series/:id with name and description answers the updated series object (nameIgnorePrefix derived from the new name) and persists name and description with a later updatedAt; the nameIgnorePrefix column is not asserted.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				before := seriesRow(t, e, id)
				resp := patchSeries(t, e, e.Token(t, "root"), id, `{"name":"The Long Saga","description":"Epic tale"}`)
				mustOK(t, "patch", resp)
				var series seriesJSON
				resp.JSON(t, &series)
				if series.ID != id || series.Name != "The Long Saga" || series.Description == nil || *series.Description != "Epic tale" || series.LibraryID != e.IDs[bookLibrary] || series.NameIgnore != "Long Saga, The" || series.Books != nil {
					t.Fatalf("patch response: %s", resp.Body)
				}
				row := expectRow(t, e, id, "The Long Saga", "Epic tale")
				if after, was := row["updatedAt"].(string), before["updatedAt"].(string); after <= was {
					t.Fatalf("updatedAt must move forward: %s -> %s", was, after)
				}
			},
		},
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "edge", State: "repeat", Assertion: "series-patch-repeat-is-unchanged",
			Sources: []string{srcSeriesPatch},
			Claim:   "Repeating the same PATCH answers 200 with the same object and leaves the row, including updatedAt, untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "The Long Saga")
				before := expectRow(t, e, id, "The Long Saga", "Epic tale")
				resp := patchSeries(t, e, e.Token(t, "root"), id, `{"name":"The Long Saga","description":"Epic tale"}`)
				mustOK(t, "repeat", resp)
				var series seriesJSON
				resp.JSON(t, &series)
				if series.Name != "The Long Saga" || series.Description == nil || *series.Description != "Epic tale" {
					t.Fatalf("repeat response: %s", resp.Body)
				}
				after := expectRow(t, e, id, "The Long Saga", "Epic tale")
				if after["updatedAt"] != before["updatedAt"] {
					t.Fatalf("updatedAt moved on an unchanged PATCH: %v -> %v", before["updatedAt"], after["updatedAt"])
				}
			},
		},
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "normal", State: "changed", Assertion: "series-patch-visible-in-reads",
			Sources: []string{srcSeriesPatch, "server/models/Series.js", srcSeriesList},
			Claim:   "After the rename the series list, both series routes, item lists and search show the new name.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "The Long Saga")
				token := e.Token(t, "listener")
				if got := getSeries(t, e, token, "/api/series/"+id, nil); got.Name != "The Long Saga" || got.Description == nil || *got.Description != "Epic tale" {
					t.Fatalf("/api/series: %+v", got)
				}
				if got := getSeries(t, e, token, detailPath(e, id), nil); got.Name != "The Long Saga" {
					t.Fatalf("library series: %+v", got)
				}
				page := getItems(t, e, token, bookLibrary, query("filter", filterParam("series", id), "minified", "1"))
				if got := page.Results[0].Media.Metadata.SeriesName; got != "The Long Saga #1" {
					t.Fatalf("item seriesName %q", got)
				}
				if names := search(t, e, token, bookLibrary, query("q", "epic", "q2", "x")).seriesNames(); len(names) != 0 {
					t.Fatalf("search must not match descriptions: %v", names)
				}
				if names := search(t, e, token, bookLibrary, query("q", "the long")).seriesNames(); len(names) != 1 || names[0] != "The Long Saga" {
					t.Fatalf("search by new name: %v", names)
				}
			},
		},
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "error", State: "initial", Assertion: "series-patch-rejections",
			Sources: []string{srcSeriesPatch, srcSeriesMiddle, "server/Auth.js:322"},
			Claim:   "PATCH /api/series/:id is 401 without a token, 404 for an unknown series or one with no accessible books, 403 without the update permission and 400 when no valid string field is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "The Long Saga")
				root := e.Token(t, "root")
				expectStatus(t, "no token", e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/series/" + id, JSON: map[string]string{"name": "X"}}), http.StatusUnauthorized)
				expectStatus(t, "unknown", patchSeries(t, e, root, "00000000-0000-4000-8000-000000000000", `{"name":"X"}`), http.StatusNotFound)
				expectStatus(t, "no update permission", patchSeries(t, e, e.Token(t, "listener"), id, `{"name":"X"}`), http.StatusForbidden)
				// Check order: 404 (unknown series) before 403 (no permission) before 400 (body).
				expectStatus(t, "404 before 403", patchSeries(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", `{}`), http.StatusNotFound)
				expectStatus(t, "403 before 400", patchSeries(t, e, e.Token(t, "listener"), id, `{}`), http.StatusForbidden)
				thirdID := seriesIDByName(t, e, third)
				expectStatus(t, "no accessible books", patchSeries(t, e, s.restrictedToken(t, e, "browse-tagged"), thirdID, `{"name":"X"}`), http.StatusNotFound)
				for _, body := range []string{`{}`, `{"name":5}`, `{"name":null,"description":false}`, `{"unknown":"x"}`} {
					expectStatus(t, "body "+body, patchSeries(t, e, root, id, body), http.StatusBadRequest)
				}
				expectRow(t, e, id, "The Long Saga", "Epic tale")
				expectRow(t, e, thirdID, third, "")
			},
		},
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "edge", State: "initial", Assertion: "series-patch-unicode-empty-and-extra-keys",
			Sources: []string{srcSeriesPatch},
			Claim:   "A description-only PATCH, an empty description, a Unicode name and extra keys are accepted: only name and description change.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "The Long Saga")
				root := e.Token(t, "root")
				resp := patchSeries(t, e, root, id, `{"description":"Nur Beschreibung","libraryId":"other","id":"other"}`)
				mustOK(t, "description only", resp)
				row := expectRow(t, e, id, "The Long Saga", "Nur Beschreibung")
				if row["libraryId"] != e.IDs[bookLibrary] {
					t.Fatalf("libraryId must not change: %v", row["libraryId"])
				}
				mustOK(t, "unicode", patchSeries(t, e, root, id, `{"name":"Série Ünïcode ☃ 日本"}`))
				expectRow(t, e, id, "Série Ünïcode ☃ 日本", "Nur Beschreibung")
				mustOK(t, "empty description", patchSeries(t, e, root, id, `{"description":""}`))
				expectRow(t, e, id, "Série Ünïcode ☃ 日本", "")
				mustOK(t, "restore", patchSeries(t, e, root, id, `{"name":"Long Saga"}`))
				expectRow(t, e, id, "Long Saga", "")
			},
		},
		{
			Requirement: "route-r139", Invariant: "INV-ROUTE-R139-CONTRACT", Case: "error", State: "failure", Assertion: "series-patch-rejected-changes-nothing",
			Sources: []string{srcSeriesPatch, srcSeriesMiddle},
			Claim:   "A rejected PATCH (no permission, invalid body, no accessible books) leaves the row unchanged, including updatedAt, and emits no series_updated event.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, long)
				before := expectRow(t, e, id, long, "")
				audience := s.connect(t, e)
				for _, attempt := range []struct {
					token, body string
				}{
					{e.Token(t, "listener"), `{"name":"Hacked"}`},
					{e.Token(t, "root"), `{"name":7}`},
					{s.restrictedToken(t, e, "browse-tagged"), `{"name":"Hacked"}`},
				} {
					target := id
					if attempt.token == s.restrictedToken(t, e, "browse-tagged") {
						target = seriesIDByName(t, e, third)
					}
					if resp := patchSeries(t, e, attempt.token, target, attempt.body); resp.Status < 400 {
						t.Fatalf("attempt %s must be rejected: %d", attempt.body, resp.Status)
					}
				}
				audience.expectNone(t, "series_updated", nil)
				after := expectRow(t, e, id, long, "")
				if after["updatedAt"] != before["updatedAt"] {
					t.Fatalf("updatedAt moved: %v -> %v", before["updatedAt"], after["updatedAt"])
				}
				expectRow(t, e, seriesIDByName(t, e, third), third, "")
			},
		},
	}
}
