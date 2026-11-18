// The persisted-column-semantics scenarios pin the SQLite encodings that a
// write produces: UTC date spellings, JSON milliseconds inside TEXT columns,
// nullable boolean integers, link-row ordering and trigger-maintained derived
// columns, and that rejected or failed writes leave all of them unchanged.
package store

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// utcTimestampPattern is the pinned SQLite DATE spelling of every DATETIME
// column in the fixture rows.
const utcTimestampPattern = `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \+00:00$`

var utcTimestamp = regexp.MustCompile(utcTimestampPattern)

// sqliteUTC renders a time in the pinned DATE spelling.
func sqliteUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000 -07:00")
}

// assertUTCWindow fails unless value is the pinned UTC spelling of a time
// between floor and now.
func assertUTCWindow(t *testing.T, column string, value any, floor time.Time) {
	t.Helper()
	text, ok := value.(string)
	if !ok || !utcTimestamp.MatchString(text) {
		t.Fatalf("%s is not a pinned UTC spelling: %v", column, value)
	}
	if text < sqliteUTC(floor) || text > sqliteUTC(time.Now()) {
		t.Fatalf("%s is outside the request window: %s (floor %s)", column, text, sqliteUTC(floor))
	}
}

// assertColumnTypes fails unless every typeof(column) query result is the
// pinned SQLite storage class.
func assertColumnTypes(t *testing.T, e *harness.Env, query string, args []any, want map[string]string) {
	t.Helper()
	rows := e.Rows(t, query, args...)
	if len(rows) != 1 {
		t.Fatalf("typeof query returned %d rows", len(rows))
	}
	for column, kind := range want {
		if rows[0][column] != kind {
			t.Fatalf("typeof(%s) = %v, want %s", column, rows[0][column], kind)
		}
	}
}

// extraDataEncoding is the pinned extraData JSON of a freshly created book
// progress row: the library item id first, then the clamped progress.
func extraDataEncoding(itemID string) string {
	return `{"libraryItemId":"` + itemID + `","progress":1}`
}

// columnWriteEncodings: the first writes on the fixture state persist the
// pinned UTC spellings, JSON milliseconds and nullable boolean encodings.
func columnWriteEncodings(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "finisher")
	const novelBookID = "85ab0f32-b17f-4c9a-883e-a61e260f1e46"
	finisherID, ok := e.Rows(t, "SELECT id FROM users WHERE username = 'trial-finisher'")[0]["id"].(string)
	if !ok || finisherID == "" {
		t.Fatal("fixture finisher user row missing")
	}
	if got := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?", finisherID, novelBookID); got != 0 {
		t.Fatalf("fixture finisher progress on the novel: %d", got)
	}
	floor := time.Now()
	r := e.Patch(t, token, "/api/me/progress/"+e.IDs["novel"], map[string]any{"currentTime": 30, "duration": 60, "isFinished": true})
	if r.Status != http.StatusOK {
		t.Fatalf("progress PATCH status %d", r.Status)
	}
	query := "SELECT id, userId, mediaItemId, mediaItemType, duration, currentTime, isFinished, hideFromContinueListening, finishedAt, extraData, podcastId, createdAt, updatedAt FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?"
	rows := e.Rows(t, query, finisherID, novelBookID)
	if len(rows) != 1 {
		t.Fatalf("progress rows: %d", len(rows))
	}
	row := rows[0]
	if row["userId"] != finisherID || row["mediaItemId"] != novelBookID || row["mediaItemType"] != "book" ||
		row["duration"] != float64(60) || row["currentTime"] != float64(30) || row["isFinished"] != int64(1) ||
		row["hideFromContinueListening"] != int64(0) || row["podcastId"] != nil {
		t.Fatalf("progress values: %v", row)
	}
	if row["extraData"] != extraDataEncoding(e.IDs["novel"]) {
		t.Fatalf("extraData encoding: %v", row["extraData"])
	}
	assertUTCWindow(t, "finishedAt", row["finishedAt"], floor)
	assertUTCWindow(t, "createdAt", row["createdAt"], floor)
	assertUTCWindow(t, "updatedAt", row["updatedAt"], floor)
	assertColumnTypes(t, e, "SELECT typeof(finishedAt) AS finishedAt, typeof(extraData) AS extraData, typeof(createdAt) AS createdAt, typeof(isFinished) AS isFinished FROM mediaProgresses WHERE userId = ? AND mediaItemId = ?", []any{finisherID, novelBookID}, map[string]string{"finishedAt": "text", "extraData": "text", "createdAt": "text", "isFinished": "integer"})
	progress := getProgress(t, e, token, e.IDs["novel"])
	if progress.CurrentTime != 30 || progress.Duration != 60 || !progress.IsFinished || progress.Progress != 1 ||
		progress.LibraryItemID != e.IDs["novel"] || progress.FinishedAt == nil || *progress.FinishedAt < float64(floor.UnixMilli()) {
		t.Fatalf("finished progress read: %+v", progress)
	}
	// A bookmark persists its createdAt as JSON milliseconds inside the users
	// TEXT bookmarks column.
	bookmarkFloor := time.Now()
	created := createBookmark(t, e, token, e.IDs["novel"], 12.5, "Encodings")
	if created.CreatedAt < bookmarkFloor.UnixMilli() || created.CreatedAt > time.Now().UnixMilli() {
		t.Fatalf("bookmark createdAt is not JSON milliseconds: %d", created.CreatedAt)
	}
	assertColumnTypes(t, e, "SELECT typeof(bookmarks) AS bookmarks FROM users WHERE id = ?", []any{finisherID}, map[string]string{"bookmarks": "text"})
	stored := decodeBookmarks(t, e.Rows(t, "SELECT bookmarks FROM users WHERE id = ?", finisherID))
	if len(stored) != 1 || stored[0] != created {
		t.Fatalf("stored bookmarks: %+v", stored)
	}
}

// columnRepeatFinish: repeating the finish request on an already finished
// progress keeps its finishedAt and refreshes updatedAt.
func columnRepeatFinish(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	const rowID = "20ae1a03-2eac-4b4a-a595-6220a2f0b475"
	const created = "2026-11-03 12:46:27.019 +00:00"
	query := "SELECT id, duration, currentTime, isFinished, finishedAt, extraData, createdAt, updatedAt FROM mediaProgresses WHERE id = ?"
	before := e.Rows(t, query, rowID)
	if len(before) != 1 || before[0]["duration"] != float64(0) || before[0]["currentTime"] != float64(0) ||
		before[0]["isFinished"] != int64(1) || before[0]["finishedAt"] != created || before[0]["createdAt"] != created ||
		before[0]["updatedAt"] != created || before[0]["extraData"] != extraDataEncoding(e.IDs["novel"]) {
		t.Fatalf("fixture novel progress row: %v", before)
	}
	token := e.Token(t, "listener")
	r := e.Patch(t, token, "/api/me/progress/"+e.IDs["novel"], map[string]any{"currentTime": 55, "duration": 60, "isFinished": true})
	if r.Status != http.StatusOK {
		t.Fatalf("repeat finish status %d", r.Status)
	}
	after := e.Rows(t, query, rowID)
	if len(after) != 1 || after[0]["finishedAt"] != created || after[0]["createdAt"] != created ||
		after[0]["duration"] != float64(60) || after[0]["currentTime"] != float64(55) || after[0]["isFinished"] != int64(1) ||
		after[0]["extraData"] != extraDataEncoding(e.IDs["novel"]) {
		t.Fatalf("repeat finish row: %v", after)
	}
	updated, ok := after[0]["updatedAt"].(string)
	if !ok || updated <= created {
		t.Fatalf("updatedAt did not move forward: %v", after[0]["updatedAt"])
	}
	progress := getProgress(t, e, token, e.IDs["novel"])
	if progress.CurrentTime != 55 || !progress.IsFinished || progress.Progress != 1 || progress.FinishedAt == nil || *progress.FinishedAt != float64(fixtureFinishedAtMS) {
		t.Fatalf("repeat finish read: %+v", progress)
	}
}

// fixtureFinishedAtMS is the JSON milliseconds of the seeded novel finishedAt
// (2026-11-03 12:46:27.019 +00:00).
var fixtureFinishedAtMS = time.Date(2026, time.November, 3, 12, 46, 27, 19_000_000, time.UTC).UnixMilli()

// columnLinkAuthorOrder: the bookAuthors link rows keep creation order, the
// authorNames columns follow it, and removing an author keeps the author row
// that other books still link.
func columnLinkAuthorOrder(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	item := e.IDs["novel"]
	const bookID = "85ab0f32-b17f-4c9a-883e-a61e260f1e46"
	const ebookID = "83013005-1614-4732-8f4b-07858cfb5de0"
	const pinnedLinkCreated = "2026-01-01 00:00:00.000 +00:00"
	itemQuery := "SELECT title, authorNamesFirstLast, authorNamesLastFirst FROM libraryItems WHERE id = ?"
	before := e.Rows(t, itemQuery, item)
	if len(before) != 1 || before[0]["title"] != "Single File Novel" || before[0]["authorNamesFirstLast"] != "Ada Example" || before[0]["authorNamesLastFirst"] != "Example, Ada" {
		t.Fatalf("fixture item author columns: %v", before)
	}
	token := e.Token(t, "root")
	// First write: the only author. A second author is added in a later write.
	r := e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"authors": []any{map[string]any{"name": "Zed Aardvark"}}}})
	if r.Status != http.StatusOK {
		t.Fatalf("author PATCH status %d", r.Status)
	}
	if got := itemAuthorNames(t, e, itemQuery, item); got != "Zed Aardvark" {
		t.Fatalf("single author names: %q", got)
	}
	// Pin the first link row's createdAt so the later two-author write proves
	// the link-row creation order (GROUP_CONCAT ... ORDER BY createdAt ASC).
	e.Stop(t)
	e.Exec(t, "UPDATE bookAuthors SET createdAt = ? WHERE authorId = (SELECT id FROM authors WHERE name = 'Zed Aardvark')", pinnedLinkCreated)
	e.Restart(t)
	floor := time.Now()
	token = e.Token(t, "root")
	r = e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"authors": []any{map[string]any{"name": "Zed Aardvark"}, map[string]any{"name": "Aaron Zebra"}}}})
	if r.Status != http.StatusOK {
		t.Fatalf("second author PATCH status %d", r.Status)
	}
	authors := e.Rows(t, "SELECT id, name, lastFirst FROM authors WHERE name IN ('Zed Aardvark', 'Aaron Zebra') ORDER BY name")
	if len(authors) != 2 || authors[0]["name"] != "Aaron Zebra" || authors[0]["lastFirst"] != "Zebra, Aaron" || authors[1]["name"] != "Zed Aardvark" || authors[1]["lastFirst"] != "Aardvark, Zed" {
		t.Fatalf("author rows: %v", authors)
	}
	byName := make(map[string]string, len(authors))
	for _, author := range authors {
		name, _ := author["name"].(string)
		id, ok := author["id"].(string)
		if name == "" || !ok || id == "" {
			t.Fatalf("author row identity: %v", author)
		}
		byName[name] = id
	}
	links := e.Rows(t, "SELECT authorId, createdAt FROM bookAuthors WHERE bookId = ? ORDER BY createdAt", bookID)
	if len(links) != 2 || links[0]["authorId"] != byName["Zed Aardvark"] || links[1]["authorId"] != byName["Aaron Zebra"] {
		t.Fatalf("link rows: %v", links)
	}
	if links[0]["createdAt"] != pinnedLinkCreated {
		t.Fatalf("pinned link createdAt changed: %v", links[0]["createdAt"])
	}
	assertUTCWindow(t, "bookAuthors.createdAt", links[1]["createdAt"], floor)
	derived := e.Rows(t, itemQuery, item)
	if len(derived) != 1 || derived[0]["authorNamesFirstLast"] != "Zed Aardvark, Aaron Zebra" ||
		derived[0]["authorNamesLastFirst"] != "Aardvark, Zed, Zebra, Aaron" || derived[0]["title"] != "Single File Novel" {
		t.Fatalf("derived columns: %v", derived)
	}
	// The removed author stays because the ebook item still links it.
	if got := e.Count(t, "SELECT COUNT(*) FROM authors WHERE name = 'Ada Example'"); got != 1 {
		t.Fatalf("removed author rows: %d", got)
	}
	adaRows := e.Rows(t, "SELECT id FROM authors WHERE name = 'Ada Example'")
	adaID, ok := adaRows[0]["id"].(string)
	if len(adaRows) != 1 || !ok || adaID == "" {
		t.Fatalf("Ada author row: %v", adaRows)
	}
	if got := e.Count(t, "SELECT COUNT(*) FROM bookAuthors WHERE authorId = ? AND bookId = ?", adaID, bookID); got != 0 {
		t.Fatalf("Ada links on the novel: %d", got)
	}
	if got := e.Count(t, "SELECT COUNT(*) FROM bookAuthors WHERE authorId = ? AND bookId = ?", adaID, ebookID); got != 1 {
		t.Fatalf("Ada links on the ebook: %d", got)
	}
	got := e.Get(t, token, "/api/items/"+item)
	if got.Status != http.StatusOK {
		t.Fatalf("item GET status %d", got.Status)
	}
	var doc struct {
		Media struct {
			Metadata struct {
				Authors []struct {
					Name string `json:"name"`
				} `json:"authors"`
			} `json:"metadata"`
		} `json:"media"`
	}
	got.JSON(t, &doc)
	if len(doc.Media.Metadata.Authors) != 2 || doc.Media.Metadata.Authors[0].Name != "Zed Aardvark" || doc.Media.Metadata.Authors[1].Name != "Aaron Zebra" {
		t.Fatalf("served authors: %+v", doc.Media.Metadata.Authors)
	}
	// The bookSeries link row persists its sequence; an update rewrites it and
	// removing the last book deletes the now-empty series row.
	floor = time.Now()
	r = e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"series": []any{map[string]any{"name": "Store Saga", "sequence": "2"}}}})
	if r.Status != http.StatusOK {
		t.Fatalf("series PATCH status %d", r.Status)
	}
	series := e.Rows(t, "SELECT id, name, libraryId FROM series WHERE name = 'Store Saga'")
	if len(series) != 1 || series[0]["name"] != "Store Saga" || series[0]["libraryId"] != e.IDs["library"] {
		t.Fatalf("series rows: %v", series)
	}
	seriesID, ok := series[0]["id"].(string)
	if !ok || seriesID == "" {
		t.Fatalf("series id: %v", series[0])
	}
	links = e.Rows(t, "SELECT sequence, createdAt FROM bookSeries WHERE bookId = ? AND seriesId = ?", bookID, seriesID)
	if len(links) != 1 || links[0]["sequence"] != "2" {
		t.Fatalf("bookSeries link: %v", links)
	}
	assertUTCWindow(t, "bookSeries.createdAt", links[0]["createdAt"], floor)
	r = e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"series": []any{map[string]any{"name": "Store Saga", "sequence": "5"}}}})
	if r.Status != http.StatusOK {
		t.Fatalf("sequence PATCH status %d", r.Status)
	}
	links = e.Rows(t, "SELECT sequence, createdAt FROM bookSeries WHERE bookId = ? AND seriesId = ?", bookID, seriesID)
	if len(links) != 1 || links[0]["sequence"] != "5" {
		t.Fatalf("updated sequence: %v", links)
	}
	r = e.Patch(t, token, "/api/items/"+item+"/media", map[string]any{"metadata": map[string]any{"series": []any{}}})
	if r.Status != http.StatusOK {
		t.Fatalf("series removal PATCH status %d", r.Status)
	}
	if got := e.Count(t, "SELECT COUNT(*) FROM bookSeries WHERE bookId = ?", bookID); got != 0 {
		t.Fatalf("bookSeries links after removal: %d", got)
	}
	if got := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", seriesID); got != 0 {
		t.Fatalf("empty series row survived: %d", got)
	}
}

// columnJSONUnknownRetained: unknown JSON fields in a JSON column survive boot
// and are still there when the column is rewritten by a later write.
func columnJSONUnknownRetained(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	const seriesID = "e987a322-9111-4f79-8cb9-c25ee714e0d6"
	const injected = `{"seriesHideFromContinueListening":[],"storeLegacy":"retained"}`
	const rewritten = `{"seriesHideFromContinueListening":["` + seriesID + `"],"storeLegacy":"retained"}`
	query := "SELECT extraData, updatedAt FROM users WHERE id = ?"
	before := e.Rows(t, query, e.IDs["listener"])
	if len(before) != 1 || before[0]["extraData"] != `{"seriesHideFromContinueListening":[]}` {
		t.Fatalf("fixture listener extraData: %v", before)
	}
	e.Stop(t)
	if n := e.Exec(t, "UPDATE users SET extraData = ? WHERE id = ?", injected, e.IDs["listener"]); n != 1 {
		t.Fatalf("extraData update: %d", n)
	}
	e.Restart(t)
	afterBoot := e.Rows(t, query, e.IDs["listener"])
	if len(afterBoot) != 1 || afterBoot[0]["extraData"] != injected {
		t.Fatalf("unknown JSON field did not survive boot: %v", afterBoot)
	}
	token := e.Token(t, "listener")
	r := e.Get(t, token, "/api/me/series/"+seriesID+"/remove-from-continue-listening")
	if r.Status != http.StatusOK {
		t.Fatalf("remove-from-continue-listening status %d", r.Status)
	}
	if hidden, ok := r.Object(t)["seriesHideFromContinueListening"].([]any); !ok || len(hidden) != 1 || hidden[0] != seriesID {
		t.Fatalf("served seriesHideFromContinueListening: %v", r.Object(t)["seriesHideFromContinueListening"])
	}
	afterWrite := e.Rows(t, query, e.IDs["listener"])
	if len(afterWrite) != 1 || afterWrite[0]["extraData"] != rewritten {
		t.Fatalf("rewritten extraData: %v", afterWrite)
	}
	beforeUpdated, _ := before[0]["updatedAt"].(string)
	updated, ok := afterWrite[0]["updatedAt"].(string)
	if !ok || updated <= beforeUpdated {
		t.Fatalf("updatedAt did not move forward: %q", updated)
	}
	assertColumnTypes(t, e, "SELECT typeof(extraData) AS extraData FROM users WHERE id = ?", []any{e.IDs["listener"]}, map[string]string{"extraData": "text"})
}

// columnRejectedWrite: a rejected bookmark changes no column of the users row.
func columnRejectedWrite(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	query := "SELECT bookmarks, extraData, updatedAt, type, isActive FROM users WHERE id = ?"
	before := e.Rows(t, query, e.IDs["listener"])
	if len(before) != 1 {
		t.Fatalf("listener row: %v", before)
	}
	token := e.Token(t, "listener")
	for name, payload := range map[string]map[string]any{
		"time":  {"time": nil, "title": "Bad time"},
		"title": {"time": 5, "title": ""},
	} {
		r := e.Post(t, token, "/api/me/item/"+e.IDs["novel"]+"/bookmark", payload)
		if r.Status != http.StatusBadRequest {
			t.Fatalf("%s rejection status %d", name, r.Status)
		}
	}
	// Unknown items are rejected before any write on the progress, bookmark
	// and series-hide routes.
	const unknown = "00000000-0000-0000-0000-000000000000"
	if r := e.Patch(t, token, "/api/me/progress/"+unknown, map[string]any{"currentTime": 5}); r.Status != http.StatusNotFound {
		t.Fatalf("unknown progress PATCH status %d", r.Status)
	}
	if r := e.Get(t, token, "/api/me/progress/"+unknown); r.Status != http.StatusNotFound {
		t.Fatalf("unknown progress GET status %d", r.Status)
	}
	if r := e.Post(t, token, "/api/me/item/"+unknown+"/bookmark", map[string]any{"time": 5, "title": "Missing"}); r.Status != http.StatusNotFound {
		t.Fatalf("unknown bookmark POST status %d", r.Status)
	}
	if r := e.Get(t, token, "/api/me/bookmarks/"+unknown); r.Status != http.StatusNotFound {
		t.Fatalf("unknown bookmarks GET status %d", r.Status)
	}
	if r := e.Get(t, token, "/api/me/series/"+unknown+"/remove-from-continue-listening"); r.Status != http.StatusNotFound {
		t.Fatalf("unknown series remove status %d", r.Status)
	}
	if got := e.Rows(t, query, e.IDs["listener"]); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejection changed the users row: %v != %v", got, before)
	}
}

// columnFailedWriteJournal: a database write failure during bookmark creation
// exits the server and leaves the users row untouched; the installation loads
// and serves the original bookmarks after the trigger is removed and reopened.
func columnFailedWriteJournal(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	query := "SELECT username, bookmarks, extraData, updatedAt, isActive, type, lastSeen, permissions FROM users WHERE id = ?"
	before := e.Rows(t, query, e.IDs["listener"])
	if len(before) != 1 {
		t.Fatalf("listener row: %v", before)
	}
	token := e.Token(t, "listener")
	db := openRW(t, e)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	drop := abortTriggers(t, db, "users")
	payload, err := json.Marshal(map[string]any{"time": 77.5, "title": "Blocked"})
	if err != nil {
		t.Fatal(err)
	}
	// The aborted UPDATE exits the server before it can answer; the closed
	// connection is the client-visible failure.
	_, _ = rawJSONRequest(http.MethodPost, e.Base+"/api/me/item/"+e.IDs["saga1"]+"/bookmark", token, payload)
	code, exited := e.WaitExit(15 * time.Second)
	if !exited || code != 1 {
		t.Fatalf("aborted bookmark write: exited=%v code=%d", exited, code)
	}
	if got := e.Rows(t, query, e.IDs["listener"]); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed write changed the users row: %v != %v", got, before)
	}
	e.Stop(t)
	for _, statement := range drop {
		// The triggers only existed for this scenario; dropping them restores
		// the install for the reopen.
		_ = e.Exec(t, statement)
	}
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, query, e.IDs["listener"]); !reflect.DeepEqual(got, before) {
		t.Fatalf("reopened users row changed: %v != %v", got, before)
	}
	bookmarks := listBookmarks(t, reopened, e.IDs["saga1"])
	want := bookmark{LibraryItemID: e.IDs["saga1"], Time: 21, Title: "Good bit", CreatedAt: 1793709987082}
	if len(bookmarks) != 1 || bookmarks[0] != want {
		t.Fatalf("reopened saga1 bookmarks: %+v", bookmarks)
	}
}

// itemAuthorNames reads the trigger-maintained authorNamesFirstLast column.
func itemAuthorNames(t *testing.T, e *harness.Env, query, item string) string {
	t.Helper()
	rows := e.Rows(t, query, item)
	if len(rows) != 1 {
		t.Fatalf("item rows: %d", len(rows))
	}
	text, ok := rows[0]["authorNamesFirstLast"].(string)
	if !ok {
		t.Fatalf("authorNamesFirstLast: %v", rows[0])
	}
	return text
}

// getProgress reads the progress of one item through the API.
func getProgress(t *testing.T, e *harness.Env, token, item string) progressBody {
	t.Helper()
	r := e.Get(t, token, "/api/me/progress/"+item)
	if r.Status != http.StatusOK {
		t.Fatalf("progress GET status %d", r.Status)
	}
	var progress progressBody
	r.JSON(t, &progress)
	return progress
}

// progressBody is the MediaProgress JSON a client reads.
type progressBody struct {
	ID            string   `json:"id"`
	LibraryItemID string   `json:"libraryItemId"`
	MediaItemID   string   `json:"mediaItemId"`
	MediaItemType string   `json:"mediaItemType"`
	Duration      float64  `json:"duration"`
	CurrentTime   float64  `json:"currentTime"`
	Progress      float64  `json:"progress"`
	IsFinished    bool     `json:"isFinished"`
	FinishedAt    *float64 `json:"finishedAt"`
}

// createBookmark posts one bookmark and returns the created object.
func createBookmark(t *testing.T, e *harness.Env, token, item string, at float64, title string) bookmark {
	t.Helper()
	r := e.Post(t, token, "/api/me/item/"+item+"/bookmark", map[string]any{"time": at, "title": title})
	if r.Status != http.StatusOK {
		t.Fatalf("bookmark status %d", r.Status)
	}
	var created bookmark
	r.JSON(t, &created)
	if created.LibraryItemID != item || created.Time != at || created.Title != title {
		t.Fatalf("created bookmark: %+v", created)
	}
	return created
}

// decodeBookmarks decodes the users bookmarks TEXT column.
func decodeBookmarks(t *testing.T, rows []map[string]any) []bookmark {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("users rows: %d", len(rows))
	}
	raw, ok := rows[0]["bookmarks"].(string)
	if !ok {
		t.Fatalf("bookmarks column: %v", rows)
	}
	var values []bookmark
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		t.Fatalf("decode bookmarks: %v", err)
	}
	return values
}

// listBookmarks reads the bookmarks of one library item through the API.
func listBookmarks(t *testing.T, e *harness.Env, item string) []bookmark {
	t.Helper()
	token := e.Token(t, "listener")
	r := e.Get(t, token, "/api/me/bookmarks/"+item)
	if r.Status != http.StatusOK {
		t.Fatalf("bookmarks status %d", r.Status)
	}
	var body struct {
		Bookmarks []bookmark `json:"bookmarks"`
	}
	r.JSON(t, &body)
	return body.Bookmarks
}
