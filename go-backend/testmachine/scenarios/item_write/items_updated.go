package item_write

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	itemsUpdatedReq = "socket-outgoing-items-updated"
	itemsUpdatedInv = "INV-SOCKET-OUTGOING-ITEMS-UPDATED"
	scannerSrc      = "server/scanner/LibraryScanner.js:185-235"
	itemsUpdatedSrc = "server/scanner/LibraryScanner.js:226,234"
	itemsEmitterSrc = "server/SocketAuthority.js:118-131"
	libraryScanSrc  = "server/controllers/LibraryController.js:1262-1275"
)

// scanStamp returns the libraries.lastScan text of a library.
func scanStamp(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT lastScan FROM libraries WHERE id = ?", libraryID)
	if len(rows) != 1 {
		t.Fatalf("library %s: %d rows", libraryID, len(rows))
	}
	text, _ := rows[0]["lastScan"].(string)
	return text
}

// scanLibrary triggers a library scan through the API and waits until the
// library recorded the completed scan, so a following event check is not raced
// by the asynchronous scan.
func scanLibrary(t *testing.T, e *harness.Env, libraryID string, force bool) {
	t.Helper()
	before := scanStamp(t, e, libraryID)
	path := "/api/libraries/" + libraryID + "/scan"
	if force {
		path += "?force=1"
	}
	wantStatus(t, "library scan", e.Post(t, e.Token(t, "root"), path, nil), http.StatusOK)
	harness.Eventually(t, 60*time.Second, "the library scan to finish", func() bool {
		return scanStamp(t, e, libraryID) != before
	})
}

// hideItemFolder moves an item folder out of the media tree and returns a
// function that puts it back.
func hideItemFolder(t *testing.T, e *harness.Env, itemID string) func() {
	t.Helper()
	from := itemPath(t, e, itemID)
	to := filepath.Join(e.Install, "hidden-"+itemID)
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("hide item folder %s: %v", from, err)
	}
	return func() {
		if err := os.Rename(to, from); err != nil {
			t.Fatalf("restore item folder %s: %v", from, err)
		}
	}
}

// itemsUpdatedFor matches an items_updated event whose payload array holds the item id.
func itemsUpdatedFor(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var list []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(args[0], &list) != nil {
			return false
		}
		for _, item := range list {
			if item.ID == id {
				return true
			}
		}
		return false
	}
}

// itemsPayload decodes the array payload of an items_updated event.
func itemsPayload(t *testing.T, ev harness.Event) []map[string]any {
	t.Helper()
	if len(ev.Args) != 1 {
		t.Fatalf("items_updated has %d arguments, want 1", len(ev.Args))
	}
	var list []map[string]any
	if err := json.Unmarshal(ev.Args[0], &list); err != nil {
		t.Fatalf("items_updated payload: %v", err)
	}
	return list
}

// requireOnlyItem fails unless the payload holds exactly the item and the item
// document returned by the API, skipping fields the scan writes asynchronously.
func requireOnlyItem(t *testing.T, e *harness.Env, ev harness.Event, itemID string, missing bool) {
	t.Helper()
	list := itemsPayload(t, ev)
	if len(list) != 1 || list[0]["id"] != itemID {
		t.Fatalf("items_updated payload ids %v, want only %s", itemIDs(list), itemID)
	}
	if got := list[0]["isMissing"]; got != missing {
		t.Fatalf("items_updated isMissing = %v, want %v", got, missing)
	}
	document := itemDocument(t, e, itemID)
	for _, key := range []string{"id", "libraryId", "mediaType"} {
		if list[0][key] != document[key] {
			t.Fatalf("items_updated %s = %v, want %v", key, list[0][key], document[key])
		}
	}
	requireEqual(t, "payload title", harness.Dig(list[0], "media", "metadata", "title"), harness.Dig(document, "media", "metadata", "title"))
	requireEqual(t, "payload libraryFiles", len(libraryFileListOf(list[0])), len(libraryFileListOf(document)))
}

// libraryFileListOf returns the libraryFiles array of a decoded item document.
func libraryFileListOf(item map[string]any) []any {
	list, _ := item["libraryFiles"].([]any)
	return list
}

// itemIDs lists the ids of an items_updated payload.
func itemIDs(list []map[string]any) []string {
	var out []string
	for _, item := range list {
		id, _ := item["id"].(string)
		out = append(out, id)
	}
	return out
}

// scanAudience opens one socket per access rule for the library-scan events.
func scanAudience(t *testing.T, e *harness.Env, s *setup) *audience {
	t.Helper()
	return connect(t, e, map[string]string{
		"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "tagallow": s.token(t, e, "tagallow"),
		"tagdeny": s.token(t, e, "tagdeny"), "podcastonly": s.token(t, e, "podcastonly"), "anonymous": "",
	})
}

// itemsUpdatedScenarios binds the library-scan producer of items_updated: a
// missing item, the repeat, a restored item, the force mode and rejected scans.
func itemsUpdatedScenarios(s *setup) []harness.Scenario {
	return []harness.Scenario{
		scenario(itemsUpdatedReq, itemsUpdatedInv, "normal", "initial", "items-updated-on-scan-marks-missing",
			"A library scan after an item folder disappeared emits items_updated with that one expanded item flagged missing to users who can access it, and to no other socket; the item row is flagged missing.",
			srcs(scannerSrc, itemsUpdatedSrc, itemsEmitterSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["saga2"]
				scanLibrary(t, e, e.IDs["library"], false) // settle the fixture so only the hidden item changes
				restore := hideItemFolder(t, e, id)
				defer restore()
				a := scanAudience(t, e, s)
				scanLibrary(t, e, e.IDs["library"], false)
				events := a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root", "listener", "tagdeny"}, []string{"tagallow", "podcastonly", "anonymous"})
				requireOnlyItem(t, e, events["root"], id, true)
				requireEqual(t, "same payload for every audience", payloadOf(t, events["tagdeny"]), payloadOf(t, events["root"]))
				if itemRow(t, e, id)["isMissing"] != int64(1) {
					t.Fatal("libraryItems.isMissing is not set")
				}
			}),
		scenario(itemsUpdatedReq, itemsUpdatedInv, "normal", "repeat", "items-updated-repeat-scan-is-silent",
			"A second scan with no further change emits no items_updated and leaves the missing flag as it was.",
			srcs(scannerSrc, itemsUpdatedSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["saga2"]
				restore := hideItemFolder(t, e, id)
				defer restore()
				a := scanAudience(t, e, s)
				scanLibrary(t, e, e.IDs["library"], false)
				a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root"}, nil)
				a.reset()
				scanLibrary(t, e, e.IDs["library"], false)
				a.receive(t, "items_updated", nil, nil, []string{"root", "listener", "tagdeny"})
				if itemRow(t, e, id)["isMissing"] != int64(1) {
					t.Fatal("repeat scan cleared the missing flag")
				}
			}),
		scenario(itemsUpdatedReq, itemsUpdatedInv, "edge", "changed", "items-updated-scan-restores-item",
			"A scan after the item folder came back emits items_updated with exactly that item, no longer missing, and clears the stored missing flag.",
			srcs(scannerSrc, itemsUpdatedSrc, itemsEmitterSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["saga2"]
				scanLibrary(t, e, e.IDs["library"], false) // settle the fixture so only the restored item changes
				restore := hideItemFolder(t, e, id)
				a := scanAudience(t, e, s)
				scanLibrary(t, e, e.IDs["library"], false)
				a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root"}, nil)
				a.reset()
				restore()
				scanLibrary(t, e, e.IDs["library"], false)
				events := a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root", "listener", "tagdeny"}, []string{"tagallow", "podcastonly", "anonymous"})
				requireOnlyItem(t, e, events["root"], id, false)
				if itemRow(t, e, id)["isMissing"] != int64(0) {
					t.Fatal("libraryItems.isMissing was not cleared")
				}
			}),
		scenario(itemsUpdatedReq, itemsUpdatedInv, "normal", "force", "items-updated-forced-scan-restores-item",
			"A forced library scan (force=1) of a restored item emits items_updated with the item no longer missing.",
			srcs(scannerSrc, libraryScanSrc, itemsUpdatedSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["saga2"]
				restore := hideItemFolder(t, e, id)
				a := scanAudience(t, e, s)
				scanLibrary(t, e, e.IDs["library"], false)
				a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root"}, nil)
				a.reset()
				restore()
				scanLibrary(t, e, e.IDs["library"], true)
				events := a.receive(t, "items_updated", itemsUpdatedFor(id), []string{"root", "listener"}, nil)
				requireOnlyItem(t, e, events["root"], id, false)
			}),
		scenario(itemsUpdatedReq, itemsUpdatedInv, "error", "failure", "items-updated-rejected-scan-emits-nothing",
			"A scan without a token answers 401, a non-admin answers 403 and a user without access to the library answers 403; none runs a scan, changes rows or emits items_updated.",
			srcs(libraryScanSrc, scannerSrc, itemsEmitterSrc),
			func(t *testing.T, e *harness.Env) {
				podcastToken := s.token(t, e, "podcastonly")
				rows, stamp := snapshot(t, e), scanStamp(t, e, e.IDs["library"])
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "anonymous": ""})
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/libraries/" + e.IDs["library"] + "/scan"}), http.StatusUnauthorized)
				wantStatus(t, "non-admin", e.Post(t, e.Token(t, "listener"), "/api/libraries/"+e.IDs["library"]+"/scan", nil), http.StatusForbidden)
				wantStatus(t, "other library", e.Post(t, podcastToken, "/api/libraries/"+e.IDs["library"]+"/scan", nil), http.StatusForbidden)
				wantSnapshot(t, e, rows)
				if got := scanStamp(t, e, e.IDs["library"]); got != stamp {
					t.Fatalf("rejected scans moved libraries.lastScan from %q to %q", stamp, got)
				}
				a.receive(t, "items_updated", nil, nil, []string{"root"})
			}),
	}
}

// payloadOf returns the first argument of an event without decoding it.
func payloadOf(t *testing.T, ev harness.Event) json.RawMessage {
	t.Helper()
	if len(ev.Args) != 1 {
		t.Fatalf("event %s has %d arguments, want 1", ev.Name, len(ev.Args))
	}
	return ev.Args[0]
}
