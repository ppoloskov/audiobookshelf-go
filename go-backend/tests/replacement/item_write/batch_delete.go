package item_write

import (
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r045           = "route-r045"
	batchDeleteSrc = controllerSrc + ":558-610"
)

func batchDelete(t *testing.T, e *harness.Env, token, query string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/batch/delete"+query, body)
}

func idsBody(ids ...string) map[string]any { return map[string]any{"libraryItemIds": ids} }

func batchDeleteScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R045-BATCH-DELETE-001"
	const errInv = "INV-ROUTE-R045-CONTRACT"
	const removed = "socket-outgoing-item-removed"
	return []harness.Scenario{
		scenario(r045, errInv, "error", "failure", "batch-delete-validation-and-authorisation",
			"Batch delete answers 401 without a token and 403 for a user without delete permission (before validating the body), then 400 for a missing, empty or non-array libraryItemIds, 404 when no id exists, and 403 for a user who may not access one of the items, hard or soft; nothing is removed.",
			srcs(batchDeleteSrc, "client/components/app/Appbar.vue:343-352"),
			func(t *testing.T, e *harness.Env) {
				ebook, novel := e.IDs["ebook"], e.IDs["novel"]
				a := removalAudience(t, e, s)
				rows, folder := snapshot(t, e), directoryList(t, itemPath(t, e, novel))
				root := e.Token(t, "root")
				missing := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/batch/delete", JSON: idsBody(novel)}), http.StatusUnauthorized)
				for _, body := range []any{idsBody(novel), map[string]any{}} {
					for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "updater")} {
						wantStatus(t, "no delete permission", batchDelete(t, e, token, "", body), http.StatusForbidden)
						wantStatus(t, "no delete permission hard", batchDelete(t, e, token, "?hard=1", body), http.StatusForbidden)
					}
				}
				for name, body := range map[string]any{"missing": map[string]any{}, "empty": idsBody(), "not an array": map[string]any{"libraryItemIds": "x"}, "null": map[string]any{"libraryItemIds": nil}} {
					wantStatus(t, name, batchDelete(t, e, root, "", body), http.StatusBadRequest)
				}
				wantStatus(t, "unknown ids", batchDelete(t, e, root, "", idsBody(missing)), http.StatusNotFound)
				tagallow := s.token(t, e, "tagallow")
				wantStatus(t, "inaccessible among accessible", batchDelete(t, e, tagallow, "", idsBody(ebook, novel)), http.StatusForbidden)
				wantStatus(t, "inaccessible among unknown", batchDelete(t, e, tagallow, "?hard=1", idsBody(missing, novel)), http.StatusForbidden)
				wantStatus(t, "other library", batchDelete(t, e, s.token(t, e, "podcastonly"), "", idsBody(e.IDs["podcast"], novel)), http.StatusForbidden)
				wantSnapshot(t, e, rows)
				requireEqual(t, "folder", directoryList(t, itemPath(t, e, novel)), folder)
				a.receive(t, "item_removed", nil, nil, []string{"root", "listener", "podcastonly", "tagallow"})
			}),
		scenario(r045, inv, "normal", "initial", "batch-delete-soft-several-items",
			"A soft batch delete of three books answers 200, removes each item with its progress, playlist entries, feeds and metadata folder, keeps the media files and emits item_removed {id, libraryId} per item to every authenticated socket.",
			srcs(batchDeleteSrc, handleDeleteSrc, "client/components/app/Appbar.vue:343-352"),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				ids := []string{e.IDs["disposable"], e.IDs["saga1"], e.IDs["novel"]}
				a := removalAudience(t, e, s)
				folders := map[string][]string{}
				for _, id := range ids {
					folders[id] = directoryList(t, itemPath(t, e, id))
				}
				resp := batchDelete(t, e, s.token(t, e, "deleter"), "", idsBody(ids...))
				wantStatus(t, "batch delete", resp, http.StatusOK)
				for _, id := range ids {
					wantItemRemoved(t, a, e, id, e.IDs["library"])
					requireEqual(t, "media files", directoryList(t, folderOf(e, id)), folders[id])
				}
				for query, want := range map[string]int{
					"SELECT COUNT(*) FROM libraryItems": 3, "SELECT COUNT(*) FROM books": 2,
					"SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemType = 'book' AND mediaItemId IN ('54374ca2-e3c2-4121-93eb-4b0d806b241f', '5e33de11-3e94-4c51-87d8-d06d2928ac38', '85ab0f32-b17f-4c9a-883e-a61e260f1e46')": 0,
					"SELECT COUNT(*) FROM feeds": 1, "SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemType = 'book'": 1,
				} {
					harness.Eventually(t, eventWait, query, func() bool { return e.Count(t, query) == want })
				}
				if pathExists(metadataDir(e, ids[0])) || pathExists(metadataDir(e, ids[1])) || pathExists(metadataDir(e, ids[2])) {
					t.Fatal("metadata folders remain")
				}
				wantStatus(t, "repeat", batchDelete(t, e, e.Token(t, "root"), "", idsBody(ids...)), http.StatusNotFound)
			}),
		scenario(r045, inv, "normal", "force", "batch-delete-hard-with-unknown-ids",
			"A hard batch delete removes the folders of the existing items from disk and ignores ids that do not exist (answer 200); orphaned authors and series are removed with author_removed and series_removed.",
			srcs(batchDeleteSrc, removeAuthorSrc, removeSeriesSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				c := buildCascade(t, e, "batch")
				ids := []string{c.ItemID, e.IDs["ebook"]}
				folders := []string{itemPath(t, e, ids[0]), itemPath(t, e, ids[1])}
				a := removalAudience(t, e, s)
				resp := batchDelete(t, e, e.Token(t, "root"), "?hard=1", idsBody(ids[0], "00000000-0000-4000-8000-000000000000", ids[1]))
				wantStatus(t, "batch hard delete", resp, http.StatusOK)
				for _, folder := range folders {
					if pathExists(folder) {
						t.Fatalf("%s still on disk", folder)
					}
				}
				for _, id := range ids {
					wantItemRemoved(t, a, e, id, e.IDs["library"])
				}
				a.receive(t, "author_removed", idOf(c.AuthorID), []string{"root", "listener", "podcastonly", "tagallow"}, []string{"anonymous"})
				a.receive(t, "series_removed", idOf(c.SeriesID), []string{"root", "podcastonly"}, []string{"anonymous"})
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE id = ?", c.AuthorID) != 0 || e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", c.SeriesID) != 0 {
					t.Fatal("orphaned author or series remains")
				}
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Ada Example'") != 1 {
					t.Fatal("the author of the remaining book was removed")
				}
				requireEqual(t, "remaining items", e.Count(t, "SELECT COUNT(*) FROM libraryItems"), 4)
			}),
		scenario(removed, "INV-SOCKET-OUTGOING-ITEM-REMOVED", "normal", "changed", "item-removed-once-per-batch-item",
			"A batch delete emits one item_removed per deleted item to the root and listener sockets, including a podcast of the other library, and delivers the podcast id and libraryId to the podcast-only socket.",
			srcs(batchDeleteSrc, "server/routers/ApiRouter.js:403"),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				ids := []string{e.IDs["podcast"], e.IDs["saga2"]}
				a := removalAudience(t, e, s)
				wantStatus(t, "batch delete", batchDelete(t, e, e.Token(t, "root"), "", idsBody(ids...)), http.StatusOK)
				wantItemRemoved(t, a, e, ids[1], e.IDs["library"])
				events := a.receive(t, "item_removed", idOf(ids[0]), []string{"podcastonly", "tagallow"}, []string{"anonymous"})
				requireEqual(t, "podcast payload", payload(t, events["podcastonly"]), map[string]any{"id": ids[0], "libraryId": e.IDs["podcastLibrary"]})
				for _, name := range []string{"root", "listener"} {
					count := 0
					for _, ev := range a.sockets[name].Events()[a.marks[name]:] {
						if ev.Name == "item_removed" {
							count++
						}
					}
					if count != 2 {
						t.Fatalf("%s received %d item_removed events, want 2", name, count)
					}
				}
			}),
	}
}

// folderOf returns the fixture folder of a fixture book after its item was removed.
func folderOf(e *harness.Env, itemID string) string {
	for key, parts := range map[string][]string{
		"disposable": {"audiobooks", "Zed Disposable", "Hard Delete Me"},
		"saga1":      {"audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning"},
		"novel":      {"audiobooks", "Ada Example", "Single File Novel"},
	} {
		if e.IDs[key] == itemID {
			return filepath.Join(append([]string{e.Media}, parts...)...)
		}
	}
	return ""
}
