package scan

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	batchScanReq = "route-r049"
	batchScanInv = "INV-ROUTE-R049-CONTRACT"
	batchScanSrc = "server/controllers/LibraryItemController.js:820-856"
)

// batchScan posts a batch scan and asserts the status.
func batchScan(t *testing.T, e *harness.Env, token string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/batch/scan", body)
}

// waitTitle waits until the item document carries the title.
func waitTitle(t *testing.T, e *harness.Env, itemID, title string) {
	t.Helper()
	harness.Eventually(t, scanTimeout, "the item to carry the title "+title, func() bool {
		return itemDocument(t, e, itemID).Media.Metadata.Title == title
	})
}

// itemUpdatedFor matches item_updated events for the item id.
func itemUpdatedFor(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &payload) == nil && payload.ID == id
	}
}

// batchScenarios binds POST /api/items/batch/scan.
func batchScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: batchScanReq, Invariant: batchScanInv, Case: "normal", State: "initial", Assertion: "batch-scan-rescans-each-item",
			Sources: []string{batchScanSrc, "server/scanner/LibraryItemScanner.js:29-76"},
			Claim:   "A batch scan of two folder items answers 200 and rescans each of them, so each item takes the title of its edited metadata.json and its sidecar is rewritten to match.",
			Run: func(t *testing.T, e *harness.Env) {
				middle, disposable := e.IDs["saga2"], e.IDs["disposable"]
				writeSidecar(t, e, middle, map[string]any{"title": "Batch Middle"})
				writeSidecar(t, e, disposable, map[string]any{"title": "Batch Disposable"})
				resp := batchScan(t, e, e.Token(t, "root"), map[string]any{"libraryItemIds": []string{middle, disposable}})
				wantStatus(t, "batch scan", resp, http.StatusOK)
				waitTitle(t, e, middle, "Batch Middle")
				waitTitle(t, e, disposable, "Batch Disposable")
				if got := readSidecar[bookMetadata](t, e, middle).Title; got != "Batch Middle" {
					t.Fatalf("sidecar of %s title = %q, want Batch Middle", middle, got)
				}
				if got := readSidecar[bookMetadata](t, e, disposable).Title; got != "Batch Disposable" {
					t.Fatalf("sidecar of %s title = %q, want Batch Disposable", disposable, got)
				}
			},
		},
		{
			Requirement: batchScanReq, Invariant: batchScanInv, Case: "normal", State: "repeat", Assertion: "batch-scan-repeat-is-silent",
			Sources: []string{batchScanSrc, "server/scanner/LibraryItemScanner.js:63-75"},
			Claim:   "The same batch scan again answers 200, leaves both items' rows and sidecars unchanged and sends no item_updated event for them.",
			Run: func(t *testing.T, e *harness.Env) {
				middle, disposable := e.IDs["saga2"], e.IDs["disposable"]
				beforeMiddle, beforeDisposable := itemDocument(t, e, middle), itemDocument(t, e, disposable)
				middleSidecar, disposableSidecar := readFile(t, sidecarPath(e, middle)), readFile(t, sidecarPath(e, disposable))
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				resp := batchScan(t, e, e.Token(t, "root"), map[string]any{"libraryItemIds": []string{middle, disposable}})
				wantStatus(t, "repeat batch scan", resp, http.StatusOK)
				socket.None(t, mark, "item_updated", 2*absentWindow, func(args []json.RawMessage) bool {
					return itemUpdatedFor(middle)(args) || itemUpdatedFor(disposable)(args)
				})
				afterMiddle, afterDisposable := itemDocument(t, e, middle), itemDocument(t, e, disposable)
				if afterMiddle.UpdatedAt != beforeMiddle.UpdatedAt || afterMiddle.LastScan != beforeMiddle.LastScan ||
					afterDisposable.UpdatedAt != beforeDisposable.UpdatedAt || afterDisposable.LastScan != beforeDisposable.LastScan {
					t.Fatal("repeat batch scan moved an item's updatedAt or lastScan")
				}
				if string(readFile(t, sidecarPath(e, middle))) != string(middleSidecar) || string(readFile(t, sidecarPath(e, disposable))) != string(disposableSidecar) {
					t.Fatal("repeat batch scan rewrote a sidecar")
				}
			},
		},
		{
			Requirement: batchScanReq, Invariant: batchScanInv, Case: "edge", State: "changed", Assertion: "batch-scan-skips-file-items",
			Sources: []string{batchScanSrc},
			Claim:   "A batch that mixes a file library item with a folder item answers 200, rescans only the folder item and leaves the file item row untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				fileItem := ensureRootFileItem(t, e)
				novel := e.IDs["novel"]
				beforeFile := itemDocument(t, e, fileItem)
				writeSidecar(t, e, novel, map[string]any{"title": "Batch Mixed Novel"})
				resp := batchScan(t, e, e.Token(t, "root"), map[string]any{"libraryItemIds": []string{fileItem, novel}})
				wantStatus(t, "mixed batch scan", resp, http.StatusOK)
				waitTitle(t, e, novel, "Batch Mixed Novel")
				afterFile := itemDocument(t, e, fileItem)
				if afterFile.UpdatedAt != beforeFile.UpdatedAt || afterFile.LastScan != beforeFile.LastScan || afterFile.Path != beforeFile.Path {
					t.Fatal("the file item was rescanned although batch scan does not support it")
				}
			},
		},
		{
			Requirement: batchScanReq, Invariant: batchScanInv, Case: "error", State: "failure", Assertion: "batch-scan-rejections-change-nothing",
			Sources: []string{batchScanSrc, "server/routers/ApiRouter.js:106"},
			Claim:   "Batch scan answers 401 without a token, 403 for a non-admin, 400 for an empty list and 400 when no id matches an item; rejected requests rescan nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				before := itemDocument(t, e, novel)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/batch/scan", JSON: map[string]any{"libraryItemIds": []string{novel}}}), http.StatusUnauthorized)
				wantStatus(t, "listener", batchScan(t, e, e.Token(t, "listener"), map[string]any{"libraryItemIds": []string{novel}}), http.StatusForbidden)
				wantStatus(t, "empty list", batchScan(t, e, e.Token(t, "root"), map[string]any{"libraryItemIds": []string{}}), http.StatusBadRequest)
				wantStatus(t, "no body", e.Post(t, e.Token(t, "root"), "/api/items/batch/scan", nil), http.StatusBadRequest)
				wantStatus(t, "unknown ids", batchScan(t, e, e.Token(t, "root"), map[string]any{"libraryItemIds": []string{"not-an-item", "also-not"}}), http.StatusBadRequest)
				after := itemDocument(t, e, novel)
				if after.UpdatedAt != before.UpdatedAt || after.LastScan != before.LastScan {
					t.Fatal("a rejected batch scan rescanned the item")
				}
			},
		},
	}
}
