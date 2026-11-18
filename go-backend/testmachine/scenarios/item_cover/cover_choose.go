package item_cover

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// chooseCover sends PATCH /api/items/:id/cover.
func chooseCover(t *testing.T, e *harness.Env, token, itemID string, body any) harness.Response {
	t.Helper()
	return e.Patch(t, token, coverURLPath(itemID), body)
}

// libraryFile returns the local path of a scanned library file by name.
func libraryFile(t *testing.T, e *harness.Env, itemID, name string) string {
	t.Helper()
	var files []struct {
		Metadata struct {
			Filename string `json:"filename"`
			Path     string `json:"path"`
		} `json:"metadata"`
	}
	raw, ok := itemRow(t, e, itemID)["libraryFiles"].(string)
	if !ok {
		t.Fatalf("item %s has no libraryFiles", itemID)
	}
	requireEqual(t, "decode libraryFiles", decodeJSON(t, raw, &files), nil)
	for _, file := range files {
		if file.Metadata.Filename == name {
			return file.Metadata.Path
		}
	}
	t.Fatalf("item %s has no library file %q", itemID, name)
	return ""
}

// chooseCoverScenarios covers PATCH /api/items/:id/cover.
func chooseCoverScenarios(s *setup) []harness.Scenario {
	const req = "route-r056"
	// prepareSaga2 adds two scannable images to the item and returns their paths.
	prepareSaga2 := func(t *testing.T, e *harness.Env) (first, second string) {
		t.Helper()
		dir := itemPath(t, e, e.IDs["saga2"])
		first = seedFile(t, dir, "picture.png", pngImage(t, 22, 22))
		second = seedFile(t, dir, "second.jpg", jpegImage(t, 18, 26))
		wantStatus(t, "rescan saga2", e.Post(t, e.Token(t, "root"), "/api/items/"+e.IDs["saga2"]+"/scan", nil), http.StatusOK)
		return first, second
	}
	return []harness.Scenario{
		scenario(req, "INV-R056-AUTH-001", "error", "initial", "choose-cover-auth-and-body-order",
			"PATCH cover answers 401 without a token, 404 for an unknown item even without the update permission, 403 for a user without library access or without the update permission, and 400 without a cover field; every rejection changes no row, file or event.",
			srcs(updateCoverSrc, middlewareSrc, "server/Auth.js:322"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				path := libraryFile(t, e, id, "desc.txt")
				wantStatus(t, "anonymous", chooseCover(t, e, "", id, map[string]any{"cover": path}), http.StatusUnauthorized)
				unknown := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, "unknown item as root", chooseCover(t, e, e.Token(t, "root"), unknown, map[string]any{"cover": path}), http.StatusNotFound)
				wantStatus(t, "unknown item before permission", chooseCover(t, e, e.Token(t, "listener"), unknown, map[string]any{"cover": path}), http.StatusNotFound)
				wantStatus(t, "no library access", chooseCover(t, e, s.token(t, e, "podcastonly"), id, map[string]any{"cover": path}), http.StatusForbidden)
				wantStatus(t, "no update permission", chooseCover(t, e, e.Token(t, "listener"), id, map[string]any{"cover": path}), http.StatusForbidden)
				wantStatus(t, "no cover field", chooseCover(t, e, e.Token(t, "root"), id, map[string]any{}), http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-R056-SUCCESS-001", "normal", "initial", "choose-cover-copies-a-library-image",
			"PATCH cover with a scanned png of the item copies it to metadata/items/<id>/cover.png byte for byte, sets the media coverPath, answers {success:true,cover}, moves updatedAt and emits item_updated; the previous cover.jpg of the item is removed.",
			srcs(updateCoverSrc, coverManagerSrc+":132-207", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				first, _ := prepareSaga2(t, e)
				o := observe(t, e, map[string]string{
					"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "anonymous": "",
				})
				before := updatedAt(t, e, id)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": first})
				wantStatus(t, "choose cover", resp, http.StatusOK)
				want := filepath.Join(coverDir(e, id), "cover.png")
				wantCoverResponse(t, resp, want)
				wantMediaCoverPath(t, e, id, want)
				requireEqual(t, "copied cover bytes", string(readDisk(t, want)), string(readDisk(t, first)))
				wantImageSize(t, "copied cover", readDisk(t, want), 22, 22)
				wantMoved(t, e, id, before)
				event := o.itemUpdated(t, e, id, []string{"root", "listener"}, []string{"anonymous"})
				requireEqual(t, "event coverPath", harness.Dig(event, "media", "coverPath"), want)
			}),
		scenario(req, "INV-R056-REPEAT-001", "normal", "repeat", "choose-cover-repeat-recopies-and-emits",
			"Repeating PATCH cover with the same scanned path copies it again, answers {success:true,cover}, moves updatedAt and emits item_updated again, because the stored coverPath is the metadata copy and not the scanned path.",
			srcs(updateCoverSrc, coverManagerSrc+":132-207"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				first := libraryFile(t, e, id, "picture.png")
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "anonymous": ""})
				before := updatedAt(t, e, id)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": first})
				wantStatus(t, "repeat choose", resp, http.StatusOK)
				want := filepath.Join(coverDir(e, id), "cover.png")
				wantCoverResponse(t, resp, want)
				requireEqual(t, "copied bytes", string(readDisk(t, want)), string(readDisk(t, first)))
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, []string{"anonymous"})
			}),
		scenario(req, "INV-R056-NOOP-001", "edge", "repeat", "choose-cover-already-stored-path-is-a-noop",
			"When the requested path is already the stored coverPath (the fixture item whose cover is its own scanned cover.jpg), PATCH cover answers 200 with that path but moves no updatedAt, copies nothing and emits no item_updated.",
			srcs(updateCoverSrc, coverManagerSrc+":161-166"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				source := libraryFile(t, e, id, "cover.jpg")
				requireEqual(t, "fixture coverPath", mediaCoverPath(t, e, id), source)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				mtime := fileModTime(t, source)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": source})
				wantStatus(t, "no-op choose", resp, http.StatusOK)
				wantCoverResponse(t, resp, source)
				wantUnchanged(t, e, id, before)
				requireEqual(t, "file mtime", fileModTime(t, source), mtime)
				if pathExists(coverDir(e, id)) {
					requireEqual(t, "no copy", sortedCopy(dirNames(t, coverDir(e, id))), []string{"metadata.json"})
				}
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-R056-SUCCESS-001", "normal", "changed", "choose-cover-switches-to-another-scanned-image",
			"After a second scanned image is added to the item, PATCH cover with it copies it as cover.jpg, removes the previous cover.png, keeps metadata.json, purges the resized cache and emits item_updated.",
			srcs(updateCoverSrc, coverManagerSrc+":182-207", cacheManagerSrc+":93-110"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				_, second := prepareSaga2(t, e)
				wantStatus(t, "seed cache", get(t, e, coverURLPath(id)+"?width=90&format=jpeg", nil), http.StatusOK)
				if len(cacheEntries(t, e, id)) == 0 {
					t.Fatal("no cache entry before the switch")
				}
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": second})
				wantStatus(t, "switch cover", resp, http.StatusOK)
				want := filepath.Join(coverDir(e, id), "cover.jpg")
				wantCoverResponse(t, resp, want)
				requireEqual(t, "cover directory", sortedCopy(dirNames(t, coverDir(e, id))), []string{"cover.jpg", "metadata.json"})
				requireEqual(t, "stored cover bytes", string(readDisk(t, want)), string(readDisk(t, second)))
				wantImageSize(t, "stored cover", readDisk(t, want), 18, 26)
				requireEqual(t, "cache purged", cacheEntries(t, e, id), []string{})
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
			}),
		scenario(req, "INV-R056-INVALID-001", "error", "failure", "choose-cover-rejects-invalid-paths",
			"PATCH cover answers 500 and changes nothing when the path is not a scanned file of the item, is an http url, is a scanned text file (Invalid image), or is a scanned image that has disappeared from disk.",
			srcs(updateCoverSrc, coverManagerSrc+":132-180"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				// Setup state first: a scanned text file and a scanned file that
				// disappears from disk; the rejected requests must not touch either.
				note := seedFile(t, itemPath(t, e, id), "note.txt", []byte("chapter list"))
				vanished := seedFile(t, itemPath(t, e, id), "vanished.png", pngImage(t, 9, 9))
				wantStatus(t, "rescan item", e.Post(t, e.Token(t, "root"), "/api/items/"+id+"/scan", nil), http.StatusOK)
				// The scan reports its own item_updated; consume it before the
				// rejected requests and prove they add none.
				o.sockets["root"].Wait(t, o.marks["root"], "item_updated", eventWait, idOf(id))
				o.reset()
				if err := removeFile(vanished); err != nil {
					t.Fatalf("remove vanished image: %v", err)
				}
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				cacheBefore := cacheEntries(t, e, id)
				for _, c := range []struct{ what, cover string }{
					{"not a library file", filepath.Join(itemPath(t, e, id), "missing.png")},
					{"http url", "https://example.com/cover.jpg"},
					{"text library file", note},
					{"missing file", vanished},
				} {
					wantStatus(t, c.what, chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": c.cover}), http.StatusInternalServerError)
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				requireEqual(t, "cache unchanged", sortedCopy(cacheEntries(t, e, id)), sortedCopy(cacheBefore))
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, "INV-R056-BODY-LIMIT-001", "edge", "initial", "choose-cover-json-body-limit",
			"PATCH cover accepts the 10 MB JSON body limit: a 1.1 MB cover path is parsed and reaches the handler's 500 invalid-path answer, while a body above 10 MB is rejected by the parser with 413; neither changes a row or file.",
			srcs("server/Server.js:352", updateCoverSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				rows := snapshot(t, e)
				wantStatus(t, "1.1 MB path", chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": strings.Repeat("a", 1_100_000)}), http.StatusInternalServerError)
				resp := chooseCover(t, e, e.Token(t, "root"), id, map[string]any{"cover": strings.Repeat("b", 11<<20)})
				requireEqual(t, "over-limit status", resp.Status, http.StatusRequestEntityTooLarge)
				wantSnapshot(t, e, rows)
			}),
	}
}
