package item_write

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Upstream source references shared by the scenarios.
const (
	controllerSrc    = "server/controllers/LibraryItemController.js"
	middlewareSrc    = controllerSrc + ":1220-1250"
	updateMediaSrc   = controllerSrc + ":103-195"
	bookUpdateSrc    = "server/models/Book.js:370-431"
	authorsUpdateSrc = "server/models/Book.js:440-485"
	seriesUpdateSrc  = "server/models/Book.js:495-545"
	podcastUpdateSrc = "server/models/Podcast.js:211-300"
	handleDeleteSrc  = "server/routers/ApiRouter.js:373-407"
	removeSeriesSrc  = "server/routers/ApiRouter.js:414-470"
	removeAuthorSrc  = "server/routers/ApiRouter.js:472-525"
	emitterSrc       = "server/SocketAuthority.js:98-112"
	metadataFileSrc  = "server/models/LibraryItem.js:612-700"
)

// eventWait bounds how long a scenario waits for an event that must arrive.
const eventWait = 10 * time.Second

// absentWindow is how long a scenario watches for an event that must not arrive.
const absentWindow = 300 * time.Millisecond

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
	item := itemRow(t, e, itemID)
	table := "books"
	if item["mediaType"] == "podcast" {
		table = "podcasts"
	}
	return firstRow(t, e, "SELECT * FROM "+table+" WHERE id = ?", item["mediaId"])
}

// column decodes a JSON column of a row (nil when the column is NULL).
func column(t *testing.T, row map[string]any, name string) any {
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

// patchMedia sends PATCH /api/items/:id/media.
func patchMedia(t *testing.T, e *harness.Env, token, itemID string, body any) harness.Response {
	t.Helper()
	return e.Patch(t, token, "/api/items/"+itemID+"/media", body)
}

// mediaResult is the body of PATCH /api/items/:id/media.
type mediaResult struct {
	Updated     bool           `json:"updated"`
	LibraryItem map[string]any `json:"libraryItem"`
}

// patchOK sends the media patch as root, requires 200 and returns the decoded result.
func patchOK(t *testing.T, e *harness.Env, itemID string, body any) mediaResult {
	t.Helper()
	resp := patchMedia(t, e, e.Token(t, "root"), itemID, body)
	wantStatus(t, "PATCH media", resp, http.StatusOK)
	var result mediaResult
	resp.JSON(t, &result)
	return result
}

// metadataOf returns media.metadata of an old-format item document.
func metadataOf(item map[string]any) map[string]any {
	metadata, _ := harness.Dig(item, "media", "metadata").(map[string]any)
	return metadata
}

// names lists the "name" of each object of a decoded JSON array.
func names(list any) []string {
	var out []string
	items, _ := list.([]any)
	for _, raw := range items {
		object, _ := raw.(map[string]any)
		name, _ := object["name"].(string)
		out = append(out, name)
	}
	return out
}

// stringList converts a decoded JSON array of strings.
func stringList(list any) []string {
	out := []string{}
	items, _ := list.([]any)
	for _, raw := range items {
		text, _ := raw.(string)
		out = append(out, text)
	}
	return out
}

// bookAuthorNames returns the author names linked to the item's book, sorted.
func bookAuthorNames(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	var out []string
	for _, row := range e.Rows(t, `SELECT a.name AS name FROM bookAuthors ba JOIN authors a ON a.id = ba.authorId
		JOIN libraryItems li ON li.mediaId = ba.bookId WHERE li.id = ? ORDER BY a.name`, itemID) {
		out = append(out, row["name"].(string))
	}
	return out
}

// bookSeriesRows returns "name|sequence" of each series linked to the item's book, sorted by name.
func bookSeriesRows(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	var out []string
	for _, row := range e.Rows(t, `SELECT s.name AS name, bs.sequence AS sequence FROM bookSeries bs JOIN series s ON s.id = bs.seriesId
		JOIN libraryItems li ON li.mediaId = bs.bookId WHERE li.id = ? ORDER BY s.name`, itemID) {
		sequence, _ := row["sequence"].(string)
		out = append(out, fmt.Sprintf("%s|%s", row["name"], sequence))
	}
	return out
}

// readMetadataFile decodes metadata.json of an item stored under metadata/items.
func readMetadataFile(t *testing.T, e *harness.Env, itemID string) map[string]any {
	t.Helper()
	return readJSONFile(t, filepath.Join(e.Metadata, "items", itemID, "metadata.json"))
}

// readJSONFile decodes a small JSON file.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("%s is larger than 1 MiB", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not a JSON object: %v", path, err)
	}
	return out
}

// pathExists reports whether a file or directory exists.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// itemPath returns the item's path from the database.
func itemPath(t *testing.T, e *harness.Env, itemID string) string {
	t.Helper()
	path, _ := itemRow(t, e, itemID)["path"].(string)
	return path
}

// snapshot captures the rows that an item write can change, to prove a rejected request changed nothing.
func snapshot(t *testing.T, e *harness.Env) string {
	t.Helper()
	var parts []string
	for _, query := range []string{
		"SELECT * FROM libraryItems ORDER BY id", "SELECT * FROM books ORDER BY id", "SELECT * FROM podcasts ORDER BY id",
		"SELECT * FROM podcastEpisodes ORDER BY id", "SELECT * FROM authors ORDER BY id", "SELECT * FROM bookAuthors ORDER BY id",
		"SELECT * FROM series ORDER BY id", "SELECT * FROM bookSeries ORDER BY id", "SELECT * FROM mediaProgresses ORDER BY id",
		"SELECT * FROM playlists ORDER BY id", "SELECT * FROM playlistMediaItems ORDER BY id", "SELECT * FROM collectionBooks ORDER BY id",
		"SELECT * FROM feeds ORDER BY id", "SELECT * FROM mediaItemShares ORDER BY id",
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
		t.Fatal("a rejected request changed stored rows")
	}
}

// itemDocument fetches GET /api/items/:id?expanded=1 as root.
func itemDocument(t *testing.T, e *harness.Env, itemID string) map[string]any {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/items/"+itemID+"?expanded=1")
	wantStatus(t, "GET item", resp, http.StatusOK)
	return resp.Object(t)
}

// stampOf parses a database DATETIME column of a row.
func stampOf(t *testing.T, row map[string]any, name string) time.Time {
	t.Helper()
	text, _ := row[name].(string)
	stamp, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("%s %q: %v", name, text, err)
	}
	return stamp
}

// mediaUpdatedAt returns the updatedAt of the books or podcasts row of an item.
func mediaUpdatedAt(t *testing.T, e *harness.Env, itemID string) time.Time {
	t.Helper()
	return stampOf(t, mediaRow(t, e, itemID), "updatedAt")
}

// audioFileList returns the books.audioFiles column as objects.
func audioFileList(t *testing.T, e *harness.Env, itemID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	list, _ := column(t, mediaRow(t, e, itemID), "audioFiles").([]any)
	for _, raw := range list {
		file, _ := raw.(map[string]any)
		out = append(out, file)
	}
	return out
}

// audioNames lists "filename:index:exclude" of stored audio files in stored order.
func audioNames(files []map[string]any) []string {
	var out []string
	for _, file := range files {
		name, _ := harness.Dig(file, "metadata", "filename").(string)
		out = append(out, fmt.Sprintf("%s:%v:%v", name, file["index"], file["exclude"]))
	}
	return out
}

// libraryFileList returns the libraryItems.libraryFiles column as objects.
func libraryFileList(t *testing.T, e *harness.Env, itemID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	list, _ := column(t, itemRow(t, e, itemID), "libraryFiles").([]any)
	for _, raw := range list {
		file, _ := raw.(map[string]any)
		out = append(out, file)
	}
	return out
}

// inoOf returns the ino of the stored library file with the given file name.
func inoOf(t *testing.T, e *harness.Env, itemID, filename string) string {
	t.Helper()
	for _, file := range libraryFileList(t, e, itemID) {
		if harness.Dig(file, "metadata", "filename") == filename {
			ino, _ := file["ino"].(string)
			return ino
		}
	}
	t.Fatalf("item %s has no library file %q", itemID, filename)
	return ""
}

// itoa formats an integer database value.
func itoa(value any) string { return fmt.Sprint(value) }

// sprint formats a value with fmt.Sprint.
func sprint(value any) string { return fmt.Sprint(value) }

// directoryList returns the slash-separated relative paths with sizes of the regular files under dir, sorted.
func directoryList(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), info.Size()))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(out)
	return out
}

// itemPath2 returns the fixture folder of an item that may already be deleted: it derives the path from the media root.
func itemPath2(e *harness.Env, itemID string) string {
	if itemID == e.IDs["disposable"] {
		return filepath.Join(e.Media, "audiobooks", "Zed Disposable", "Hard Delete Me")
	}
	return ""
}
