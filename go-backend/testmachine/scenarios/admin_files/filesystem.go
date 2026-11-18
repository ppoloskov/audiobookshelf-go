package admin_files

import (
	"cmp"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	fsController = "server/controllers/FileSystemController.js"
	fsBrowse     = fsController + ":24-77"
	fsExists     = fsController + ":85-160"
)

type directoryEntry struct {
	Path    string `json:"path"`
	Dirname string `json:"dirname"`
	Level   int    `json:"level"`
}

type browseResult struct {
	Posix       bool             `json:"posix"`
	Directories []directoryEntry `json:"directories"`
}

func browse(t *testing.T, e *harness.Env, token string, query url.Values) (harness.Response, browseResult) {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/filesystem", Token: token, Query: query})
	var result browseResult
	if resp.Status == http.StatusOK {
		resp.JSON(t, &result)
		object := decode(resp)
		decoded := exactKeys(t, object, "posix", "directories")
		if _, ok := decoded["posix"].(bool); !ok {
			t.Fatalf("posix is not boolean: %s", resp.Body)
		}
		directories, ok := decoded["directories"].([]any)
		if !ok {
			t.Fatalf("directories is not an array: %s", resp.Body)
		}
		for _, directory := range directories {
			entry := exactKeys(t, directory, "path", "dirname", "level")
			if _, ok := entry["path"].(string); !ok {
				t.Fatal("path is not a string")
			}
			if _, ok := entry["dirname"].(string); !ok {
				t.Fatal("dirname is not a string")
			}
			if _, ok := entry["level"].(float64); !ok {
				t.Fatal("level is not numeric")
			}
		}
	}
	return resp, result
}

// browseDir creates a directory with known content below the install and returns its path.
func browseDir(t *testing.T, e *harness.Env) string {
	t.Helper()
	dir := filepath.Join(e.Install, "browse-fixture")
	for _, sub := range []string{"alpha", "beta/inner", ".hidden", "Ünï code"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink to a directory is not a directory for lstat, so it is not listed.
	if err := os.Symlink(filepath.Join(dir, "alpha"), filepath.Join(dir, "link")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	return dir
}

func filesystemScenarios() []harness.Scenario {
	const req = "route-r129"
	return []harness.Scenario{
		scenario(req, "INV-ROUTE-R129-ROUTE", "normal", "stateless", "filesystem-lists-install-directories",
			"GET /api/filesystem?path=<install> as admin lists exactly the install's config, media and metadata directories as {path, dirname, level 0} with posix true.",
			[]string{fsBrowse}, func(t *testing.T, e *harness.Env) {
				resp, result := browse(t, e, e.Token(t, "root"), url.Values{"path": {e.Install}})
				wantStatus(t, "browse install", resp, http.StatusOK)
				if !result.Posix {
					t.Errorf("posix = false")
				}
				var names []string
				for _, d := range result.Directories {
					names = append(names, d.Dirname)
					if d.Level != 0 || d.Path != filepath.ToSlash(filepath.Join(e.Install, d.Dirname)) {
						t.Errorf("entry %+v, want level 0 and path under the install", d)
					}
				}
				sameList(t, "directories", sortedStrings(names), []string{"config", "media", "metadata"})
			}),
		scenario(req, "INV-ROUTE-R129-ROUTE", "normal", "stateless", "filesystem-directories-only-and-level",
			"Only real directories are listed (files and symlinks are not; dot directories and Unicode names are), and the level query value is echoed numerically in every entry.",
			[]string{fsBrowse, "server/utils/fileUtils.js:509-535", "server/utils/index.js:168-171"}, func(t *testing.T, e *harness.Env) {
				dir := browseDir(t, e)
				resp, result := browse(t, e, e.Token(t, "root"), url.Values{"path": {dir}, "level": {"2"}})
				wantStatus(t, "browse", resp, http.StatusOK)
				var names []string
				for _, d := range result.Directories {
					names = append(names, d.Dirname)
					if d.Level != 2 {
						t.Errorf("%s level = %d, want 2", d.Dirname, d.Level)
					}
					if d.Path != filepath.ToSlash(filepath.Join(dir, d.Dirname)) {
						t.Errorf("%s path = %q", d.Dirname, d.Path)
					}
				}
				sameList(t, "directories", sortedStrings(names), []string{".hidden", "alpha", "beta", "Ünï code"})
				_, nested := browse(t, e, e.Token(t, "root"), url.Values{"path": {filepath.Join(dir, "beta")}})
				if len(nested.Directories) != 1 || nested.Directories[0].Dirname != "inner" {
					t.Errorf("nested = %+v, want only inner (not recursive)", nested.Directories)
				}
			}),
		scenario(req, "INV-ROUTE-R129-ROUTE", "edge", "stateless", "filesystem-root-and-level-fallback",
			"Without a path the server lists the filesystem root, and a non-numeric level falls back to 0.",
			[]string{fsBrowse, "server/utils/index.js:168-171"}, func(t *testing.T, e *harness.Env) {
				resp, result := browse(t, e, e.Token(t, "root"), url.Values{"level": {"abc"}})
				wantStatus(t, "browse root", resp, http.StatusOK)
				explicitResp, explicit := browse(t, e, e.Token(t, "root"), url.Values{"path": {"/"}, "level": {"abc"}})
				wantStatus(t, "browse explicit root", explicitResp, http.StatusOK)
				slices.SortFunc(result.Directories, func(a, b directoryEntry) int { return cmp.Compare(a.Path, b.Path) })
				slices.SortFunc(explicit.Directories, func(a, b directoryEntry) int { return cmp.Compare(a.Path, b.Path) })
				if !reflect.DeepEqual(result, explicit) {
					t.Errorf("implicit and explicit root listings differ")
				}
				if len(result.Directories) == 0 {
					t.Fatal("root listing is empty")
				}
				for _, d := range result.Directories {
					if d.Level != 0 || filepath.Dir(d.Path) != "/" {
						t.Fatalf("entry %+v: want level 0 directly below /", d)
					}
				}
			}),
		scenario(req, "INV-ROUTE-R129-ROUTE", "edge", "stateless", "filesystem-path-is-a-file",
			"An existing absolute path that is a file (not a directory) answers 200 with an empty directory list.",
			[]string{fsBrowse, "server/utils/fileUtils.js:531-534"}, func(t *testing.T, e *harness.Env) {
				dir := browseDir(t, e)
				resp, result := browse(t, e, e.Token(t, "root"), url.Values{"path": {filepath.Join(dir, "file.txt")}})
				wantStatus(t, "browse file", resp, http.StatusOK)
				if result.Directories == nil || len(result.Directories) != 0 {
					t.Errorf("directories = %v, want empty array", result.Directories)
				}
				if !harnessIsArray(resp, "directories") {
					t.Errorf("directories must be a JSON array: %s", resp.Body)
				}
			}),
		scenario(req, "INV-ROUTE-R129-ROUTE", "error", "stateless", "filesystem-rejects-callers-and-paths",
			"The route answers 401 without a token, 403 to a non-admin (before looking at the path), and 400 for a relative path or an absolute path that does not exist.",
			[]string{fsBrowse}, func(t *testing.T, e *harness.Env) {
				_ = e.Token(t, "listener")
				_ = e.Token(t, "root")
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				anonymousAndListener(t, e, "GET", "/api/filesystem", nil)
				listener := e.Do(t, harness.Request{Path: "/api/filesystem", Token: e.Token(t, "listener"), Query: url.Values{"path": {"relative/dir"}}})
				wantStatus(t, "listener with invalid path", listener, http.StatusForbidden)
				for _, bad := range []string{"relative/dir", "media", filepath.Join(e.Install, "does-not-exist")} {
					resp, _ := browse(t, e, e.Token(t, "root"), url.Values{"path": {bad}})
					wantStatus(t, "path "+bad, resp, http.StatusBadRequest)
				}
			}),
	}
}

func harnessIsArray(resp harness.Response, key string) bool {
	_, ok := harness.Dig(decode(resp), key).([]any)
	return ok
}
