package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// authorRow returns the authors row of a name in the fixture library, or nil.
func authorRow(t *testing.T, e *harness.Env, name string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM authors WHERE name = ? AND libraryId = ?", name, e.IDs["library"])
	if len(rows) == 0 {
		return nil
	}
	if len(rows) != 1 {
		t.Fatalf("author %q: %d rows", name, len(rows))
	}
	return rows[0]
}

// authorBody builds the authors array of the edit form.
func authorBody(list ...any) map[string]any {
	if list == nil {
		list = []any{} // an empty array, not null
	}
	return map[string]any{"metadata": map[string]any{"authors": list}}
}

func named(name string) map[string]any { return map[string]any{"name": name} }

func mediaAuthorScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R053-AUTHORS-001"
	var newID string
	return []harness.Scenario{
		scenario(r053, inv, "normal", "initial", "authors-create-new-author",
			"Adding an author name that does not exist creates the authors row (lastFirst, library), links it in bookAuthors, updates the item's author sort names and metadata.json, answers the author list and emits author_added to every authenticated socket.",
			srcs(updateMediaSrc, authorsUpdateSrc, "server/models/Author.js:116-125", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				a := peopleAudience(t, e, s)
				if authorRow(t, e, "Brand New") != nil {
					t.Fatal("fixture already has Brand New")
				}
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, authorBody(named("Ada Example"), named("Brand New")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "response authors", names(metadataOf(result.LibraryItem)["authors"]), []string{"Ada Example", "Brand New"})
				row := authorRow(t, e, "Brand New")
				if row == nil || row["lastFirst"] != "New, Brand" {
					t.Fatalf("authors row %v", row)
				}
				newID, _ = row["id"].(string)
				requireEqual(t, "bookAuthors", bookAuthorNames(t, e, id), []string{"Ada Example", "Brand New"})
				item := itemRow(t, e, id)
				if item["authorNamesFirstLast"] != "Ada Example, Brand New" || item["authorNamesLastFirst"] != "Example, Ada, New, Brand" {
					t.Fatalf("author sort names %v / %v", item["authorNamesFirstLast"], item["authorNamesLastFirst"])
				}
				requireEqual(t, "metadata.json authors", readMetadataFile(t, e, id)["authors"], []any{"Ada Example", "Brand New"})
				wantMoved(t, e, id, before)
				events := a.receive(t, "author_added", idOf(newID), []string{"root", "podcastonly"}, []string{"anonymous"})
				got := payload(t, events["root"])
				if got["name"] != "Brand New" || got["libraryId"] != e.IDs["library"] {
					t.Fatalf("author_added payload %v", got)
				}
			}),
		scenario(r053, inv, "edge", "changed", "authors-reuse-existing-case-insensitive",
			"An author name matching an existing author of the library ignoring case links that author without creating a row and emits author_updated with the new book count.",
			srcs(authorsUpdateSrc, "server/models/Author.js:55-65"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				terry := authorRow(t, e, "Terry Fixture")
				if terry == nil || terry["id"] != "75e57a0b-a39d-4073-8501-e3b745ebd79d" {
					t.Fatalf("fixture author %v", terry)
				}
				count := e.Count(t, "SELECT COUNT(*) FROM authors")
				a := peopleAudience(t, e, s)
				result := patchOK(t, e, id, authorBody(named("Ada Example"), named("Brand New"), named("terry fixture")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM authors"); got != count {
					t.Fatalf("authors %d -> %d", count, got)
				}
				requireEqual(t, "bookAuthors", bookAuthorNames(t, e, id), []string{"Ada Example", "Brand New", "Terry Fixture"})
				ev := a.receive(t, "author_updated", idOf("75e57a0b-a39d-4073-8501-e3b745ebd79d"), []string{"root"}, []string{"anonymous"})
				got := payload(t, ev["root"])
				if got["name"] != "Terry Fixture" || got["numBooks"] != float64(3) {
					t.Fatalf("author_updated payload %v", got)
				}
			}),
		scenario(r053, inv, "edge", "changed", "authors-trim-and-skip-invalid-names",
			"Author names are trimmed and entries without a string name are skipped.",
			srcs(updateMediaSrc, authorsUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				result := patchOK(t, e, id, authorBody(named("Ada Example"), named("Brand New"), named("terry fixture"), named("   Padded Name  "), map[string]any{"name": 5}, map[string]any{}, named("   ")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "bookAuthors", bookAuthorNames(t, e, id), []string{"Ada Example", "Brand New", "Padded Name", "Terry Fixture"})
				if authorRow(t, e, "Padded Name") == nil {
					t.Fatal("trimmed author row missing")
				}
			}),
		scenario(r053, inv, "normal", "repeat", "authors-same-list-is-noop",
			"Sending the author list the book already has changes nothing: updated is false, updatedAt unchanged and no author event is emitted.",
			srcs(updateMediaSrc, authorsUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				a := peopleAudience(t, e, s)
				result := patchOK(t, e, id, authorBody(named("Ada Example"), named("brand new"), named("Padded Name"), named("Terry Fixture")))
				if result.Updated {
					t.Fatal("updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				for _, name := range []string{"author_added", "author_updated", "author_removed"} {
					a.sockets["root"].None(t, a.marks["root"], name, absentWindow, nil)
				}
			}),
		scenario(r053, inv, "normal", "changed", "authors-remove-orphan-is-deleted",
			"Removing authors that have no other book, description, asin or image deletes their rows and emits author_removed {id, libraryId}; shared authors stay with author_updated.",
			srcs(updateMediaSrc, authorsUpdateSrc, removeAuthorSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				paddedID, _ := authorRow(t, e, "Padded Name")["id"].(string)
				a := peopleAudience(t, e, s)
				result := patchOK(t, e, id, authorBody(named("Ada Example")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "response authors", names(metadataOf(result.LibraryItem)["authors"]), []string{"Ada Example"})
				requireEqual(t, "bookAuthors", bookAuthorNames(t, e, id), []string{"Ada Example"})
				for _, name := range []string{"Brand New", "Padded Name"} {
					if authorRow(t, e, name) != nil {
						t.Fatalf("author %q was not removed", name)
					}
				}
				if authorRow(t, e, "Terry Fixture") == nil {
					t.Fatal("author with other books was removed")
				}
				removed := a.receive(t, "author_removed", idOf(newID), []string{"root", "podcastonly"}, []string{"anonymous"})
				got := payload(t, removed["root"])
				if got["libraryId"] != e.IDs["library"] {
					t.Fatalf("author_removed payload %v", got)
				}
				a.receive(t, "author_removed", idOf(paddedID), []string{"root"}, nil)
				ev := a.receive(t, "author_updated", idOf("75e57a0b-a39d-4073-8501-e3b745ebd79d"), []string{"root"}, nil)
				if payload(t, ev["root"])["numBooks"] != float64(2) {
					t.Fatalf("author_updated payload %v", payload(t, ev["root"]))
				}
				requireEqual(t, "metadata.json authors", readMetadataFile(t, e, id)["authors"], []any{"Ada Example"})
			}),
		scenario(r053, inv, "edge", "changed", "authors-removed-author-with-description-is-kept",
			"A removed author that has a description keeps its row and no author_removed is emitted.",
			srcs(removeAuthorSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				wantStatus(t, "add author", patchMedia(t, e, e.Token(t, "root"), id, authorBody(named("Ada Example"), named("Described Person"))), http.StatusOK)
				described, _ := authorRow(t, e, "Described Person")["id"].(string)
				wantStatus(t, "describe author", e.Patch(t, e.Token(t, "root"), "/api/authors/"+described, map[string]any{"description": "Has a bio"}), http.StatusOK)
				a := peopleAudience(t, e, s)
				result := patchOK(t, e, id, authorBody(named("Ada Example")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				row := authorRow(t, e, "Described Person")
				if row == nil || row["description"] != "Has a bio" {
					t.Fatalf("author row %v", row)
				}
				a.sockets["root"].None(t, a.marks["root"], "author_removed", absentWindow, idOf(described))
			}),
		scenario(r053, inv, "edge", "changed", "authors-empty-list-removes-all-links",
			"An empty authors array removes every link of the book, updates the item sort names to empty and stores no authors in metadata.json.",
			srcs(updateMediaSrc, authorsUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, authorBody())
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "bookAuthors", bookAuthorNames(t, e, id), []string(nil))
				requireEqual(t, "response authors", names(metadataOf(result.LibraryItem)["authors"]), []string(nil))
				requireEqual(t, "metadata.json authors", readMetadataFile(t, e, id)["authors"], []any{})
				wantMoved(t, e, id, before)
				if authorRow(t, e, "Ada Example") == nil {
					t.Fatal("author still linked to the novel was removed")
				}
				patchOK(t, e, id, authorBody(named("Ada Example")))
				requireEqual(t, "bookAuthors restored", bookAuthorNames(t, e, id), []string{"Ada Example"})
			}),
	}
}

func seriesBody(list ...any) map[string]any {
	if list == nil {
		list = []any{} // an empty array, not null
	}
	return map[string]any{"metadata": map[string]any{"series": list}}
}

func seriesEntry(name, sequence string) map[string]any {
	return map[string]any{"name": name, "sequence": sequence}
}

// seriesRow returns the series row of a name in the fixture library, or nil.
func seriesRow(t *testing.T, e *harness.Env, name string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM series WHERE name = ? AND libraryId = ?", name, e.IDs["library"])
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

func mediaSeriesScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R053-SERIES-001"
	const sagaSeries = "e987a322-9111-4f79-8cb9-c25ee714e0d6"
	var newID string
	return []harness.Scenario{
		scenario(r053, inv, "normal", "initial", "series-create-and-sequence",
			"Adding a series name creates the series row (nameIgnorePrefix, library), links it with its sequence, answers {id,name,sequence} and writes 'Name #sequence' to metadata.json; an existing series keeps its row.",
			srcs(updateMediaSrc, seriesUpdateSrc, "server/models/Series.js:75-83", metadataFileSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				requireEqual(t, "fixture series", bookSeriesRows(t, e, id), []string{"Long Saga|2"})
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, seriesBody(seriesEntry("Long Saga", "2"), seriesEntry("The New Series", "1")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				row := seriesRow(t, e, "The New Series")
				if row == nil || row["nameIgnorePrefix"] != "New Series" {
					t.Fatalf("series row %v", row)
				}
				newID, _ = row["id"].(string)
				requireEqual(t, "bookSeries", bookSeriesRows(t, e, id), []string{"Long Saga|2", "The New Series|1"})
				series, _ := harness.Dig(result.LibraryItem, "media", "metadata", "series").([]any)
				if len(series) != 2 {
					t.Fatalf("response series %v", series)
				}
				first, _ := series[0].(map[string]any)
				second, _ := series[1].(map[string]any)
				if first["id"] != sagaSeries || first["sequence"] != "2" || second["id"] != newID || second["name"] != "The New Series" || second["sequence"] != "1" {
					t.Fatalf("response series %v", series)
				}
				requireEqual(t, "metadata.json series", readMetadataFile(t, e, id)["series"], []any{"Long Saga #2", "The New Series #1"})
				wantMoved(t, e, id, before)
			}),
		scenario(r053, inv, "edge", "changed", "series-sequence-change-and-case-insensitive-reuse",
			"Changing the sequence of a linked series (matched ignoring case) updates the bookSeries row only, creates no series row and answers updated:true.",
			srcs(seriesUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				count := e.Count(t, "SELECT COUNT(*) FROM series")
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, seriesBody(seriesEntry("long saga", "2.5"), seriesEntry("The New Series", "1")))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "bookSeries", bookSeriesRows(t, e, id), []string{"Long Saga|2.5", "The New Series|1"})
				if got := e.Count(t, "SELECT COUNT(*) FROM series"); got != count {
					t.Fatalf("series %d -> %d", count, got)
				}
				requireEqual(t, "metadata.json series", readMetadataFile(t, e, id)["series"], []any{"Long Saga #2.5", "The New Series #1"})
				wantMoved(t, e, id, before)
				resp := patchMedia(t, e, e.Token(t, "root"), id, seriesBody(seriesEntry("Long Saga", "2.5"), seriesEntry("The New Series", "1")))
				wantStatus(t, "repeat", resp, http.StatusOK)
				if updated, _ := resp.Object(t)["updated"].(bool); updated {
					t.Fatal("repeat reported an update")
				}
				wantUnchanged(t, e, id, updatedAt(t, e, id))
			}),
		scenario(r053, inv, "edge", "failure", "series-entry-without-name-ignores-whole-list",
			"A series list with an entry that has no string name is ignored entirely: updated is false and no rows change.",
			srcs(seriesUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				result := patchOK(t, e, id, seriesBody(seriesEntry("Dropped", "1"), map[string]any{"name": 7}))
				if result.Updated {
					t.Fatal("updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, inv, "normal", "changed", "series-remove-orphan-is-deleted",
			"Removing the only link of a series deletes the series row and emits series_removed {id, libraryId}; a series that other books still use stays.",
			srcs(updateMediaSrc, seriesUpdateSrc, removeSeriesSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				a := peopleAudience(t, e, s)
				result := patchOK(t, e, id, seriesBody())
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "bookSeries", bookSeriesRows(t, e, id), []string(nil))
				if seriesRow(t, e, "The New Series") != nil {
					t.Fatal("orphaned series was not deleted")
				}
				if seriesRow(t, e, "Long Saga") == nil || len(bookSeriesRows(t, e, e.IDs["saga1"])) != 1 {
					t.Fatal("series used by another book was removed")
				}
				ev := a.receive(t, "series_removed", idOf(newID), []string{"root"}, []string{"anonymous"})
				if payload(t, ev["root"])["libraryId"] != e.IDs["library"] {
					t.Fatalf("series_removed payload %v", payload(t, ev["root"]))
				}
				a.sockets["root"].None(t, a.marks["root"], "series_removed", absentWindow, idOf(sagaSeries))
				requireEqual(t, "metadata.json series", readMetadataFile(t, e, id)["series"], []any{})
				patchOK(t, e, id, seriesBody(seriesEntry("Long Saga", "2")))
				requireEqual(t, "bookSeries restored", bookSeriesRows(t, e, id), []string{"Long Saga|2"})
			}),
	}
}

// peopleAudience opens the sockets of one scenario: an admin, a user of another library and an unauthenticated one.
func peopleAudience(t *testing.T, e *harness.Env, s *setup) *audience {
	t.Helper()
	return connect(t, e, map[string]string{"root": e.Token(t, "root"), "podcastonly": s.token(t, e, "podcastonly"), "anonymous": ""})
}
