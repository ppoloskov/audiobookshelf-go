package scan

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventTimeout = 15 * time.Second // a socket event the server sends during a request
	scanTimeout  = 90 * time.Second // a scan triggered through the API
	watchTimeout = 120 * time.Second
	absentWindow = 500 * time.Millisecond
)

// wantStatus fails unless the response has the status.
func wantStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: status %d, want %d: %.300s", what, resp.Status, status, resp.Body)
	}
}

// decode unmarshals a JSON body into a new T.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %T: %v: %.300s", value, err, body)
	}
	return value
}

// itemScanResult is the body of POST /api/items/:id/scan.
type itemScanResult struct {
	Result string `json:"result"`
}

// scanItem scans one item through the API and returns the ScanResult name.
func scanItem(t *testing.T, e *harness.Env, id string) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/items/"+id+"/scan", nil)
	wantStatus(t, "scan item "+id, resp, http.StatusOK)
	result := decode[itemScanResult](t, resp.Body)
	if result.Result == "" {
		t.Fatalf("scan item %s: no result: %.300s", id, resp.Body)
	}
	return result.Result
}

// libraryScanStamp returns the libraries.lastScan text of a library.
func libraryScanStamp(t *testing.T, e *harness.Env, libraryID string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT lastScan FROM libraries WHERE id = ?", libraryID)
	if len(rows) != 1 {
		t.Fatalf("library %s: %d rows", libraryID, len(rows))
	}
	stamp, _ := rows[0]["lastScan"].(string)
	return stamp
}

// libraryScanPath is the scan route the web client calls: it always sends the
// force query, 0 for a plain scan (client/store/libraries.js:76), and upstream
// only treats the exact value 1 as forced (server/controllers/LibraryController.js:1272).
func libraryScanPath(libraryID string, force bool) string {
	if force {
		return "/api/libraries/" + libraryID + "/scan?force=1"
	}
	return "/api/libraries/" + libraryID + "/scan?force=0"
}

// libraryScan posts a library scan and waits until the library recorded the
// completed scan, so a following assertion is not raced by the asynchronous scan.
func libraryScan(t *testing.T, e *harness.Env, libraryID string, force bool) {
	t.Helper()
	before := libraryScanStamp(t, e, libraryID)
	resp := e.Post(t, e.Token(t, "root"), libraryScanPath(libraryID, force), nil)
	wantStatus(t, "library scan", resp, http.StatusOK)
	harness.Eventually(t, scanTimeout, "the library scan to finish", func() bool {
		return libraryScanStamp(t, e, libraryID) != before
	})
}

// libraryItemsPayload is the item list of GET /api/libraries/:id/items.
type libraryItemsPayload struct {
	Results []struct {
		ID    string `json:"id"`
		Media struct {
			Metadata struct {
				Title string `json:"title"`
			} `json:"metadata"`
		} `json:"media"`
	} `json:"results"`
	Total int `json:"total"`
}

// libraryItems reads the first page of a library's items.
func libraryItems(t *testing.T, e *harness.Env, libraryID string) libraryItemsPayload {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries/"+libraryID+"/items?limit=200&minified=1&sort=addedAt&desc=1")
	wantStatus(t, "list library items", resp, http.StatusOK)
	return decode[libraryItemsPayload](t, resp.Body)
}

// itemTitles returns the titles of a library's items sorted.
func itemTitles(t *testing.T, e *harness.Env, libraryID string) []string {
	t.Helper()
	payload := libraryItems(t, e, libraryID)
	titles := make([]string, 0, len(payload.Results))
	for _, result := range payload.Results {
		titles = append(titles, result.Media.Metadata.Title)
	}
	slices.Sort(titles)
	return titles
}

// itemIDByTitle returns the item id of the library item with that exact title.
func itemIDByTitle(t *testing.T, e *harness.Env, libraryID, title string) string {
	t.Helper()
	rows := e.Rows(t, `SELECT li.id AS id FROM libraryItems li JOIN books b ON b.id = li.mediaId
		WHERE li.libraryId = ? AND b.title = ?`, libraryID, title)
	if len(rows) != 1 {
		t.Fatalf("want one item titled %q in library %s, got %d", title, libraryID, len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// itemIDByRelPath returns the item id stored for a library item's relative path.
func itemIDByRelPath(t *testing.T, e *harness.Env, relPath string) string {
	t.Helper()
	rows := e.Rows(t, "SELECT id FROM libraryItems WHERE relPath = ?", relPath)
	if len(rows) != 1 {
		t.Fatalf("want one item at relPath %q, got %d", relPath, len(rows))
	}
	id, _ := rows[0]["id"].(string)
	return id
}

// itemScanState is the persisted identity and scan timestamp of one item, so a
// scenario can prove that a scan moves neither the id of a relative path nor
// the item's own lastScan stamp.
type itemScanState struct {
	ID       string
	RelPath  string
	LastScan any // the DATE column text, or nil for a never-scanned row
}

// itemScanStates reads every item of a library ordered by relPath.
func itemScanStates(t *testing.T, e *harness.Env, libraryID string) []itemScanState {
	t.Helper()
	rows := e.Rows(t, "SELECT id, relPath, lastScan FROM libraryItems WHERE libraryId = ? ORDER BY relPath", libraryID)
	states := make([]itemScanState, 0, len(rows))
	for _, row := range rows {
		id, _ := row["id"].(string)
		relPath, _ := row["relPath"].(string)
		states = append(states, itemScanState{ID: id, RelPath: relPath, LastScan: row["lastScan"]})
	}
	return states
}

// requireSameItemScans fails unless both lists carry the same items: equal
// count, the same id for every relPath, and an unchanged lastScan stamp.
func requireSameItemScans(t *testing.T, what string, got, want []itemScanState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d items, want the %d stored ones", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: item %d = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// readFile reads a file of the install.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() > 8<<20 {
		t.Fatalf("%s is larger than 8 MiB", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// writeFile writes a test file and returns its modification time.
func writeFile(t *testing.T, path string, body []byte) time.Time {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// copyFile copies src to dst and returns the destination size.
func copyFile(t *testing.T, src, dst string) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close() // read-only
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	written, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		t.Fatalf("copy %s: %v", src, copyErr)
	}
	if closeErr != nil {
		t.Fatalf("close %s: %v", dst, closeErr)
	}
	return written
}

// stripCopy writes a copy of the fixture audio file without any metadata tags,
// so folder names alone name the book (the same way a ripped library looks).
func stripCopy(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(dst), err)
	}
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := exec.Command(ffmpeg, "-v", "error", "-y", "-i", src, "-map", "0:a", "-map_metadata", "-1", "-write_id3v2", "0", "-c", "copy", dst)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("strip tags into %s: %v: %s", dst, err, output)
	}
}

// withEmbeddedCover writes audio with cover as attached cover art (a stream
// copy, so the audio bytes and duration are unchanged).
func withEmbeddedCover(t *testing.T, audio, cover, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(dst), err)
	}
	ffmpeg := os.Getenv("FFMPEG_PATH")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	command := exec.Command(ffmpeg, "-v", "error", "-y",
		"-i", audio, "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c", "copy", "-id3v2_version", "3",
		"-metadata:s:v", "title=Album cover", "-metadata:s:v", "comment=Cover (front)", dst)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("embed cover into %s: %v: %s", dst, err, output)
	}
}

// fixtureAudio returns the path of a fixture audio file relative to the media root.
func fixtureAudio(e *harness.Env, rel string) string { return filepath.Join(e.Media, rel) }

// stageFolder moves a staged folder into the library; build fills it.
func stageFolder(t *testing.T, e *harness.Env, name, relDir string, build func(dir string)) string {
	t.Helper()
	staging := filepath.Join(e.Install, "staging", name)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	build(staging)
	target := filepath.Join(e.Media, relDir)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, target); err != nil {
		t.Fatalf("stage %s: %v", relDir, err)
	}
	return target
}

// setLibrarySettings patches a library's settings through the API.
func setLibrarySettings(t *testing.T, e *harness.Env, libraryID string, settings map[string]any) {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+libraryID, map[string]any{"settings": settings})
	wantStatus(t, "patch library settings", resp, http.StatusOK)
}

// disableWatcher stops the watcher of a library so a scenario's file changes are
// only processed by the scans it triggers itself.
func disableWatcher(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	setLibrarySettings(t, e, libraryID, map[string]any{"disableWatcher": true})
}

// enableWatcher starts the watcher of a library again.
func enableWatcher(t *testing.T, e *harness.Env, libraryID string) {
	t.Helper()
	setLibrarySettings(t, e, libraryID, map[string]any{"disableWatcher": false})
}

// libraryRow returns selected columns of a library row for direct assertions.
func libraryRow(t *testing.T, e *harness.Env, libraryID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, name, mediaType, settings, lastScan, lastScanVersion, extraData FROM libraries WHERE id = ?", libraryID)
	if len(rows) != 1 {
		t.Fatalf("library %s: %d rows", libraryID, len(rows))
	}
	return rows[0]
}

// libraryPrecedence returns the persisted metadataPrecedence of a library.
func libraryPrecedence(t *testing.T, e *harness.Env, libraryID string) []string {
	t.Helper()
	var settings struct {
		MetadataPrecedence []string `json:"metadataPrecedence"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(libraryRow(t, e, libraryID)["settings"])), &settings); err != nil {
		t.Fatalf("decode library settings: %v", err)
	}
	return settings.MetadataPrecedence
}

// lastScanPrecedence returns extraData.lastScanMetadataPrecedence of a library.
func lastScanPrecedence(t *testing.T, e *harness.Env, libraryID string) []string {
	t.Helper()
	text := fmt.Sprint(libraryRow(t, e, libraryID)["extraData"])
	var extra struct {
		Precedence []string `json:"lastScanMetadataPrecedence"`
	}
	if err := json.Unmarshal([]byte(text), &extra); err != nil {
		t.Fatalf("decode library extraData: %v", err)
	}
	return extra.Precedence
}

// taskPayload is the task object of task_started/task_finished events.
type taskPayload struct {
	ID          string  `json:"id"`
	Action      string  `json:"action"`
	IsFinished  bool    `json:"isFinished"`
	IsFailed    bool    `json:"isFailed"`
	Description *string `json:"description"`
	Data        struct {
		LibraryID   string `json:"libraryId"`
		ScanResults struct {
			Added   int    `json:"added"`
			Updated int    `json:"updated"`
			Missing int    `json:"missing"`
			Elapsed int    `json:"elapsed"`
			Text    string `json:"text"`
		} `json:"scanResults"`
	} `json:"data"`
}

// waitTaskFinished waits for the next task_finished event of the action.
func waitTaskFinished(t *testing.T, socket *harness.Socket, mark int, action string) taskPayload {
	t.Helper()
	event := socket.Wait(t, mark, "task_finished", watchTimeout, func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var payload taskPayload
		return json.Unmarshal(args[0], &payload) == nil && payload.Action == action
	})
	return decode[taskPayload](t, event.Args[0])
}

// boolString renders a *string for failure messages.
func ptrText(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

// stringsOf points a *string slice out for comparable messages.
func equalStrings(a, b []string) bool { return slices.Equal(a, b) }

// requireStrings fails unless got equals want.
func requireStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !equalStrings(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}
