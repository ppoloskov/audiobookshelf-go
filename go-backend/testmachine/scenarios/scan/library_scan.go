package scan

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	libScanReq      = "route-r038"
	libScanResponse = "INV-R038-RESPONSE-001"
	libScanReject   = "INV-R038-SAMPLED"
	libScanForce    = "INV-R038-FORCE-001"
	libScanSrc      = "server/controllers/LibraryController.js:1262-1275"
	libScanMidSrc   = "server/controllers/LibraryController.js:1475-1500"
	libScannerSrc   = "server/scanner/LibraryScanner.js:51-135"
)

// libraryDocument is the library object of GET /api/libraries/:id (Library.toOldJSON).
type libraryDocument struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	MediaType       string         `json:"mediaType"`
	Settings        map[string]any `json:"settings"`
	LastScan        *int64         `json:"lastScan"`
	LastScanVersion *string        `json:"lastScanVersion"`
}

// libraryDocumentOf reads one library through the API.
func libraryDocumentOf(t *testing.T, e *harness.Env, id string) libraryDocument {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+id)
	wantStatus(t, "get library", resp, http.StatusOK)
	return decode[libraryDocument](t, resp.Body)
}

// scanThenAdvanced scans the library and fails unless the API lastScan moved strictly forward.
func scanThenAdvanced(t *testing.T, e *harness.Env, libraryID string, force bool) int64 {
	t.Helper()
	before := libraryDocumentOf(t, e, libraryID)
	if before.LastScan == nil {
		t.Fatal("the library has no lastScan before the scan")
	}
	libraryScan(t, e, libraryID, force)
	after := libraryDocumentOf(t, e, libraryID)
	if after.LastScan == nil || *after.LastScan <= *before.LastScan {
		t.Fatalf("lastScan = %v, want strictly after %d", after.LastScan, *before.LastScan)
	}
	return *after.LastScan
}

// createRestrictedAdmin creates an admin whose librariesAccessible is only the
// podcast library and returns that user's token.
func createRestrictedAdmin(t *testing.T, e *harness.Env) string {
	t.Helper()
	body := map[string]any{
		"username": "scan-restricted-admin", "password": "scan-restricted-password", "type": "admin", "isActive": true,
		"permissions": map[string]any{"accessAllLibraries": false, "librariesAccessible": []string{e.IDs["podcastLibrary"]}},
	}
	wantStatus(t, "create restricted admin", e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK)
	token, _ := e.Login(t, "scan-restricted-admin", "scan-restricted-password")
	return token
}

// libraryScenarios binds POST /api/libraries/:id/scan.
func libraryScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: libScanReq, Invariant: libScanResponse, Case: "normal", State: "initial", Assertion: "library-scan-records-completion",
			Sources: []string{libScanSrc, libScannerSrc, "server/scanner/LibraryScanner.js:94-104"},
			Claim:   "An admin scan answers 200 and, when it completes, moves the library's lastScan strictly forward, stamps lastScanVersion 2.37.2 and persists the configured metadata precedence in extraData while the item rows keep their ids and titles.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				before := itemTitles(t, e, library)
				scanThenAdvanced(t, e, library, false)
				doc := libraryDocumentOf(t, e, library)
				if doc.LastScanVersion == nil || *doc.LastScanVersion != "2.37.2" {
					t.Fatalf("lastScanVersion = %v, want 2.37.2", doc.LastScanVersion)
				}
				requireStrings(t, "persisted precedence", lastScanPrecedence(t, e, library), defaultPrecedence)
				requireStrings(t, "item titles", itemTitles(t, e, library), before)
			},
		},
		{
			Requirement: libScanReq, Invariant: libScanResponse, Case: "normal", State: "repeat", Assertion: "library-scan-repeat-is-stable",
			Sources: []string{libScanSrc, libScannerSrc},
			Claim:   "A second scan of an unchanged library answers 200, moves lastScan strictly forward again, adds no item and leaves every item's lastScan timestamp unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				before := itemTitles(t, e, library)
				var itemLastScan int64
				if len(before) > 0 {
					itemLastScan = itemDocument(t, e, itemIDByTitle(t, e, library, before[0])).LastScan
				}
				scanThenAdvanced(t, e, library, false)
				requireStrings(t, "item titles", itemTitles(t, e, library), before)
				if len(before) > 0 {
					if got := itemDocument(t, e, itemIDByTitle(t, e, library, before[0])).LastScan; got != itemLastScan {
						t.Fatalf("repeat scan moved the item lastScan from %d to %d", itemLastScan, got)
					}
				}
			},
		},
		{
			Requirement: libScanReq, Invariant: libScanForce, Case: "edge", State: "changed", Assertion: "library-scan-without-force-skips-unread-sidecar",
			Sources: []string{libScanSrc, "server/scanner/LibraryScanner.js:169-222"},
			Claim:   "A plain scan does not reread a metadata.json stored outside the library tree, so an edit there leaves the item title unchanged while the library records the scan.",
			Run: func(t *testing.T, e *harness.Env) {
				library, middle := e.IDs["library"], e.IDs["saga2"]
				stored := itemDocument(t, e, middle).Media.Metadata.Title
				writeSidecar(t, e, middle, map[string]any{"title": "Unforced Sidecar"})
				scanThenAdvanced(t, e, library, false)
				if got := itemDocument(t, e, middle).Media.Metadata.Title; got != stored {
					t.Fatalf("title after a plain scan = %q, want the stored title %q", got, stored)
				}
			},
		},
		{
			Requirement: libScanReq, Invariant: libScanForce, Case: "normal", State: "force", Assertion: "library-scan-force-rereads-sidecar",
			Sources: []string{libScanSrc, libScannerSrc, "server/scanner/LibraryScanner.js:203-218"},
			Claim:   "A forced scan (?force=1) rescans every item even without file changes, so the same edited metadata.json is applied and the item title becomes the sidecar title.",
			Run: func(t *testing.T, e *harness.Env) {
				library, middle := e.IDs["library"], e.IDs["saga2"]
				scanThenAdvanced(t, e, library, true)
				doc := itemDocument(t, e, middle)
				if doc.Media.Metadata.Title != "Unforced Sidecar" {
					t.Fatalf("title after the forced scan = %q, want Unforced Sidecar", doc.Media.Metadata.Title)
				}
				if got := readSidecar[bookMetadata](t, e, middle).Title; got != "Unforced Sidecar" {
					t.Fatalf("rewritten sidecar title = %q, want Unforced Sidecar", got)
				}
			},
		},
		{
			Requirement: libScanReq, Invariant: libScanResponse, Case: "edge", State: "changed", Assertion: "library-scan-adds-new-item",
			Sources: []string{libScanSrc, "server/scanner/LibraryScanner.js:258-281", "server/scanner/BookScanner.js:459-470"},
			Claim:   "A scan after a new book folder appears adds exactly one item whose title and author come from the folder names and writes its metadata.json sidecar; the response itself stays 200.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				before := itemTitles(t, e, library)
				stageFolder(t, e, "new-book", "audiobooks/New Author/New Book Title", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "New Book Title.mp3"))
				})
				libraryScan(t, e, library, false)
				titles := itemTitles(t, e, library)
				if len(titles) != len(before)+1 {
					t.Fatalf("titles = %v, want one more than %v", titles, before)
				}
				id := itemIDByTitle(t, e, library, "New Book Title")
				doc := itemDocument(t, e, id)
				requireStrings(t, "authors", authorNames(doc), []string{"New Author"})
				if !strings.HasSuffix(doc.Path, "New Author/New Book Title") {
					t.Fatalf("item path = %q", doc.Path)
				}
				requireStrings(t, "sidecar authors", readSidecar[bookMetadata](t, e, id).Authors, []string{"New Author"})
			},
		},
		{
			Requirement: libScanReq, Invariant: libScanReject, Case: "error", State: "failure", Assertion: "library-scan-rejections-change-nothing",
			Sources: []string{libScanSrc, libScanMidSrc, "server/routers/ApiRouter.js:91"},
			Claim:   "Library scan answers 401 without a token, 403 for a non-admin, 403 for an admin without access to the library and 404 for an unknown library id; no rejected request scans or moves lastScan.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				token := createRestrictedAdmin(t, e)
				before := libraryDocumentOf(t, e, library)
				titles := itemTitles(t, e, library)
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/libraries/" + library + "/scan"}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/libraries/"+library+"/scan", nil), http.StatusForbidden)
				wantStatus(t, "restricted admin", e.Post(t, token, "/api/libraries/"+library+"/scan", nil), http.StatusForbidden)
				wantStatus(t, "unknown library", e.Post(t, e.Token(t, "root"), "/api/libraries/not-a-library/scan", nil), http.StatusNotFound)
				after := libraryDocumentOf(t, e, library)
				if after.LastScan == nil || before.LastScan == nil || *after.LastScan != *before.LastScan {
					t.Fatalf("a rejected scan moved lastScan from %v to %v", before.LastScan, after.LastScan)
				}
				requireStrings(t, "item titles", itemTitles(t, e, library), titles)
			},
		},
	}
}

// defaultPrecedence is Database.libraryModel.defaultMetadataPrecedence.
var defaultPrecedence = []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}
