package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	removedReq = "socket-outgoing-item-removed"
	removedInv = "INV-SOCKET-OUTGOING-ITEM-REMOVED"
)

// countEvents counts the events of a name a socket received since it was marked.
func countEvents(a *audience, name, event string) int {
	total := 0
	for _, ev := range a.sockets[name].Events()[a.marks[name]:] {
		if ev.Name == event {
			total++
		}
	}
	return total
}

// socketItemRemovedScenarios covers the emitter side of item_removed: one event
// per deleted item, the repeat of a delete, and the hard (force) mode.
func socketItemRemovedScenarios(s *setup) []harness.Scenario {
	removedSrc := "server/routers/ApiRouter.js:402-405"
	return []harness.Scenario{
		scenario(removedReq, removedInv, "edge", "initial", "item-removed-duplicate-batch-id-once",
			"A batch delete whose id list repeats one item answers 200 and emits item_removed {id, libraryId} once per item to every authenticated socket and never to an unauthenticated one.",
			srcs(batchDeleteSrc, removedSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["disposable"]
				a := removalAudience(t, e, s)
				wantStatus(t, "batch delete duplicate id", batchDelete(t, e, e.Token(t, "root"), "", idsBody(id, id)), http.StatusOK)
				wantItemRemoved(t, a, e, id, e.IDs["library"])
				for _, name := range []string{"root", "listener", "podcastonly", "tagallow"} {
					if got := countEvents(a, name, "item_removed"); got != 1 {
						t.Fatalf("%s received %d item_removed events, want 1", name, got)
					}
				}
			}),
		scenario(removedReq, removedInv, "normal", "repeat", "item-removed-repeat-delete-once",
			"Repeating a soft item delete answers 404 and emits no second item_removed to any socket.",
			srcs(middlewareSrc, deleteItemSrc, removedSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["disposable"]
				book, _ := itemRow(t, e, id)["mediaId"].(string)
				a := removalAudience(t, e, s)
				wantStatus(t, "first delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusOK)
				wantItemRemoved(t, a, e, id, e.IDs["library"])
				harness.Eventually(t, eventWait, "the book row to be destroyed", func() bool {
					return e.Count(t, "SELECT COUNT(*) FROM books WHERE id = ?", book) == 0
				})
				a.reset()
				rows, folder := snapshot(t, e), directoryList(t, folderOf(e, id))
				wantStatus(t, "repeat delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusNotFound)
				wantSnapshot(t, e, rows)
				requireEqual(t, "media files", directoryList(t, folderOf(e, id)), folder)
				a.receive(t, "item_removed", nil, nil, []string{"root", "listener", "podcastonly", "tagallow"})
			}),
		scenario(removedReq, removedInv, "normal", "force", "item-removed-hard-delete",
			"A hard item delete answers 200, removes the item folder and emits item_removed {id, libraryId} to every authenticated socket.",
			srcs(deleteItemSrc, removedSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["disposable"]
				folder := itemPath(t, e, id)
				a := removalAudience(t, e, s)
				wantStatus(t, "hard delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id+"?hard=1"), http.StatusOK)
				if pathExists(folder) {
					t.Fatal("hard delete kept the item folder")
				}
				wantItemRemoved(t, a, e, id, e.IDs["library"])
			}),
	}
}
