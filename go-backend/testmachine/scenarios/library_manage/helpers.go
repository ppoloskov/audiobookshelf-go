package library_manage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// folderJSON is a library folder as clients read it.
type folderJSON struct {
	ID        string `json:"id"`
	FullPath  string `json:"fullPath"`
	LibraryID string `json:"libraryId"`
	AddedAt   int64  `json:"addedAt"`
}

// libraryJSON is a library as clients read it. Settings is an open document
// whose keys depend on the media type, so tests compare it as a map.
type libraryJSON struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Folders         []folderJSON   `json:"folders"`
	DisplayOrder    float64        `json:"displayOrder"`
	Icon            string         `json:"icon"`
	MediaType       string         `json:"mediaType"`
	Provider        string         `json:"provider"`
	Settings        map[string]any `json:"settings"`
	LastScan        *int64         `json:"lastScan"`
	LastScanVersion *string        `json:"lastScanVersion"`
	CreatedAt       int64          `json:"createdAt"`
	LastUpdate      int64          `json:"lastUpdate"`
}

const userPassword = "library-manage-pw"

var counter atomic.Int64

// unique returns a name that no earlier call of the same run produced.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, counter.Add(1))
}

// dir returns a not yet existing directory path under the install.
func dir(e *harness.Env, prefix string) string {
	return filepath.Join(e.Install, unique(prefix))
}

// existingDir creates and returns a directory under the install.
func existingDir(t *testing.T, e *harness.Env, prefix string) string {
	t.Helper()
	path := dir(e, prefix)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func decodeLibrary(t *testing.T, resp harness.Response) libraryJSON {
	t.Helper()
	var lib libraryJSON
	resp.JSON(t, &lib)
	return lib
}

// createLibrary creates a library as root and returns it; it fails the test on any other status.
func createLibrary(t *testing.T, e *harness.Env, body map[string]any) libraryJSON {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/libraries", body)
	if resp.Status != http.StatusOK {
		t.Fatalf("setup: create library returned %d %s", resp.Status, resp.Body)
	}
	return decodeLibrary(t, resp)
}

// newBookLibrary creates a book library over a fresh directory.
func newBookLibrary(t *testing.T, e *harness.Env, prefix string) libraryJSON {
	t.Helper()
	return createLibrary(t, e, map[string]any{"name": unique(prefix), "folders": []any{map[string]any{"fullPath": dir(e, prefix)}}})
}

func listLibraries(t *testing.T, e *harness.Env, token string) []libraryJSON {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries")
	var body struct {
		Libraries []libraryJSON `json:"libraries"`
	}
	resp.JSON(t, &body)
	if resp.Status != http.StatusOK {
		t.Fatalf("list libraries returned %d %s", resp.Status, resp.Body)
	}
	return body.Libraries
}

func getLibrary(t *testing.T, e *harness.Env, token, id string) libraryJSON {
	t.Helper()
	resp := e.Get(t, token, "/api/libraries/"+id)
	if resp.Status != http.StatusOK {
		t.Fatalf("get library %s returned %d %s", id, resp.Status, resp.Body)
	}
	return decodeLibrary(t, resp)
}

// sameLibrary compares two library documents. create omits lastScanVersion
// (the key is absent, a read returns null), so that field is not compared.
func sameLibrary(a, b libraryJSON) bool {
	a.LastScanVersion, b.LastScanVersion = nil, nil
	return reflect.DeepEqual(a, b)
}

func libraryIDs(libs []libraryJSON) []string {
	ids := make([]string, len(libs))
	for i, lib := range libs {
		ids[i] = lib.ID
	}
	return ids
}

func folderPaths(lib libraryJSON) []string {
	paths := make([]string, len(lib.Folders))
	for i, f := range lib.Folders {
		paths[i] = f.FullPath
	}
	slices.Sort(paths)
	return paths
}

// bookSettings and podcastSettings are the defaults a new library gets.
func bookSettings(overrides map[string]any) map[string]any {
	settings := map[string]any{
		"coverAspectRatio": 1.0, "disableWatcher": false, "autoScanCronExpression": nil,
		"skipMatchingMediaWithAsin": false, "skipMatchingMediaWithIsbn": false, "audiobooksOnly": false,
		"epubsAllowScriptedContent": false, "hideSingleBookSeries": false, "onlyShowLaterBooksInContinueSeries": false,
		"metadataPrecedence":            []any{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"},
		"markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10.0,
	}
	for key, value := range overrides {
		settings[key] = value
	}
	return settings
}

func podcastSettings(overrides map[string]any) map[string]any {
	settings := map[string]any{
		"coverAspectRatio": 1.0, "disableWatcher": false, "autoScanCronExpression": nil, "podcastSearchRegion": "us",
		"markAsFinishedPercentComplete": nil, "markAsFinishedTimeRemaining": 10.0,
	}
	for key, value := range overrides {
		settings[key] = value
	}
	return settings
}

// dbLibrary returns the libraries row and the folder paths stored for it.
func dbLibrary(t *testing.T, e *harness.Env, id string) (row map[string]any, folders []string) {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM libraries WHERE id = ?", id)
	if len(rows) == 1 {
		row = rows[0]
	}
	for _, folder := range e.Rows(t, "SELECT path FROM libraryFolders WHERE libraryId = ? ORDER BY path", id) {
		folders = append(folders, fmt.Sprint(folder["path"]))
	}
	return row, folders
}

// dbSettings decodes the settings column of a library row.
func dbSettings(t *testing.T, row map[string]any) map[string]any {
	t.Helper()
	var settings map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(row["settings"])), &settings); err != nil {
		t.Fatalf("settings column is not JSON: %v (%v)", err, row["settings"])
	}
	return settings
}

// checkRow asserts the persisted row agrees with the API document.
func checkRow(t *testing.T, e *harness.Env, lib libraryJSON) {
	t.Helper()
	row, folders := dbLibrary(t, e, lib.ID)
	if row == nil {
		t.Fatalf("library %s has no row", lib.ID)
	}
	if row["name"] != lib.Name || row["mediaType"] != lib.MediaType || row["provider"] != lib.Provider || row["icon"] != lib.Icon {
		t.Fatalf("row %v disagrees with API document %+v", row, lib)
	}
	if fmt.Sprint(row["displayOrder"]) != fmt.Sprint(int64(lib.DisplayOrder)) {
		t.Fatalf("row displayOrder %v, API %v", row["displayOrder"], lib.DisplayOrder)
	}
	if got := dbSettings(t, row); !reflect.DeepEqual(got, lib.Settings) {
		t.Fatalf("row settings %v, API %v", got, lib.Settings)
	}
	if want := folderPaths(lib); !slices.Equal(folders, want) {
		t.Fatalf("row folders %v, API %v", folders, want)
	}
}

// libraryState is a snapshot of persisted library data, to prove a rejected request changed nothing.
type libraryState struct {
	Libraries []map[string]any
	Folders   []map[string]any
}

func snapshot(t *testing.T, e *harness.Env) libraryState {
	t.Helper()
	return libraryState{
		Libraries: e.Rows(t, "SELECT * FROM libraries ORDER BY id"),
		Folders:   e.Rows(t, "SELECT * FROM libraryFolders ORDER BY id"),
	}
}

func requireUnchanged(t *testing.T, e *harness.Env, before libraryState) {
	t.Helper()
	if after := snapshot(t, e); !reflect.DeepEqual(before, after) {
		t.Fatalf("a rejected request changed persisted library data:\nbefore %v\nafter  %v", before, after)
	}
}

func requireStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d (%s)", what, resp.Status, want, resp.Body)
	}
}

// accountUser is a user created through the API for access scenarios.
type accountUser struct {
	ID, Token string
}

// createUser creates a regular user. librariesAccessible is only meaningful when accessAll is false.
func createUser(t *testing.T, e *harness.Env, prefix string, accessAll bool, accessible []string) accountUser {
	t.Helper()
	name := unique(prefix)
	if accessible == nil {
		accessible = []string{}
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", map[string]any{
		"username": name, "password": userPassword, "type": "user", "isActive": true,
		"permissions": map[string]any{"accessAllLibraries": accessAll}, "librariesAccessible": accessible,
	})
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &body)
	requireStatus(t, resp, http.StatusOK, "create user")
	token, _ := e.Login(t, name, userPassword)
	return accountUser{ID: body.User.ID, Token: token}
}

// restrictedUser returns a user that may access only the fixture podcast library.
func restrictedUser(t *testing.T, e *harness.Env) accountUser {
	t.Helper()
	return createUser(t, e, "lm-restricted", false, []string{e.IDs["podcastLibrary"]})
}
