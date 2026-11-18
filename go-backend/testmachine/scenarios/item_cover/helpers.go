package item_cover

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Upstream source references shared by the scenarios.
const (
	controllerSrc        = "server/controllers/LibraryItemController.js"
	middlewareSrc        = controllerSrc + ":1220-1251"
	getCoverSrc          = controllerSrc + ":382-440"
	uploadCoverSrc       = controllerSrc + ":289-341"
	updateCoverSrc       = controllerSrc + ":344-375"
	removeCoverSrc       = controllerSrc + ":377-397"
	batchQuickMatch      = controllerSrc + ":757-816"
	coverManagerSrc      = "server/managers/CoverManager.js"
	cacheManagerSrc      = "server/managers/CacheManager.js"
	fileUtilsSrc         = "server/utils/fileUtils.js"
	fileDownloadSrc      = "server/utils/fileUtils.js:289-356"
	emitterSrc           = "server/SocketAuthority.js:98-135"
	libraryScanSrc       = "server/scanner/LibraryScanner.js:254-285"
	scannerSrc           = "server/scanner/LibraryScanner.js"
	watcherScanSrc       = "server/scanner/LibraryScanner.js:620-636"
	podcastCreateSrc     = "server/controllers/PodcastController.js:36-186"
	podcastControllerSrc = "server/controllers/PodcastController.js"
	opmlImportSrc        = "server/managers/PodcastManager.js:530-730"
)

// eventWait bounds how long a scenario waits for an event that must arrive.
const eventWait = 15 * time.Second

// absentWindow is how long a scenario watches for an event that must not arrive.
const absentWindow = 400 * time.Millisecond

// scenario builds a harness.Scenario with the shared fields filled in.
func scenario(requirement, invariant, caseName, state, assertion, claim string, sources []string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: caseName, State: state,
		Assertion: assertion, Sources: sources, Claim: claim, Run: run,
	}
}

// srcs is shorthand for a list of upstream references.
func srcs(list ...string) []string { return list }

// wantStatus fails the test unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d: %.300s", what, resp.Status, want, resp.Body)
	}
}

// requireEqual fails the test unless got and want are deeply equal.
func requireEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)   // diagnostics only
		wantJSON, _ := json.Marshal(want) // diagnostics only
		t.Fatalf("%s = %s, want %s", what, gotJSON, wantJSON)
	}
}

// firstRow returns the only row of a query.
func firstRow(t *testing.T, e *harness.Env, query string, args ...any) map[string]any {
	t.Helper()
	rows := e.Rows(t, query, args...)
	if len(rows) != 1 {
		t.Fatalf("query %q %v: %d rows, want 1", query, args, len(rows))
	}
	return rows[0]
}

// itemRow returns the libraryItems row.
func itemRow(t *testing.T, e *harness.Env, itemID string) map[string]any {
	t.Helper()
	return firstRow(t, e, "SELECT * FROM libraryItems WHERE id = ?", itemID)
}

// mediaRow returns the books or podcasts row of an item.
func mediaRow(t *testing.T, e *harness.Env, itemID string) map[string]any {
	t.Helper()
	table := "books"
	if itemRow(t, e, itemID)["mediaType"] == "podcast" {
		table = "podcasts"
	}
	return firstRow(t, e, "SELECT * FROM "+table+" WHERE id = ?", itemRow(t, e, itemID)["mediaId"])
}

// updatedAt returns the libraryItems.updatedAt of an item.
func updatedAt(t *testing.T, e *harness.Env, itemID string) time.Time {
	t.Helper()
	text, _ := itemRow(t, e, itemID)["updatedAt"].(string)
	stamp, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("libraryItems.updatedAt %q: %v", text, err)
	}
	return stamp
}

// wantMoved fails unless updatedAt of the item is strictly after before.
func wantMoved(t *testing.T, e *harness.Env, itemID string, before time.Time) {
	t.Helper()
	if after := updatedAt(t, e, itemID); !after.After(before) {
		t.Fatalf("libraryItems.updatedAt %v did not move after %v", after, before)
	}
}

// wantUnchanged fails unless updatedAt of the item equals before.
func wantUnchanged(t *testing.T, e *harness.Env, itemID string, before time.Time) {
	t.Helper()
	if after := updatedAt(t, e, itemID); !after.Equal(before) {
		t.Fatalf("libraryItems.updatedAt moved from %v to %v", before, after)
	}
}

// itemPath returns the item's path from the database.
func itemPath(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	path, _ := itemRow(t, e, itemID)["path"].(string)
	if path == "" {
		t.Fatalf("item %s has no path", itemID)
	}
	return path
}

// coverDir is the directory CoverManager stores uploaded covers in while
// storeCoverWithItem is off (the fixture setting): metadata/items/<id>.
func coverDir(e *harness.Env, itemID string) string {
	return filepath.Join(e.Metadata, "items", itemID)
}

// coverCacheDir is the resized cover cache directory.
func coverCacheDir(e *harness.Env) string {
	return filepath.Join(e.Metadata, "cache", "covers")
}

// readDisk fails the test unless path can be read.
func readDisk(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// pathExists reports whether a file or directory exists.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirNames lists the entries of a directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// cacheEntries lists the cache files of one item.
func cacheEntries(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	out := []string{}
	for _, name := range dirNames(t, coverCacheDir(e)) {
		if strings.HasPrefix(name, itemID+"_") {
			out = append(out, name)
		}
	}
	return out
}

// snapshot captures the rows a cover write can change, to prove a rejected
// request changed nothing.
func snapshot(t *testing.T, e *harness.Env) string {
	t.Helper()
	var parts []string
	for _, query := range []string{
		"SELECT * FROM libraryItems ORDER BY id", "SELECT * FROM books ORDER BY id", "SELECT * FROM podcasts ORDER BY id",
		"SELECT * FROM customMetadataProviders ORDER BY id", "SELECT * FROM podcastEpisodes ORDER BY id",
		"SELECT * FROM libraryFolders ORDER BY id", "SELECT * FROM authors ORDER BY id",
	} {
		for _, row := range e.Rows(t, query) {
			parts = append(parts, fmt.Sprint(row))
		}
	}
	return strings.Join(parts, "\n")
}

// wantSnapshot fails the test when any snapshotted row differs from before.
func wantSnapshot(t *testing.T, e *harness.Env, before string) {
	t.Helper()
	if after := snapshot(t, e); after != before {
		t.Fatalf("database rows changed:\n%s", diffText(before, after))
	}
}

// diffText returns the first differing line pair of two snapshots.
func diffText(before, after string) string {
	beforeLines, afterLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	for i := 0; i < len(beforeLines) && i < len(afterLines); i++ {
		if beforeLines[i] != afterLines[i] {
			return "before: " + beforeLines[i] + "\nafter:  " + afterLines[i]
		}
	}
	return fmt.Sprintf("%d lines before, %d after", len(beforeLines), len(afterLines))
}

// jsonColumn decodes a JSON column of a row (nil when the column is NULL).
func jsonColumn(t *testing.T, row map[string]any, name string) any {
	t.Helper()
	raw, ok := row[name].(string)
	if !ok {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("column %s is not JSON: %v: %.200s", name, err, raw)
	}
	return value
}

// sortedCopy returns a sorted copy of the list.
func sortedCopy(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}

// itoa formats a decimal integer for header comparisons.
func itoa(value int) string { return strconv.Itoa(value) }

// removeFile deletes a file, failing the scenario on error.
func removeFile(path string) error { return os.Remove(path) }

// fileModTime returns the modification time of a file.
func fileModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// decodeJSON decodes JSON text into out and returns the error for requireEqual.
func decodeJSON(t *testing.T, raw string, out any) error {
	t.Helper()
	return json.Unmarshal([]byte(raw), out)
}

// get performs an unauthenticated GET with optional headers.
func get(t *testing.T, e *harness.Env, path string, header map[string]string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: path, Header: header})
}

// uploadURL posts a cover url as the caller and returns the response.
func uploadURL(t *testing.T, e *harness.Env, token, itemID, url string) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/"+itemID+"/cover", map[string]any{"url": url})
}

// uploadFile posts a multipart cover file as the caller and returns the response.
func uploadFile(t *testing.T, e *harness.Env, token, itemID, field, name string, content []byte) harness.Response {
	t.Helper()
	body, contentType := harness.Multipart(nil, harness.File{Field: field, Name: name, Content: content})
	return e.Do(t, harness.Request{
		Method: http.MethodPost, Path: "/api/items/" + itemID + "/cover", Token: token,
		Header: map[string]string{"Content-Type": contentType}, Body: body,
	})
}

// setStoreCoverWithItem toggles the server setting that stores covers inside
// the item folder instead of metadata/items/<id>.
func setStoreCoverWithItem(t *testing.T, e *harness.Env, enabled bool) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/settings", map[string]any{"storeCoverWithItem": enabled})
	wantStatus(t, "PATCH settings storeCoverWithItem", resp, http.StatusOK)
}

// wantCoverResponse checks the successful upload body and returns the cover path.
func wantCoverResponse(t *testing.T, resp harness.Response, want string) {
	t.Helper()
	var result struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}
	resp.JSON(t, &result)
	if !result.Success || result.Cover != want {
		t.Fatalf("cover response = %+v, want success true cover %q", result, want)
	}
}

// mediaCoverPath returns the stored coverPath of the item's media row.
func mediaCoverPath(t *testing.T, e *harness.Env, itemID string) any {
	t.Helper()
	return mediaRow(t, e, itemID)["coverPath"]
}

// wantMediaCoverPath fails unless the stored coverPath equals want.
func wantMediaCoverPath(t *testing.T, e *harness.Env, itemID string, want any) {
	t.Helper()
	requireEqual(t, "media coverPath", mediaCoverPath(t, e, itemID), want)
}
