package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const r053 = "route-r053"

// bookFixture pins the literal fixture facts of the single-file novel.
func pinNovel(t *testing.T, e *harness.Env) {
	t.Helper()
	id := e.IDs["novel"]
	row := mediaRow(t, e, id)
	if row["title"] != "Single File Novel" || row["id"] != "85ab0f32-b17f-4c9a-883e-a61e260f1e46" {
		t.Fatalf("fixture novel changed: %v", row["title"])
	}
	requireEqual(t, "fixture genres", column(t, row, "genres"), []any{"Fiction"})
}

func mediaBookScenarios(s *setup) []harness.Scenario {
	var firstStamp = map[string]any{}
	return []harness.Scenario{
		scenario(r053, "INV-R053-BOOK-META-001", "normal", "initial", "book-all-metadata-fields",
			"PATCH media with every metadata field of the web edit form answers updated:true with the old-format item, stores each value in the books row, derives titleIgnorePrefix, moves updatedAt forward and rewrites metadata/items/<id>/metadata.json.",
			srcs(updateMediaSrc, bookUpdateSrc, metadataFileSrc, "client/components/widgets/BookDetailsEdit.vue:240-270"),
			func(t *testing.T, e *harness.Env) {
				s.token(t, e, "updater")
				id := e.IDs["novel"]
				pinNovel(t, e)
				before := updatedAt(t, e, id)
				firstStamp["before"] = before
				body := map[string]any{
					"metadata": map[string]any{
						"title": "The Edited Novel", "subtitle": "An Edited Subtitle", "description": "<p>Edited description</p>",
						"narrators": []string{"Nora One", "Nora Two"}, "genres": []string{"Fantasy", "Drama"},
						"publishedYear": "2019", "publisher": "Edit Press", "language": "German", "isbn": "9780000000001", "asin": "B000EDITED",
					},
					"tags": []string{"tag-one", "tag-two"},
				}
				result := patchOK(t, e, id, body)
				if !result.Updated {
					t.Fatal("updated = false")
				}
				want := map[string]any{
					"title": "The Edited Novel", "subtitle": "An Edited Subtitle", "description": "<p>Edited description</p>",
					"publishedYear": "2019", "publisher": "Edit Press", "language": "German", "isbn": "9780000000001", "asin": "B000EDITED",
				}
				metadata := metadataOf(result.LibraryItem)
				for key, value := range want {
					if metadata[key] != value {
						t.Fatalf("response metadata.%s = %v, want %v", key, metadata[key], value)
					}
				}
				requireEqual(t, "response narrators", stringList(metadata["narrators"]), []string{"Nora One", "Nora Two"})
				requireEqual(t, "response genres", stringList(metadata["genres"]), []string{"Fantasy", "Drama"})
				requireEqual(t, "response tags", stringList(harness.Dig(result.LibraryItem, "media", "tags")), []string{"tag-one", "tag-two"})
				if result.LibraryItem["id"] != id || result.LibraryItem["mediaType"] != "book" {
					t.Fatalf("response item id/mediaType = %v/%v", result.LibraryItem["id"], result.LibraryItem["mediaType"])
				}
				row := mediaRow(t, e, id)
				for key, value := range want {
					if row[key] != value {
						t.Fatalf("books.%s = %v, want %v", key, row[key], value)
					}
				}
				if row["titleIgnorePrefix"] != "Edited Novel" {
					t.Fatalf("books.titleIgnorePrefix = %v", row["titleIgnorePrefix"])
				}
				requireEqual(t, "books.narrators", column(t, row, "narrators"), []any{"Nora One", "Nora Two"})
				requireEqual(t, "books.genres", column(t, row, "genres"), []any{"Fantasy", "Drama"})
				requireEqual(t, "books.tags", column(t, row, "tags"), []any{"tag-one", "tag-two"})
				item := itemRow(t, e, id)
				if item["title"] != "The Edited Novel" || item["titleIgnorePrefix"] != "Edited Novel" {
					t.Fatalf("libraryItems title/titleIgnorePrefix = %v/%v", item["title"], item["titleIgnorePrefix"])
				}
				wantMoved(t, e, id, before)
				file := readMetadataFile(t, e, id)
				requireEqual(t, "metadata.json", file, map[string]any{
					"tags": []any{"tag-one", "tag-two"}, "title": "The Edited Novel", "subtitle": "An Edited Subtitle",
					"authors": []any{"Ada Example"}, "narrators": []any{"Nora One", "Nora Two"}, "series": []any{},
					"genres": []any{"Fantasy", "Drama"}, "publishedYear": "2019", "publishedDate": nil, "publisher": "Edit Press",
					"description": "<p>Edited description</p>", "isbn": "9780000000001", "asin": "B000EDITED", "language": "German",
					"explicit": false, "abridged": false, "chapters": file["chapters"],
				})
				requireEqual(t, "metadata.json chapters", len(file["chapters"].([]any)), 3)
				requireEqual(t, "GET item after PATCH title", metadataOf(itemDocument(t, e, id))["title"], "The Edited Novel")
			}),
		scenario(r053, "INV-R053-BOOK-META-001", "normal", "changed", "book-explicit-abridged",
			"PATCH media with explicit and abridged true stores 1 in both columns and writes both flags to metadata.json.",
			srcs(bookUpdateSrc, metadataFileSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, map[string]any{"metadata": map[string]any{"explicit": true, "abridged": true}})
				if !result.Updated || metadataOf(result.LibraryItem)["explicit"] != true || metadataOf(result.LibraryItem)["abridged"] != true {
					t.Fatalf("result %+v", result)
				}
				row := mediaRow(t, e, id)
				if row["explicit"] != int64(1) || row["abridged"] != int64(1) {
					t.Fatalf("books explicit/abridged = %v/%v", row["explicit"], row["abridged"])
				}
				file := readMetadataFile(t, e, id)
				if file["explicit"] != true || file["abridged"] != true {
					t.Fatalf("metadata.json explicit/abridged = %v/%v", file["explicit"], file["abridged"])
				}
				wantMoved(t, e, id, before)
				// restore so that the item stays accessible to every account in later scenarios
				patchOK(t, e, id, map[string]any{"metadata": map[string]any{"explicit": false, "abridged": false}})
				row = mediaRow(t, e, id)
				if row["explicit"] != int64(0) || row["abridged"] != int64(0) {
					t.Fatalf("books explicit/abridged after reset = %v/%v", row["explicit"], row["abridged"])
				}
			}),
		scenario(r053, "INV-R053-NOOP-001", "normal", "repeat", "book-repeat-is-noop",
			"Repeating a payload whose values equal the stored ones answers updated:false, leaves updatedAt, rows and metadata.json untouched and emits no item_updated.",
			srcs(updateMediaSrc, bookUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows, file := updatedAt(t, e, id), snapshot(t, e), readMetadataFile(t, e, id)
				body := map[string]any{
					"metadata": map[string]any{"title": "The Edited Novel", "subtitle": "An Edited Subtitle", "genres": []string{"Fantasy", "Drama"}, "explicit": false},
					"tags":     []string{"tag-one", "tag-two"},
				}
				result := patchOK(t, e, id, body)
				if result.Updated {
					t.Fatal("updated = true for equal values")
				}
				if metadataOf(result.LibraryItem)["title"] != "The Edited Novel" {
					t.Fatalf("no-op response title = %v", metadataOf(result.LibraryItem)["title"])
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				requireEqual(t, "metadata.json", readMetadataFile(t, e, id), file)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
			}),
		scenario(r053, "INV-R053-NOOP-001", "edge", "initial", "book-absent-keys-untouched",
			"A payload with only tags changes only the tags: title, narrators and genres stay and absent metadata keys are not cleared.",
			srcs(bookUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				result := patchOK(t, e, id, map[string]any{"tags": []string{"only-tag"}})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				row := mediaRow(t, e, id)
				requireEqual(t, "tags", column(t, row, "tags"), []any{"only-tag"})
				requireEqual(t, "genres", column(t, row, "genres"), []any{"Fantasy", "Drama"})
				if row["title"] != "The Edited Novel" || row["subtitle"] != "An Edited Subtitle" || row["publisher"] != "Edit Press" {
					t.Fatalf("absent keys changed: %v %v %v", row["title"], row["subtitle"], row["publisher"])
				}
				resp := patchMedia(t, e, e.Token(t, "root"), id, map[string]any{"metadata": map[string]any{}})
				wantStatus(t, "empty metadata", resp, http.StatusOK)
				if updated, _ := resp.Object(t)["updated"].(bool); updated { // upstream omits the key when nothing changed (`false || undefined`)
					t.Fatal("empty metadata object reported an update")
				}
				requireEqual(t, "tags cleared", column(t, mediaRow(t, e, id), "tags"), []any{"only-tag"})
				patchOK(t, e, id, map[string]any{"tags": []string{}})
				requireEqual(t, "tags emptied", column(t, mediaRow(t, e, id), "tags"), []any{})
			}),
		scenario(r053, "INV-R053-BOOK-META-001", "edge", "changed", "book-null-and-empty-clear-to-null",
			"Sending null or an empty string for subtitle, description and publisher stores NULL and the response shows null.",
			srcs(bookUpdateSrc, "client/components/widgets/BookDetailsEdit.vue:240-262"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, map[string]any{"metadata": map[string]any{"subtitle": nil, "description": "", "publisher": nil}})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				row := mediaRow(t, e, id)
				for _, key := range []string{"subtitle", "description", "publisher"} {
					if row[key] != nil || metadataOf(result.LibraryItem)[key] != nil {
						t.Fatalf("%s: row %v response %v, want NULL/null", key, row[key], metadataOf(result.LibraryItem)[key])
					}
				}
				if row["language"] != "German" {
					t.Fatalf("language changed to %v", row["language"])
				}
				wantMoved(t, e, id, before)
				file := readMetadataFile(t, e, id)
				if file["subtitle"] != nil || file["description"] != nil || file["publisher"] != nil {
					t.Fatalf("metadata.json still has cleared values: %v", file)
				}
			}),
		scenario(r053, "INV-R053-NOOP-001", "edge", "failure", "book-invalid-typed-values-ignored",
			"Arrays holding non-strings and a non-array tags value are ignored: updated:false and nothing stored changes.",
			srcs(bookUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				body := map[string]any{"metadata": map[string]any{"genres": []any{"Fine", 3}, "narrators": "Nora", "title": []string{"x"}}, "tags": "not-a-list"}
				result := patchOK(t, e, id, body)
				if result.Updated {
					t.Fatal("updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, "INV-R053-BOOK-META-001", "edge", "changed", "book-description-html-sanitized",
			"A description containing a script element is stored and returned sanitized, with the script removed.",
			srcs(bookUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				result := patchOK(t, e, id, map[string]any{"metadata": map[string]any{"description": "<p>Safe <b>bold</b></p><script>alert(1)</script>"}})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				stored, _ := mediaRow(t, e, id)["description"].(string)
				if stored != "<p>Safe <b>bold</b></p>" {
					t.Fatalf("stored description %q", stored)
				}
				requireEqual(t, "response description", metadataOf(result.LibraryItem)["description"], stored)
				requireEqual(t, "metadata.json description", readMetadataFile(t, e, id)["description"], stored)
			}),
	}
}
