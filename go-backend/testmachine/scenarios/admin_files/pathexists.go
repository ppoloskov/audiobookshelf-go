package admin_files

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const pathExistsPath = "/api/filesystem/pathexists"

type pathExistsResult struct {
	Exists           bool    `json:"exists"`
	LibraryItemTitle *string `json:"libraryItemTitle"`
}

// pathExists posts a pathexists request and returns the response.
func pathExists(t *testing.T, e *harness.Env, token string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, pathExistsPath, body)
}

// wantExists asserts a 200 answer with the exists flag and optional item title.
func wantExists(t *testing.T, what string, resp harness.Response, exists bool, title string) {
	t.Helper()
	wantStatus(t, what, resp, http.StatusOK)
	var result pathExistsResult
	resp.JSON(t, &result)
	object := decode(resp)
	var fields map[string]any
	if title == "" {
		fields = exactKeys(t, object, "exists")
	} else {
		fields = exactKeys(t, object, "exists", "libraryItemTitle")
	}
	if _, ok := fields["exists"].(bool); !ok {
		t.Fatalf("exists is not boolean: %s", resp.Body)
	}
	if result.Exists != exists {
		t.Fatalf("%s: exists = %v, want %v (%s)", what, result.Exists, exists, resp.Body)
	}
	switch {
	case title == "" && result.LibraryItemTitle != nil:
		t.Fatalf("%s: unexpected libraryItemTitle %q", what, *result.LibraryItemTitle)
	case title != "" && (result.LibraryItemTitle == nil || *result.LibraryItemTitle != title):
		t.Fatalf("%s: libraryItemTitle missing or wrong, want %q: %s", what, title, resp.Body)
	}
}

func pathExistsScenarios() []harness.Scenario {
	const req = "route-r130"
	sources := []string{fsExists}
	body := func(folderPath, directory string) map[string]any {
		return map[string]any{"folderPath": folderPath, "directory": directory}
	}
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R130-ROUTE", "normal", "initial", "pathexists-existing-and-missing",
			"For a library folder path, an existing relative directory answers {exists:true} and a missing one {exists:false}, both without libraryItemTitle.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				wantExists(t, "existing", pathExists(t, e, root, body(folder, "Ada Example")), true, "")
				wantExists(t, "existing item dir", pathExists(t, e, root, body(folder, "Terry Fixture/Long Saga/Book 1 - The Beginning")), true, "")
				wantExists(t, "missing", pathExists(t, e, root, body(folder, "Nobody Here")), false, "")
				if _, err := os.Stat(filepath.Join(folder, "Nobody Here")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("the check must not create the directory")
				}
			}),
		scenario(req, "INV-ROUTE-R130-REPEAT", "normal", "repeat", "pathexists-repeat-is-stable",
			"Asking the same question twice gives the same answer and does not create the directory.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				for range 2 {
					wantExists(t, "repeat missing", pathExists(t, e, root, body(folder, "Repeat Author/Repeat Book")), false, "")
				}
				for range 2 {
					wantExists(t, "repeat existing", pathExists(t, e, root, body(folder, "Zed Disposable")), true, "")
				}
				if _, err := os.Stat(filepath.Join(folder, "Repeat Author")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("the check must not create the directory")
				}
			}),
		scenario(req, "INV-ROUTE-R130-ROUTE", "edge", "changed", "pathexists-follows-the-disk",
			"A directory reported missing is reported existing after it appears on disk, and missing again once it is removed.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				dir := filepath.Join(folder, "Changing Author", "Changing Book")
				wantExists(t, "before", pathExists(t, e, root, body(folder, "Changing Author/Changing Book")), false, "")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				wantExists(t, "after create", pathExists(t, e, root, body(folder, "Changing Author/Changing Book")), true, "")
				if err := os.RemoveAll(filepath.Join(folder, "Changing Author")); err != nil {
					t.Fatal(err)
				}
				wantExists(t, "after remove", pathExists(t, e, root, body(folder, "Changing Author/Changing Book")), false, "")
			}),
		scenario(req, "INV-ROUTE-R130-ROUTE", "edge", "initial", "pathexists-library-item-in-parent",
			"A missing directory below an existing library item (one or two levels deep) answers exists:true with that item's title; a third level answers false; a leading slash is accepted.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				root := e.Token(t, "root")
				wantExists(t, "one level", pathExists(t, e, root, body(folder, "Ada Example/Single File Novel/Part 2")), true, "Single File Novel")
				wantExists(t, "two levels", pathExists(t, e, root, body(folder, "Terry Fixture/Long Saga/Book 1 - The Beginning/a/b")), true, "The Beginning")
				wantExists(t, "three levels", pathExists(t, e, root, body(folder, "Terry Fixture/Long Saga/Book 1 - The Beginning/a/b/c")), false, "")
				wantExists(t, "no item above", pathExists(t, e, root, body(folder, "Nobody/Nothing/Part 2")), false, "")
				wantExists(t, "leading slash", pathExists(t, e, root, body(folder, "/Ada Example")), true, "")
			}),
		scenario(req, "INV-ROUTE-R130-ROUTE", "normal", "initial", "pathexists-podcast-folder-and-upload-user",
			"A non-admin user with upload permission and access to the library may check paths, also in a podcast library folder.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["podcastLibrary"])
				_, token := createUser(t, e, "pathexists-uploader", map[string]any{"upload": true}, nil)
				wantExists(t, "podcast existing", pathExists(t, e, token, body(folder, "Fixture Cast")), true, "")
				wantExists(t, "podcast missing", pathExists(t, e, token, body(folder, "Other Cast")), false, "")
			}),
		scenario(req, "INV-ROUTE-R130-ROUTE", "error", "failure", "pathexists-rejects-callers-and-bodies",
			"401 without a token, 403 to a user without upload permission (before the body is checked), and 400 for missing, empty or non-string directory or folderPath.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				good := body(folder, "Ada Example")
				_ = e.Token(t, "listener")
				_ = e.Token(t, "root")
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				anonymousAndListener(t, e, "POST", pathExistsPath, good)
				listener := pathExists(t, e, e.Token(t, "listener"), map[string]any{})
				wantStatus(t, "listener with invalid body", listener, http.StatusForbidden)
				root := e.Token(t, "root")
				for name, bad := range map[string]any{
					"empty":            map[string]any{},
					"no directory":     map[string]any{"folderPath": folder},
					"no folderPath":    map[string]any{"directory": "Ada Example"},
					"empty directory":  body(folder, ""),
					"empty folderPath": body("", "Ada Example"),
					"number directory": map[string]any{"folderPath": folder, "directory": 5},
					"list folderPath":  map[string]any{"folderPath": []string{folder}, "directory": "x"},
				} {
					wantStatus(t, name, pathExists(t, e, root, bad), http.StatusBadRequest)
				}
			}),
		scenario(req, "INV-ROUTE-R130-ROUTE", "error", "failure", "pathexists-rejects-folders-and-traversal",
			"404 for a folderPath that is no library folder (even a subdirectory of one), 403 for a user without access to the folder's library, and 400 when the directory leaves the library folder; no directory is created.",
			sources, func(t *testing.T, e *harness.Env) {
				_, folder := libraryFolder(t, e, e.IDs["library"])
				_, podcastFolder := libraryFolder(t, e, e.IDs["podcastLibrary"])
				root := e.Token(t, "root")
				_, podcastOnly := createUser(t, e, "pathexists-podcast-only", map[string]any{"upload": true}, []string{e.IDs["podcastLibrary"]})
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				wantStatus(t, "unknown folder", pathExists(t, e, root, body(filepath.Join(folder, "Ada Example"), "Single File Novel")), http.StatusNotFound)
				wantStatus(t, "folder of nothing", pathExists(t, e, root, body("/nonexistent/folder", "x")), http.StatusNotFound)
				for _, traversal := range []string{"../outside", "Ada Example/../../outside", "../" + filepath.Base(folder) + "x"} {
					wantStatus(t, "traversal "+traversal, pathExists(t, e, root, body(folder, traversal)), http.StatusBadRequest)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(folder), "outside")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("outside directory must not exist")
				}
				wantStatus(t, "no access", pathExists(t, e, podcastOnly, body(folder, "Ada Example")), http.StatusForbidden)
				wantStatus(t, "no access before traversal", pathExists(t, e, podcastOnly, body(folder, "../outside")), http.StatusForbidden)
				wantStatus(t, "unknown folder before access", pathExists(t, e, podcastOnly, body("/nonexistent/folder", "x")), http.StatusNotFound)
				wantExists(t, "own library", pathExists(t, e, podcastOnly, body(podcastFolder, "Fixture Cast")), true, "")
			}),
	}
}
