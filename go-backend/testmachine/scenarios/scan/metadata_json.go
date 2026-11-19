package scan

import (
	"net/http"
	"os"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	metadataReq       = "format-metadata-json"
	metadataInv       = "INV-FORMAT-METADATA-JSON"
	metadataWriterSrc = "server/models/LibraryItem.js:614-711"
	bookWriterSrc     = "server/scanner/BookScanner.js:825-901"
	metadataParseSrc  = "server/utils/generators/abmetadataGenerator.js:60-150"
)

// sidecarKeysOfBook is the key set BookScanner.saveMetadataFile writes.
var sidecarKeysOfBook = []string{
	"abridged", "asin", "authors", "chapters", "description", "explicit", "genres", "isbn",
	"language", "narrators", "publishedDate", "publishedYear", "publisher", "series", "subtitle", "tags", "title",
}

// mediaPatch updates the media metadata of an item through the API.
func mediaPatch(t *testing.T, e *harness.Env, itemID string, payload map[string]any) map[string]any {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", payload)
	wantStatus(t, "patch item media", resp, http.StatusOK)
	return resp.Object(t)
}

// wantSidecarMatchesItem checks the stored sidecar against the item document.
func wantSidecarMatchesItem(t *testing.T, e *harness.Env, itemID string, doc item) {
	t.Helper()
	stored := readSidecar[bookMetadata](t, e, itemID)
	if stored.Title != doc.Media.Metadata.Title {
		t.Fatalf("sidecar title = %q, want %q", stored.Title, doc.Media.Metadata.Title)
	}
	requireStrings(t, "sidecar authors", stored.Authors, authorNames(doc))
	requireStrings(t, "sidecar series", stored.Series, seriesNames(doc))
	requireStrings(t, "sidecar genres", stored.Genres, doc.Media.Metadata.Genres)
	requireStrings(t, "sidecar narrators", stored.Narrators, doc.Media.Metadata.Narrators)
	requireStrings(t, "sidecar tags", stored.Tags, doc.Media.Tags)
	if len(stored.Chapters) != len(doc.Media.Chapters) {
		t.Fatalf("sidecar chapters = %+v, want %+v", stored.Chapters, doc.Media.Chapters)
	}
	for i, chapter := range doc.Media.Chapters {
		if stored.Chapters[i] != chapter {
			t.Fatalf("sidecar chapter %d = %+v, want %+v", i, stored.Chapters[i], chapter)
		}
	}
	if ptrText(stored.Subtitle) != ptrText(doc.Media.Metadata.Subtitle) ||
		ptrText(stored.PublishedYear) != ptrText(doc.Media.Metadata.PublishedYear) ||
		ptrText(stored.PublishedDate) != ptrText(doc.Media.Metadata.PublishedDate) ||
		ptrText(stored.Publisher) != ptrText(doc.Media.Metadata.Publisher) ||
		ptrText(stored.Description) != ptrText(doc.Media.Metadata.Description) ||
		ptrText(stored.ISBN) != ptrText(doc.Media.Metadata.ISBN) ||
		ptrText(stored.ASIN) != ptrText(doc.Media.Metadata.ASIN) ||
		ptrText(stored.Language) != ptrText(doc.Media.Metadata.Language) {
		t.Fatalf("sidecar scalar fields %+v do not match the item metadata %+v", stored, doc.Media.Metadata)
	}
	if stored.Explicit != doc.Media.Metadata.Explicit || stored.Abridged != doc.Media.Metadata.Abridged {
		t.Fatalf("sidecar flags explicit=%v abridged=%v, want %v/%v", stored.Explicit, stored.Abridged, doc.Media.Metadata.Explicit, doc.Media.Metadata.Abridged)
	}
	for _, name := range fileNames(doc) {
		if name == "metadata.json" {
			t.Fatalf("metadata.json must not be a library file of the item: %v", fileNames(doc))
		}
	}
}

// metadataScenarios binds the metadata.json writer and reader.
func metadataScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "normal", State: "initial", Assertion: "metadata-json-written-by-media-update",
			Sources: []string{metadataWriterSrc, "server/controllers/LibraryItemController.js:269-286"},
			Claim:   "A media update writes metadata/items/<id>/metadata.json with exactly the seventeen book keys, the values of the item document and the book key set (no podcast keys); the file is not one of the item's library files.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				result := mediaPatch(t, e, novel, map[string]any{
					"tags": []string{"tag-one", "tag-two"},
					"metadata": map[string]any{
						"title": "Format Metadata Book", "subtitle": "Format Subtitle",
						"authors":       []map[string]any{{"name": "Format Author"}},
						"narrators":     []string{"Narr One", "Narr Two"},
						"series":        []map[string]any{{"name": "Format Series", "sequence": "4"}},
						"genres":        []string{"Format Genre"},
						"publishedYear": "2001", "publishedDate": "2001-02-03", "publisher": "Format Publisher",
						"description": "Plain description", "isbn": "9780000000001", "asin": "B000000001",
						"language": "en", "explicit": true, "abridged": true,
					},
				})
				if result["updated"] != true {
					t.Fatalf("patch response = %v, want updated true", result["updated"])
				}
				doc := itemDocument(t, e, novel)
				keys := sidecarKeys(t, e, novel)
				if !slices.Equal(keys, sidecarKeysOfBook) {
					t.Fatalf("sidecar keys = %v, want %v", keys, sidecarKeysOfBook)
				}
				wantSidecarMatchesItem(t, e, novel, doc)
				if doc.Media.Metadata.Title != "Format Metadata Book" || !doc.Media.Metadata.Explicit || len(doc.Media.Tags) != 2 {
					t.Fatalf("the API document does not carry the update: %+v", doc.Media.Metadata)
				}
			},
		},
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "normal", State: "repeat", Assertion: "metadata-json-repeat-write-is-skipped",
			Sources: []string{metadataWriterSrc, "server/controllers/LibraryItemController.js:269-273"},
			Claim:   "Repeating the same media update answers updated false and leaves metadata.json byte-identical, because no change is written again.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				before := readFile(t, sidecarPath(e, novel))
				result := mediaPatch(t, e, novel, map[string]any{
					"tags": []string{"tag-one", "tag-two"},
					"metadata": map[string]any{
						"title": "Format Metadata Book", "subtitle": "Format Subtitle",
						"authors":       []map[string]any{{"name": "Format Author"}},
						"narrators":     []string{"Narr One", "Narr Two"},
						"series":        []map[string]any{{"name": "Format Series", "sequence": "4"}},
						"genres":        []string{"Format Genre"},
						"publishedYear": "2001", "publishedDate": "2001-02-03", "publisher": "Format Publisher",
						"description": "Plain description", "isbn": "9780000000001", "asin": "B000000001",
						"language": "en", "explicit": true, "abridged": true,
					},
				})
				if value, present := result["updated"]; present {
					t.Fatalf("repeat patch response carries updated = %v, want the key absent", value)
				}
				if string(readFile(t, sidecarPath(e, novel))) != string(before) {
					t.Fatal("the repeat update rewrote metadata.json")
				}
			},
		},
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "edge", State: "changed", Assertion: "metadata-json-round-trips-through-scan",
			Sources: []string{metadataWriterSrc, metadataParseSrc, sidecarReadSrc},
			Claim:   "A metadata.json edited directly is reread by a scan: the series string \"Round Trip Saga #7\" becomes series name and sequence, the edited publishedYear and isbn reach the item, and the writer restores the same values in the file.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				stored := readSidecar[bookMetadata](t, e, novel)
				stored.Series = []string{"Round Trip Saga #7"}
				stored.PublishedYear = ptrOf("1998")
				stored.ISBN = ptrOf("9780000000002")
				writeSidecar(t, e, novel, stored)
				if got := scanItem(t, e, novel); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, novel)
				requireStrings(t, "series", seriesNames(doc), []string{"Round Trip Saga #7"})
				if doc.Media.Metadata.PublishedYear == nil || *doc.Media.Metadata.PublishedYear != "1998" ||
					doc.Media.Metadata.ISBN == nil || *doc.Media.Metadata.ISBN != "9780000000002" {
					t.Fatalf("metadata = %+v, want the edited publishedYear and isbn", doc.Media.Metadata)
				}
				written := readSidecar[bookMetadata](t, e, novel)
				requireStrings(t, "written series", written.Series, []string{"Round Trip Saga #7"})
				if ptrText(written.PublishedYear) != "1998" || ptrText(written.ISBN) != "9780000000002" {
					t.Fatalf("written sidecar = %+v, want the reread values", written)
				}
			},
		},
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "edge", State: "changed", Assertion: "metadata-json-writes-cleared-values",
			Sources: []string{metadataWriterSrc, metadataParseSrc, "server/models/Book.js:370-431"},
			Claim:   "Clearing fields writes explicit null and empty arrays (not missing keys) into metadata.json, and a following scan still rebuilds the values other sources supply, so the file's null and empty values neither break reading nor erase the item.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				mediaPatch(t, e, novel, map[string]any{
					"tags": []string{},
					"metadata": map[string]any{
						"subtitle": nil, "description": nil, "isbn": nil, "asin": nil,
						"publishedYear": nil, "publishedDate": nil, "publisher": nil, "language": nil,
						"genres": []string{}, "narrators": []string{}, "explicit": false, "abridged": false,
					},
				})
				raw := readSidecar[map[string]any](t, e, novel)
				for _, key := range []string{"subtitle", "description", "isbn", "asin", "publishedYear", "publishedDate", "publisher", "language"} {
					value, ok := raw[key]
					if !ok || value != nil {
						t.Fatalf("sidecar %s = %v (present %v), want an explicit null", key, value, ok)
					}
				}
				for _, key := range []string{"genres", "narrators", "tags"} {
					value, ok := raw[key].([]any)
					if !ok || len(value) != 0 {
						t.Fatalf("sidecar %s = %v, want an empty array", key, raw[key])
					}
				}
				if raw["explicit"] != false || raw["abridged"] != false {
					t.Fatalf("sidecar flags = %v/%v, want false", raw["explicit"], raw["abridged"])
				}
				if got := scanItem(t, e, novel); got != "UPDATED" {
					t.Fatalf("item scan result = %q, want UPDATED", got)
				}
				doc := itemDocument(t, e, novel)
				requireStrings(t, "genres", doc.Media.Metadata.Genres, []string{"Fiction"})
				if doc.Media.Metadata.PublishedYear == nil || *doc.Media.Metadata.PublishedYear != "2021" {
					t.Fatalf("publishedYear = %v, want 2021 from the audio tags", doc.Media.Metadata.PublishedYear)
				}
				wantSidecarMatchesItem(t, e, novel, doc)
			},
		},
		{
			Requirement: metadataReq, Invariant: metadataInv, Case: "error", State: "failure", Assertion: "metadata-json-write-failure-keeps-old-file",
			Sources: []string{metadataWriterSrc, "server/scanner/BookScanner.js:895-901"},
			Claim:   "When metadata.json cannot be written the media update still answers updated true and the item keeps the new values, while the file on disk keeps its previous content.",
			Run: func(t *testing.T, e *harness.Env) {
				novel := e.IDs["novel"]
				path := sidecarPath(e, novel)
				before := readFile(t, path)
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatalf("make metadata.json read-only: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(path, 0o644); err != nil {
						t.Errorf("restore metadata.json permissions: %v", err)
					}
				})
				result := mediaPatch(t, e, novel, map[string]any{"metadata": map[string]any{"title": "Unwritable Sidecar Title"}})
				if result["updated"] != true {
					t.Fatalf("patch response = %v, want updated true", result["updated"])
				}
				if got := itemDocument(t, e, novel).Media.Metadata.Title; got != "Unwritable Sidecar Title" {
					t.Fatalf("item title = %q, want the new title", got)
				}
				if string(readFile(t, path)) != string(before) {
					t.Fatal("the read-only metadata.json was modified")
				}
			},
		},
	}
}

// ptrOf returns a pointer to value.
func ptrOf[T any](value T) *T { return &value }
