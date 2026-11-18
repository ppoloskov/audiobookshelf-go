package item_read

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	batchGetSource = "server/controllers/LibraryItemController.js:740-754"
	batchGuard     = "server/controllers/LibraryItemController.js:48-56"
)

// batchBody builds the request body of POST /api/items/batch/get.
func batchBody(ids ...string) map[string][]string { return map[string][]string{"libraryItemIds": ids} }

// batchItems decodes the libraryItems of a batch response keyed by item id.
func batchItems(t *testing.T, resp harness.Response) map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"libraryItems"`
	}
	resp.JSON(t, &body)
	out := map[string]any{}
	for _, item := range body.Items {
		id, _ := item["id"].(string)
		if _, dup := out[id]; dup {
			t.Fatalf("item %s appears twice in the batch response", id)
		}
		out[id] = item
	}
	return out
}

// wantBatch checks that the batch response holds exactly the expanded documents of the items.
func wantBatch(t *testing.T, e *harness.Env, resp harness.Response, ids ...string) {
	t.Helper()
	wantStatus(t, "POST batch/get", resp, http.StatusOK)
	got := batchItems(t, resp)
	if len(got) != len(ids) {
		t.Fatalf("batch returned %d items, want %d", len(got), len(ids))
	}
	for _, id := range ids {
		requireItem(t, "batch item "+id, expandedItem(t, e, id), got[id])
	}
}

func batchScenarios(s *setup) []harness.Scenario {
	scenario := func(assertion, caseName, state, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r047", Invariant: "INV-ROUTE-R047-CONTRACT", Case: caseName, State: state, Assertion: assertion,
			Sources: []string{batchGetSource, batchGuard, "server/routers/ApiRouter.js:104"}, Claim: claim, Run: run,
		}
	}
	all := func(e *harness.Env) []string {
		return []string{e.IDs["saga1"], e.IDs["saga2"], e.IDs["novel"], e.IDs["ebook"], e.IDs["podcast"]}
	}
	return []harness.Scenario{
		scenario("batch-get-expanded-documents", "normal", "initial",
			"POST /api/items/batch/get with item ids returns {libraryItems} holding the full expanded document of each book and the podcast.",
			func(t *testing.T, e *harness.Env) {
				wantBatch(t, e, e.Post(t, e.Token(t, "root"), "/api/items/batch/get", batchBody(all(e)...)), all(e)...)
			}),
		scenario("batch-get-repeat-same-documents", "normal", "repeat",
			"Repeating the same batch request returns the same documents and changes nothing.",
			func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e)
				for range 2 {
					wantBatch(t, e, e.Post(t, e.Token(t, "listener"), "/api/items/batch/get", batchBody(all(e)...)), all(e)...)
				}
				if snapshotItems(t, e) != before {
					t.Fatal("a batch get changed stored rows")
				}
			}),
		scenario("batch-get-unknown-and-duplicate-ids", "edge", "initial",
			"Unknown ids are ignored and a repeated id is returned once; all-unknown ids give an empty list with 200.",
			func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				ids := batchBody(e.IDs["saga1"], e.IDs["saga1"], missingID, "not-a-uuid")
				wantBatch(t, e, e.Post(t, token, "/api/items/batch/get", ids), e.IDs["saga1"])
				resp := e.Post(t, token, "/api/items/batch/get", batchBody(missingID))
				wantStatus(t, "unknown only", resp, http.StatusOK)
				var body struct {
					Items []any `json:"libraryItems"`
				}
				resp.JSON(t, &body)
				if body.Items == nil || len(body.Items) != 0 {
					t.Fatalf("libraryItems = %v, want an empty array", body.Items)
				}
			}),
		scenario("batch-get-needs-no-update-permission", "edge", "initial",
			"A user without update, delete or download permission can batch get items,.",
			func(t *testing.T, e *harness.Env) {
				wantBatch(t, e, e.Post(t, s.token(t, e, "nodownload"), "/api/items/batch/get", batchBody(e.IDs["saga1"])), e.IDs["saga1"])
				wantBatch(t, e, e.Post(t, e.Token(t, "listener"), "/api/items/batch/get", batchBody(e.IDs["novel"])), e.IDs["novel"])
			}),
		scenario("batch-get-unauthenticated", "error", "initial",
			"Without a valid token batch get answers 401.",
			func(t *testing.T, e *harness.Env) {
				wantStatus(t, "no token", e.Post(t, "", "/api/items/batch/get", batchBody(e.IDs["saga1"])), http.StatusUnauthorized)
				wantStatus(t, "bad token", e.Post(t, "bad", "/api/items/batch/get", batchBody(e.IDs["saga1"])), http.StatusUnauthorized)
			}),
		scenario("batch-get-invalid-payload-failure", "error", "failure",
			"A body without ids, with an empty list or with no JSON is rejected with 403 and nothing is read or changed.",
			func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e)
				for _, body := range []any{map[string]any{}, map[string]any{"libraryItemIds": []string{}}, map[string]any{"other": 1}, nil} {
					wantStatus(t, "invalid payload", e.Post(t, e.Token(t, "root"), "/api/items/batch/get", body), http.StatusForbidden)
				}
				if snapshotItems(t, e) != before {
					t.Fatal("a rejected batch get changed stored rows")
				}
			}),
		scenario("batch-get-one-forbidden-item-failure", "error", "failure",
			"If the user may not access one of the items the whole request is refused with 403 and no document is returned; without that item it succeeds.",
			func(t *testing.T, e *harness.Env) {
				before := snapshotItems(t, e)
				for _, d := range denials(false) {
					token := s.token(t, e, d.user)
					resp := e.Post(t, token, "/api/items/batch/get", batchBody(e.IDs["podcast"], e.IDs[d.item]))
					wantStatus(t, d.user+": "+d.why, resp, http.StatusForbidden)
				}
				if snapshotItems(t, e) != before {
					t.Fatal("a refused batch get changed stored rows")
				}
				wantBatch(t, e, e.Post(t, s.token(t, e, "plain"), "/api/items/batch/get", batchBody(e.IDs["saga1"], e.IDs["podcast"])), e.IDs["saga1"], e.IDs["podcast"])
			}),
		scenario("batch-get-reflects-changes", "normal", "changed",
			"After an item is edited the batch document shows the new tags, and a user restricted by tag loses access to the same request.",
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				id := e.IDs["ebook"]
				token := s.token(t, e, "tagallow")
				wantBatch(t, e, e.Post(t, token, "/api/items/batch/get", batchBody(id)), id)
				wantStatus(t, "retag", e.Patch(t, root, "/api/items/"+id+"/media", map[string]any{"tags": []string{"ir-changed"}}), http.StatusOK)
				defer func() {
					wantStatus(t, "restore tags", e.Patch(t, root, "/api/items/"+id+"/media", map[string]any{"tags": []string{restrictedTag}}), http.StatusOK)
				}()
				resp := e.Post(t, root, "/api/items/batch/get", batchBody(id))
				wantBatch(t, e, resp, id)
				tags, _ := Dig(batchItems(t, resp)[id], "media", "tags").([]any)
				if len(tags) != 1 || tags[0] != "ir-changed" {
					t.Fatalf("media.tags = %v, want [ir-changed]", tags)
				}
				wantStatus(t, "tag allow user after retag", e.Post(t, token, "/api/items/batch/get", batchBody(id)), http.StatusForbidden)
			}),
	}
}
