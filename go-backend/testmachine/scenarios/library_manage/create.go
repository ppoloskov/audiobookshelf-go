package library_manage

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	createSrc   = "server/controllers/LibraryController.js:50-176"
	createRoute = "server/routers/ApiRouter.js:69"
)

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r017", Invariant: "INV-R017-SAMPLED", Case: "normal", State: "initial", Assertion: "create-book-library-defaults-and-rows",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:80-87", "server/controllers/LibraryController.js:148-176", "server/models/Library.js:56-82", "server/models/Library.js:202-219"},
			Claim:   "POST /api/libraries with only name and one folder creates a book library with provider google, icon database, default settings, displayOrder max+1, the folder directory on disk, matching library and libraryFolders rows, and answers the same document a later GET returns.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				folder := dir(e, "created")
				maxOrder := e.Count(t, "SELECT MAX(displayOrder) FROM libraries")
				start := time.Now().UnixMilli()
				resp := e.Post(t, root, "/api/libraries", map[string]any{"name": "Created Library", "folders": []any{map[string]any{"fullPath": folder}}})
				requireStatus(t, resp, http.StatusOK, "create")
				lib := decodeLibrary(t, resp)
				if lib.ID == "" || lib.Name != "Created Library" || lib.MediaType != "book" || lib.Provider != "google" || lib.Icon != "database" || int(lib.DisplayOrder) != maxOrder+1 {
					t.Fatalf("unexpected defaults: %s", resp.Body)
				}
				if !reflect.DeepEqual(lib.Settings, bookSettings(nil)) {
					t.Fatalf("settings are not the book defaults: %v", lib.Settings)
				}
				if len(lib.Folders) != 1 || lib.Folders[0].FullPath != folder || lib.Folders[0].LibraryID != lib.ID || lib.Folders[0].ID == "" || lib.Folders[0].AddedAt < start-1000 {
					t.Fatalf("unexpected folders: %+v", lib.Folders)
				}
				if lib.LastScan != nil || lib.CreatedAt < start || lib.LastUpdate < start {
					t.Fatalf("lastScan must be null and timestamps current: %s", resp.Body)
				}
				if !isDir(folder) {
					t.Fatalf("folder %s was not created on disk", folder)
				}
				checkRow(t, e, lib)
				if got := getLibrary(t, e, root, lib.ID); !sameLibrary(got, lib) {
					t.Fatalf("GET differs from create response:\n%+v\n%+v", got, lib)
				}
				row, _ := dbLibrary(t, e, lib.ID)
				if row["lastScan"] != nil {
					t.Fatalf("row lastScan = %v, want NULL", row["lastScan"])
				}
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-SETTINGS-001", Case: "normal", State: "initial", Assertion: "create-podcast-explicit-fields-and-settings",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:80-120", "server/models/Library.js:57-64"},
			Claim:   "POST /api/libraries honours mediaType, icon, provider and valid podcast settings (numeric strings become numbers), ignores settings keys the media type does not have, and keeps the other defaults.",
			Run: func(t *testing.T, e *harness.Env) {
				folder := dir(e, "podcast")
				lib := createLibrary(t, e, map[string]any{
					"name": "Created Podcasts", "mediaType": "podcast", "icon": "podcast", "provider": "itunes",
					"folders": []any{map[string]any{"fullPath": folder}},
					"settings": map[string]any{
						"podcastSearchRegion": "de", "markAsFinishedTimeRemaining": "30", "markAsFinishedPercentComplete": 80,
						"coverAspectRatio": 2, "disableWatcher": true, "audiobooksOnly": true, "unknownKey": 1,
					},
				})
				want := podcastSettings(map[string]any{"podcastSearchRegion": "de", "markAsFinishedTimeRemaining": 30.0, "markAsFinishedPercentComplete": 80.0, "coverAspectRatio": 2.0, "disableWatcher": true})
				if lib.MediaType != "podcast" || lib.Icon != "podcast" || lib.Provider != "itunes" || !reflect.DeepEqual(lib.Settings, want) {
					t.Fatalf("unexpected podcast library: %+v", lib)
				}
				checkRow(t, e, lib)
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-SETTINGS-001", Case: "edge", State: "initial", Assertion: "create-book-settings-variants",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:86-120"},
			Claim:   "POST /api/libraries accepts book settings including a custom metadataPrecedence, a cron expression, null mark-as-finished values and boolean flags, and treats an empty autoScanCronExpression as not given.",
			Run: func(t *testing.T, e *harness.Env) {
				precedence := []any{"absMetadata", "folderStructure"}
				lib := createLibrary(t, e, map[string]any{
					"name": unique("variants"), "folders": []any{map[string]any{"fullPath": dir(e, "variants")}},
					"settings": map[string]any{
						"metadataPrecedence": precedence, "autoScanCronExpression": "0 2 * * *", "markAsFinishedPercentComplete": nil,
						"markAsFinishedTimeRemaining": nil, "skipMatchingMediaWithAsin": true, "hideSingleBookSeries": true,
					},
				})
				want := bookSettings(map[string]any{
					"metadataPrecedence": precedence, "autoScanCronExpression": "0 2 * * *", "markAsFinishedTimeRemaining": nil,
					"skipMatchingMediaWithAsin": true, "hideSingleBookSeries": true,
				})
				if !reflect.DeepEqual(lib.Settings, want) {
					t.Fatalf("settings %v, want %v", lib.Settings, want)
				}
				checkRow(t, e, lib)
				empty := createLibrary(t, e, map[string]any{
					"name": unique("empty-cron"), "folders": []any{map[string]any{"fullPath": dir(e, "empty-cron")}},
					"settings": map[string]any{"autoScanCronExpression": ""},
				})
				if !reflect.DeepEqual(empty.Settings, bookSettings(nil)) {
					t.Fatalf("an empty cron expression must leave the default: %v", empty.Settings)
				}
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-FOLDERS-001", Case: "edge", State: "initial", Assertion: "create-folder-path-key-existing-and-multiple",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:61-69", "server/controllers/LibraryController.js:131-146"},
			Claim:   "POST /api/libraries accepts folders keyed by path as well as fullPath, keeps an existing directory and its files, creates missing ones, and stores one libraryFolders row per folder.",
			Run: func(t *testing.T, e *harness.Env) {
				existing := existingDir(t, e, "existing")
				marker := filepath.Join(existing, "keep.txt")
				if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
				missing := filepath.Join(dir(e, "nested"), "deeper")
				lib := createLibrary(t, e, map[string]any{
					"name":    unique("multi"),
					"folders": []any{map[string]any{"path": existing}, map[string]any{"fullPath": missing}},
				})
				if want := []string{existing, missing}; !slices.Equal(folderPaths(lib), sortedCopy(want)) {
					t.Fatalf("folders %v, want %v", folderPaths(lib), want)
				}
				if !isDir(missing) {
					t.Fatal("missing nested folder was not created")
				}
				if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
					t.Fatalf("existing folder content must be untouched: %v %q", err, data)
				}
				checkRow(t, e, lib)
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-SAMPLED", Case: "normal", State: "repeat", Assertion: "create-same-body-twice-makes-two-libraries",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:131-165"},
			Claim:   "Posting the identical create body twice creates two distinct libraries with distinct folder rows and consecutive display orders.",
			Run: func(t *testing.T, e *harness.Env) {
				body := map[string]any{"name": "Twin Library", "folders": []any{map[string]any{"fullPath": dir(e, "twin")}}}
				first := createLibrary(t, e, body)
				second := createLibrary(t, e, body)
				if first.ID == second.ID || second.DisplayOrder != first.DisplayOrder+1 || first.Folders[0].ID == second.Folders[0].ID {
					t.Fatalf("repeat create must make a second library: %+v %+v", first, second)
				}
				if first.Folders[0].FullPath != second.Folders[0].FullPath {
					t.Fatalf("both libraries must point at the same folder path")
				}
				checkRow(t, e, first)
				checkRow(t, e, second)
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-SAMPLED", Case: "normal", State: "changed", Assertion: "create-visible-in-list-and-per-user",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:188-205", "server/models/User.js:646-650"},
			Claim:   "A created library appears at the end of GET /api/libraries for an admin and an all-access user, is absent for a user limited to other libraries, and that user gets 403 on GET /api/libraries/:id.",
			Run: func(t *testing.T, e *harness.Env) {
				lib := newBookLibrary(t, e, "visible")
				restricted := restrictedUser(t, e)
				for _, token := range []string{e.Token(t, "root"), e.Token(t, "listener")} {
					libs := listLibraries(t, e, token)
					if last := libs[len(libs)-1]; !sameLibrary(last, lib) {
						t.Fatalf("created library must be last in the list: %+v", last)
					}
				}
				if slices.Contains(libraryIDs(listLibraries(t, e, restricted.Token)), lib.ID) {
					t.Fatal("restricted user must not see the new library")
				}
				requireStatus(t, e.Get(t, restricted.Token, "/api/libraries/"+lib.ID), http.StatusForbidden, "restricted GET")
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-CUSTOM-001", Case: "edge", State: "initial", Assertion: "create-with-existing-custom-provider",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:123-129"},
			Claim:   "POST /api/libraries accepts a custom-<id> provider that exists and stores it; a custom provider that does not exist is rejected with 400 and creates nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				resp := e.Post(t, root, "/api/custom-metadata-providers", map[string]any{"name": unique("provider"), "url": "http://127.0.0.1:9/search", "mediaType": "book"})
				var created struct {
					Provider struct {
						ID string `json:"id"`
					} `json:"provider"`
				}
				resp.JSON(t, &created)
				requireStatus(t, resp, http.StatusOK, "create custom provider")
				slug := "custom-" + created.Provider.ID
				lib := createLibrary(t, e, map[string]any{"name": unique("custom"), "provider": slug, "folders": []any{map[string]any{"fullPath": dir(e, "custom")}}})
				if lib.Provider != slug {
					t.Fatalf("provider %q, want %q", lib.Provider, slug)
				}
				checkRow(t, e, lib)
				before := snapshot(t, e)
				folder := dir(e, "custom-missing")
				resp = e.Post(t, root, "/api/libraries", map[string]any{"name": unique("custom"), "provider": "custom-does-not-exist", "folders": []any{map[string]any{"fullPath": folder}}})
				requireStatus(t, resp, http.StatusBadRequest, "missing custom provider")
				requireUnchanged(t, e, before)
				if isDir(folder) {
					t.Fatal("a rejected create must not create the folder")
				}
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-AUTH-001", Case: "error", State: "initial", Assertion: "create-requires-admin",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:51-54", "server/Auth.js:54-62"},
			Claim:   "POST /api/libraries answers 401 without a token and 403 for a non-admin user, and creates no row and no folder.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshot(t, e)
				folder := dir(e, "denied")
				body := map[string]any{"name": "Denied", "folders": []any{map[string]any{"fullPath": folder}}}
				requireStatus(t, e.Post(t, "", "/api/libraries", body), http.StatusUnauthorized, "anonymous")
				requireStatus(t, e.Post(t, e.Token(t, "listener"), "/api/libraries", body), http.StatusForbidden, "non-admin")
				requireUnchanged(t, e, before)
				if isDir(folder) {
					t.Fatal("a denied create must not create the folder")
				}
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-VALIDATION-001", Case: "error", State: "failure", Assertion: "create-invalid-bodies-rejected-without-effects",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:56-120"},
			Claim:   "POST /api/libraries answers 400 for each invalid body (name, folders, mediaType/icon/provider types, settings shape and values), and leaves rows, folders and the folder directory untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				before := snapshot(t, e)
				folder := dir(e, "invalid")
				ok := []any{map[string]any{"fullPath": folder}}
				cases := map[string]map[string]any{
					"missing name":                 {"folders": ok},
					"empty name":                   {"name": "", "folders": ok},
					"numeric name":                 {"name": 5, "folders": ok},
					"missing folders":              {"name": "x"},
					"folders not an array":         {"name": "x", "folders": "path"},
					"folder without path":          {"name": "x", "folders": []any{map[string]any{}}},
					"folder path not a string":     {"name": "x", "folders": []any{map[string]any{"fullPath": 7}}},
					"folder entry null":            {"name": "x", "folders": []any{nil}},
					"mediaType not a string":       {"name": "x", "folders": ok, "mediaType": 5},
					"icon not a string":            {"name": "x", "folders": ok, "icon": []any{}},
					"provider not a string":        {"name": "x", "folders": ok, "provider": 5},
					"settings array":               {"name": "x", "folders": ok, "settings": []any{}},
					"settings string":              {"name": "x", "folders": ok, "settings": "s"},
					"coverAspectRatio string":      {"name": "x", "folders": ok, "settings": map[string]any{"coverAspectRatio": "wide"}},
					"disableWatcher string":        {"name": "x", "folders": ok, "settings": map[string]any{"disableWatcher": "yes"}},
					"metadataPrecedence not array": {"name": "x", "folders": ok, "settings": map[string]any{"metadataPrecedence": "absMetadata"}},
					"cron not a string":            {"name": "x", "folders": ok, "settings": map[string]any{"autoScanCronExpression": 5}},
					"percent not a number":         {"name": "x", "folders": ok, "settings": map[string]any{"markAsFinishedPercentComplete": "abc"}},
					"percent above 100":            {"name": "x", "folders": ok, "settings": map[string]any{"markAsFinishedPercentComplete": 101}},
					"percent below 0":              {"name": "x", "folders": ok, "settings": map[string]any{"markAsFinishedPercentComplete": -1}},
					"time remaining not a number":  {"name": "x", "folders": ok, "settings": map[string]any{"markAsFinishedTimeRemaining": "abc"}},
					"time remaining negative":      {"name": "x", "folders": ok, "settings": map[string]any{"markAsFinishedTimeRemaining": -1}},
				}
				for name, body := range cases {
					if resp := e.Post(t, root, "/api/libraries", body); resp.Status != http.StatusBadRequest {
						t.Fatalf("%s: status %d, want 400 (%s)", name, resp.Status, resp.Body)
					}
				}
				requireUnchanged(t, e, before)
				if isDir(folder) {
					t.Fatal("rejected creates must not create the folder")
				}
			},
		},
		{
			Requirement: "route-r017", Invariant: "INV-R017-FOLDERS-001", Case: "error", State: "failure", Assertion: "create-uncreatable-folder-rejected",
			Sources: []string{createRoute, "server/controllers/LibraryController.js:131-146"},
			Claim:   "POST /api/libraries answers 400 when a folder cannot be created (its parent is a regular file) and creates no library row.",
			Run: func(t *testing.T, e *harness.Env) {
				blocker := filepath.Join(e.Install, unique("blocker"))
				if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
					t.Fatal(err)
				}
				before := snapshot(t, e)
				resp := e.Post(t, e.Token(t, "root"), "/api/libraries", map[string]any{"name": "Blocked", "folders": []any{map[string]any{"fullPath": filepath.Join(blocker, "sub")}}})
				requireStatus(t, resp, http.StatusBadRequest, "uncreatable folder")
				requireUnchanged(t, e, before)
			},
		},
	}
}

func sortedCopy(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}
