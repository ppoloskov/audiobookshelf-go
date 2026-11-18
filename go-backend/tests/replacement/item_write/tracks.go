package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r061      = "route-r061"
	tracksSrc = controllerSrc + ":475-515"
)

func tracksRequest(t *testing.T, e *harness.Env, token, itemID string, body any) harness.Response {
	t.Helper()
	return e.Patch(t, token, "/api/items/"+itemID+"/tracks", body)
}

// trackData builds the orderedFileData of the audiobook edit page.
func trackData(inos []string, excluded ...string) map[string]any {
	var list []any
	for i, ino := range inos {
		exclude := false
		for _, ex := range excluded {
			exclude = exclude || ex == ino
		}
		list = append(list, map[string]any{"index": i + 1, "ino": ino, "exclude": exclude})
	}
	return map[string]any{"orderedFileData": list}
}

func tracksScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R061-TRACKS-001"
	const errInv = "INV-ROUTE-R061-CONTRACT"
	original := []string{"01 - Part 1.mp3:1:false", "02 - Part 2.mp3:2:false", "03 - Part 3.mp3:3:false"}
	return []harness.Scenario{
		scenario(r061, inv, "normal", "initial", "tracks-reorder",
			"PATCH tracks with a new order stores the audio files in that order with index 1..n, manuallyVerified true and exclude false, answers the old-format item and emits item_updated.",
			srcs(tracksSrc, "client/pages/audiobook/_id/edit.vue:205-222"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				requireEqual(t, "fixture audio files", audioNames(audioFileList(t, e, id)), original)
				ino1, ino2, ino3 := inoOf(t, e, id, "01 - Part 1.mp3"), inoOf(t, e, id, "02 - Part 2.mp3"), inoOf(t, e, id, "03 - Part 3.mp3")
				a := itemAudience(t, e, s)
				before, mediaBefore := updatedAt(t, e, id), mediaUpdatedAt(t, e, id)
				resp := tracksRequest(t, e, e.Token(t, "root"), id, trackData([]string{ino3, ino1, ino2}))
				wantStatus(t, "PATCH tracks", resp, http.StatusOK)
				requireEqual(t, "stored order", audioNames(audioFileList(t, e, id)), []string{"03 - Part 3.mp3:1:false", "01 - Part 1.mp3:2:false", "02 - Part 2.mp3:3:false"})
				for _, file := range audioFileList(t, e, id) {
					if file["manuallyVerified"] != true {
						t.Fatalf("manuallyVerified = %v", file["manuallyVerified"])
					}
				}
				doc := resp.Object(t)
				if doc["id"] != id || doc["mediaType"] != "book" {
					t.Fatalf("response id/mediaType %v/%v", doc["id"], doc["mediaType"])
				}
				files, _ := harness.Dig(doc, "media", "audioFiles").([]any)
				if len(files) != 3 || harness.Dig(files, 0, "metadata", "filename") != "03 - Part 3.mp3" || harness.Dig(files, 0, "index") != float64(1) || harness.Dig(files, 2, "ino") != ino2 {
					t.Fatalf("response audioFiles %v", files)
				}
				if !mediaUpdatedAt(t, e, id).After(mediaBefore) {
					t.Fatal("books.updatedAt did not move")
				}
				wantUnchanged(t, e, id, before) // upstream saves the book row only
				events := a.receive(t, "item_updated", idOf(id), []string{"root", "listener"}, []string{"podcastonly", "anonymous"})
				got := payload(t, events["root"])
				tracks, _ := harness.Dig(got, "media", "tracks").([]any)
				if len(tracks) != 3 || harness.Dig(tracks, 0, "title") != "03 - Part 3.mp3" || harness.Dig(tracks, 1, "startOffset") != harness.Dig(tracks, 0, "duration") {
					t.Fatalf("payload tracks %v", tracks)
				}
			}),
		scenario(r061, inv, "edge", "changed", "tracks-exclude-file",
			"Excluding a file stores exclude true with index -1 and sorts it first, renumbers the others from 1, and the item's tracks no longer include it.",
			srcs(tracksSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				ino1, ino2, ino3 := inoOf(t, e, id, "01 - Part 1.mp3"), inoOf(t, e, id, "02 - Part 2.mp3"), inoOf(t, e, id, "03 - Part 3.mp3")
				wantStatus(t, "exclude", tracksRequest(t, e, e.Token(t, "root"), id, trackData([]string{ino1, ino2, ino3}, ino2)), http.StatusOK)
				requireEqual(t, "stored order", audioNames(audioFileList(t, e, id)), []string{"02 - Part 2.mp3:-1:true", "01 - Part 1.mp3:1:false", "03 - Part 3.mp3:2:false"})
				tracks, _ := harness.Dig(itemDocument(t, e, id), "media", "tracks").([]any)
				if len(tracks) != 2 || harness.Dig(tracks, 0, "title") != "01 - Part 1.mp3" || harness.Dig(tracks, 1, "title") != "03 - Part 3.mp3" {
					t.Fatalf("tracks %v", tracks)
				}
			}),
		scenario(r061, inv, "normal", "repeat", "tracks-restore-and-repeat",
			"Sending the original order restores indexes 1..3 with exclude false; the identical request again answers 200 with the same stored order.",
			srcs(tracksSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				inos := []string{inoOf(t, e, id, "01 - Part 1.mp3"), inoOf(t, e, id, "02 - Part 2.mp3"), inoOf(t, e, id, "03 - Part 3.mp3")}
				for range 2 {
					wantStatus(t, "restore", tracksRequest(t, e, e.Token(t, "root"), id, trackData(inos)), http.StatusOK)
					requireEqual(t, "stored order", audioNames(audioFileList(t, e, id)), original)
				}
			}),
		scenario(r061, errInv, "error", "failure", "tracks-invalid-requests-change-nothing",
			"A podcast item, a missing or empty orderedFileData, an entry without ino and an ino that is not an audio file of the book answer 400 and change nothing.",
			srcs(tracksSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				a := itemAudience(t, e, s)
				rows := snapshot(t, e)
				ino1 := inoOf(t, e, id, "01 - Part 1.mp3")
				root := e.Token(t, "root")
				bad := map[string]any{
					"missing": map[string]any{}, "empty": map[string]any{"orderedFileData": []any{}}, "not a list": map[string]any{"orderedFileData": "x"},
					"no ino":      map[string]any{"orderedFileData": []any{map[string]any{"ino": ino1}, map[string]any{"index": 2}}},
					"unknown ino": map[string]any{"orderedFileData": []any{map[string]any{"ino": ino1}, map[string]any{"ino": "999999999"}}},
				}
				for name, body := range bad {
					wantStatus(t, name, tracksRequest(t, e, root, id, body), http.StatusBadRequest)
				}
				wantStatus(t, "podcast", tracksRequest(t, e, root, e.IDs["podcast"], trackData([]string{ino1})), http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", nil, nil, []string{"root"})
			}),
		scenario(r061, errInv, "error", "initial", "tracks-authorisation",
			"PATCH tracks answers 401 without a token, 404 for an unknown item (also for a user without update), 403 for users without update permission or access to the item, and changes nothing.",
			srcs(middlewareSrc, tracksSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				rows := snapshot(t, e)
				body := trackData([]string{inoOf(t, e, id, "02 - Part 2.mp3"), inoOf(t, e, id, "01 - Part 1.mp3"), inoOf(t, e, id, "03 - Part 3.mp3")})
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/items/" + id + "/tracks", JSON: body}), http.StatusUnauthorized)
				wantStatus(t, "unknown", tracksRequest(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", body), http.StatusNotFound)
				for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "deleter"), s.token(t, e, "podcastonly"), s.token(t, e, "tagallow")} {
					wantStatus(t, "forbidden", tracksRequest(t, e, token, id, body), http.StatusForbidden)
				}
				wantSnapshot(t, e, rows)
			}),
	}
}
