package item_write

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r046           = "route-r046"
	batchUpdateSrc = controllerSrc + ":611-725"
)

// batchEntry builds one element of the batch update payload.
func batchEntry(id string, mediaPayload any) map[string]any {
	return map[string]any{"id": id, "mediaPayload": mediaPayload}
}

// batchResult is the body of POST /api/items/batch/update.
type batchResult struct {
	Success bool `json:"success"`
	Updates int  `json:"updates"`
}

func postBatchUpdate(t *testing.T, e *harness.Env, token string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/batch/update", body)
}

func batchOK(t *testing.T, e *harness.Env, body any) batchResult {
	t.Helper()
	resp := postBatchUpdate(t, e, e.Token(t, "root"), body)
	wantStatus(t, "batch update", resp, http.StatusOK)
	var result batchResult
	resp.JSON(t, &result)
	if !result.Success {
		t.Fatalf("success = false: %s", resp.Body)
	}
	return result
}

func batchUpdateScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R046-BATCH-001"
	const errInv = "INV-ROUTE-R046-CONTRACT"
	return []harness.Scenario{
		scenario(r046, inv, "normal", "initial", "batch-update-several-items",
			"A batch of a book, a second book and a podcast answers {success:true, updates:3}, stores each payload, moves each updatedAt, rewrites each metadata.json and emits one item_updated per item.",
			srcs(batchUpdateSrc, bookUpdateSrc, podcastUpdateSrc, "client/pages/batch/index.vue:430-452"),
			func(t *testing.T, e *harness.Env) {
				ids := []string{e.IDs["novel"], e.IDs["saga2"], e.IDs["podcast"]}
				a := itemAudience(t, e, s)
				before := map[string]time.Time{}
				for _, id := range ids {
					before[id] = updatedAt(t, e, id)
				}
				result := batchOK(t, e, []any{
					batchEntry(ids[0], map[string]any{"metadata": map[string]any{"publisher": "Batch Press"}, "tags": []string{"batch-a"}}),
					batchEntry(ids[1], map[string]any{"metadata": map[string]any{"genres": []string{"Batch Genre"}}, "tags": []string{"batch-b"}}),
					batchEntry(ids[2], map[string]any{"metadata": map[string]any{"author": "Batch Host"}, "tags": []string{"batch-c"}}),
				})
				if result.Updates != 3 {
					t.Fatalf("updates = %d, want 3", result.Updates)
				}
				if mediaRow(t, e, ids[0])["publisher"] != "Batch Press" || mediaRow(t, e, ids[2])["author"] != "Batch Host" {
					t.Fatal("scalar fields not stored")
				}
				requireEqual(t, "saga2 genres", column(t, mediaRow(t, e, ids[1]), "genres"), []any{"Batch Genre"})
				for i, tag := range []string{"batch-a", "batch-b", "batch-c"} {
					requireEqual(t, "tags", column(t, mediaRow(t, e, ids[i]), "tags"), []any{tag})
					wantMoved(t, e, ids[i], before[ids[i]])
				}
				requireEqual(t, "metadata.json publisher", readMetadataFile(t, e, ids[0])["publisher"], "Batch Press")
				requireEqual(t, "metadata.json author", readMetadataFile(t, e, ids[2])["author"], "Batch Host")
				requireEqual(t, "metadata.json genres", readMetadataFile(t, e, ids[1])["genres"], []any{"Batch Genre"})
				for _, id := range ids {
					events := a.receive(t, "item_updated", idOf(id), []string{"root"}, nil)
					got := payload(t, events["root"])
					if got["updatedAt"] != float64(updatedAt(t, e, id).UnixMilli()) {
						t.Fatalf("payload updatedAt %v", got["updatedAt"])
					}
				}
			}),
		scenario(r046, inv, "normal", "repeat", "batch-update-counts-only-changed-items",
			"Repeating the batch answers updates:0 and changes nothing; a batch with one unchanged and one changed item answers updates:1 and emits item_updated for the changed item only.",
			srcs(batchUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				novel, saga2 := e.IDs["novel"], e.IDs["saga2"]
				a := itemAudience(t, e, s)
				before, rows := updatedAt(t, e, novel), snapshot(t, e)
				same := []any{batchEntry(novel, map[string]any{"metadata": map[string]any{"publisher": "Batch Press"}, "tags": []string{"batch-a"}})}
				if result := batchOK(t, e, same); result.Updates != 0 {
					t.Fatalf("repeat updates = %d", result.Updates)
				}
				wantUnchanged(t, e, novel, before)
				wantSnapshot(t, e, rows)
				mixed := []any{same[0], batchEntry(saga2, map[string]any{"tags": []string{"batch-b2"}})}
				if result := batchOK(t, e, mixed); result.Updates != 1 {
					t.Fatalf("mixed updates = %d", result.Updates)
				}
				a.receive(t, "item_updated", idOf(saga2), []string{"root"}, nil)
				a.sockets["root"].None(t, a.marks["root"], "item_updated", absentWindow, idOf(novel))
				wantUnchanged(t, e, novel, before)
			}),
		scenario(r046, inv, "edge", "changed", "batch-update-authors-and-series-cleanup",
			"A batch that creates an author and a series and a second batch that removes them applies both, deletes the emptied author and series after the loop and emits author_added, series_removed and author_removed.",
			srcs(batchUpdateSrc, authorsUpdateSrc, seriesUpdateSrc, removeSeriesSrc, removeAuthorSrc),
			func(t *testing.T, e *harness.Env) {
				novel, saga2 := e.IDs["novel"], e.IDs["saga2"]
				a := peopleAudience(t, e, s)
				result := batchOK(t, e, []any{
					batchEntry(novel, map[string]any{"metadata": map[string]any{"authors": []any{named("Ada Example"), named("Batch Author")}}}),
					batchEntry(saga2, map[string]any{"metadata": map[string]any{"series": []any{seriesEntry("Long Saga", "2"), seriesEntry("Batch Series", "9")}}}),
				})
				if result.Updates != 2 {
					t.Fatalf("updates = %d", result.Updates)
				}
				author, series := authorRow(t, e, "Batch Author"), seriesRow(t, e, "Batch Series")
				if author == nil || series == nil {
					t.Fatalf("author %v series %v", author, series)
				}
				requireEqual(t, "bookSeries", bookSeriesRows(t, e, saga2), []string{"Batch Series|9", "Long Saga|2"})
				a.receive(t, "author_added", idOf(author["id"].(string)), []string{"root"}, []string{"anonymous"})
				a.reset()
				result = batchOK(t, e, []any{
					batchEntry(novel, map[string]any{"metadata": map[string]any{"authors": []any{named("Ada Example")}}}),
					batchEntry(saga2, map[string]any{"metadata": map[string]any{"series": []any{seriesEntry("Long Saga", "2")}}}),
				})
				if result.Updates != 2 {
					t.Fatalf("updates = %d", result.Updates)
				}
				if authorRow(t, e, "Batch Author") != nil || seriesRow(t, e, "Batch Series") != nil {
					t.Fatal("emptied author or series still stored")
				}
				a.receive(t, "author_removed", idOf(author["id"].(string)), []string{"root"}, []string{"anonymous"})
				a.receive(t, "series_removed", idOf(series["id"].(string)), []string{"root"}, []string{"anonymous"})
			}),
		scenario(r046, inv, "edge", "failure", "batch-update-skips-invalid-podcast-cron",
			"A podcast entry with an invalid auto download cron is skipped without failing the batch: the other entries are applied and the count excludes it.",
			srcs(batchUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				podcast, novel := e.IDs["podcast"], e.IDs["novel"]
				before := updatedAt(t, e, podcast)
				title := mediaRow(t, e, podcast)["title"]
				result := batchOK(t, e, []any{
					batchEntry(podcast, map[string]any{"autoDownloadSchedule": "bad cron", "metadata": map[string]any{"title": "Skipped Title"}}),
					batchEntry(novel, map[string]any{"tags": []string{"after-skip"}}),
				})
				if result.Updates != 1 {
					t.Fatalf("updates = %d", result.Updates)
				}
				wantUnchanged(t, e, podcast, before)
				if mediaRow(t, e, podcast)["title"] != title {
					t.Fatal("skipped podcast was changed")
				}
				requireEqual(t, "novel tags", column(t, mediaRow(t, e, novel), "tags"), []any{"after-skip"})
			}),
		scenario(r046, errInv, "error", "initial", "batch-update-permission-before-validation",
			"A user without update permission gets 403 even for an invalid body, and nothing changes; without a token the answer is 401.",
			srcs(batchUpdateSrc, "client/pages/batch/index.vue:430"),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				valid := []any{batchEntry(e.IDs["novel"], map[string]any{"tags": []string{"nope"}})}
				for _, body := range []any{valid, []any{}} {
					for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "deleter")} {
						wantStatus(t, "no update permission", postBatchUpdate(t, e, token, body), http.StatusForbidden)
					}
				}
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/batch/update", JSON: valid}), http.StatusUnauthorized)
				wantSnapshot(t, e, rows)
			}),
		scenario(r046, errInv, "error", "failure", "batch-update-invalid-payloads-are-400",
			"A body that is not a non-empty array, has an entry without id, or repeats an id answers 400 and changes nothing.",
			srcs(batchUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				id := e.IDs["novel"]
				entry := batchEntry(id, map[string]any{"tags": []string{"bad"}})
				for name, body := range map[string]any{
					"object": map[string]any{"id": id}, "empty array": []any{}, "no id": []any{map[string]any{"mediaPayload": map[string]any{}}},
					"duplicate id": []any{entry, entry}, "null entry": []any{nil},
				} {
					wantStatus(t, name, postBatchUpdate(t, e, e.Token(t, "root"), body), http.StatusBadRequest)
				}
				wantSnapshot(t, e, rows)
			}),
		scenario(r046, errInv, "error", "failure", "batch-update-unknown-and-inaccessible-items",
			"An unknown id answers 404 and applies nothing, also when another entry is inaccessible; an inaccessible entry answers 403 and applies none of the entries.",
			srcs(batchUpdateSrc, "server/controllers/LibraryItemController.js:36-44"),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				ebook, novel := e.IDs["ebook"], e.IDs["novel"]
				missing := "00000000-0000-4000-8000-000000000000"
				change := map[string]any{"tags": []string{"batch-denied"}}
				tagallow := s.token(t, e, "tagallow")
				wantStatus(t, "unknown id", postBatchUpdate(t, e, e.Token(t, "root"), []any{batchEntry(ebook, change), batchEntry(missing, change)}), http.StatusNotFound)
				wantStatus(t, "unknown before inaccessible", postBatchUpdate(t, e, tagallow, []any{batchEntry(novel, change), batchEntry(missing, change)}), http.StatusNotFound)
				wantStatus(t, "inaccessible entry", postBatchUpdate(t, e, tagallow, []any{batchEntry(ebook, change), batchEntry(novel, change)}), http.StatusForbidden)
				wantStatus(t, "library rule", postBatchUpdate(t, e, s.token(t, e, "podcastonly"), []any{batchEntry(e.IDs["podcast"], change), batchEntry(novel, change)}), http.StatusForbidden)
				wantSnapshot(t, e, rows)
			}),
	}
}
