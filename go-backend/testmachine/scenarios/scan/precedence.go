package scan

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	precedenceReq     = "scan-precedence-timeline"
	precedenceOrder   = "INV-SCAN-PRECEDENCE-TIMELINE"
	precedenceAbsent  = "INV-SCAN-PRECEDENCE-ABSENT-001"
	precedenceChapter = "INV-SCAN-CHAPTERS-OFFSETS-001"
	precedenceRemoval = "INV-SCAN-RECONCILE-001"
	precedenceCancel  = "INV-SCAN-CANCEL-001"
	sourceOrderSrc    = "server/scanner/BookScanner.js:660-700"
	sidecarReadSrc    = "server/scanner/AbsMetadataFileScanner.js:20-51"
	sidecarSkipSrc    = "server/scanner/AbsMetadataFileScanner.js:33-48"
	chapterBuildSrc   = "server/scanner/AudioFileScanner.js:495-573"
	scannerMoveSrc    = "server/scanner/LibraryScanner.js:169-200"
	scannerAddSrc     = "server/scanner/LibraryScanner.js:258-281"
)

// precedenceScenarios binds the configured metadata precedence, the multi-file
// chapter timeline, the reconciliation of moves/removals and the cancel path.
func precedenceScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: precedenceReq, Invariant: precedenceOrder, Case: "normal", State: "initial", Assertion: "precedence-default-sidecar-wins",
			Sources: []string{sourceOrderSrc, sidecarReadSrc, "server/scanner/BookScanner.js:702-710"},
			Claim:   "With the default precedence the metadata.json of a new item overrides folder and tag sources for every field it supplies, so title, authors, genres and the numeric publishedYear come from the sidecar while the item folder names supply nothing of them.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageFolder(t, e, "precedence", "audiobooks/Precedence Author/Precedence Folder", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Precedence Folder.mp3"))
					writeFile(t, filepath.Join(dir, "metadata.json"), marshalJSON(t, map[string]any{
						"title":         "Sidecar Precedence",
						"authors":       []string{"Sidecar Author"},
						"genres":        []string{"Sidecar Genre"},
						"publishedYear": 1999,
					}))
				})
				libraryScan(t, e, library, false)
				id := itemIDByTitle(t, e, library, "Sidecar Precedence")
				doc := itemDocument(t, e, id)
				requireStrings(t, "authors", authorNames(doc), []string{"Sidecar Author"})
				requireStrings(t, "genres", doc.Media.Metadata.Genres, []string{"Sidecar Genre"})
				if doc.Media.Metadata.PublishedYear == nil || *doc.Media.Metadata.PublishedYear != "1999" {
					t.Fatalf("publishedYear = %v, want the string 1999", doc.Media.Metadata.PublishedYear)
				}
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Sidecar Author' AND libraryId = ?", library) != 1 {
					t.Fatal("the sidecar author row was not created")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceOrder, Case: "edge", State: "force", Assertion: "precedence-change-forces-rescan",
			Sources: []string{"server/scanner/LibraryScanner.js:62-67", "server/scanner/LibraryScanner.js:94-104", sourceOrderSrc},
			Claim:   "Changing the library's metadataPrecedence to put absMetadata first forces the next scan to re-evaluate items whose files did not change, so folder names win for title and authors while sidecar-only fields keep their values, and the order is persisted in extraData.lastScanMetadataPrecedence.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByTitle(t, e, library, "Sidecar Precedence")
				order := []string{"absMetadata", "folderStructure"}
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": order})
				scanThenAdvanced(t, e, library, false)
				doc := itemDocument(t, e, id)
				if doc.Media.Metadata.Title != "Precedence Folder" {
					t.Fatalf("title = %q, want the folder title Precedence Folder", doc.Media.Metadata.Title)
				}
				requireStrings(t, "authors", authorNames(doc), []string{"Precedence Author"})
				requireStrings(t, "genres", doc.Media.Metadata.Genres, []string{"Sidecar Genre"})
				requireStrings(t, "persisted precedence", lastScanPrecedence(t, e, library), order)
				requireStrings(t, "library precedence", libraryPrecedence(t, e, library), order)
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": defaultPrecedence})
				scanThenAdvanced(t, e, library, false)
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceAbsent, Case: "edge", State: "changed", Assertion: "precedence-absent-and-empty-keep-values",
			Sources: []string{sidecarSkipSrc, "server/utils/generators/abmetadataGenerator.js:85-118", "server/scanner/BookScanner.js:230-262"},
			Claim:   "A sidecar whose null and empty values are skipped applies its non-empty title and leaves the fields only the sidecar supplied (series, genres, tags, chapters, publishedYear) at their stored values, while authors are re-derived from the folder because folderStructure supplies them.",
			Run: func(t *testing.T, e *harness.Env) {
				id := itemIDByRelPath(t, e, "Precedence Author/Precedence Folder")
				inFolder := filepath.Join(e.Media, "audiobooks/Precedence Author/Precedence Folder/metadata.json")
				writeFile(t, inFolder, marshalJSON(t, map[string]any{
					"title":         "Absent Base",
					"authors":       []string{"Base Author"},
					"series":        []string{"Base Series #5"},
					"genres":        []string{"Base Genre"},
					"tags":          []string{"Base Tag"},
					"publishedYear": "1984",
					"chapters":      []map[string]any{{"start": 0, "end": 5, "title": "Base One"}, {"start": 5, "end": 10, "title": "Base Two"}},
				}))
				if got := scanItem(t, e, id); got != "UPDATED" {
					t.Fatalf("base item scan result = %q, want UPDATED", got)
				}
				base := itemDocument(t, e, id)
				requireStrings(t, "base authors", authorNames(base), []string{"Base Author"})
				requireStrings(t, "base series", seriesNames(base), []string{"Base Series #5"})
				requireStrings(t, "base genres", base.Media.Metadata.Genres, []string{"Base Genre"})
				requireStrings(t, "base tags", base.Media.Tags, []string{"Base Tag"})
				writeFile(t, inFolder, marshalJSON(t, map[string]any{
					"title":     "Absent Fields Title",
					"subtitle":  nil,
					"publisher": nil,
					"authors":   []string{},
					"series":    []string{},
					"genres":    []string{},
					"tags":      []string{},
					"chapters":  []map[string]any{},
					"narrators": []string{},
				}))
				if got := scanItem(t, e, id); got != "UPDATED" {
					t.Fatalf("empty-sidecar scan result = %q, want UPDATED", got)
				}
				after := itemDocument(t, e, id)
				if after.Media.Metadata.Title != "Absent Fields Title" {
					t.Fatalf("title = %q, want Absent Fields Title", after.Media.Metadata.Title)
				}
				requireStrings(t, "authors", authorNames(after), []string{"Precedence Author"})
				requireStrings(t, "series", seriesNames(after), []string{"Base Series #5"})
				requireStrings(t, "genres", after.Media.Metadata.Genres, []string{"Base Genre"})
				requireStrings(t, "tags", after.Media.Tags, []string{"Base Tag"})
				if after.Media.Metadata.PublishedYear == nil || *after.Media.Metadata.PublishedYear != "1984" {
					t.Fatalf("publishedYear = %v, want the stored 1984", after.Media.Metadata.PublishedYear)
				}
				if len(after.Media.Chapters) != 2 || after.Media.Chapters[0].Title != "Base One" {
					t.Fatalf("chapters = %+v, want the two stored chapters", after.Media.Chapters)
				}
				if after.Media.Metadata.Subtitle != nil || after.Media.Metadata.Publisher != nil {
					t.Fatalf("null sidecar values overwrote subtitle/publisher: %+v", after.Media.Metadata)
				}
				stored := readSidecar[bookMetadata](t, e, id)
				requireStrings(t, "written sidecar series", stored.Series, []string{"Base Series #5"})
				requireStrings(t, "written sidecar genres", stored.Genres, []string{"Base Genre"})
				requireStrings(t, "written sidecar tags", stored.Tags, []string{"Base Tag"})
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceChapter, Case: "edge", State: "changed", Assertion: "chapters-removal-rebuilds-offsets",
			Sources: []string{chapterBuildSrc, "server/scanner/BookScanner.js:100-150"},
			Claim:   "After one of a three-file book's audio files is deleted, a rescan rebuilds the chapters from the two remaining files: ids 0/1, titles from the title tags and offsets that are the cumulative durations of the files in track order.",
			Run: func(t *testing.T, e *harness.Env) {
				saga := e.IDs["saga1"]
				removed := filepath.Join(e.Media, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/03 - Part 3.mp3")
				saved := filepath.Join(e.Install, "saved-03.mp3")
				if err := os.Rename(removed, saved); err != nil {
					t.Fatalf("remove the third audio file: %v", err)
				}
				defer func() {
					if err := os.Rename(saved, removed); err != nil {
						t.Fatalf("restore the third audio file: %v", err)
					}
					writeSidecar(t, e, saga, map[string]any{"title": "Chapters Book", "chapters": []map[string]any{}})
					if got := scanItem(t, e, saga); got != "UPDATED" {
						t.Fatalf("restore scan result = %q, want UPDATED", got)
					}
				}()
				writeSidecar(t, e, saga, map[string]any{"title": "Chapters Book", "chapters": []map[string]any{}})
				if got := scanItem(t, e, saga); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, saga)
				if len(doc.Media.AudioFiles) != 2 {
					t.Fatalf("audioFiles = %d, want 2", len(doc.Media.AudioFiles))
				}
				if len(doc.Media.Chapters) != 2 {
					t.Fatalf("chapters = %+v, want 2 rebuilt chapters", doc.Media.Chapters)
				}
				start := 0.0
				for i, file := range doc.Media.AudioFiles {
					if file.Index != i+1 || !slices.Contains([]string{"01 - Part 1.mp3", "02 - Part 2.mp3"}, file.Metadata.Filename) {
						t.Fatalf("audio file %d = %+v, want index %d and a remaining fixture file", i, file, i+1)
					}
					chapter := doc.Media.Chapters[i]
					if chapter.ID != i || chapter.Title != "Part "+strconv.Itoa(i+1) {
						t.Fatalf("chapter %d = %+v, want id %d titled Part %d", i, chapter, i, i+1)
					}
					if math.Abs(chapter.Start-start) > 1e-6 || math.Abs(chapter.End-(start+file.Duration)) > 1e-6 {
						t.Fatalf("chapter %d = %+v, want the offset %.6f to %.6f of its audio file", i, chapter, start, start+file.Duration)
					}
					start += file.Duration
				}
				if e.Count(t, `SELECT COUNT(*) FROM libraryItems li, json_each(li.libraryFiles) lf
					WHERE li.id = ? AND json_extract(lf.value, '$.metadata.filename') = '03 - Part 3.mp3'`, saga) != 0 {
					t.Fatal("the deleted audio file is still a library file of the item")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceChapter, Case: "normal", State: "repeat", Assertion: "chapters-repeat-scan-is-unchanged",
			Sources: []string{chapterBuildSrc, "server/scanner/LibraryItemScanner.js:63-75"},
			Claim:   "A repeat scan of the restored three-file book answers UPTODATE and leaves its three audio files, three chapters and metadata.json unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				saga := e.IDs["saga1"]
				before := itemDocument(t, e, saga)
				sidecar := readFile(t, sidecarPath(e, saga))
				if got := scanItem(t, e, saga); got != "UPTODATE" {
					t.Fatalf("repeat scan result = %q, want UPTODATE", got)
				}
				after := itemDocument(t, e, saga)
				if len(after.Media.AudioFiles) != 3 || len(after.Media.Chapters) != 3 {
					t.Fatalf("audioFiles/chapters = %d/%d, want 3/3", len(after.Media.AudioFiles), len(after.Media.Chapters))
				}
				for i, chapter := range after.Media.Chapters {
					if chapter != before.Media.Chapters[i] {
						t.Fatalf("chapter %d = %+v, want %+v", i, chapter, before.Media.Chapters[i])
					}
				}
				if string(readFile(t, sidecarPath(e, saga))) != string(sidecar) {
					t.Fatal("the repeat scan rewrote metadata.json")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceRemoval, Case: "edge", State: "changed", Assertion: "scan-move-reconciles-authors-and-series",
			Sources: []string{scannerMoveSrc, "server/scanner/BookScanner.js:221-290", "server/scanner/LibraryItemScanner.js:84-92"},
			Claim:   "Moving a book folder to another author and series keeps the same library item and book id, relinks the book to the new author and series with sequence 2, and deletes the author and series rows left without books.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				source := filepath.Join(e.Media, "audiobooks/Reconcile Author/Reconcile Series/Book 1 - Reconcile Title")
				stageFolder(t, e, "reconcile", "audiobooks/Reconcile Author/Reconcile Series/Book 1 - Reconcile Title", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Book 1.mp3"))
				})
				libraryScan(t, e, library, false)
				itemID := itemIDByTitle(t, e, library, "Reconcile Title")
				before := itemDocument(t, e, itemID)
				requireStrings(t, "authors before the move", authorNames(before), []string{"Reconcile Author"})
				requireStrings(t, "series before the move", seriesNames(before), []string{"Reconcile Series #1"})
				if err := os.Remove(sidecarPath(e, itemID)); err != nil {
					t.Fatalf("remove the imported sidecar: %v", err)
				}
				target := filepath.Join(e.Media, "audiobooks/New Reconcile Author/New Reconcile Series/Book 2 - Reconcile Title")
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(source, target); err != nil {
					t.Fatalf("move the book folder: %v", err)
				}
				libraryScan(t, e, library, false)
				after := itemDocument(t, e, itemID)
				if after.ID != itemID || after.Media.ID != before.Media.ID {
					t.Fatalf("identity changed: item %s -> %s, media %s -> %s", itemID, after.ID, before.Media.ID, after.Media.ID)
				}
				if !filepath.IsAbs(after.Path) || after.Path != target {
					t.Fatalf("item path = %q, want %q", after.Path, target)
				}
				requireStrings(t, "authors after the move", authorNames(after), []string{"New Reconcile Author"})
				requireStrings(t, "series after the move", seriesNames(after), []string{"New Reconcile Series #2"})
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Reconcile Author' AND libraryId = ?", library) != 0 {
					t.Fatal("the author left without books was not deleted")
				}
				if e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Reconcile Series' AND libraryId = ?", library) != 0 {
					t.Fatal("the series left without books was not deleted")
				}
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'New Reconcile Author' AND libraryId = ?", library) != 1 {
					t.Fatal("the new author row was not created")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceRemoval, Case: "edge", State: "changed", Assertion: "scan-inode-collision-keeps-identity",
			Sources: []string{"server/scanner/LibraryScanner.js:171-200", "server/scanner/LibraryScanner.js:641-647"},
			Claim:   "Moving a single-file item's audio file into a folder keeps the item's identity through the inode fallback: the same item and book id, now a folder item with the moved file as its only audio file.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				fileItem := ensureRootFileItem(t, e)
				before := itemDocument(t, e, fileItem)
				if !before.IsFile {
					t.Fatalf("item %s must be a file item before the move", fileItem)
				}
				from := filepath.Join(e.Media, "audiobooks", "Root File Item.mp3")
				target := filepath.Join(e.Media, "audiobooks/Collision Host/Moved Root.mp3")
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(from, target); err != nil {
					t.Fatalf("move the root file: %v", err)
				}
				libraryScan(t, e, library, false)
				after := itemDocument(t, e, fileItem)
				if after.ID != fileItem || after.Media.ID != before.Media.ID {
					t.Fatalf("identity changed: item %s, media %s -> %s", fileItem, before.Media.ID, after.Media.ID)
				}
				if after.IsFile || after.RelPath != "Collision Host" || after.Path != filepath.Join(e.Media, "audiobooks/Collision Host") {
					t.Fatalf("moved item = isFile %v relPath %q path %q, want a folder item at Collision Host", after.IsFile, after.RelPath, after.Path)
				}
				if len(after.Media.AudioFiles) != 1 || after.Media.AudioFiles[0].Metadata.Filename != "Moved Root.mp3" {
					t.Fatalf("audioFiles = %+v, want the moved file only", after.Media.AudioFiles)
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE path = ?", from) != 0 {
					t.Fatal("a second item was created for the moved audio file")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceRemoval, Case: "error", State: "changed", Assertion: "scan-invalid-sidecar-is-ignored",
			Sources: []string{sidecarReadSrc, "server/utils/generators/abmetadataGenerator.js:50-58"},
			Claim:   "A metadata.json that is not valid JSON is ignored by the scan: the item answers UPTODATE, keeps its stored metadata and the file is left as it is.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				scanItem(t, e, novel) // settle whatever earlier scenarios left behind
				before := itemDocument(t, e, novel)
				broken := []byte("{ this is not json")
				writeFile(t, sidecarPath(e, novel), broken)
				if got := scanItem(t, e, novel); got != "UPTODATE" {
					t.Fatalf("item scan result = %q, want UPTODATE", got)
				}
				after := itemDocument(t, e, novel)
				if after.Media.Metadata.Title != before.Media.Metadata.Title {
					t.Fatalf("title = %q, want the stored %q", after.Media.Metadata.Title, before.Media.Metadata.Title)
				}
				requireStrings(t, "authors", authorNames(after), authorNames(before))
				if string(readFile(t, sidecarPath(e, novel))) != string(broken) {
					t.Fatal("the invalid metadata.json was rewritten")
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceRemoval, Case: "error", State: "failure", Assertion: "scan-partial-probe-failure-imports-valid-files",
			Sources: []string{"server/scanner/AudioFileScanner.js:160-180", "server/scanner/AudioFileScanner.js:190-200", scannerAddSrc},
			Claim:   "A new item folder whose second audio file cannot be probed still imports with exactly one audio file; the scan completes, records the folder as one item and stores the unusable file without an audio entry.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				before := libraryItems(t, e, library).Total
				stageFolder(t, e, "partial", "audiobooks/Partial Author/Partial Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "01 - Good.mp3"))
					writeFile(t, filepath.Join(dir, "02 - Broken.mp3"), []byte("not an audio stream\n"))
				})
				scanThenAdvanced(t, e, library, false)
				if got := libraryItems(t, e, library).Total; got != before+1 {
					t.Fatalf("item total = %d, want %d", got, before+1)
				}
				id := itemIDByTitle(t, e, library, "Partial Book")
				doc := itemDocument(t, e, id)
				if len(doc.Media.AudioFiles) != 1 || doc.Media.AudioFiles[0].Metadata.Filename != "01 - Good.mp3" || doc.Media.AudioFiles[0].Duration <= 0 {
					t.Fatalf("audioFiles = %+v, want only the probed file", doc.Media.AudioFiles)
				}
				names := fileNames(doc)
				if !slices.Contains(names, "02 - Broken.mp3") {
					t.Fatalf("libraryFiles = %v, want the unusable file stored as a library file", names)
				}
			},
		},
	}
}

// fileNames lists the library file names of an item.
func fileNames(doc item) []string {
	names := make([]string, 0, len(doc.LibraryFiles))
	for _, file := range doc.LibraryFiles {
		names = append(names, file.Metadata.Filename)
	}
	return names
}
