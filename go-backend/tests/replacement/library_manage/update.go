package library_manage

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	updateRoute = "server/routers/ApiRouter.js:72"
	updateSrc   = "server/controllers/LibraryController.js:255-515"
)

// patchLibrary sends PATCH /api/libraries/:id as root and requires 200.
func patchLibrary(t *testing.T, e *harness.Env, id string, body map[string]any) libraryJSON {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+id, body)
	requireStatus(t, resp, http.StatusOK, "patch library")
	return decodeLibrary(t, resp)
}

// libraryFolderEntry is a folder as a client sends it back in PATCH folders.
func libraryFolderEntry(f folderJSON) map[string]any {
	return map[string]any{"id": f.ID, "fullPath": f.FullPath, "libraryId": f.LibraryID, "addedAt": f.AddedAt}
}

func updateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r020", Invariant: "INV-R020-SAMPLED", Case: "normal", State: "initial", Assertion: "update-fields-persist-and-answer",
			Sources: []string{updateRoute, updateSrc, "server/models/Library.js:202-219"},
			Claim:   "PATCH /api/libraries/:id with name, icon, provider and displayOrder changes exactly those fields, keeps folders, mediaType and settings, advances lastUpdate, persists the row and answers the updated document.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "update")
				start := time.Now().UnixMilli()
				got := patchLibrary(t, e, lib.ID, map[string]any{"name": "Renamed", "icon": "audiobookshelf", "provider": "openlibrary", "displayOrder": 41})
				want := lib
				want.Name, want.Icon, want.Provider, want.DisplayOrder = "Renamed", "audiobookshelf", "openlibrary", 41
				if got.LastUpdate < start || got.LastUpdate <= lib.LastUpdate || got.CreatedAt != lib.CreatedAt {
					t.Fatalf("timestamps: created %d->%d lastUpdate %d->%d", lib.CreatedAt, got.CreatedAt, lib.LastUpdate, got.LastUpdate)
				}
				want.LastUpdate = got.LastUpdate
				if !sameLibrary(got, want) {
					t.Fatalf("unexpected document:\n got %+v\nwant %+v", got, want)
				}
				checkRow(t, e, got)
				if read := getLibrary(t, e, e.Token(t, "root"), lib.ID); !sameLibrary(read, got) {
					t.Fatalf("GET differs from PATCH answer: %+v", read)
				}
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-SETTINGS-001", Case: "normal", State: "changed", Assertion: "update-settings-merge-over-previous-state",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:288-375"},
			Claim:   "PATCH /api/libraries/:id merges only the given known settings into the stored ones (numeric strings become numbers, unknown keys are dropped) and a second PATCH keeps what the first one set.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "settings")
				precedence := []any{"absMetadata", "audioMetatags"}
				first := patchLibrary(t, e, lib.ID, map[string]any{"settings": map[string]any{
					"audiobooksOnly": true, "markAsFinishedPercentComplete": "25", "markAsFinishedTimeRemaining": "5", "metadataPrecedence": precedence,
					"autoScanCronExpression": "0 3 * * *", "coverAspectRatio": 2, "bogus": true, "podcastSearchRegion": "de",
				}})
				want := bookSettings(map[string]any{
					"audiobooksOnly": true, "markAsFinishedPercentComplete": 25.0, "markAsFinishedTimeRemaining": 5.0, "metadataPrecedence": precedence,
					"autoScanCronExpression": "0 3 * * *", "coverAspectRatio": 2.0,
				})
				if !reflect.DeepEqual(first.Settings, want) {
					t.Fatalf("first merge: %v, want %v", first.Settings, want)
				}
				checkRow(t, e, first)
				second := patchLibrary(t, e, lib.ID, map[string]any{"settings": map[string]any{"markAsFinishedPercentComplete": nil, "disableWatcher": true, "autoScanCronExpression": nil}})
				want["markAsFinishedPercentComplete"], want["disableWatcher"], want["autoScanCronExpression"] = nil, true, nil
				if !reflect.DeepEqual(second.Settings, want) {
					t.Fatalf("second merge: %v, want %v", second.Settings, want)
				}
				checkRow(t, e, second)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-SETTINGS-001", Case: "edge", State: "initial", Assertion: "update-podcast-settings-and-region",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:288-328"},
			Claim:   "PATCH /api/libraries/:id on a podcast library accepts podcastSearchRegion, ignores book-only settings and keeps the other podcast defaults.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := createLibrary(t, e, map[string]any{"name": unique("podcast-update"), "mediaType": "podcast", "folders": []any{map[string]any{"fullPath": dir(e, "podcast-update")}}})
				got := patchLibrary(t, e, lib.ID, map[string]any{"settings": map[string]any{"podcastSearchRegion": "gb", "audiobooksOnly": true, "markAsFinishedTimeRemaining": 0}})
				if want := podcastSettings(map[string]any{"podcastSearchRegion": "gb", "markAsFinishedTimeRemaining": 0.0}); !reflect.DeepEqual(got.Settings, want) {
					t.Fatalf("settings %v, want %v", got.Settings, want)
				}
				checkRow(t, e, got)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-FIELDS-001", Case: "edge", State: "initial", Assertion: "update-empty-values-ignored-order-and-media-type",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:261-298"},
			Claim:   "PATCH /api/libraries/:id ignores empty or null name, icon and provider, stores a displayOrder shared with another library or zero as given without renumbering, and stores a new mediaType while leaving the settings document as it was.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "edge")
				other := newBookLibrary(t, e, "edge-other")
				got := patchLibrary(t, e, lib.ID, map[string]any{"name": "", "icon": nil, "provider": "", "displayOrder": other.DisplayOrder})
				if want := lib; !sameLibrary(withoutUpdate(got), withoutUpdate(want)) {
					t.Fatalf("empty values must be ignored and displayOrder stored:\n%+v", got)
				}
				if got.DisplayOrder != other.DisplayOrder || getLibrary(t, e, e.Token(t, "root"), other.ID).DisplayOrder != other.DisplayOrder {
					t.Fatalf("both libraries must share displayOrder %v", other.DisplayOrder)
				}
				zero := patchLibrary(t, e, lib.ID, map[string]any{"displayOrder": 0})
				if zero.DisplayOrder != 0 {
					t.Fatalf("displayOrder 0 must be stored, got %v", zero.DisplayOrder)
				}
				changed := patchLibrary(t, e, lib.ID, map[string]any{"mediaType": "podcast"})
				if changed.MediaType != "podcast" || !reflect.DeepEqual(changed.Settings, lib.Settings) {
					t.Fatalf("mediaType must be stored with settings untouched: %+v", changed)
				}
				checkRow(t, e, changed)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-FOLDERS-001", Case: "normal", State: "changed", Assertion: "update-adds-folder",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:377-412", "server/controllers/LibraryController.js:497-515"},
			Claim:   "PATCH /api/libraries/:id with the existing folder (by id) plus a new path creates the directory and a libraryFolders row, keeps the existing folder row, and answers both folders.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "addfolder")
				added := dir(e, "added")
				got := patchLibrary(t, e, lib.ID, map[string]any{"folders": []any{libraryFolderEntry(lib.Folders[0]), map[string]any{"fullPath": added}}})
				if len(got.Folders) != 2 || !slices.Equal(folderPaths(got), sortedCopy([]string{lib.Folders[0].FullPath, added})) {
					t.Fatalf("folders %+v", got.Folders)
				}
				for _, f := range got.Folders {
					if f.LibraryID != lib.ID || f.ID == "" {
						t.Fatalf("folder %+v", f)
					}
					if f.FullPath == lib.Folders[0].FullPath && (f.ID != lib.Folders[0].ID || f.AddedAt != lib.Folders[0].AddedAt) {
						t.Fatalf("existing folder row must be kept: %+v", f)
					}
				}
				if !isDir(added) {
					t.Fatal("new folder directory was not created")
				}
				checkRow(t, e, got)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-FOLDERS-001", Case: "normal", State: "changed", Assertion: "update-removes-folder-keeps-files",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:414-480"},
			Claim:   "PATCH /api/libraries/:id omitting a folder deletes its row but leaves the directory and its files on disk.",
			Run: func(t *testing.T, e *harness.Env) {
				first, second := existingDir(t, e, "keep-a"), existingDir(t, e, "keep-b")
				marker := filepath.Join(second, "book.mp3")
				if err := os.WriteFile(marker, []byte("audio"), 0o644); err != nil {
					t.Fatal(err)
				}
				lib := createLibrary(t, e, map[string]any{"name": unique("removefolder"), "folders": []any{map[string]any{"fullPath": first}, map[string]any{"fullPath": second}}})
				var keep folderJSON
				for _, f := range lib.Folders {
					if f.FullPath == first {
						keep = f
					}
				}
				got := patchLibrary(t, e, lib.ID, map[string]any{"folders": []any{libraryFolderEntry(keep)}})
				if len(got.Folders) != 1 || got.Folders[0].ID != keep.ID {
					t.Fatalf("folders %+v", got.Folders)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("files of a removed folder must stay on disk: %v", err)
				}
				checkRow(t, e, got)
				if n := e.Count(t, "SELECT COUNT(*) FROM libraryFolders WHERE path = ?", second); n != 0 {
					t.Fatalf("removed folder row still present")
				}
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-NOOP-001", Case: "edge", State: "repeat", Assertion: "update-unchanged-values-leave-row-untouched",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:288-375", "server/controllers/LibraryController.js:482-515"},
			Claim:   "PATCH /api/libraries/:id with an empty body or the values the library already has answers 200 with the same document and does not touch the stored row.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "noop")
				first := patchLibrary(t, e, lib.ID, map[string]any{"name": "Noop Library"})
				rowBefore, _ := dbLibrary(t, e, lib.ID)
				for _, body := range []map[string]any{
					{}, {"name": "Noop Library"}, {"icon": first.Icon, "provider": first.Provider, "displayOrder": first.DisplayOrder},
					{"settings": map[string]any{"audiobooksOnly": false, "metadataPrecedence": first.Settings["metadataPrecedence"], "markAsFinishedTimeRemaining": 10}},
				} {
					got := patchLibrary(t, e, lib.ID, body)
					if !sameLibrary(got, first) {
						t.Fatalf("body %v changed the document: %+v", body, got)
					}
				}
				if rowAfter, _ := dbLibrary(t, e, lib.ID); !reflect.DeepEqual(rowBefore, rowAfter) {
					t.Fatalf("row changed:\n%v\n%v", rowBefore, rowAfter)
				}
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-AUTH-001", Case: "error", State: "initial", Assertion: "update-requires-admin-and-existing-library",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:1475-1484", "server/controllers/LibraryController.js:255-259"},
			Claim:   "PATCH /api/libraries/:id answers 401 without a token, 403 for a non-admin user and for a limited user on an inaccessible library, and 404 for an unknown id as admin, changing nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "denied")
				limited := restrictedUser(t, e)
				before := snapshot(t, e)
				body := map[string]any{"name": "Hijacked"}
				path := "/api/libraries/" + lib.ID
				requireStatus(t, e.Patch(t, "", path, body), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Patch(t, e.Token(t, "listener"), path, body), http.StatusForbidden, "non-admin")
				requireStatus(t, e.Patch(t, limited.Token, path, body), http.StatusForbidden, "limited user")
				requireStatus(t, e.Patch(t, e.Token(t, "root"), "/api/libraries/00000000-0000-4000-8000-000000000000", body), http.StatusNotFound, "unknown id")
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-VALIDATION-001", Case: "error", State: "failure", Assertion: "update-invalid-bodies-rejected-without-effects",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:261-375"},
			Claim:   "PATCH /api/libraries/:id answers 400 for each invalid field or setting, even when valid fields accompany it, and leaves the library row unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "invalid")
				podcast := createLibrary(t, e, map[string]any{"name": unique("invalid-podcast"), "mediaType": "podcast", "folders": []any{map[string]any{"fullPath": dir(e, "invalid-podcast")}}})
				before := snapshot(t, e)
				settings := func(key string, value any) map[string]any {
					return map[string]any{"name": "Must Not Apply", "settings": map[string]any{key: value}}
				}
				cases := map[string]map[string]any{
					"name number":                  {"name": 5},
					"icon array":                   {"icon": []any{"x"}},
					"provider number":              {"provider": 5},
					"mediaType number":             {"mediaType": 5},
					"displayOrder not a number":    {"displayOrder": "abc"},
					"missing custom provider":      {"provider": "custom-does-not-exist"},
					"coverAspectRatio string":      settings("coverAspectRatio", "wide"),
					"disableWatcher string":        settings("disableWatcher", "yes"),
					"metadataPrecedence not array": settings("metadataPrecedence", "absMetadata"),
					"cron number":                  settings("autoScanCronExpression", 5),
					"percent not a number":         settings("markAsFinishedPercentComplete", "abc"),
					"percent above 100":            settings("markAsFinishedPercentComplete", 100.5),
					"percent negative":             settings("markAsFinishedPercentComplete", -1),
					"time remaining not a number":  settings("markAsFinishedTimeRemaining", "abc"),
					"time remaining negative":      settings("markAsFinishedTimeRemaining", -0.5),
				}
				for name, body := range cases {
					if resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+lib.ID, body); resp.Status != http.StatusBadRequest {
						t.Fatalf("%s: status %d, want 400 (%s)", name, resp.Status, resp.Body)
					}
				}
				if resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+podcast.ID, settings("podcastSearchRegion", 5)); resp.Status != http.StatusBadRequest {
					t.Fatalf("podcastSearchRegion number: status %d, want 400", resp.Status)
				}
				requireUnchanged(t, e, before)
			},
		},
		{
			Requirement: "route-r020", Invariant: "INV-R020-FOLDERS-001", Case: "error", State: "failure", Assertion: "update-uncreatable-folder-rejected",
			Sources: []string{updateRoute, "server/controllers/LibraryController.js:377-412"},
			Claim:   "PATCH /api/libraries/:id answers 400 when a new folder cannot be created and leaves the library and its folders unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "blocked")
				blocker := filepath.Join(e.Install, unique("blocker"))
				if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
					t.Fatal(err)
				}
				before := snapshot(t, e)
				resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+lib.ID, map[string]any{"name": "Must Not Apply", "folders": []any{libraryFolderEntry(lib.Folders[0]), map[string]any{"fullPath": filepath.Join(blocker, "sub")}}})
				requireStatus(t, resp, http.StatusBadRequest, "uncreatable folder")
				requireUnchanged(t, e, before)
			},
		},
	}
}

// withoutUpdate zeroes the fields that a change advances, to compare the rest.
func withoutUpdate(lib libraryJSON) libraryJSON {
	lib.LastUpdate = 0
	lib.DisplayOrder = 0
	return lib
}
