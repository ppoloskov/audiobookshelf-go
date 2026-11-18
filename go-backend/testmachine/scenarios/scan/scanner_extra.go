package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	subCover   = "INV-SCAN-COVER-001"
	subImport  = "INV-SCAN-IMPORT-001"
	subMissing = "INV-SCAN-MISSING-001"
)

// scannerExtraScenarios covers the remaining scanner paths behind the scan
// routes: podcast import and episode changes, the item cover from library
// files, a book that loses its only media file, and chunked new-item events.
func scannerExtraScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: subReq, Invariant: subImport, Case: "normal", State: "changed", Assertion: "podcast-import-and-episode-changes",
			Sources: []string{"server/scanner/PodcastScanner.js:274-373", "server/scanner/PodcastScanner.js:70-165", "server/scanner/LibraryItemScanner.js:29-76"},
			Claim:   "A new podcast folder imports as one item whose episodes take their titles and dates from the audio tags; a later item scan adds a newly copied episode file and removes an episode whose file was deleted.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["podcastLibrary"]
				stageFolder(t, e, "new-cast", "podcasts/New Cast", func(dir string) {
					copyFile(t, fixtureAudio(e, "podcasts/Fixture Cast/episode-1.mp3"), filepath.Join(dir, "ep-a.mp3"))
					copyFile(t, fixtureAudio(e, "podcasts/Fixture Cast/episode-2.mp3"), filepath.Join(dir, "ep-b.mp3"))
				})
				libraryScan(t, e, library, false)
				id := itemIDByRelPath(t, e, "New Cast")
				doc := itemDocument(t, e, id)
				if doc.Media.Metadata.Title != "Fixture Cast" || len(doc.Media.Episodes) != 2 {
					t.Fatalf("imported podcast = %q with %d episodes, want the album-tag title Fixture Cast with 2", doc.Media.Metadata.Title, len(doc.Media.Episodes))
				}
				requireStrings(t, "imported episode titles", episodeTitles(doc), []string{"Pilot Episode", "Second Outing"})
				for i, want := range []string{"2025-03-01", "2025-03-08"} {
					date := doc.Media.Episodes[i].PublishedAt
					if date == nil || !sameDay(*date, want) {
						t.Fatalf("episode %d publishedAt = %v, want %s", i, date, want)
					}
				}
				copyFile(t, fixtureAudio(e, "podcasts/Fixture Cast/episode-3.mp3"), filepath.Join(e.Media, "podcasts/New Cast/ep-c.mp3"))
				if got := scanItem(t, e, id); got != "UPDATED" {
					t.Fatalf("add-episode scan result = %q, want UPDATED", got)
				}
				doc = itemDocument(t, e, id)
				requireStrings(t, "episode titles after add", episodeTitles(doc), []string{"Pilot Episode", "Second Outing", "Third Time"})
				removed := filepath.Join(e.Install, "removed-ep-a.mp3")
				if err := os.Rename(filepath.Join(e.Media, "podcasts/New Cast/ep-a.mp3"), removed); err != nil {
					t.Fatalf("remove an episode file: %v", err)
				}
				defer func() {
					if err := os.Rename(removed, filepath.Join(e.Media, "podcasts/New Cast/ep-a.mp3")); err != nil {
						t.Fatalf("restore the episode file: %v", err)
					}
					scanItem(t, e, id)
				}()
				if got := scanItem(t, e, id); got != "UPDATED" {
					t.Fatalf("remove-episode scan result = %q, want UPDATED", got)
				}
				doc = itemDocument(t, e, id)
				requireStrings(t, "episode titles after remove", episodeTitles(doc), []string{"Second Outing", "Third Time"})
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId = ? AND title = 'Pilot Episode'", doc.Media.ID) != 0 {
					t.Fatal("the removed episode row is still stored")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subCover, Case: "edge", State: "changed", Assertion: "cover-file-sets-and-clears-cover-path",
			Sources: []string{"server/scanner/BookScanner.js:142-176", "server/scanner/BookScanner.js:407-423"},
			Claim:   "Adding a cover.jpg to an item folder that has no cover sets media.coverPath to that file on the next scan, and deleting the file clears coverPath again.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByRelPath(t, e, "Sidecar Author/Sidecar Book")
				folder := filepath.Join(e.Media, "audiobooks/Sidecar Author/Sidecar Book")
				cover := filepath.Join(folder, "cover.jpg")
				copyFile(t, fixtureAudio(e, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/cover.jpg"), cover)
				libraryScan(t, e, library, false)
				doc := itemDocument(t, e, id)
				if doc.Media.CoverPath == nil || *doc.Media.CoverPath != cover {
					t.Fatalf("coverPath = %v, want %q", doc.Media.CoverPath, cover)
				}
				renamed := filepath.Join(folder, "cover.png")
				if err := os.Rename(cover, renamed); err != nil {
					t.Fatalf("rename the cover: %v", err)
				}
				libraryScan(t, e, library, false)
				if got := itemDocument(t, e, id).Media.CoverPath; got == nil || *got != renamed {
					t.Fatalf("coverPath = %v after the cover was renamed, want %q", got, renamed)
				}
				if err := os.Rename(renamed, filepath.Join(e.Install, "removed-cover.png")); err != nil {
					t.Fatalf("remove the cover: %v", err)
				}
				libraryScan(t, e, library, false)
				if got := itemDocument(t, e, id).Media.CoverPath; got != nil {
					t.Fatalf("coverPath = %q after the cover file was deleted, want null", *got)
				}
				audio := filepath.Join(folder, "Sidecar Book.mp3")
				backup := filepath.Join(e.Install, "sidecar-book-audio.mp3")
				if err := os.Rename(audio, backup); err != nil {
					t.Fatalf("save the audio file: %v", err)
				}
				withEmbeddedCover(t, backup, fixtureAudio(e, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/cover.jpg"), audio)
				defer func() {
					if err := os.Rename(backup, audio); err != nil {
						t.Fatalf("restore the audio file: %v", err)
					}
					scanItem(t, e, id)
				}()
				if got := scanItem(t, e, id); got != "UPDATED" {
					t.Fatalf("embedded-cover rescan result = %q, want UPDATED", got)
				}
				if got := itemDocument(t, e, id).Media.CoverPath; got == nil || !fileExists(*got) {
					t.Fatalf("coverPath = %v after the audio file gained embedded art, want an extracted file", got)
				}
			},
		},
		{
			Requirement: subReq, Invariant: subMissing, Case: "error", State: "failure", Assertion: "book-without-media-is-missing",
			Sources: []string{"server/scanner/BookScanner.js:175-207", "server/scanner/BookScanner.js:390-410", "server/scanner/LibraryScanner.js:169-200"},
			Claim:   "A book that keeps audio but loses its ebook drops ebookFile without becoming missing and regains it when the file returns; a book left with no media file at all is flagged missing by the next scan and recovers when a media file returns.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := e.IDs["ebook"]
				folder := filepath.Join(e.Media, "audiobooks/Ada Example/Ebook Only")
				epub := filepath.Join(folder, "Ebook Only.epub")
				savedEpub := filepath.Join(e.Install, "saved-ebook.epub")
				audio := filepath.Join(folder, "Ebook Only.mp3")
				savedAudio := filepath.Join(e.Install, "saved-ebook.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), audio)
				defer func() {
					if fileExists(savedEpub) {
						if err := os.Rename(savedEpub, epub); err != nil {
							t.Fatalf("restore the ebook: %v", err)
						}
					}
					if fileExists(savedAudio) {
						if err := os.Rename(savedAudio, audio); err != nil {
							t.Fatalf("restore the audio file: %v", err)
						}
					}
					if err := os.Remove(audio); err != nil && !os.IsNotExist(err) {
						t.Fatalf("remove the staged audio file: %v", err)
					}
					scanItem(t, e, id) // settle the ebook-only item again
				}()
				libraryScan(t, e, library, false)
				if doc := itemDocument(t, e, id); doc.IsMissing || doc.Media.EbookFile == nil || len(doc.Media.AudioFiles) != 1 {
					t.Fatalf("book with audio and ebook = missing %v ebook %v audio %d, want both media files", doc.IsMissing, doc.Media.EbookFile, len(doc.Media.AudioFiles))
				}
				if err := os.Rename(epub, savedEpub); err != nil {
					t.Fatalf("remove the ebook: %v", err)
				}
				libraryScan(t, e, library, false)
				if doc := itemDocument(t, e, id); doc.IsMissing || doc.Media.EbookFile != nil || len(doc.Media.AudioFiles) != 1 {
					t.Fatalf("book after ebook removal = missing %v ebook %v, want not missing without ebookFile", doc.IsMissing, doc.Media.EbookFile)
				}
				if err := os.Rename(savedEpub, epub); err != nil {
					t.Fatalf("restore the ebook: %v", err)
				}
				libraryScan(t, e, library, false)
				if doc := itemDocument(t, e, id); doc.Media.EbookFile == nil {
					t.Fatal("the returned ebook file was not set on the book")
				}
				if err := os.Rename(audio, savedAudio); err != nil {
					t.Fatalf("remove the audio file: %v", err)
				}
				if err := os.Rename(epub, savedEpub); err != nil {
					t.Fatalf("remove the ebook: %v", err)
				}
				libraryScan(t, e, library, false)
				if !itemDocument(t, e, id).IsMissing {
					t.Fatal("the book without any media file is not flagged missing")
				}
				if err := os.Rename(savedAudio, audio); err != nil {
					t.Fatalf("restore the audio file: %v", err)
				}
				libraryScan(t, e, library, false)
				if itemDocument(t, e, id).IsMissing {
					t.Fatal("the missing flag was not cleared when a media file returned")
				}
				if doc := itemDocument(t, e, id); doc.Media.EbookFile != nil {
					t.Fatal("the ebook file is set although it is still missing")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subCover, Case: "normal", State: "initial", Assertion: "embedded-cover-art-becomes-item-cover",
			Sources: []string{"server/scanner/BookScanner.js:362-372", "server/managers/CoverManager.js"},
			Claim:   "A book whose audio file carries attached cover art and has no image file in its folder gets that art extracted into the metadata directory and set as the item cover.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageFolder(t, e, "embedded-cover", "audiobooks/Cover Author/Embedded Cover Book", func(dir string) {
					withEmbeddedCover(t,
						fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"),
						fixtureAudio(e, "audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning/cover.jpg"),
						filepath.Join(dir, "Embedded Cover Book.mp3"))
				})
				libraryScan(t, e, library, false)
				id := itemIDByRelPath(t, e, "Cover Author/Embedded Cover Book")
				doc := itemDocument(t, e, id)
				if doc.Media.CoverPath == nil {
					t.Fatal("no cover was extracted from the audio file")
				}
				if !fileExists(*doc.Media.CoverPath) {
					t.Fatalf("coverPath %q does not exist", *doc.Media.CoverPath)
				}
				if !strings.HasPrefix(*doc.Media.CoverPath, filepath.Join(e.Metadata, "items", id)+string(filepath.Separator)) {
					t.Fatalf("coverPath = %q, want a file under metadata/items/%s", *doc.Media.CoverPath, id)
				}
			},
		},
		{
			Requirement: subReq, Invariant: subMissing, Case: "error", State: "failure", Assertion: "book-with-unprobeable-audio-is-missing",
			Sources: []string{"server/scanner/BookScanner.js:175-207", "server/scanner/AudioFileScanner.js:160-180"},
			Claim:   "A book whose remaining audio file cannot be probed and which has no ebook is flagged missing by the scan, and the flag clears when a probeable file returns.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				id := itemIDByRelPath(t, e, "Sidecar Author/Sidecar Book")
				folder := filepath.Join(e.Media, "audiobooks/Sidecar Author/Sidecar Book")
				audio := filepath.Join(folder, "Sidecar Book.mp3")
				saved := filepath.Join(e.Install, "saved-sidecar-book.mp3")
				if err := os.Rename(audio, saved); err != nil {
					t.Fatalf("remove the audio file: %v", err)
				}
				writeFile(t, filepath.Join(folder, "Broken.mp3"), []byte("not an audio stream\n"))
				defer func() {
					if err := os.Remove(filepath.Join(folder, "Broken.mp3")); err != nil {
						t.Fatalf("remove the broken file: %v", err)
					}
					if err := os.Rename(saved, audio); err != nil {
						t.Fatalf("restore the audio file: %v", err)
					}
					if got := scanItem(t, e, id); got != "UPDATED" {
						t.Fatalf("restore scan result = %q, want UPDATED", got)
					}
				}()
				libraryScan(t, e, library, false)
				if !itemDocument(t, e, id).IsMissing {
					t.Fatal("the book whose only audio file failed probing is not flagged missing")
				}
			},
		},
		{
			Requirement: subReq, Invariant: subImport, Case: "normal", State: "changed", Assertion: "book-import-keeps-audio-and-ebook",
			Sources: []string{"server/scanner/BookScanner.js:447-470", "server/scanner/BookScanner.js:530-545"},
			Claim:   "A new book folder holding one audio file and one epub imports as one item with the probed audio file and the epub as its ebookFile with ebookFormat epub.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				stageFolder(t, e, "mixed-book", "audiobooks/Terry Fixture/Long Saga/Book 7 - Mixed Book", func(dir string) {
					stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), filepath.Join(dir, "Mixed Book.mp3"))
					copyFile(t, fixtureAudio(e, "audiobooks/Ada Example/Ebook Only/Ebook Only.epub"), filepath.Join(dir, "Mixed Book.epub"))
				})
				libraryScan(t, e, library, false)
				doc := itemDocument(t, e, itemIDByRelPath(t, e, "Terry Fixture/Long Saga/Book 7 - Mixed Book"))
				if doc.Media.EbookFile == nil || doc.Media.EbookFile.EbookFormat != "epub" {
					t.Fatalf("ebookFile = %+v, want the epub", doc.Media.EbookFile)
				}
				if len(doc.Media.AudioFiles) != 1 {
					t.Fatalf("audioFiles = %d, want the one audio file", len(doc.Media.AudioFiles))
				}
				requireStrings(t, "authors", authorNames(doc), []string{"Terry Fixture"})
				requireStrings(t, "series", seriesNames(doc), []string{"Long Saga #7"})
			},
		},
		{
			Requirement: subReq, Invariant: subEvents, Case: "edge", State: "changed", Assertion: "items-added-events-are-chunked",
			Sources: []string{"server/scanner/LibraryScanner.js:258-281", "server/SocketAuthority.js:112-131"},
			Claim:   "A scan that adds twelve books delivers every new item in items_added events of at most ten items each, and no item is delivered twice.",
			Run: func(t *testing.T, e *harness.Env) {
				library := e.IDs["library"]
				source := filepath.Join(e.Install, "chunk-source.mp3")
				stripCopy(t, fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3"), source)
				const count = 12
				for i := 0; i < count; i++ {
					name := "Chunk Book " + itoaSmall(i)
					stageFolder(t, e, "chunk-"+itoaSmall(i), "audiobooks/Chunk Author/"+name, func(dir string) {
						copyFile(t, source, filepath.Join(dir, name+".mp3"))
					})
				}
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				libraryScan(t, e, library, false)
				seen := map[string]int{}
				events := socket.Events()
				for _, event := range events[mark:] {
					if event.Name != "items_added" {
						continue
					}
					var payload []struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(event.Args[0], &payload); err != nil {
						t.Fatalf("items_added payload: %v", err)
					}
					if len(payload) > 10 {
						t.Fatalf("items_added carried %d items, want at most 10", len(payload))
					}
					for _, entry := range payload {
						seen[entry.ID]++
					}
				}
				added := 0
				for _, title := range itemTitles(t, e, library) {
					if len(title) > len("Chunk Book") && title[:len("Chunk Book")] == "Chunk Book" {
						added++
					}
				}
				if added != count {
					t.Fatalf("added %d chunk books, want %d", added, count)
				}
				if len(seen) != count {
					t.Fatalf("items_added delivered %d distinct items, want %d", len(seen), count)
				}
				for id, times := range seen {
					if times != 1 {
						t.Fatalf("item %s was delivered %d times", id, times)
					}
				}
			},
		},
	}
}

// episodeTitles lists the podcast episode titles of an item sorted.
func episodeTitles(doc item) []string {
	titles := make([]string, 0, len(doc.Media.Episodes))
	for _, episode := range doc.Media.Episodes {
		titles = append(titles, episode.Title)
	}
	slices.Sort(titles)
	return titles
}

// sameDay reports whether the epoch milliseconds fall on the UTC date yyyy-mm-dd.
func sameDay(epochMillis int64, date string) bool {
	return time.UnixMilli(epochMillis).UTC().Format("2006-01-02") == date
}

// itoaSmall renders a non-negative small integer.
func itoaSmall(value int) string { return strconv.Itoa(value) }
