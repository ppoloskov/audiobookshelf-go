package author

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// stageBook creates media/audiobooks/<authorDir>/<title>/<title>.mp3 in one rename, so that the
// watcher and the scan never see a half-written folder. The audio is a fixture book with its
// tags stripped, so author and title come from the folder structure alone.
func stageBook(t *testing.T, e *harness.Env, authorDir, title string) {
	t.Helper()
	stageBookIn(t, e, filepath.Join(e.Media, "audiobooks"), authorDir, title)
}

// stageBookIn is stageBook for any library folder.
func stageBookIn(t *testing.T, e *harness.Env, folder, authorDir, title string) {
	t.Helper()
	staging := filepath.Join(e.Install, "staging", authorDir, title)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3")
	command := exec.Command(os.Getenv("FFMPEG_PATH"), "-v", "error", "-y", "-i", source, "-map", "0:a", "-map_metadata", "-1", "-write_id3v2", "0", "-c", "copy", filepath.Join(staging, title+".mp3"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("strip tags: %v: %s", err, output)
	}
	target := filepath.Join(folder, authorDir)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(target, title)); err != nil {
		t.Fatal(err)
	}
}

// scanLibrary starts a scan of the audiobook library as root.
func scanLibrary(t *testing.T, e *harness.Env) {
	t.Helper()
	wantStatus(t, "scan", e.Post(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"/scan", nil), http.StatusOK)
}

// numBooksPayload is the authors_num_books_updated payload.
type numBooksPayload struct {
	LibraryID string `json:"libraryId"`
	Authors   []struct {
		ID       string `json:"id"`
		NumBooks int    `json:"numBooks"`
	} `json:"authors"`
}

// numBooksMap decodes the payload to author id -> numBooks.
func numBooksMap(t *testing.T, raw json.RawMessage) map[string]int {
	t.Helper()
	payload := decode[numBooksPayload](t, raw)
	counts := map[string]int{}
	for _, au := range payload.Authors {
		counts[au.ID] = au.NumBooks
	}
	if len(counts) != len(payload.Authors) {
		t.Fatalf("duplicate author in payload: %s", raw)
	}
	return counts
}

// numBooksIncludes matches an authors_num_books_updated event that lists the author.
func numBooksIncludes(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var payload numBooksPayload
		if len(args) == 0 || json.Unmarshal(args[0], &payload) != nil {
			return false
		}
		return slices.ContainsFunc(payload.Authors, func(au struct {
			ID       string `json:"id"`
			NumBooks int    `json:"numBooks"`
		}) bool {
			return au.ID == id
		})
	}
}

func scanEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "normal", State: "initial", Assertion: "author-added-by-scan",
			Sources: []string{"server/scanner/BookScanner.js:604", "server/SocketAuthority.js:57-65"},
			Claim:   "Scanning a new book whose folder names an unknown author creates the author and sends author_added with the author JSON (no numBooks) to every connected client.",
			Run: func(t *testing.T, e *harness.Env) {
				w := watch(t, e)
				stageBook(t, e, "Scan Author", "Scan Book")
				scanLibrary(t, e)
				raw := w.waitBoth(t, "author_added", func(args []json.RawMessage) bool { return first(args)["name"] == "Scan Author" })
				got := authorEvent(t, raw, -1)
				row := mustRow(t, e, got.ID)
				if got.LibraryID != e.IDs["library"] || got.ASIN != nil || got.Description != nil || got.ImagePath != nil || got.AddedAt != dbTime(t, row["createdAt"]) || got.UpdatedAt != dbTime(t, row["updatedAt"]) || bookCount(t, e, got.ID) != 1 {
					t.Fatalf("event %+v does not match row %v", got, row)
				}
			},
		},
		{
			Requirement: "socket-outgoing-authors-num-books-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHORS-NUM-BOOKS-UPDATED", Case: "normal", State: "initial", Assertion: "num-books-after-scan-adds-books",
			Sources: []string{"server/scanner/BookScanner.js:225-232,484-494,905-930", "server/scanner/LibraryItemScanner.js:84-86", "server/scanner/LibraryScanner.js:284"},
			Claim:   "A scan that adds books to existing authors sends one authors_num_books_updated {libraryId, authors:[{id,numBooks}]} to every client with the new book count of each such author and no author_added for them.",
			Run: func(t *testing.T, e *harness.Env) {
				ada, scanAuthor := authorID(t, e, "Ada Example"), authorID(t, e, "Scan Author")
				w := watch(t, e)
				stageBook(t, e, "Ada Example", "Scan Two")
				stageBook(t, e, "Scan Author", "Scan Three")
				scanLibrary(t, e)
				raw := w.waitBoth(t, "authors_num_books_updated", numBooksIncludes(ada))
				counts := numBooksMap(t, raw)
				if decode[numBooksPayload](t, raw).LibraryID != e.IDs["library"] || len(counts) != 2 || counts[ada] != 3 || counts[scanAuthor] != 2 {
					t.Fatalf("payload = %s, want Ada 3 and Scan Author 2", raw)
				}
				if bookCount(t, e, ada) != 3 || bookCount(t, e, scanAuthor) != 2 {
					t.Fatal("the rows must agree with the event")
				}
				w.root.None(t, w.rootMark, "author_added", absentWindow, func(args []json.RawMessage) bool { return first(args)["id"] == ada || first(args)["id"] == scanAuthor })
			},
		},
		{
			Requirement: "socket-outgoing-authors-num-books-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHORS-NUM-BOOKS-UPDATED", Case: "edge", State: "changed", Assertion: "num-books-omits-new-authors",
			Sources: []string{"server/scanner/BookScanner.js:225-262,484-494,905-930"},
			Claim:   "A scan that only adds a book of a brand-new author sends author_added and no authors_num_books_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				w := watch(t, e)
				stageBook(t, e, "Scan New", "Scan Four")
				scanLibrary(t, e)
				w.waitBoth(t, "author_added", func(args []json.RawMessage) bool { return first(args)["name"] == "Scan New" })
				w.waitBoth(t, "items_added", nil)
				w.none(t, "authors_num_books_updated", nil)
			},
		},
		{
			Requirement: "socket-outgoing-authors-num-books-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHORS-NUM-BOOKS-UPDATED", Case: "normal", State: "repeat", Assertion: "num-books-after-second-scan",
			Sources: []string{"server/scanner/BookScanner.js:484-494,905-930"},
			Claim:   "Each later scan that adds another book of the same author sends a new authors_num_books_updated with the increased count, listing only the changed authors.",
			Run: func(t *testing.T, e *harness.Env) {
				ada := authorID(t, e, "Ada Example")
				w := watch(t, e)
				stageBook(t, e, "Ada Example", "Scan Five")
				scanLibrary(t, e)
				counts := numBooksMap(t, w.waitBoth(t, "authors_num_books_updated", numBooksIncludes(ada)))
				if len(counts) != 1 || counts[ada] != 4 || bookCount(t, e, ada) != 4 {
					t.Fatalf("counts = %v, want only Ada with 4", counts)
				}
			},
		},
		{
			Requirement: "socket-outgoing-author-added", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-ADDED", Case: "normal", State: "changed", Assertion: "author-added-by-scan-metadata-update",
			Sources: []string{"server/scanner/BookScanner.js:221-247"},
			Claim:   "A rescan whose metadata.json adds a new author name to an existing book creates that author, links it to the book and sends author_added with the author JSON (no numBooks) to every client, without sending author_added again for the author already on the book.",
			Run: func(t *testing.T, e *harness.Env) {
				first := watch(t, e)
				stageBook(t, e, "Meta Author", "Meta Book")
				scanLibrary(t, e)
				first.waitBoth(t, "author_added", nameIs("Meta Author"))
				first.waitBoth(t, "items_added", nil)
				metadataFile := filepath.Join(e.Media, "audiobooks", "Meta Author", "Meta Book", "metadata.json")
				if err := os.WriteFile(metadataFile, []byte(`{"title":"Meta Book","authors":["Meta Author","Late Author"]}`), 0o644); err != nil {
					t.Fatal(err)
				}
				metaID := authorID(t, e, "Meta Author")
				w := watch(t, e)
				scanLibrary(t, e)
				got := authorEvent(t, w.waitBoth(t, "author_added", nameIs("Late Author")), -1)
				row := mustRow(t, e, got.ID)
				if got.LibraryID != e.IDs["library"] || got.AddedAt != dbTime(t, row["createdAt"]) || got.UpdatedAt != dbTime(t, row["updatedAt"]) ||
					rowText(row, "lastFirst") != "Author, Late" || bookCount(t, e, got.ID) != 1 || bookCount(t, e, metaID) != 1 {
					t.Fatalf("event %+v does not match row %v", got, row)
				}
				w.none(t, "author_added", idIs(metaID))
			},
		},
	}
}

// unlinkByScan stages a book of a new author, scans it in, then gives the book a metadata.json naming
// Ada Example and scans again; it returns the watchers marked just before the second scan.
func unlinkByScan(t *testing.T, e *harness.Env, authorName string) (w *watchers, authorID string) {
	t.Helper()
	stage := watch(t, e)
	stageBook(t, e, authorName, "Moving Book")
	scanLibrary(t, e)
	authorID, _ = first([]json.RawMessage{stage.waitBoth(t, "author_added", nameIs(authorName))})["id"].(string)
	if authorID == "" {
		t.Fatal("author_added carried no id")
	}
	stage.waitBoth(t, "items_added", nil)
	file := filepath.Join(e.Media, "audiobooks", authorName, "Moving Book", "metadata.json")
	if err := os.WriteFile(file, []byte(`{"title":"Moving Book","authors":["Ada Example"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	w = watch(t, e)
	scanLibrary(t, e)
	return w, authorID
}

func scanUnlinkScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-author-removed", Invariant: "INV-SOCKET-OUTGOING-AUTHOR-REMOVED", Case: "normal", State: "repeat", Assertion: "author-removed-by-rescan",
			Sources: []string{"server/scanner/BookScanner.js:240-262,935-975", "server/scanner/LibraryItemScanner.js:84-92"},
			Claim:   "A rescan that moves a book from its only author to another author deletes the now bookless author and sends author_removed {id, libraryId} to every client.",
			Run: func(t *testing.T, e *harness.Env) {
				w, removedID := unlinkByScan(t, e, "Scan Gone")
				raw := w.waitBoth(t, "author_removed", idIs(removedID))
				got := decode[removedPayload](t, raw)
				if got.LibraryID != e.IDs["library"] || e.Count(t, "SELECT COUNT(*) FROM authors WHERE id = ?", removedID) != 0 || e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Scan Gone'") != 0 {
					t.Fatalf("payload %+v: the author row must be gone", got)
				}
			},
		},
		{
			Requirement: "socket-outgoing-authors-num-books-updated", Invariant: "INV-SOCKET-OUTGOING-AUTHORS-NUM-BOOKS-UPDATED", Case: "edge", State: "changed", Assertion: "num-books-omits-authors-without-books",
			Sources: []string{"server/scanner/BookScanner.js:905-930,432-433"},
			Claim:   "When a rescan unlinks a book from its only author and links it to another, authors_num_books_updated lists the receiving author with its new count and omits the author left without books.",
			Run: func(t *testing.T, e *harness.Env) {
				ada := authorID(t, e, "Ada Example")
				w, _ := unlinkByScan(t, e, "Scan Gone Two")
				counts := numBooksMap(t, w.waitBoth(t, "authors_num_books_updated", numBooksIncludes(ada)))
				if len(counts) != 1 || counts[ada] != bookCount(t, e, ada) {
					t.Fatalf("counts = %v, want only Ada with %d", counts, bookCount(t, e, ada))
				}
			},
		},
	}
}
