package author

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// updateResponse is the body of PATCH /api/authors/:id.
type updateResponse struct {
	Author  authorJSON `json:"author"`
	Updated *bool      `json:"updated"`
	Merged  *bool      `json:"merged"`
}

func patch(t *testing.T, e *harness.Env, token, id string, body any) harness.Response {
	t.Helper()
	return e.Patch(t, token, authorURL(id, ""), body)
}

func updateScenarios() []harness.Scenario {
	const missingID = "00000000-0000-4000-8000-000000000000"
	return []harness.Scenario{
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-AUTH", Case: "error", State: "failure", Assertion: "update-denied-changes-nothing",
			Sources: []string{"server/controllers/AuthorController.js:427-442", "server/Auth.js:322"},
			Claim:   "PATCH /api/authors/:id is 401 without a token, 404 for an unknown author or one in an inaccessible library (before the permission check) and 403 for a user without update permission; nothing changes and no event is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				restricted := ensureUser(t, e, "author-podcast-only", map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}})
				w := watch(t, e)
				body := map[string]any{"name": "Terry Changed", "description": "changed"}
				wantStatus(t, "no token", patch(t, e, "", id, body), http.StatusUnauthorized)
				wantStatus(t, "unknown as root", patch(t, e, e.Token(t, "root"), missingID, body), http.StatusNotFound)
				wantStatus(t, "unknown as listener", patch(t, e, e.Token(t, "listener"), missingID, body), http.StatusNotFound)
				wantStatus(t, "restricted library", patch(t, e, restricted.token, id, body), http.StatusNotFound)
				wantStatus(t, "listener", patch(t, e, e.Token(t, "listener"), id, body), http.StatusForbidden)
				wantStatus(t, "finisher", patch(t, e, e.Token(t, "finisher"), id, body), http.StatusForbidden)
				wantStatus(t, "listener with invalid body", patch(t, e, e.Token(t, "listener"), id, map[string]any{}), http.StatusForbidden)
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", nil)
				w.none(t, "author_removed", nil)
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-INPUT", Case: "error", State: "initial", Assertion: "update-invalid-payload-rejected",
			Sources: []string{"server/controllers/AuthorController.js:96-107"},
			Claim:   "PATCH with no name, description or asin of type string or null (empty object, unknown keys, numbers, booleans, arrays, objects) is rejected with 400 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				for _, body := range []any{
					map[string]any{}, map[string]any{"foo": "bar"}, map[string]any{"imagePath": "/tmp/x.jpg"}, map[string]any{"name": 5},
					map[string]any{"name": true, "description": 1.5}, map[string]any{"asin": []string{"x"}}, map[string]any{"description": map[string]any{}},
				} {
					wantStatus(t, "payload", patch(t, e, e.Token(t, "root"), id, body), http.StatusBadRequest)
				}
				wantStatus(t, "no body", e.Do(t, harness.Request{Method: http.MethodPatch, Path: authorURL(id, ""), Token: e.Token(t, "root")}), http.StatusBadRequest)
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", nil)
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-CONTRACT", Case: "normal", State: "initial", Assertion: "update-description-and-asin",
			Sources: []string{"server/controllers/AuthorController.js:96-103,177-224", "server/models/Author.js:190-200"},
			Claim:   "PATCH with description and asin answers {author, updated:true}, stores both, keeps name and lastFirst, raises updatedAt, and emits author_updated with numBooks to every client; a later read shows the values.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				resp := patch(t, e, e.Token(t, "root"), id, map[string]any{"description": "Terry writes sagas.", "asin": "B000AP9A6K"})
				wantStatus(t, "patch", resp, http.StatusOK)
				got := decode[updateResponse](t, resp.Body)
				if got.Updated == nil || !*got.Updated || got.Merged != nil || got.Author.ID != id || got.Author.Name != "Terry Fixture" ||
					got.Author.Description == nil || *got.Author.Description != "Terry writes sagas." || got.Author.ASIN == nil || *got.Author.ASIN != "B000AP9A6K" {
					t.Fatalf("response = %s", resp.Body)
				}
				row := mustRow(t, e, id)
				if rowText(row, "description") != "Terry writes sagas." || rowText(row, "asin") != "B000AP9A6K" || rowText(row, "name") != "Terry Fixture" || rowText(row, "lastFirst") != rowText(before, "lastFirst") {
					t.Fatalf("row = %v", row)
				}
				if dbTime(t, row["updatedAt"]) <= dbTime(t, before["updatedAt"]) || got.Author.UpdatedAt != dbTime(t, row["updatedAt"]) {
					t.Fatalf("updatedAt must rise and match the row: %v -> %v, response %d", before["updatedAt"], row["updatedAt"], got.Author.UpdatedAt)
				}
				event := authorEvent(t, w.waitBoth(t, "author_updated", idIs(id)), bookCount(t, e, id))
				if event.Description == nil || *event.Description != "Terry writes sagas." || event.ASIN == nil || *event.ASIN != "B000AP9A6K" || event.UpdatedAt != got.Author.UpdatedAt {
					t.Fatalf("event = %+v", event)
				}
				read := decode[authorJSON](t, e.Get(t, e.Token(t, "listener"), authorURL(id, "")).Body)
				if read.Description == nil || *read.Description != "Terry writes sagas." {
					t.Fatalf("a later read must show the description: %+v", read)
				}
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-FIELDS", Case: "edge", State: "repeat", Assertion: "update-same-values-is-noop",
			Sources: []string{"server/controllers/AuthorController.js:109-110,182-187,220-223"},
			Claim:   "Repeating the same PATCH, or sending the current name, answers 200 {updated:false}, leaves the row (including updatedAt) untouched and emits no event.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				before := mustRow(t, e, id)
				w := watch(t, e)
				for _, body := range []any{
					map[string]any{"description": "Terry writes sagas.", "asin": "B000AP9A6K"}, map[string]any{"name": "Terry Fixture"},
					map[string]any{"name": "Terry Fixture", "description": "Terry writes sagas."},
				} {
					resp := patch(t, e, e.Token(t, "root"), id, body)
					wantStatus(t, "patch", resp, http.StatusOK)
					got := decode[updateResponse](t, resp.Body)
					if got.Updated == nil || *got.Updated || got.Author.ID != id || got.Author.UpdatedAt != dbTime(t, before["updatedAt"]) {
						t.Fatalf("response = %s", resp.Body)
					}
				}
				assertRowEqual(t, before, mustRow(t, e, id))
				w.none(t, "author_updated", nil)
			},
		},
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-FIELDS", Case: "edge", State: "changed", Assertion: "update-null-and-empty-values",
			Sources: []string{"server/controllers/AuthorController.js:99-103,183-187"},
			Claim:   "A null clears description and asin to NULL, an empty string is stored as an empty string, and keys of other types or other names (imagePath, libraryId) are ignored while the valid keys of the same request apply.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Terry Fixture")
				root := e.Token(t, "root")
				imagePath := rowText(mustRow(t, e, id), "imagePath")
				resp := patch(t, e, root, id, map[string]any{"description": nil, "asin": nil})
				wantStatus(t, "clear", resp, http.StatusOK)
				row := mustRow(t, e, id)
				if got := decode[updateResponse](t, resp.Body); got.Updated == nil || !*got.Updated || got.Author.Description != nil || got.Author.ASIN != nil || row["description"] != nil || row["asin"] != nil {
					t.Fatalf("null must clear: %s, row %v", resp.Body, row)
				}
				resp = patch(t, e, root, id, map[string]any{"description": "", "asin": 7, "imagePath": "/tmp/evil.jpg", "libraryId": e.IDs["podcastLibrary"], "id": "other"})
				wantStatus(t, "empty and ignored", resp, http.StatusOK)
				row = mustRow(t, e, id)
				if got := decode[updateResponse](t, resp.Body); got.Updated == nil || !*got.Updated || row["description"] != "" || row["asin"] != nil || rowText(row, "imagePath") != imagePath || row["libraryId"] != e.IDs["library"] || row["id"] != id {
					t.Fatalf("empty string stored, others ignored: %s, row %v", resp.Body, row)
				}
				resp = patch(t, e, root, id, map[string]any{"description": nil})
				if got := decode[updateResponse](t, resp.Body); got.Updated == nil || !*got.Updated || mustRow(t, e, id)["description"] != nil {
					t.Fatalf("null clears an empty description too: %s", resp.Body)
				}
			},
		},
	}
}

func updateLastFirstScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r132", Invariant: "INV-ROUTE-R132-FIELDS", Case: "edge", State: "initial", Assertion: "update-fills-missing-last-first",
			Sources: []string{"server/controllers/AuthorController.js:177-180", "server/models/Author.js:25-28"},
			Claim:   "When an author row has no lastFirst, a PATCH that does not rename it stores lastFirst derived from the current name together with the requested change.",
			Run: func(t *testing.T, e *harness.Env) {
				id := authorID(t, e, "Scan Author")
				e.Stop(t)
				if n := e.Exec(t, "UPDATE authors SET lastFirst = NULL WHERE id = ?", id); n != 1 {
					t.Fatalf("prepared %d rows", n)
				}
				e.Restart(t)
				if mustRow(t, e, id)["lastFirst"] != nil {
					t.Fatal("setup: lastFirst must be NULL")
				}
				wantStatus(t, "patch", patch(t, e, e.Token(t, "root"), id, map[string]any{"description": "Has a derived sort name."}), http.StatusOK)
				if row := mustRow(t, e, id); rowText(row, "lastFirst") != "Author, Scan" || rowText(row, "description") != "Has a derived sort name." {
					t.Fatalf("row = %v", row)
				}
			},
		},
	}
}
