package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
)

func batchRig(t *testing.T) *authorRig {
	t.Helper()
	rig := newAuthorRig(t, nil)
	for _, ddl := range []string{
		`CREATE TABLE mediaProgresses (id TEXT,userId TEXT,mediaItemId TEXT)`,
		`CREATE TABLE playlistMediaItems (id TEXT,mediaItemId TEXT,mediaItemType TEXT,"order" INTEGER,playlistId TEXT)`,
		`CREATE TABLE playlists (id TEXT,userId TEXT)`,
		`CREATE TABLE feeds (id TEXT,entityType TEXT,entityId TEXT)`,
		`CREATE TABLE mediaItemShares (id TEXT,mediaItemType TEXT,mediaItemId TEXT)`,
	} {
		if _, err := rig.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func post(rig *authorRig, target, body string, user store.User) (int, string) {
	w := rig.do("POST", target, body, user)
	return w.Code, w.Body.String()
}

func TestBatchUpdateValidation(t *testing.T) {
	rig := batchRig(t)
	cases := []struct {
		name, body string
		user       store.User
		status     int
	}{
		{"no update permission", `[{"id":"i1","mediaPayload":{}}]`, readOnlyUser(), 403},
		{"not an array", `{"id":"i1"}`, rootUser(), 400},
		{"empty array", `[]`, rootUser(), 400},
		{"entry without id", `[{"mediaPayload":{}}]`, rootUser(), 400},
		{"duplicate ids", `[{"id":"i1"},{"id":"i1"}]`, rootUser(), 400},
		{"unknown item", `[{"id":"i1","mediaPayload":{}},{"id":"nope","mediaPayload":{}}]`, rootUser(), 404},
	}
	for _, c := range cases {
		if status, body := post(rig, "/api/items/batch/update", c.body, c.user); status != c.status {
			t.Errorf("%s: %d %q, want %d", c.name, status, body, c.status)
		}
	}
	// A user who cannot see one of the items gets 403 for the whole batch.
	if _, err := rig.db.Exec(`UPDATE books SET explicit=1 WHERE id='b2'`); err != nil {
		t.Fatal(err)
	}
	clean := store.User{ID: "u9", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"update":true}`)}
	if status, _ := post(rig, "/api/items/batch/update", `[{"id":"i1","mediaPayload":{}},{"id":"i2","mediaPayload":{}}]`, clean); status != 403 {
		t.Fatalf("explicit item: %d", status)
	}
}

func TestBatchUpdateCountsOnlyRealChanges(t *testing.T) {
	rig := batchRig(t)
	status, body := post(rig, "/api/items/batch/update", `[
		{"id":"i1","mediaPayload":{"metadata":{"title":"The Book","genres":["fiction"]},"tags":["tag"]}},
		{"id":"i2","mediaPayload":{"metadata":{"title":"The New Second","subtitle":"Sub","publishedYear":1999,"authors":[{"name":"Ada Author"}]}}}
	]`, rootUser())
	if status != 200 || !sameJSON(body, `{"success":true,"updates":1}`) {
		t.Fatalf("%d %s", status, body)
	}
	var title, ignore, subtitle, year string
	if err := rig.db.QueryRow(`SELECT title,titleIgnorePrefix,subtitle,publishedYear FROM books WHERE id='b2'`).Scan(&title, &ignore, &subtitle, &year); err != nil {
		t.Fatal(err)
	}
	if title != "The New Second" || ignore != "New Second" || subtitle != "Sub" || year != "1999" {
		t.Fatalf("book %q %q %q %q", title, ignore, subtitle, year)
	}
	// b2 moved from Ben Writer to Ada Author: Ben has no book, asin or image left.
	var links int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM bookAuthors WHERE bookId='b2' AND authorId='a1'`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("link %d %v", links, err)
	}
	var benLeft int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM authors WHERE id='a2'`).Scan(&benLeft); err != nil || benLeft != 0 {
		t.Fatalf("empty author kept: %d %v", benLeft, err)
	}
	var names []string
	for _, e := range rig.takeEvents() {
		names = append(names, e.event)
	}
	// Every media write finishes in one order: author events, the empty-author
	// removal, then item_updated for each saved item.
	if got := strings.Join(names, ","); got != "author_updated,author_removed,item_updated" {
		t.Fatalf("events %s", got)
	}
	if len(rig.touched) != 1 || rig.touched[0] != "i2" {
		t.Fatalf("metadata files %v", rig.touched)
	}
	// The same payload again changes nothing.
	status, body = post(rig, "/api/items/batch/update", `[{"id":"i2","mediaPayload":{"metadata":{"title":"The New Second"}}}]`, rootUser())
	if status != 200 || !sameJSON(body, `{"success":true,"updates":0}`) || len(rig.takeEvents()) != 0 {
		t.Fatalf("repeat: %d %s", status, body)
	}
}

func TestBatchDelete(t *testing.T) {
	rig := batchRig(t)
	itemDir := t.TempDir()
	hardDir := filepath.Join(itemDir, "Second")
	if err := os.MkdirAll(hardDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hardDir, "a.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, itemDir); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`UPDATE libraryItems SET path=? WHERE id='i2'`, hardDir); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, target, body string
		user               store.User
		status             int
	}{
		{"no delete permission", "/api/items/batch/delete", `{"libraryItemIds":["i1"]}`, readOnlyUser(), 403},
		{"no ids", "/api/items/batch/delete", `{}`, rootUser(), 400},
		{"empty ids", "/api/items/batch/delete", `{"libraryItemIds":[]}`, rootUser(), 400},
		{"ids not an array", "/api/items/batch/delete", `{"libraryItemIds":"i1"}`, rootUser(), 400},
		{"nothing found", "/api/items/batch/delete", `{"libraryItemIds":["nope"]}`, rootUser(), 404},
	} {
		if status, body := post(rig, c.target, c.body, c.user); status != c.status {
			t.Errorf("%s: %d %q, want %d", c.name, status, body, c.status)
		}
	}
	// Soft: the item and its now-empty author go, the files stay.
	status, body := post(rig, "/api/items/batch/delete", `{"libraryItemIds":["i2","unknown"]}`, rootUser())
	if status != 200 || body != "OK" {
		t.Fatalf("soft delete: %d %q", status, body)
	}
	if _, err := os.Stat(hardDir); err != nil {
		t.Fatal("soft delete removed files")
	}
	var names []string
	for _, e := range rig.takeEvents() {
		names = append(names, e.event)
	}
	if got := strings.Join(names, ","); got != "item_removed,author_removed" {
		t.Fatalf("events %s", got)
	}
	// Hard: ?hard=1 (any value that equals 1 as a number) removes the folder.
	if _, err := rig.db.Exec(`INSERT INTO libraryItems SELECT 'i3','ino-3',?,'Third','b3','book',0,0,0,mtime,ctime,birthtime,size,NULL,NULL,'[]','{}','Third','Third','','',libraryId,libraryFolderId,createdAt,updatedAt FROM libraryItems WHERE id='i1'`, hardDir); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`INSERT INTO books SELECT 'b3',title,titleIgnorePrefix,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt FROM books WHERE id='b1'`); err != nil {
		t.Fatal(err)
	}
	if status, body := post(rig, "/api/items/batch/delete?hard=01", `{"libraryItemIds":["i3"]}`, rootUser()); status != 200 {
		t.Fatalf("hard delete: %d %q", status, body)
	}
	if _, err := os.Stat(hardDir); err == nil {
		t.Fatal("hard delete kept the folder")
	}
	if status, _ := post(rig, "/api/items/batch/delete", `{"libraryItemIds":["i3"]}`, rootUser()); status != 404 {
		t.Fatalf("deleted again: %d", status)
	}
}

// sameJSON reports whether got and want are the same JSON value; key order and
// whitespace are not part of the contract (ADR-002).
func sameJSON(got, want string) bool {
	var g, w any
	return json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
}

// A saved podcast whose auto-download schedule changed refreshes its cron entry
// (LibraryItemController.js:276); an unchanged one does not.
func TestFinishItemWritesReschedulesPodcast(t *testing.T) {
	var rescheduled []string
	h := &Handler{deps: Deps{PodcastScheduleChanged: func(_ context.Context, id string) { rescheduled = append(rescheduled, id) }}}
	err := h.finishItemWrites(context.Background(),
		itemWrite{itemID: "changed", updated: true, reschedule: true},
		itemWrite{itemID: "other", updated: true},
		itemWrite{itemID: "unsaved", reschedule: true})
	if err != nil || len(rescheduled) != 1 || rescheduled[0] != "changed" {
		t.Fatalf("rescheduled %v, err %v", rescheduled, err)
	}
}
