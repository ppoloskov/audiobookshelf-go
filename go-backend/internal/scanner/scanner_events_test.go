package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
	_ "modernc.org/sqlite"
)

// Acceptance tests for LibraryScanner scan-event, association-cleanup,
// author-ordering and sidecar-saving behaviors. Expectations are hand-checked
// against the pinned upstream sources and cited per test. The fake ffprobe is
// the same boundary injection the package's other tests use (FFPROBE_PATH).

type capturedEvent struct {
	scope   string
	event   string
	payload any
}

// Fakes ffprobe per files.go probeAudio: FFPROBE_PATH env boundary.
func fakeFFprobe(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	script := `#!/bin/sh
printf '%s' '{"format":{"format_name":"mp3","duration":"1"},"streams":[{"codec_type":"audio","codec_name":"mp3"}]}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", path)
}

// fakeFFprobeJSON makes ffprobe answer every call with the given JSON.
func fakeFFprobeJSON(t *testing.T, out string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", path)
}

func captureEvents() (events.Emitter, *[]capturedEvent) {
	events := &[]capturedEvent{}
	return func(userID, event string, payload any) {
		*events = append(*events, capturedEvent{scope: userID, event: event, payload: payload})
	}, events
}

func eventsNamed(list *[]capturedEvent, name string) []capturedEvent {
	out := []capturedEvent{}
	for _, e := range *list {
		if e.event == name {
			out = append(out, e)
		}
	}
	return out
}

// payloadItems decodes an item event payload (one item or a list) by value.
func payloadItems(t *testing.T, payload any) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if json.Unmarshal(raw, &items) != nil {
		var one map[string]any
		if err := json.Unmarshal(raw, &one); err != nil {
			t.Fatalf("payload %T is not an item or item list: %v", payload, err)
		}
		items = []map[string]any{one}
	}
	return items
}

// The SQLite driver serializes DATETIME values as strings; decode either
// representation into epoch milliseconds.
func decodeDBTime(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case []byte:
		return parseDBTime(string(v))
	case string:
		return parseDBTime(v)
	}
	return -1
}

func parseDBTime(value string) int64 {
	for _, layout := range []string{"2006-01-02 15:04:05.000 -07:00", "2006-01-02 15:04:05 -07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UnixMilli()
		}
	}
	return -1
}

// LibraryScanner.js:224-235 and :269-280 emit items_added/items_updated in
// chunks of ten with expanded legacy item JSON; the remainder emits at the
// end. Per-client filtering happens in the hub payloadForUser step.
func TestScanEmitsChunkedExpandedItems(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	emit, events := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	const count = 11
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("root-book-%02d.mp3", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Scan(context.Background(), "lib-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != count {
		t.Fatalf("result = %+v, want %d added", res, count)
	}
	chunks := eventsNamed(events, "items_added")
	if len(chunks) != 2 {
		t.Fatalf("items_added chunks = %d, want 2 (10 + 1)", len(chunks))
	}
	first := payloadItems(t, chunks[0].payload)
	second := payloadItems(t, chunks[1].payload)
	if len(first) != 10 || len(second) != 1 {
		t.Fatalf("chunk sizes = %d,%d want 10,1", len(first), len(second))
	}
	var ids []string
	for _, chunk := range chunks {
		for _, item := range payloadItems(t, chunk.payload) {
			id, _ := item["id"].(string)
			if id == "" {
				t.Fatalf("chunk payload item missing id: %v", item)
			}
			if _, ok := item["path"].(string); !ok {
				t.Fatalf("expanded item missing path: %v", item)
			}
			if item["mediaType"] != "book" {
				t.Fatalf("expanded item mediaType = %v", item["mediaType"])
			}
			ids = append(ids, id)
		}
	}
	var dbIDs []string
	rows, err := db.Query(`SELECT id FROM libraryItems WHERE libraryId='lib-1' ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		dbIDs = append(dbIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if strings.Join(ids, ",") != strings.Join(dbIDs, ",") {
		t.Fatalf("chunked ids %v != db order %v", ids, dbIDs)
	}
}

// BookScanner.js:942-1010: only authors and series a rescan unlinked from a
// book are removed (when empty and without asin/description/image), emitting
// author_removed/series_removed with {id, libraryId}. Rows that were already
// empty before the scan are left alone.
func TestScanRemovesOnlyAuthorsUnlinkedByRescan(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Book")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeFFprobe(t)
	if err := os.WriteFile(filepath.Join(itemDir, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(itemDir, "metadata.json")
	if err := os.WriteFile(sidecar, []byte(`{"title":"Real Title","authors":["Author A","Author B"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	emit, events := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := db.Exec(`INSERT INTO authors(id,name,libraryId,createdAt,updatedAt) VALUES('orphan-author','Orphan Author','lib-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO series(id,name,libraryId,createdAt,updatedAt) VALUES('empty-series','Orphaned Series','lib-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1", false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM authors WHERE name='Orphan Author'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pre-existing empty author count=%d err=%v, want it kept", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE id='empty-series'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pre-existing empty series count=%d err=%v, want it kept", count, err)
	}
	if got := eventsNamed(events, "author_removed"); len(got) != 0 {
		t.Fatalf("author_removed after first scan = %+v", got)
	}

	// Dropping Author B from the sidecar and rescanning unlinks and removes it.
	if err := os.WriteFile(sidecar, []byte(`{"title":"Real Title","authors":["Author A"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1", true); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM authors WHERE name='Author B'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unlinked author count=%d err=%v, want removed", count, err)
	}
	removed := eventsNamed(events, "author_removed")
	if len(removed) != 1 {
		t.Fatalf("author_removed events = %+v, want 1", removed)
	}
	payload, _ := removed[0].payload.(map[string]any)
	if id, _ := payload["id"].(string); id == "" || payload["libraryId"] != "lib-1" {
		t.Fatalf("author_removed payload = %v", payload)
	}
}

// BookScanner.js:388-397 and :825-901: sidecars save during the scan after
// the media save; default location is MetadataPath/items/<itemId>/metadata.json
// and storeMetadataWithItem writes in-item with a libraryFiles refresh.
func TestScanSidecarDefaultLocationAndInItem(t *testing.T) {
	root := t.TempDir()
	metaRoot := filepath.Join(t.TempDir(), "meta")
	itemDir := filepath.Join(root, "Book")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeFFprobe(t)
	if err := os.WriteFile(filepath.Join(itemDir, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "metadata.json"), []byte(`{"title":"Real Title"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{MetadataPath: metaRoot}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Scan(context.Background(), "lib-1", false); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := db.QueryRow(`SELECT id FROM libraryItems WHERE libraryId='lib-1'`).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	defaultSidecar, err := os.ReadFile(filepath.Join(metaRoot, "items", itemID, "metadata.json"))
	if err != nil {
		t.Fatalf("default sidecar location: %v", err)
	}
	if !strings.Contains(string(defaultSidecar), "Real Title") {
		t.Fatalf("default sidecar = %s", defaultSidecar)
	}
	if _, err := db.Exec(`INSERT INTO settings(key,value,createdAt,updatedAt) VALUES('server-settings','{"id":"server-settings","storeMetadataWithItem":true,"metadataFileFormat":"json"}','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(7 * time.Second)
	if err := os.Chtimes(filepath.Join(itemDir, "track.mp3"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	res, err := s.Scan(context.Background(), "lib-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("mtime-only change result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(itemDir, "metadata.json")); err != nil {
		t.Fatalf("in-item sidecar not written: %v", err)
	}
	var libraryFiles string
	if err := db.QueryRow(`SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryFiles); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(libraryFiles, "metadata.json") {
		t.Fatalf("stored sidecar not in libraryFiles: %s", libraryFiles)
	}
}

// BookScanner.js:235-245 creates and announces new authors; only associations
// to pre-existing authors enter authorsNumBooksChangedIds (:225-232), so a
// first-scan count event is intentionally absent. New task lifecycle follows
// LibraryScanner.js:74-85, 106-132 and TaskManager.js:22-36.
func TestScanSequentialAuthorTimestampsAndCounts(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Book")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeFFprobe(t)
	if err := os.WriteFile(filepath.Join(itemDir, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "metadata.json"), []byte(`{"title":"Real Title","authors":["A","B","C"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	emit, events := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Scan(context.Background(), "lib-1", false); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name,createdAt FROM authors WHERE libraryId='lib-1' ORDER BY createdAt`)
	if err != nil {
		t.Fatal(err)
	}
	type authorRow struct {
		name      string
		createdAt int64
	}
	ordered := []authorRow{}
	for rows.Next() {
		var row authorRow
		var createdAt any
		if err := rows.Scan(&row.name, &createdAt); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		row.createdAt = decodeDBTime(createdAt)
		if row.createdAt == -1 {
			rows.Close()
			t.Fatalf("unparseable author createdAt %v", createdAt)
		}
		ordered = append(ordered, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(ordered) != 3 {
		t.Fatalf("author rows = %+v, want A,B,C", ordered)
	}
	wantOrder := []string{"A", "B", "C"}
	for i, want := range wantOrder {
		if ordered[i].name != want {
			t.Fatalf("author scan order = %v, want %v (createdAt %v)", ordered[i].name, wantOrder, ordered[i].createdAt)
		}
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].createdAt <= ordered[i-1].createdAt {
			t.Fatalf("author createdAt not strictly increasing in scan order: %v", ordered)
		}
	}
	countEvent := eventsNamed(events, "authors_num_books_updated")
	if len(countEvent) != 0 {
		t.Fatalf("newly created authors must not emit count updates: %+v", countEvent)
	}
	if (*events)[0].event != "task_started" || (*events)[len(*events)-1].event != "task_finished" {
		t.Fatalf("scan task lifecycle events = %v ... %v", (*events)[0].event, (*events)[len(*events)-1].event)
	}
	finished, ok := (*events)[len(*events)-1].payload.(eventsTask)
	if !ok || !finished.IsFinished || finished.IsFailed {
		t.Fatalf("finished task payload = %v", (*events)[len(*events)-1].payload)
	}
	if finished.Description != nil || finished.DescriptionKey != nil {
		t.Fatalf("successful task description should be cleared: %v", finished)
	}
	if finished.FinishedAt == nil {
		t.Fatalf("finished task timestamp missing: %v", finished)
	}
	if finished.Data.(scanTaskData).ScanResults == nil {
		t.Fatalf("scan result missing from task: %v", finished)
	}

	// Rescan an existing author after adding a second book: its book count
	// changes, so upstream BookScanner.js:231-232 requires a count event.
	secondDir := filepath.Join(root, "Another Book")
	if err := os.MkdirAll(secondDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "metadata.json"), []byte(`{"title":"Second","authors":["A"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	*events = nil
	if _, err := s.Scan(context.Background(), "lib-1", false); err != nil {
		t.Fatal(err)
	}
	countEvent = eventsNamed(events, "authors_num_books_updated")
	if len(countEvent) < 1 {
		t.Fatalf("existing author count events = %+v, want count change", countEvent)
	}
	var payload *authorsNumBooksUpdated
	for _, event := range countEvent {
		p, _ := event.payload.(authorsNumBooksUpdated)
		for _, author := range p.Authors {
			if author.NumBooks == 2 {
				payload = &p
			}
		}
	}
	if payload == nil {
		t.Fatalf("count events lack changed count: %+v", countEvent)
	}
	if payload.LibraryID != "lib-1" {
		t.Fatalf("numBooks payload = %v", payload)
	}
	if len(payload.Authors) != 1 || payload.Authors[0].NumBooks != 2 {
		t.Fatalf("numBooks authors = %v", payload.Authors)
	}
}

// TestScanRelationTable pins the scanner's author and series maintenance (packet
// C2 characterisation): author names match by exact case, a rescan relinks to
// the sidecar's list, an unlinked author with an asin stays, an unlinked one
// without goes with author_removed, a changed sequence is rewritten, and a
// dropped series goes with series_removed.
func TestScanRelationTable(t *testing.T) {
	root := t.TempDir()
	itemDir := filepath.Join(root, "Book")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeFFprobe(t)
	if err := os.WriteFile(filepath.Join(itemDir, "track.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(itemDir, "metadata.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(sidecar, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db := scannerTestDB(t, root)
	emit, events := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	state := func() string {
		t.Helper()
		var authors, links, series string
		for query, dst := range map[string]*string{
			`SELECT COALESCE(group_concat(name,','),'') FROM (SELECT name FROM authors ORDER BY name)`:                                                              &authors,
			`SELECT COALESCE(group_concat(name,','),'') FROM (SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId ORDER BY ba.createdAt,ba.rowid)`: &links,
			`SELECT COALESCE(group_concat(s.name||'#'||COALESCE(bs.sequence,''),',') ,'') FROM bookSeries bs JOIN series s ON s.id=bs.seriesId`:                     &series,
		} {
			if err := db.QueryRow(query).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		var seriesRows string
		_ = db.QueryRow(`SELECT COALESCE(group_concat(name,','),'') FROM series`).Scan(&seriesRows)
		return "authors=" + authors + " links=" + links + " series=" + series + " seriesRows=" + seriesRows
	}
	names := func(name string) string {
		var out []string
		for _, e := range eventsNamed(events, name) {
			if p, ok := e.payload.(map[string]any); ok {
				out = append(out, p["id"].(string))
			} else {
				out = append(out, "x")
			}
		}
		return strings.Join(out, ",")
	}
	write(`{"title":"T","authors":["john","John"],"series":[{"name":"Saga","sequence":"1"}]}`)
	if _, err := s.Scan(context.Background(), "lib-1", false); err != nil {
		t.Fatal(err)
	}
	if got, want := state(), "authors=John,john links=john,John series=Saga#1 seriesRows=Saga"; got != want {
		t.Fatalf("first scan\n got %s\nwant %s", got, want)
	}
	if got := len(eventsNamed(events, "author_added")); got != 2 {
		t.Fatalf("author_added %d", got)
	}
	if _, err := db.Exec(`UPDATE authors SET asin='B000' WHERE name='john'`); err != nil {
		t.Fatal(err)
	}
	write(`{"title":"T","authors":["Other"],"series":[{"name":"Saga","sequence":"2"}]}`)
	if _, err := s.Scan(context.Background(), "lib-1", true); err != nil {
		t.Fatal(err)
	}
	if got, want := state(), "authors=Other,john links=Other series=Saga#2 seriesRows=Saga"; got != want {
		t.Fatalf("rescan\n got %s\nwant %s", got, want)
	}
	if got := len(eventsNamed(events, "author_removed")); got != 1 {
		t.Fatalf("author_removed %d (%s)", got, names("author_removed"))
	}
	write(`{"title":"T","authors":["Other"],"series":[{"name":"Next","sequence":"1"}]}`)
	if _, err := s.Scan(context.Background(), "lib-1", true); err != nil {
		t.Fatal(err)
	}
	if got, want := state(), "authors=Other,john links=Other series=Next#1 seriesRows=Next"; got != want {
		t.Fatalf("series swap\n got %s\nwant %s", got, want)
	}
	if got := len(eventsNamed(events, "series_removed")); got != 1 {
		t.Fatalf("series_removed %d", got)
	}
}

// eventsTask names events.Task where a test's local "events" hides the package.
type eventsTask = events.Task
