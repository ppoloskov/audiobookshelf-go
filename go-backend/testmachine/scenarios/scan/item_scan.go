package scan

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	itemScanReq      = "route-r062"
	itemScanResponse = "INV-R062-RESPONSE-001"
	itemScanReject   = "INV-R062-SAMPLED"
	itemScanMetadata = "INV-R062-SIDECAR-001"
	itemScanSrc      = "server/controllers/LibraryItemController.js:860-877"
	itemScanAuthSrc  = "server/controllers/LibraryItemController.js:1220-1251"
	itemRescanSrc    = "server/scanner/LibraryItemScanner.js:29-76"
	absSidecarSrc    = "server/scanner/AbsMetadataFileScanner.js:20-51"
)

// itemScenarios binds POST /api/items/:id/scan: the response result values, the
// sidecar reread for books and podcasts, and the deliberate rejections.
func itemScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: itemScanReq, Invariant: itemScanMetadata, Case: "normal", State: "initial", Assertion: "item-scan-applies-edited-sidecar",
			Sources: []string{itemScanSrc, itemRescanSrc, absSidecarSrc, "server/scanner/BookScanner.js:230-262", "server/scanner/BookScanner.js:264-272"},
			Claim:   "Scanning an item rereads its metadata/items/<id>/metadata.json, applies title, authors and series from it, creates the unknown author row and the unknown series row with its sequence, and answers {result:\"UPDATED\"}.",
			Run: func(t *testing.T, e *harness.Env) {
				// The packet changes library files; its own scans must be the only
				// processor of those changes (the fixture ships the watcher on).
				disableWatcher(t, e, e.IDs["library"])
				disableWatcher(t, e, e.IDs["podcastLibrary"])
				novel := e.IDs["novel"]
				writeSidecar(t, e, novel, map[string]any{
					"title":     "Item Scan Novel",
					"authors":   []string{"Ada Example", "Scan Coauthor"},
					"series":    []string{"Scan Fresh Saga #2"},
					"genres":    []string{"Fiction", "Adventure"},
					"tags":      []string{"Scan Tag"},
					"narrators": []string{"Scan Narrator"},
				})
				if got := scanItem(t, e, novel); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, novel)
				if doc.Media.Metadata.Title != "Item Scan Novel" {
					t.Fatalf("title = %q, want the sidecar title", doc.Media.Metadata.Title)
				}
				requireStrings(t, "authors", authorNames(doc), []string{"Ada Example", "Scan Coauthor"})
				requireStrings(t, "series", seriesNames(doc), []string{"Scan Fresh Saga #2"})
				requireStrings(t, "genres", doc.Media.Metadata.Genres, []string{"Fiction", "Adventure"})
				requireStrings(t, "tags", doc.Media.Tags, []string{"Scan Tag"})
				requireStrings(t, "narrators", doc.Media.Metadata.Narrators, []string{"Scan Narrator"})
				if e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Scan Coauthor' AND libraryId = ?", e.IDs["library"]) != 1 {
					t.Fatal("the unknown author row was not created")
				}
				if e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Scan Fresh Saga' AND libraryId = ?", e.IDs["library"]) != 1 {
					t.Fatal("the unknown series row was not created exactly once")
				}
				rows := e.Rows(t, `SELECT bs.sequence AS sequence FROM bookSeries bs JOIN series s ON s.id = bs.seriesId
					JOIN libraryItems li ON li.mediaId = bs.bookId WHERE li.id = ? AND s.name = 'Scan Fresh Saga'`, novel)
				if len(rows) != 1 || rows[0]["sequence"] != "2" {
					t.Fatalf("bookSeries sequence rows = %v, want one row with sequence 2", rows)
				}
				stored := readSidecar[bookMetadata](t, e, novel)
				if stored.Title != "Item Scan Novel" {
					t.Fatalf("rewritten sidecar title = %q, want Item Scan Novel", stored.Title)
				}
				requireStrings(t, "rewritten sidecar authors", stored.Authors, []string{"Ada Example", "Scan Coauthor"})
				requireStrings(t, "rewritten sidecar series", stored.Series, []string{"Scan Fresh Saga #2"})
			},
		},
		{
			Requirement: itemScanReq, Invariant: itemScanResponse, Case: "normal", State: "repeat", Assertion: "item-scan-repeat-is-uptodate",
			Sources: []string{itemScanSrc, itemRescanSrc, "server/scanner/LibraryItemScanner.js:66-75"},
			Claim:   "An immediate second scan of an unchanged item answers {result:\"UPTODATE\"} and leaves the item row and its metadata.json untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				before := itemDocument(t, e, novel)
				sidecar := readFile(t, sidecarPath(e, novel))
				if got := scanItem(t, e, novel); got != "UPTODATE" {
					t.Fatalf("repeat item scan result = %q, want UPTODATE", got)
				}
				after := itemDocument(t, e, novel)
				if after.UpdatedAt != before.UpdatedAt || after.LastScan != before.LastScan {
					t.Fatalf("repeat scan moved updatedAt %d -> %d or lastScan %d -> %d", before.UpdatedAt, after.UpdatedAt, before.LastScan, after.LastScan)
				}
				if string(readFile(t, sidecarPath(e, novel))) != string(sidecar) {
					t.Fatal("repeat scan rewrote metadata.json although nothing changed")
				}
			},
		},
		{
			Requirement: itemScanReq, Invariant: itemScanMetadata, Case: "edge", State: "changed", Assertion: "item-scan-applies-sidecar-chapters",
			Sources: []string{itemScanSrc, absSidecarSrc, "server/scanner/BookScanner.js:352-356", "server/utils/generators/abmetadataGenerator.js:120-150"},
			Claim:   "A scan applies the chapters array of metadata.json verbatim (ids renumbered from 0, start/end untouched) together with the title while the three audio files keep their stored order and durations.",
			Run: func(t *testing.T, e *harness.Env) {
				saga := e.IDs["saga1"]
				chapters := []map[string]any{
					{"start": 0, "end": 9.5, "title": "Sidecar One"},
					{"start": 9.5, "end": 25.25, "title": "Sidecar Two"},
					{"start": 25.25, "end": 60.186123, "title": "Sidecar Three"},
				}
				writeSidecar(t, e, saga, map[string]any{"title": "Chapters Book", "chapters": chapters, "series": []string{"Long Saga #6"}})
				if got := scanItem(t, e, saga); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, saga)
				if doc.Media.Metadata.Title != "Chapters Book" {
					t.Fatalf("title = %q, want Chapters Book", doc.Media.Metadata.Title)
				}
				want := []chapter{{0, 0, 9.5, "Sidecar One"}, {1, 9.5, 25.25, "Sidecar Two"}, {2, 25.25, 60.186123, "Sidecar Three"}}
				if len(doc.Media.Chapters) != len(want) {
					t.Fatalf("chapters = %v, want %v", doc.Media.Chapters, want)
				}
				for i, chapter := range doc.Media.Chapters {
					if chapter != want[i] {
						t.Fatalf("chapter %d = %+v, want %+v", i, chapter, want[i])
					}
				}
				requireStrings(t, "series", seriesNames(doc), []string{"Long Saga #6"})
				if len(doc.Media.AudioFiles) != 3 {
					t.Fatalf("audioFiles = %d, want the three stored files", len(doc.Media.AudioFiles))
				}
				for i, file := range doc.Media.AudioFiles {
					if file.Index != i+1 {
						t.Fatalf("audio file %d index = %d, want %d", i, file.Index, i+1)
					}
					if file.Duration <= 0 || !strings.HasSuffix(file.Metadata.Filename, ".mp3") {
						t.Fatalf("audio file %d = %+v, want a probed mp3", i, file)
					}
				}
				stored := readSidecar[bookMetadata](t, e, saga)
				if len(stored.Chapters) != 3 || stored.Chapters[2].End != 60.186123 || stored.Chapters[0].ID != 0 {
					t.Fatalf("rewritten sidecar chapters = %+v, want the applied chapters with ids 0..2", stored.Chapters)
				}
			},
		},
		{
			Requirement: itemScanReq, Invariant: itemScanMetadata, Case: "normal", State: "changed", Assertion: "item-scan-applies-podcast-sidecar",
			Sources: []string{itemScanSrc, "server/scanner/AbsMetadataFileScanner.js:57-84", "server/scanner/PodcastScanner.js:207-224", "server/scanner/PodcastScanner.js:425-459"},
			Claim:   "Scanning a podcast item rereads metadata/items/<id>/metadata.json, applies the podcast keys including the author \"Sidecar Host\", answers {result:\"UPDATED\"} and rewrites the sidecar with exactly the fifteen podcast keys of the writer.",
			Run: func(t *testing.T, e *harness.Env) {
				podcast := e.IDs["podcast"]
				writeSidecar(t, e, podcast, map[string]any{
					"title":          "Sidecar Cast",
					"author":         "Sidecar Host",
					"description":    "Sidecar description.",
					"releaseDate":    "2024-02-03",
					"genres":         []string{"Technology", "Science"},
					"feedURL":        "http://127.0.0.1/sidecar-feed.xml",
					"imageURL":       "http://127.0.0.1/sidecar-image.jpg",
					"itunesPageURL":  "http://127.0.0.1/podcast",
					"itunesId":       "123456789",
					"itunesArtistId": "987654321",
					"asin":           "B00SIDECAR",
					"language":       "en",
					"explicit":       true,
					"podcastType":    "serial",
					"tags":           []string{"Sidecar Tag"},
				})
				if got := scanItem(t, e, podcast); got != "UPDATED" {
					t.Fatalf("podcast item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, podcast)
				meta := doc.Media.Metadata
				if meta.Title != "Sidecar Cast" {
					t.Fatalf("podcast title = %q, want Sidecar Cast", meta.Title)
				}
				if ptrText(meta.Author) != "Sidecar Host" {
					t.Fatalf("podcast author = %q, want Sidecar Host", ptrText(meta.Author))
				}
				requireStrings(t, "podcast genres", meta.Genres, []string{"Technology", "Science"})
				requireStrings(t, "podcast tags", doc.Media.Tags, []string{"Sidecar Tag"})
				// Podcast.oldMetadataToJSON carries no asin (server/models/Podcast.js:402-418),
				// so the item document proves the other applied keys and the sidecar below
				// proves asin.
				if ptrText(meta.Description) != "Sidecar description." || ptrText(meta.ReleaseDate) != "2024-02-03" ||
					ptrText(meta.FeedURL) != "http://127.0.0.1/sidecar-feed.xml" || ptrText(meta.ImageURL) != "http://127.0.0.1/sidecar-image.jpg" ||
					ptrText(meta.ItunesPageURL) != "http://127.0.0.1/podcast" || ptrText(meta.ItunesID) != "123456789" ||
					ptrText(meta.ItunesArtistID) != "987654321" || ptrText(meta.Language) != "en" {
					t.Fatalf("podcast metadata = %+v, want the applied sidecar values", meta)
				}
				if !meta.Explicit || ptrText(meta.PodcastType) != "serial" {
					t.Fatalf("podcast explicit/type = %v/%q, want true/serial", meta.Explicit, ptrText(meta.PodcastType))
				}
				body := readFile(t, sidecarPath(e, podcast))
				var stored podcastMetadata
				if err := json.Unmarshal(body, &stored); err != nil {
					t.Fatalf("rewritten podcast sidecar: %v", err)
				}
				requireStrings(t, "rewritten podcast keys", sidecarKeys(t, e, podcast), podcastSidecarKeys)
				if stored.Title != "Sidecar Cast" || ptrText(stored.Author) != "Sidecar Host" || ptrText(stored.PodcastType) != "serial" {
					t.Fatalf("rewritten podcast sidecar = %+v, want the applied title, author and podcastType", stored)
				}
				requireStrings(t, "rewritten podcast genres", stored.Genres, []string{"Technology", "Science"})
				requireStrings(t, "rewritten podcast tags", stored.Tags, []string{"Sidecar Tag"})
				if ptrText(stored.Description) != "Sidecar description." || ptrText(stored.ReleaseDate) != "2024-02-03" ||
					ptrText(stored.FeedURL) != "http://127.0.0.1/sidecar-feed.xml" || ptrText(stored.ImageURL) != "http://127.0.0.1/sidecar-image.jpg" ||
					ptrText(stored.ItunesPageURL) != "http://127.0.0.1/podcast" || ptrText(stored.ItunesID) != "123456789" ||
					ptrText(stored.ItunesArtistID) != "987654321" || ptrText(stored.ASIN) != "B00SIDECAR" || ptrText(stored.Language) != "en" {
					t.Fatalf("rewritten podcast sidecar = %+v, want every applied value", stored)
				}
				if !stored.Explicit {
					t.Fatalf("rewritten podcast sidecar explicit = %v, want true", stored.Explicit)
				}
			},
		},
		{
			Requirement: itemScanReq, Invariant: itemScanReject, Case: "error", State: "failure", Assertion: "item-scan-rejections-change-nothing",
			Sources: []string{itemScanAuthSrc, itemScanSrc, "server/controllers/LibraryItemController.js:868-871"},
			Claim:   "An anonymous request answers 401, a user without update permission 403, an unknown item 404 and a file library item 500; none changes the item, its sidecar or its author links.",
			Run: func(t *testing.T, e *harness.Env) {
				fileItem := ensureRootFileItem(t, e)
				novel := e.IDs["novel"]
				before := itemDocument(t, e, novel)
				sidecar := readFile(t, sidecarPath(e, novel))
				authors := e.Count(t, "SELECT COUNT(*) FROM authors")
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/" + novel + "/scan"}), http.StatusUnauthorized)
				wantStatus(t, "listener", e.Post(t, e.Token(t, "listener"), "/api/items/"+novel+"/scan", nil), http.StatusForbidden)
				wantStatus(t, "finisher", e.Post(t, e.Token(t, "finisher"), "/api/items/"+novel+"/scan", nil), http.StatusForbidden)
				wantStatus(t, "unknown item", e.Post(t, e.Token(t, "root"), "/api/items/not-an-item/scan", nil), http.StatusNotFound)
				wantStatus(t, "file item", e.Post(t, e.Token(t, "root"), "/api/items/"+fileItem+"/scan", nil), http.StatusInternalServerError)
				after := itemDocument(t, e, novel)
				if after.UpdatedAt != before.UpdatedAt || after.LastScan != before.LastScan {
					t.Fatalf("rejected scans changed the item: updatedAt %d -> %d, lastScan %d -> %d", before.UpdatedAt, after.UpdatedAt, before.LastScan, after.LastScan)
				}
				if string(readFile(t, sidecarPath(e, novel))) != string(sidecar) {
					t.Fatal("rejected scans rewrote metadata.json")
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM authors"); got != authors {
					t.Fatalf("rejected scans changed the author rows: %d -> %d", authors, got)
				}
				doc := itemDocument(t, e, fileItem)
				if !doc.IsFile {
					t.Fatalf("item %s must stay a file item, got %+v", fileItem, doc)
				}
			},
		},
	}
}

// ensureRootFileItem stages one audio file in the library root, scans it in and
// returns the id of the single-file library item it becomes.
func ensureRootFileItem(t *testing.T, e *harness.Env) string {
	t.Helper()
	const name = "Root File Item.mp3"
	target := filepath.Join(e.Media, "audiobooks", name)
	if _, err := os.Stat(target); err != nil {
		source := fixtureAudio(e, "audiobooks/Zed Disposable/Hard Delete Me/Hard Delete Me.mp3")
		staging := filepath.Join(e.Install, "staging-root", name)
		copyFile(t, source, staging)
		if err := os.Rename(staging, target); err != nil {
			t.Fatalf("stage root file item: %v", err)
		}
		libraryScan(t, e, e.IDs["library"], false)
	}
	rows := e.Rows(t, "SELECT id FROM libraryItems WHERE path = ?", target)
	if len(rows) != 1 {
		t.Fatalf("want one library item for the root file, got %d", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}
