package scan

import (
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// sourceScenarios covers the file-based metadata sources of the configured
// precedence order (nfo, txt, opf), an unknown source name, the scanner's own
// sidecar write failure and an item whose only media is an ebook.
func sourceScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: precedenceReq, Invariant: precedenceOrder, Case: "normal", State: "initial", Assertion: "precedence-nfo-txt-opf-sources",
			Sources: []string{sourceOrderSrc, "server/scanner/NfoFileScanner.js:13-50", "server/scanner/OpfFileScanner.js:13-45", "server/scanner/BookScanner.js:777-808"},
			Claim:   "With the default precedence each file source contributes in order: the nfo sets series, genres and publisher, desc.txt/reader.txt set description and narrators, and the later opf overwrites title, authors, description, publisher, language and publishedYear while the fields the opf does not supply keep their nfo/txt values.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageFolder(t, e, "sources-book", "audiobooks/Sources Author/Sources Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Sources Book.mp3"))
					writeFile(t, filepath.Join(dir, "book.nfo"), []byte(
						"title: NFO Title\nauthor: NFO Author\nnarrator: NFO Narrator\nseries name: NFO Series\nposition in series: 2\n"+
							"genre: NFO Genre\npublisher: NFO Publisher\nlanguage: en\nbook description\nNFO description.\n"))
					writeFile(t, filepath.Join(dir, "desc.txt"), []byte("Description from desc.txt\n"))
					writeFile(t, filepath.Join(dir, "reader.txt"), []byte("Reader Narrator\nsecond line ignored\n"))
					writeFile(t, filepath.Join(dir, "book.opf"), []byte(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>OPF Title</dc:title>
    <dc:creator opf:role="aut">OPF Author</dc:creator>
    <dc:description>OPF description.</dc:description>
    <dc:publisher>OPF Publisher</dc:publisher>
    <dc:language>fr</dc:language>
    <dc:date>2003</dc:date>
  </metadata>
</package>
`))
				})
				libraryScan(t, e, library, false)
				doc := itemDocument(t, e, itemIDByRelPath(t, e, "Sources Author/Sources Book"))
				meta := doc.Media.Metadata
				if meta.Title != "OPF Title" {
					t.Fatalf("title = %q, want the opf title", meta.Title)
				}
				requireStrings(t, "authors", authorNames(doc), []string{"OPF Author"})
				requireStrings(t, "narrators", meta.Narrators, []string{"Reader Narrator"})
				requireStrings(t, "series", seriesNames(doc), []string{"NFO Series #2"})
				requireStrings(t, "genres", meta.Genres, []string{"NFO Genre"})
				if ptrText(meta.Description) != "OPF description." {
					t.Fatalf("description = %q, want the opf description", ptrText(meta.Description))
				}
				if ptrText(meta.Publisher) != "OPF Publisher" || ptrText(meta.Language) != "fr" || ptrText(meta.PublishedYear) != "2003" {
					t.Fatalf("publisher/language/publishedYear = %q/%q/%q, want the opf values", ptrText(meta.Publisher), ptrText(meta.Language), ptrText(meta.PublishedYear))
				}
			},
		},
		{
			Requirement: precedenceReq, Invariant: precedenceOrder, Case: "error", State: "changed", Assertion: "precedence-unknown-source-is-ignored",
			Sources: []string{sourceOrderSrc, "server/scanner/BookScanner.js:684-692"},
			Claim:   "A metadataPrecedence array naming an unknown source is accepted and persisted, and a scan ignores that name: only the folder-derived title is applied while the stored series and genres stay, and restoring the default order applies the sidecar again.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByRelPath(t, e, "Precedence Author/Precedence Folder")
				before := itemDocument(t, e, id)
				order := []string{"bogusSource"}
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": order})
				scanThenAdvanced(t, e, library, false)
				doc := itemDocument(t, e, id)
				if doc.Media.Metadata.Title != "Precedence Folder" {
					t.Fatalf("title = %q, want the folder title with no metadata source applied", doc.Media.Metadata.Title)
				}
				requireStrings(t, "series", seriesNames(doc), seriesNames(before))
				requireStrings(t, "genres", doc.Media.Metadata.Genres, before.Media.Metadata.Genres)
				requireStrings(t, "persisted order", lastScanPrecedence(t, e, library), order)
				setLibrarySettings(t, e, library, map[string]any{"metadataPrecedence": defaultPrecedence})
				scanThenAdvanced(t, e, library, false)
				if got := itemDocument(t, e, id).Media.Metadata.Title; got != "Absent Fields Title" {
					t.Fatalf("title after restoring the default order = %q, want the sidecar title", got)
				}
			},
		},
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "error", State: "failure", Assertion: "scan-sidecar-write-failure-keeps-file",
			Sources: []string{"server/scanner/BookScanner.js:864-901"},
			Claim:   "When the scanner cannot write metadata.json it still applies and stores the new metadata and the scan answers UPDATED, while the read-only file keeps its previous content.",
			Run: func(t *testing.T, e *harness.Env) {
				saga := e.IDs["saga1"]
				edited := marshalJSON(t, map[string]any{"title": "Read-only Scan Title", "chapters": []map[string]any{}})
				writeFile(t, sidecarPath(e, saga), edited)
				if err := os.Chmod(sidecarPath(e, saga), 0o444); err != nil {
					t.Fatalf("make metadata.json read-only: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(sidecarPath(e, saga), 0o644); err != nil {
						t.Errorf("restore metadata.json permissions: %v", err)
					}
				})
				if got := scanItem(t, e, saga); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				if got := itemDocument(t, e, saga).Media.Metadata.Title; got != "Read-only Scan Title" {
					t.Fatalf("title = %q, want the reread sidecar title", got)
				}
				if string(readFile(t, sidecarPath(e, saga))) != string(edited) {
					t.Fatal("the read-only metadata.json was rewritten")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subPrecedence, Case: "edge", State: "changed", Assertion: "ebook-only-item-scans-with-ebook-metadata",
			Sources: []string{"server/scanner/BookScanner.js:743-770", "server/scanner/BookScanner.js:660-700", "server/scanner/AbsMetadataFileScanner.js:20-51"},
			Claim:   "An item whose only media is an epub imports with the epub as ebookFile and no audio file and takes its title \"Ebook Only\" and language \"en\" from the epub (audioMetatags falls back to the ebook because there are no audio files, overriding the folder title), while the author stays the folder name \"Ebook Author\" because the epub's dc:creator has no opf:role; a rescan keeps those values.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageFolder(t, e, "ebook-only", "audiobooks/Ebook Author/Ebook Only Item", func(dir string) {
					copyFile(t, fixtureAudio(e, "audiobooks/Ada Example/Ebook Only/Ebook Only.epub"), filepath.Join(dir, "Ebook Only Item.epub"))
				})
				libraryScan(t, e, library, false)
				id := itemIDByRelPath(t, e, "Ebook Author/Ebook Only Item")
				before := itemDocument(t, e, id)
				if before.Media.EbookFile == nil || before.Media.EbookFile.EbookFormat != "epub" || len(before.Media.AudioFiles) != 0 || before.IsMissing {
					t.Fatalf("ebook-only item = ebook %+v audio %d missing %v", before.Media.EbookFile, len(before.Media.AudioFiles), before.IsMissing)
				}
				if before.Media.Metadata.Title != "Ebook Only" {
					t.Fatalf("imported title = %q, want the epub title Ebook Only", before.Media.Metadata.Title)
				}
				if ptrText(before.Media.Metadata.Language) != "en" {
					t.Fatalf("imported language = %q, want the epub language en", ptrText(before.Media.Metadata.Language))
				}
				requireStrings(t, "imported authors", authorNames(before), []string{"Ebook Author"})
				result := scanItem(t, e, id)
				if result != "UPDATED" && result != "UPTODATE" {
					t.Fatalf("item scan result = %q, want UPDATED or UPTODATE", result)
				}
				doc := itemDocument(t, e, id)
				if doc.Media.EbookFile == nil || doc.Media.EbookFile.EbookFormat != "epub" {
					t.Fatalf("ebookFile after the scan = %+v, want the epub", doc.Media.EbookFile)
				}
				if doc.Media.Metadata.Title != "Ebook Only" {
					t.Fatalf("title after the rescan = %q, want Ebook Only", doc.Media.Metadata.Title)
				}
				requireStrings(t, "authors after the rescan", authorNames(doc), []string{"Ebook Author"})
				stored := readSidecar[bookMetadata](t, e, id)
				if stored.Title != "Ebook Only" || ptrText(stored.Language) != "en" {
					t.Fatalf("sidecar title/language = %q/%q, want Ebook Only/en", stored.Title, ptrText(stored.Language))
				}
				requireStrings(t, "sidecar authors", stored.Authors, []string{"Ebook Author"})
			},
		},
	}
}
