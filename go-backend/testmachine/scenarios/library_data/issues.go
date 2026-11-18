package library_data

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	issuesRoute = "server/routers/ApiRouter.js:76"
	issuesSrc   = "server/controllers/LibraryController.js:650-735"
	removeItem  = "server/routers/ApiRouter.js:373-407"
)

// issuesEnv is a server whose book library holds one missing and one invalid item.
type issuesEnv struct {
	e                *harness.Env
	libraryID        string
	missing, invalid string
}

// disableWatcher turns the library watcher off so only API calls scan the library.
func disableWatcher(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+libraryID, map[string]any{"settings": map[string]any{"disableWatcher": true}})
	requireStatus(t, resp, http.StatusOK, "disable watcher")
}

// scanLibrary starts a scan through the API; the caller waits for its result.
func scanLibrary(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	requireStatus(t, e.Post(t, e.Token(t, "root"), libPath(libraryID, "/scan"), nil), http.StatusOK, "scan")
}

// startIssues builds the issue rows in the database while the server is
// stopped and then restarts it, so the cached filter data is rebuilt from the
// rows. Writing a flag while a scan runs is not deterministic: the scan keeps
// the library items it loaded before the write and saves them again for every
// item it updates, which restores the old flag value, and the numIssues it then
// resets never counts the lost flag.
func startIssues(t *testing.T) *issuesEnv {
	t.Helper()
	e := harness.Start(t, harness.Options{})
	lib := e.IDs[bookLib]
	disableWatcher(t, e, lib)
	patchMedia(t, e, e.IDs["novel"], `{"metadata":{"series":[{"name":"Gone Series","sequence":"1"},{"name":"Kept Series","sequence":"2"}]}}`)
	patchMedia(t, e, e.IDs["disposable"], `{"metadata":{"series":[{"name":"Gone Series","sequence":"2"}]}}`)
	patchMedia(t, e, e.IDs["ebook"], `{"metadata":{"series":[{"name":"Kept Series","sequence":"3"}]}}`)
	root := e.Token(t, "root")
	if got := getFilterData(t, e, root, lib); got.NumIssues != 0 {
		t.Fatalf("numIssues before any issue: %d", got.NumIssues)
	}
	e.Stop(t)
	if n := e.Exec(t, "UPDATE libraryItems SET isMissing = 1 WHERE id = ?", e.IDs["novel"]); n != 1 {
		t.Fatalf("flagged %d rows missing", n)
	}
	if n := e.Exec(t, "UPDATE libraryItems SET isInvalid = 1 WHERE id = ?", e.IDs["disposable"]); n != 1 {
		t.Fatalf("flagged %d rows invalid", n)
	}
	e.Restart(t)
	requireEqual(t, "seeded issue flags", e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE (id = ? AND isMissing = 1 AND isInvalid = 0) OR (id = ? AND isMissing = 0 AND isInvalid = 1)", e.IDs["novel"], e.IDs["disposable"]), 2)
	if got := getFilterData(t, e, e.Token(t, "root"), lib); got.NumIssues != 2 {
		t.Fatalf("numIssues with a missing and an invalid item: %d", got.NumIssues)
	}
	return &issuesEnv{e: e, libraryID: lib, missing: e.IDs["novel"], invalid: e.IDs["disposable"]}
}

// issueFacts counts the rows tied to the items with issues and to the healthy ones.
type issueFacts struct {
	Items, Books, Progress, PlaylistItems, Feeds, CollectionBooks int
	MetadataDirs                                                  []string
	Authors, Series                                               []string
}

func (v *issuesEnv) facts(t *testing.T) issueFacts {
	e := v.e
	media := `SELECT mediaId FROM libraryItems WHERE id IN (?, ?)`
	f := issueFacts{
		Items:           e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id IN (?, ?)", v.missing, v.invalid),
		Books:           e.Count(t, "SELECT COUNT(*) FROM books WHERE id IN ("+media+")", v.missing, v.invalid),
		Progress:        e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN ("+media+")", v.missing, v.invalid),
		PlaylistItems:   e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemId IN ("+media+")", v.missing, v.invalid),
		Feeds:           e.Count(t, "SELECT COUNT(*) FROM feeds WHERE entityId IN (?, ?)", v.missing, v.invalid),
		CollectionBooks: e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE bookId IN ("+media+")", v.missing, v.invalid),
	}
	for _, id := range []string{v.missing, v.invalid} {
		if _, err := os.Stat(filepath.Join(e.Metadata, "items", id)); err == nil {
			f.MetadataDirs = append(f.MetadataDirs, id)
		}
	}
	for _, row := range e.Rows(t, "SELECT name FROM authors WHERE libraryId = ? ORDER BY name", v.libraryID) {
		f.Authors = append(f.Authors, row["name"].(string))
	}
	for _, row := range e.Rows(t, "SELECT name FROM series WHERE libraryId = ? ORDER BY name", v.libraryID) {
		f.Series = append(f.Series, row["name"].(string))
	}
	return f
}

func (v *issuesEnv) healthyRows(t *testing.T) []itemRow {
	var out []itemRow
	for _, item := range itemRows(t, v.e, v.libraryID) {
		if item.ID != v.missing && item.ID != v.invalid {
			out = append(out, item)
		}
	}
	return out
}

// idsOf returns the sorted "id" fields of an event's first argument across receipts.
func idsOf(t *testing.T, raws []json.RawMessage, libraryID string) []string {
	t.Helper()
	var out []string
	for _, raw := range raws {
		var payload struct{ ID, LibraryID string }
		if err := json.Unmarshal(raw, &payload); err != nil || payload.LibraryID != libraryID {
			t.Fatalf("bad payload %s (%v)", raw, err)
		}
		out = append(out, payload.ID)
	}
	slices.Sort(out)
	return out
}

func issuesScenarios(s *suite) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r023", Invariant: "INV-ROUTE-R023-CONTRACT", Case: "normal", State: "initial", Assertion: "issues-remove-missing-and-invalid-items",
			Sources: []string{issuesRoute, issuesSrc, removeItem, "server/routers/ApiRouter.js:414-523", "server/Database.js:653-672"},
			Claim:   "DELETE /api/libraries/:id/issues removes the items flagged missing or invalid with their media, progress, playlist entries, feeds, collection entries and metadata directories, keeps the healthy items and the media files, removes authors and series left without books (keeping the others), answers 200, emits item_removed per item, author_removed and series_removed to every authenticated socket, and resets numIssues to 0.",
			Run: func(t *testing.T, _ *harness.Env) {
				v := startIssues(t)
				e := v.e
				before := v.facts(t)
				healthy := v.healthyRows(t)
				if before.Items != 2 || before.Books != 2 || before.Progress == 0 || before.PlaylistItems == 0 || before.Feeds == 0 || before.CollectionBooks == 0 || len(before.MetadataDirs) != 2 {
					t.Fatalf("setup must give the items progress, playlist entries, feed, collection entry and metadata: %+v", before)
				}
				requireEqual(t, "authors before", before.Authors, []string{"Ada Example", "Terry Fixture", "Zed Disposable"})
				requireEqual(t, "series before", before.Series, []string{"Gone Series", "Kept Series", "Long Saga"})
				zed, goneSeries := e.Rows(t, "SELECT id FROM authors WHERE name = 'Zed Disposable'"), e.Rows(t, "SELECT id FROM series WHERE name = 'Gone Series'")
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": s.podcastOnly(t, e).Token, "anonymous": ""})
				resp := e.Delete(t, e.Token(t, "root"), libPath(v.libraryID, "/issues"))
				requireStatus(t, resp, http.StatusOK, "remove issues")
				after := v.facts(t)
				if after.Items != 0 || after.Books != 0 || after.Progress != 0 || after.PlaylistItems != 0 || after.Feeds != 0 || after.CollectionBooks != 0 || len(after.MetadataDirs) != 0 {
					t.Fatalf("rows of the removed items remain: %+v", after)
				}
				requireEqual(t, "authors after", after.Authors, []string{"Ada Example", "Terry Fixture"})
				requireEqual(t, "series after", after.Series, []string{"Kept Series", "Long Saga"})
				requireEqual(t, "healthy items", v.healthyRows(t), healthy)
				if len(healthy) != 3 {
					t.Fatalf("three healthy items must remain: %+v", healthy)
				}
				for _, item := range healthy {
					if item.Missing || item.Invalid {
						t.Fatalf("healthy item flagged: %+v", item)
					}
				}
				if _, err := os.Stat(filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me", "Hard Delete Me.mp3")); err != nil {
					t.Fatalf("removal must not delete media files: %v", err)
				}
				want := []string{v.missing, v.invalid}
				slices.Sort(want)
				for _, name := range []string{"root", "listener", "limited"} {
					harness.Eventually(t, eventWait, "events for "+name, func() bool {
						return len(a.received(name, "item_removed")) == 2 && len(a.received(name, "author_removed")) == 1 && len(a.received(name, "series_removed")) == 1
					})
					requireEqual(t, name+" item_removed", idsOf(t, a.received(name, "item_removed"), v.libraryID), want)
					requireEqual(t, name+" author_removed", idsOf(t, a.received(name, "author_removed"), v.libraryID), []string{zed[0]["id"].(string)})
					requireEqual(t, name+" series_removed", idsOf(t, a.received(name, "series_removed"), v.libraryID), []string{goneSeries[0]["id"].(string)})
				}
				a.silent(t, "item_removed", "anonymous")
				filter := getFilterData(t, e, e.Token(t, "root"), v.libraryID)
				if filter.NumIssues != 0 {
					t.Fatalf("numIssues after removal: %d", filter.NumIssues)
				}
				for _, ref := range filter.Authors {
					if ref.Name == "Zed Disposable" {
						t.Fatal("removed author still in filterdata")
					}
				}
				for _, ref := range filter.Series {
					if ref.Name == "Gone Series" {
						t.Fatal("removed series still in filterdata")
					}
				}
				items := e.Get(t, e.Token(t, "root"), libPath(v.libraryID, "/items?limit=10&page=0"))
				var page struct {
					Total int `json:"total"`
				}
				items.JSON(t, &page)
				if page.Total != 3 {
					t.Fatalf("items list total %d, want 3", page.Total)
				}
			},
		},
		{
			Requirement: "route-r023", Invariant: "INV-ROUTE-R023-REPEAT-001", Case: "edge", State: "repeat", Assertion: "issues-remove-twice-and-without-issues",
			Sources: []string{issuesRoute, "server/controllers/LibraryController.js:677-680"},
			Claim:   "DELETE issues on a library with no flagged item answers 200 and changes and emits nothing, also when repeated after a removal.",
			Run: func(t *testing.T, _ *harness.Env) {
				v := startIssues(t)
				e := v.e
				root := e.Token(t, "root")
				requireStatus(t, e.Delete(t, root, libPath(v.libraryID, "/issues")), http.StatusOK, "first")
				a := connect(t, e, map[string]string{"root": root})
				before := e.Rows(t, "SELECT id, updatedAt FROM libraryItems ORDER BY id")
				authors := e.Count(t, "SELECT COUNT(*) FROM authors")
				requireStatus(t, e.Delete(t, root, libPath(v.libraryID, "/issues")), http.StatusOK, "second")
				requireEqual(t, "items unchanged", e.Rows(t, "SELECT id, updatedAt FROM libraryItems ORDER BY id"), before)
				requireEqual(t, "authors unchanged", e.Count(t, "SELECT COUNT(*) FROM authors"), authors)
				a.silent(t, "item_removed", "root")
				a.silent(t, "author_removed", "root")
				// the library with no issue at all: the podcast library
				requireStatus(t, e.Delete(t, root, libPath(e.IDs[podcastLib], "/issues")), http.StatusOK, "library without issues")
				requireEqual(t, "podcast items", e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?", e.IDs[podcastLib]), 1)
			},
		},
		{
			Requirement: "route-r023", Invariant: "INV-ROUTE-R023-PODCAST-001", Case: "edge", State: "changed", Assertion: "issues-remove-missing-podcast",
			Sources: []string{issuesRoute, "server/controllers/LibraryController.js:702-706", removeItem, "server/models/Playlist.js"},
			Claim:   "In a podcast library DELETE issues removes the missing podcast with its episodes, their progress and playlist entries (a playlist left empty is deleted), keeps the book library, and resets numIssues.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				lib := e.IDs[podcastLib]
				disableWatcher(t, e, lib)
				root := e.Token(t, "root")
				// Load the filter cache before the scan: the scan's completion
				// reset writes numIssues into the cached document, and the
				// podcast branch of the upstream reload leaves numIssues at 0.
				if before := getFilterData(t, e, root, lib); before.NumIssues != 0 {
					t.Fatalf("numIssues before the scan: %d", before.NumIssues)
				}
				if err := os.RemoveAll(filepath.Join(e.Media, "podcasts", "Fixture Cast")); err != nil {
					t.Fatal(err)
				}
				scanLibrary(t, e, lib)
				harness.Eventually(t, scanWait, "the scan to flag the removed podcast folder missing", func() bool {
					return e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ? AND isMissing = 1", e.IDs["podcast"]) == 1
				})
				harness.Eventually(t, scanWait, "numIssues to count the missing podcast", func() bool {
					return getFilterData(t, e, root, lib).NumIssues == 1
				})
				episodes := []string{e.IDs["ep1"], e.IDs["ep2"], e.IDs["ep3"]}
				progress := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN (?, ?, ?)", episodes[0], episodes[1], episodes[2])
				playlist := e.Count(t, "SELECT COUNT(*) FROM playlists WHERE id = ?", e.IDs["podcastPlaylist"])
				books := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?", e.IDs[bookLib])
				if progress != 3 || playlist != 1 {
					t.Fatalf("fixture: progress %d, playlist %d", progress, playlist)
				}
				a := connect(t, e, map[string]string{"root": root})
				requireStatus(t, e.Delete(t, root, libPath(lib, "/issues")), http.StatusOK, "remove")
				requireEqual(t, "podcast row", e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", e.IDs["podcast"]), 0)
				requireEqual(t, "podcast media", e.Count(t, "SELECT COUNT(*) FROM podcasts WHERE id IN (SELECT mediaId FROM libraryItems WHERE id = ?)", e.IDs["podcast"]), 0)
				requireEqual(t, "episodes", e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes WHERE id IN (?, ?, ?)", episodes[0], episodes[1], episodes[2]), 0)
				requireEqual(t, "episode progress", e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN (?, ?, ?)", episodes[0], episodes[1], episodes[2]), 0)
				requireEqual(t, "emptied playlist", e.Count(t, "SELECT COUNT(*) FROM playlists WHERE id = ?", e.IDs["podcastPlaylist"]), 0)
				requireEqual(t, "book library untouched", e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?", e.IDs[bookLib]), books)
				harness.Eventually(t, eventWait, "item_removed", func() bool { return len(a.received("root", "item_removed")) == 1 })
				requireEqual(t, "item_removed", idsOf(t, a.received("root", "item_removed"), lib), []string{e.IDs["podcast"]})
				if got := getFilterData(t, e, root, lib); got.NumIssues != 0 {
					t.Fatalf("numIssues after removal: %d", got.NumIssues)
				}
			},
		},
		{
			Requirement: "route-r023", Invariant: "INV-ROUTE-R023-AUTH-001", Case: "error", State: "failure", Assertion: "issues-remove-denied-changes-nothing",
			Sources: []string{issuesRoute, "server/controllers/LibraryController.js:650-657", middleware},
			Claim:   "DELETE issues answers 401 without a token, 403 for a non-admin user (also one with update permission) and for a user without library access, 404 for an unknown library and 400 for an invalid limit before the admin check, and removes nothing while items with issues exist.",
			Run: func(t *testing.T, _ *harness.Env) {
				v := startIssues(t)
				e := v.e
				updater := s.createUser(t, e, "data-issue-updater", `"permissions":{"update":true,"delete":true}`)
				limited := s.podcastOnly(t, e)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before := v.facts(t)
				rows := e.Rows(t, "SELECT id, isMissing, isInvalid FROM libraryItems ORDER BY id")
				path := libPath(v.libraryID, "/issues")
				requireStatus(t, e.Delete(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden, "listener")
				requireStatus(t, e.Delete(t, updater.Token, path), http.StatusForbidden, "updater")
				requireStatus(t, e.Delete(t, limited.Token, path), http.StatusForbidden, "limited")
				requireStatus(t, e.Delete(t, e.Token(t, "root"), libPath(missingID, "/issues")), http.StatusNotFound, "unknown")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path+"?limit=-1"), http.StatusBadRequest, "paging before admin check")
				requireEqual(t, "facts", v.facts(t), before)
				requireEqual(t, "rows", e.Rows(t, "SELECT id, isMissing, isInvalid FROM libraryItems ORDER BY id"), rows)
				a.silent(t, "item_removed", "root")
			},
		},
	}
}
