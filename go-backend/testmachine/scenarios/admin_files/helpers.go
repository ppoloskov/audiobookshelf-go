package admin_files

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// scenario builds a harness.Scenario with one claim and its upstream sources.
func scenario(requirement, invariant, caseName, state, assertion, claim string, sources []string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: caseName, State: state,
		Assertion: assertion, Sources: sources, Claim: claim, Run: run,
	}
}

// wantStatus fails unless the response has exactly the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want status %d, got %d: %.300s", what, status, resp.Status, resp.Body)
	}
}

// anonymousAndListener asserts 401 without a token and 403 for the (non-admin,
// no-upload) fixture listener.
func anonymousAndListener(t *testing.T, e *harness.Env, method, path string, body any) {
	t.Helper()
	anonymous := e.Do(t, harness.Request{Method: method, Path: path, JSON: body})
	wantStatus(t, method+" "+path+" without token", anonymous, http.StatusUnauthorized)
	listener := e.Do(t, harness.Request{Method: method, Path: path, JSON: body, Token: e.Token(t, "listener")})
	wantStatus(t, method+" "+path+" as listener", listener, http.StatusForbidden)
}

// libraryFolder returns the id and the full path (as the server reports it) of
// the first folder of a library.
func libraryFolder(t *testing.T, e *harness.Env, libraryID string) (folderID, fullPath string) {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID)
	wantStatus(t, "get library", resp, http.StatusOK)
	object := resp.Object(t)
	folderID, _ = harness.Dig(object, "folders", 0, "id").(string)
	fullPath, _ = harness.Dig(object, "folders", 0, "fullPath").(string)
	if folderID == "" || fullPath == "" {
		t.Fatalf("library %s has no folder: %s", libraryID, resp.Body)
	}
	return folderID, fullPath
}

// createUser creates an active user through the API and returns its id and token.
func createUser(t *testing.T, e *harness.Env, username string, permissions map[string]any, libraries []string) (id, token string) {
	t.Helper()
	body := map[string]any{"username": username, "password": "pw-" + username, "type": "user", "isActive": true, "permissions": permissions}
	if libraries != nil {
		body["librariesAccessible"] = libraries
		permissions["accessAllLibraries"] = false
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	wantStatus(t, "create user "+username, resp, http.StatusOK)
	id, _ = harness.Dig(resp.Object(t), "user", "id").(string)
	token, _ = e.Login(t, username, "pw-"+username)
	return id, token
}

// sameList fails unless the two string lists are equal.
func sameList(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// sortedStrings returns a sorted copy.
func sortedStrings(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// decode returns the response body as a decoded JSON value (nil when invalid).
func decode(resp harness.Response) any {
	var value any
	if err := json.Unmarshal(resp.Body, &value); err != nil {
		return nil
	}
	return value
}

// unchanged records database rows and the media and metadata trees around a read-only rejection.
func unchanged(t *testing.T, e *harness.Env) func() {
	t.Helper()
	return unchangedExcept(t, e, "logs") // rejections are logged; diagnostic logs are not product state
}

// unchangedExcept is unchanged with further metadata subdirectories excluded.
// An upload stages multipart files in metadata/tmp before the route runs.
func unchangedExcept(t *testing.T, e *harness.Env, skip ...string) func() {
	t.Helper()
	excluded := make(map[string]bool, len(skip))
	for _, name := range skip {
		excluded[filepath.Join(e.Metadata, name)] = true
	}
	snapshot := func() any {
		tables := e.Rows(t, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		rows := make(map[string][]string)
		for _, table := range tables {
			name, ok := table["name"].(string)
			if !ok {
				t.Fatalf("unexpected table name: %v", table["name"])
			}
			for _, row := range e.Rows(t, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`) {
				data, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				rows[name] = append(rows[name], string(data))
			}
			slices.Sort(rows[name])
		}
		files := make(map[string]string)
		for _, root := range []string{e.Media, e.Metadata} {
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if excluded[path] {
					return filepath.SkipDir
				}
				if entry.Type().IsRegular() {
					file, err := os.Open(path)
					if err != nil {
						return err
					}
					hash := sha256.New()
					_, copyErr := io.Copy(hash, file)
					closeErr := file.Close()
					if copyErr != nil {
						return copyErr
					}
					if closeErr != nil {
						return closeErr
					}
					files[path] = fmt.Sprintf("%x", hash.Sum(nil))
				} else {
					files[path] = entry.Type().String()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return struct {
			Rows  map[string][]string
			Files map[string]string
		}{rows, files}
	}
	before := snapshot()
	return func() {
		t.Helper()
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Error("rejected requests changed database rows or media/metadata files")
		}
	}
}

// exactKeys checks presence and absence of response fields, including false and null values.
func exactKeys(t *testing.T, value any, keys ...string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", value)
	}
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(keys)
	sameList(t, "JSON keys", got, keys)
	return object
}
