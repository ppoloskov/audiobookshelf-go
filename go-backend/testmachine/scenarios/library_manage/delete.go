package library_manage

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	deleteRoute = "server/routers/ApiRouter.js:73"
	deleteSrc   = "server/controllers/LibraryController.js:497-566"
)

// itemRemovedIDs returns the sorted ids of item_removed events for a library after a mark.
func itemRemovedIDs(s *harness.Socket, from int, libraryID string) []string {
	var ids []string
	for _, ev := range s.Events()[from:] {
		if ev.Name != "item_removed" || len(ev.Args) != 1 {
			continue
		}
		var payload struct{ ID, LibraryID string }
		if json.Unmarshal(ev.Args[0], &payload) == nil && payload.LibraryID == libraryID {
			ids = append(ids, payload.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// fixtureRemoval runs on its own server: it connects sockets, applies remove to a fixture library and
// returns what was observed before and after.
type fixtureRemoval struct {
	e      *harness.Env
	lib    libraryJSON
	before cascadeFacts
	after  cascadeFacts
	a      *audience
	files  int
}

func startRemoval(t *testing.T, key string, remove func(e *harness.Env, lib libraryJSON)) *fixtureRemoval {
	t.Helper()
	e := harness.Start(t, harness.Options{})
	r := &fixtureRemoval{e: e, lib: getLibrary(t, e, e.Token(t, "root"), e.IDs[key])}
	r.before = factsFor(t, e, r.lib.ID)
	if r.before.Items == 0 || r.before.Progress == 0 || r.before.PlaylistItems == 0 || r.before.Feeds == 0 {
		t.Fatalf("fixture library must have items, progress, playlist items and feeds: %+v", r.before)
	}
	// load the cached filter data so removal has to drop it (LibraryController.js:591)
	requireStatus(t, e.Get(t, e.Token(t, "root"), "/api/libraries/"+r.lib.ID+"?include=filterdata"), http.StatusOK, "filterdata")
	r.files = mediaFileCount(t, e.Media)
	limited := restrictedUser(t, e)
	r.a = connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "limited": limited.Token, "anonymous": ""})
	t.Cleanup(r.a.close)
	remove(e, r.lib)
	r.after = factsFor(t, e, r.lib.ID)
	return r
}

// checkItemsRemoved asserts the item-level cascade shared by library deletion and folder removal.
func (r *fixtureRemoval) checkItemsRemoved(t *testing.T) {
	t.Helper()
	after := r.after
	if after.Items != 0 || after.Progress != 0 || after.PlaylistItems != 0 || after.Feeds != 0 {
		t.Fatalf("items and their progress, playlist entries and feeds must be gone: %+v", after)
	}
	if left := itemMetadataDirs(r.e, r.before.ItemIDs); len(left) != 0 {
		t.Fatalf("metadata/items directories remain: %v", left)
	}
	if got := mediaFileCount(t, r.e.Media); got != r.files {
		t.Fatalf("media files on disk changed: %d -> %d", r.files, got)
	}
	want := slices.Clone(r.before.ItemIDs)
	slices.Sort(want)
	for _, name := range []string{"root", "listener", "limited"} {
		harness.Eventually(t, eventWait, "item_removed for "+name, func() bool {
			return slices.Equal(itemRemovedIDs(r.a.sockets[name], r.a.marks[name], r.lib.ID), want)
		})
	}
	if got := itemRemovedIDs(r.a.sockets["anonymous"], r.a.marks["anonymous"], r.lib.ID); len(got) != 0 {
		t.Fatalf("an unauthenticated socket got item_removed: %v", got)
	}
}

// deleteFixtureScenario deletes a fixture library on its own server and checks the cascade.
func deleteFixtureScenario(assertion, caseName, state, key, claim string) harness.Scenario {
	return harness.Scenario{
		Requirement: "route-r021", Invariant: "INV-R021-CASCADE-001", Case: caseName, State: state, Assertion: assertion,
		Sources: []string{deleteRoute, deleteSrc, "server/routers/ApiRouter.js:373-407", "server/models/Collection.js:151-158", "server/models/Library.js:135-150"},
		Claim:   claim,
		Run: func(t *testing.T, _ *harness.Env) {
			var resp harness.Response
			var sessionsNull int
			r := startRemoval(t, key, func(e *harness.Env, lib libraryJSON) {
				sessionsNull = e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE libraryId IS NULL")
				resp = e.Delete(t, e.Token(t, "root"), "/api/libraries/"+lib.ID)
			})
			e, lib := r.e, r.lib
			requireStatus(t, resp, http.StatusOK, "delete")
			if got := decodeLibrary(t, resp); !reflect.DeepEqual(got, lib) {
				t.Fatalf("delete must answer the library as it was:\n%+v\n%+v", got, lib)
			}
			r.checkItemsRemoved(t)
			after := r.after
			if after.Libraries != 0 || after.Folders != 0 || after.Collections != 0 || after.CollectionBooks != 0 || after.Playlists != 0 || after.Sessions != 0 {
				t.Fatalf("library, folders, collections, playlists must be gone and sessions detached: %+v", after)
			}
			if after.Authors != 0 || after.Series != 0 || (key == "library" && (r.before.Authors == 0 || r.before.Series == 0)) {
				t.Fatalf("authors and series rows of the library must cascade away: before %+v after %+v", r.before, after)
			}
			if r.before.Sessions == 0 {
				t.Fatal("fixture must have playback sessions in the library")
			}
			if now := e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE libraryId IS NULL"); now != sessionsNull+r.before.Sessions {
				t.Fatalf("sessions of the library must be kept with libraryId NULL: %d, want %d", now, sessionsNull+r.before.Sessions)
			}
			removed := r.a.expect(t, "library_removed", lib.ID, []string{"root", "listener", "limited"}, []string{"anonymous"})
			if !reflect.DeepEqual(removed, lib) {
				t.Fatalf("library_removed payload differs from the deleted document: %+v", removed)
			}
			requireStatus(t, e.Get(t, e.Token(t, "root"), "/api/libraries/"+lib.ID), http.StatusNotFound, "GET after delete")
			remaining := listLibraries(t, e, e.Token(t, "root"))
			if slices.Contains(libraryIDs(remaining), lib.ID) || len(remaining) != 1 || remaining[0].DisplayOrder != 1 {
				t.Fatalf("remaining library must be renumbered to 1: %+v", remaining)
			}
		},
	}
}

func deleteScenarios() []harness.Scenario {
	return []harness.Scenario{
		deleteFixtureScenario("delete-book-library-cascade", "normal", "changed", "library",
			"DELETE /api/libraries/:id on the fixture book library answers its document and removes its row, folders, items with their progress, playlist entries, feeds and metadata, collections and playlists, detaches playback sessions, keeps the files, emits item_removed per item and library_removed to every authenticated socket, and renumbers the remaining library."),
		deleteFixtureScenario("delete-podcast-library-cascade", "edge", "initial", "podcastLibrary",
			"DELETE /api/libraries/:id on the fixture podcast library removes episode progress and playlist entries, feeds and playlists the same way and keeps the files."),
		{
			Requirement: "route-r021", Invariant: "INV-R021-AUTH-001", Case: "error", State: "failure", Assertion: "delete-denied-or-unknown-changes-nothing",
			Sources: []string{deleteRoute, deleteSrc, "server/controllers/LibraryController.js:1475-1484"},
			Claim:   "DELETE /api/libraries/:id answers 401 without a token, 403 for a non-admin user and for a limited user on an inaccessible library, 404 for an unknown id, and changes no data and emits no library_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "keep")
				a, limited := everyone(t, e)
				defer a.close()
				before := snapshot(t, e)
				path := "/api/libraries/" + lib.ID
				requireStatus(t, e.Delete(t, "", path), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Delete(t, e.Token(t, "listener"), path), http.StatusForbidden, "non-admin")
				requireStatus(t, e.Delete(t, limited.Token, path), http.StatusForbidden, "limited user")
				requireStatus(t, e.Delete(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown id")
				requireUnchanged(t, e, before)
				a.silent(t, "library_removed")
			},
		},
		{
			Requirement: "route-r021", Invariant: "INV-R021-SAMPLED", Case: "normal", State: "initial", Assertion: "delete-empty-library-renumbers-display-order",
			Sources: []string{deleteRoute, deleteSrc, "server/models/Library.js:135-150"},
			Claim:   "DELETE /api/libraries/:id of a library without items answers its document, removes the row and folder rows but not the directory, and renumbers every remaining library to 1..n keeping their relative order.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				first, middle, last := newBookLibrary(t, e, "order-a"), newBookLibrary(t, e, "order-b"), newBookLibrary(t, e, "order-c")
				_ = first
				resp := e.Delete(t, root, "/api/libraries/"+middle.ID)
				requireStatus(t, resp, http.StatusOK, "delete")
				if got := decodeLibrary(t, resp); !reflect.DeepEqual(got, middle) {
					t.Fatalf("delete must answer the library as it was:\n%+v\n%+v", got, middle)
				}
				if row, folders := dbLibrary(t, e, middle.ID); row != nil || len(folders) != 0 {
					t.Fatalf("rows remain: %v %v", row, folders)
				}
				if !isDir(middle.Folders[0].FullPath) {
					t.Fatal("the library folder must stay on disk")
				}
				libs := listLibraries(t, e, root)
				for i, lib := range libs {
					if int(lib.DisplayOrder) != i+1 {
						t.Fatalf("display orders must be 1..n: position %d has %v", i, lib.DisplayOrder)
					}
				}
				ids := libraryIDs(libs)
				if slices.Contains(ids, middle.ID) || slices.Index(ids, first.ID) >= slices.Index(ids, last.ID) {
					t.Fatalf("remaining order wrong: %v", ids)
				}
			},
		},
		{
			Requirement: "route-r021", Invariant: "INV-R021-SAMPLED", Case: "error", State: "repeat", Assertion: "delete-twice-second-is-not-found",
			Sources: []string{deleteRoute, "server/controllers/LibraryController.js:1481-1484"},
			Claim:   "A second DELETE of the same library answers 404 and emits no second library_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "twice")
				a, _ := everyone(t, e)
				defer a.close()
				root := e.Token(t, "root")
				requireStatus(t, e.Delete(t, root, "/api/libraries/"+lib.ID), http.StatusOK, "first delete")
				a.expect(t, "library_removed", lib.ID, []string{"root"}, nil)
				before := snapshot(t, e)
				requireStatus(t, e.Delete(t, root, "/api/libraries/"+lib.ID), http.StatusNotFound, "second delete")
				requireUnchanged(t, e, before)
				a.rearm()
				a.silent(t, "library_removed")
			},
		},
	}
}
