package item_cover

import (
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// removeCover sends DELETE /api/items/:id/cover.
func deleteCoverRequest(t *testing.T, e *harness.Env, token, itemID string) harness.Response {
	t.Helper()
	return e.Delete(t, token, coverURLPath(itemID))
}

// deleteCoverScenarios covers DELETE /api/items/:id/cover.
func deleteCoverScenarios(s *setup) []harness.Scenario {
	const req = "route-r057"
	return []harness.Scenario{
		scenario(req, "INV-R057-DELETE-001", "normal", "initial", "delete-cover-clears-path-and-cache",
			"DELETE cover answers 200, clears the media coverPath, purges every resized cache entry of the item, moves updatedAt and emits item_updated; the uploaded cover file itself stays on disk and later raw and resized reads answer 404.",
			srcs(removeCoverSrc, cacheManagerSrc+":93-110", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				file := mediaCoverPath(t, e, id).(string)
				wantStatus(t, "seed cache", get(t, e, coverURLPath(id)+"?width=70&format=jpeg", nil), http.StatusOK)
				if len(cacheEntries(t, e, id)) == 0 {
					t.Fatal("no cache entry before the delete")
				}
				o := observe(t, e, map[string]string{
					"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": "",
				})
				before := updatedAt(t, e, id)
				wantStatus(t, "delete cover", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				wantMediaCoverPath(t, e, id, nil)
				requireEqual(t, "cache purged", cacheEntries(t, e, id), []string{})
				if !pathExists(file) {
					t.Fatalf("the uploaded cover file %s was removed", file)
				}
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root", "listener"}, []string{"anonymous"})
				wantStatus(t, "raw after delete", get(t, e, coverURLPath(id)+"?raw=1", nil), http.StatusNotFound)
				wantStatus(t, "resized after delete", get(t, e, coverURLPath(id)+"?width=70&format=jpeg", nil), http.StatusNotFound)
			}),
		scenario(req, "INV-R057-DELETE-001", "normal", "repeat", "delete-cover-without-cover-is-a-noop",
			"Repeating DELETE cover on an item whose coverPath is already empty answers 200 without moving updatedAt or emitting item_updated.",
			srcs(removeCoverSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				wantStatus(t, "repeat delete", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				wantUnchanged(t, e, id, before)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-R057-DELETE-001", "normal", "changed", "delete-cover-then-a-new-upload-is-served",
			"After a delete, uploading a new cover sets coverPath again and the raw cover route serves the new bytes, so a deletion does not block later covers.",
			srcs(removeCoverSrc, uploadCoverSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				image := jpegImage(t, 18, 22)
				stub := imageStub(t, "image/jpeg", image)
				wantStatus(t, "upload after delete", uploadURL(t, e, e.Token(t, "root"), id, stub.URL+"/new.jpg"), http.StatusOK)
				want := filepath.Join(coverDir(e, id), "cover.jpg")
				wantMediaCoverPath(t, e, id, want)
				raw := get(t, e, coverURLPath(id)+"?raw=1", nil)
				wantStatus(t, "raw restored cover", raw, http.StatusOK)
				requireEqual(t, "restored cover bytes", string(raw.Body), string(image))
				wantImageSize(t, "restored cover", raw.Body, 18, 22)
				wantStatus(t, "delete again", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				wantMediaCoverPath(t, e, id, nil)
			}),
		scenario(req, "INV-R057-AUTH-001", "error", "failure", "delete-cover-auth-order",
			"DELETE cover answers 401 without a token, 404 for an unknown item, 403 for a user without library access and 403 for a user without the delete permission; nothing changes and no item_updated is emitted.",
			srcs(removeCoverSrc, middlewareSrc, "client/components/modals/item/tabs/Cover.vue:140"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				coverBefore := mediaCoverPath(t, e, id)
				wantStatus(t, "anonymous", deleteCoverRequest(t, e, "", id), http.StatusUnauthorized)
				wantStatus(t, "unknown item", deleteCoverRequest(t, e, e.Token(t, "root"), "00000000-0000-4000-8000-000000000000"), http.StatusNotFound)
				wantStatus(t, "no library access", deleteCoverRequest(t, e, s.token(t, e, "podcastonly"), id), http.StatusForbidden)
				wantStatus(t, "no delete permission", deleteCoverRequest(t, e, e.Token(t, "listener"), id), http.StatusForbidden)
				wantStatus(t, "update but no delete", deleteCoverRequest(t, e, s.token(t, e, "noupload"), id), http.StatusForbidden)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				requireEqual(t, "coverPath unchanged", mediaCoverPath(t, e, id), coverBefore)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-R057-AUTH-001", "edge", "initial", "delete-cover-without-a-cover",
			"DELETE cover of an item that never had a cover answers 200 and leaves the rows, files and events unchanged.",
			srcs(removeCoverSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				requireEqual(t, "fixture coverPath", mediaCoverPath(t, e, id), nil)
				wantStatus(t, "delete without cover", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				o.none(t, "item_updated", idOf(id))
			}),
	}
}
