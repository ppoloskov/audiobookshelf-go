package share

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// scanLibrary triggers an API scan and waits for the item at folder to appear.
func scanLibrary(t *testing.T, e *harness.Env, libraryID, folder string) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries/"+libraryID+"/scan", nil)
	wantStatus(t, resp, http.StatusOK, "trigger scan")
	var itemID string
	harness.Eventually(t, 60*time.Second, "scanned single-file item "+folder, func() bool {
		rows := e.Rows(t, "SELECT id FROM libraryItems WHERE path = ?", folder)
		if len(rows) != 1 {
			return false
		}
		itemID, _ = rows[0]["id"].(string)
		return itemID != ""
	})
	return itemID
}

// singleFileScenarios cover the file branch of the download route on an item
// whose library item is a single audio file. They run last because they add a
// media folder to the shared install.
func singleFileScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r223", Invariant: "INV-R223-CONTRACT-001", Case: "edge", State: "changed", Assertion: "download-serves-single-file-item",
			Sources: []string{"server/controllers/ShareController.js:258-270", "server/utils/fileUtils.js:424-429"},
			Claim:   "A share of a single-file library item downloads the audio file itself, with its audio MIME type, attachment disposition and exact bytes, instead of a zip.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				library := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+fx.library, map[string]any{"settings": map[string]any{"disableWatcher": true}})
				wantStatus(t, library, http.StatusOK, "disable watcher")
				source := mediaFile(t, e, libraryPath("audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3"))
				audio := filepath.Join(e.Media, "audiobooks", "Singled Out.mp3")
				if err := os.WriteFile(audio, source, 0o644); err != nil {
					t.Fatal(err)
				}
				itemID := scanLibrary(t, e, fx.library, audio)
				rows := e.Rows(t, "SELECT isFile, relPath FROM libraryItems WHERE id = ?", itemID)
				if len(rows) != 1 || rows[0]["isFile"] != int64(1) {
					t.Fatalf("scanned item must be a single-file item: %v", rows)
				}
				relPath, _ := rows[0]["relPath"].(string)
				if relPath == "" {
					t.Fatal("single-file item must have a relPath")
				}

				created := mustCreateShare(t, e, "share-slug-single-file", mediaIDForItem(t, e, itemID), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				resp := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, resp, http.StatusOK, "single-file download")
				if got := resp.Header.Get("Content-Type"); got != "audio/mpeg" {
					t.Fatalf("single-file Content-Type = %q, want audio/mpeg", got)
				}
				disposition := resp.Header.Get("Content-Disposition")
				if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "Singled Out.mp3") {
					t.Fatalf("single-file Content-Disposition = %q", disposition)
				}
				if !bytes.Equal(resp.Body, source) {
					t.Fatalf("single-file download body differs (%d vs %d bytes)", len(resp.Body), len(source))
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "cleanup single-file share")
			},
		},
		{
			Requirement: "route-r223", Invariant: "INV-R223-CONTRACT-001", Case: "error", State: "failure", Assertion: "download-denies-missing-file-and-item",
			Sources: []string{"server/controllers/ShareController.js:244-252", "server/controllers/ShareController.js:266-274"},
			Claim:   "A share whose single-file audio disappeared from disk answers 500 from the download route, and after the library item is deleted the same session answers 404, never streaming a partial file.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				library := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+fx.library, map[string]any{"settings": map[string]any{"disableWatcher": true}})
				wantStatus(t, library, http.StatusOK, "disable watcher")
				source := mediaFile(t, e, libraryPath("audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3"))
				audio := filepath.Join(e.Media, "audiobooks", "Now Missing.mp3")
				if err := os.WriteFile(audio, source, 0o644); err != nil {
					t.Fatal(err)
				}
				itemID := scanLibrary(t, e, fx.library, audio)
				created := mustCreateShare(t, e, "share-slug-missing-file", mediaIDForItem(t, e, itemID), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				if err := os.Remove(audio); err != nil {
					t.Fatal(err)
				}
				failed := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, failed, http.StatusInternalServerError, "download with the audio file gone")
				if len(failed.Body) > 1000 {
					t.Fatalf("failed download must not stream media, got %d bytes", len(failed.Body))
				}
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/items/"+itemID+"?hard=1"), http.StatusOK, "delete the library item")
				gone := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, gone, http.StatusNotFound, "download after item deletion")
				if len(gone.Body) > 1000 {
					t.Fatalf("download after item deletion must not stream media, got %d bytes", len(gone.Body))
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "cleanup missing-file share")
			},
		},
	}
}
