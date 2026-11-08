package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// One watcher batch with an add, a directory rename and a delete: pins the
// emitted event sequence and ids. Current behaviour: there is no
// item_removed on this path; a delete marks the item missing and emits
// item_updated. A rename keeps the item id and emits item_updated for the new
// path. Events follow the order of the batch's item directories.
func TestWatcherBatchEventOrder(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{MetadataPath: t.TempDir()}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	R := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	writeBook(t, root, "Auth", "Old Name", "a.mp3")
	writeBook(t, root, "Auth", "Doomed", "d.mp3")
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	idOf := func(path string) string {
		var id string
		if err := db.QueryRow(`SELECT id FROM libraryItems WHERE path=?`, path).Scan(&id); err != nil {
			t.Fatalf("no item at %s: %v", path, err)
		}
		return id
	}
	renamedID, doomedID := idOf(R("Auth/Old Name")), idOf(R("Auth/Doomed"))

	// Create the new directory before deleting anything so a freed inode is
	// never reused by it (the watcher falls back to inode matching).
	writeBook(t, root, "Auth", "Fresh", "f.mp3")
	if err := os.Rename(R("Auth/Old Name"), R("Auth/New Name")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(R("Auth/Doomed")); err != nil {
		t.Fatal(err)
	}
	var upd []fileUpdate
	for _, u := range []struct{ rel, kind string }{
		{"Auth/Fresh/f.mp3", "added"},
		{"Auth/Old Name/a.mp3", "deleted"},
		{"Auth/New Name/a.mp3", "added"},
		{"Auth/Doomed/d.mp3", "deleted"},
	} {
		upd = append(upd, fileUpdate{path: R(u.rel), relPath: u.rel, folderID: "folder-1", libraryID: "lib-1"})
	}
	*evs = nil
	{
		res, err := s.scanFolderUpdates(ctx, "lib-1", "folder-1", upd)
		if err != nil {
			t.Fatal(err)
		}
		var seq []string
		for _, e := range *evs {
			if e.event != "item_added" && e.event != "item_updated" && e.event != "item_removed" {
				continue
			}
			items := payloadItems(t, e.payload)
			if len(items) != 1 {
				t.Fatalf("%s payload has %d items", e.event, len(items))
			}
			seq = append(seq, fmt.Sprintf("%s %v missing=%v", e.event, items[0]["path"], items[0]["isMissing"]))
		}
		{
			want := []string{
				"item_added " + R("Auth/Fresh") + " missing=false",
				"item_updated " + R("Auth/Old Name") + " missing=true",
				"item_updated " + R("Auth/New Name") + " missing=false",
				"item_updated " + R("Auth/Doomed") + " missing=true",
			}
			if !reflect.DeepEqual(seq, want) {
				t.Fatalf("events:\n got %q\nwant %q", seq, want)
			}
			// Ids: the added item is new; the renamed item keeps its id; the
			// deleted item keeps its row, flagged missing.
			var ids []string
			for _, e := range *evs {
				if e.event == "item_added" || e.event == "item_updated" {
					ids = append(ids, payloadItems(t, e.payload)[0]["id"].(string))
				}
			}
			freshID := idOf(R("Auth/Fresh"))
			if want := []string{freshID, renamedID, renamedID, doomedID}; !reflect.DeepEqual(ids, want) {
				t.Fatalf("payload ids = %v, want %v", ids, want)
			}
			if idOf(R("Auth/New Name")) != renamedID {
				t.Fatal("rename changed the item id")
			}
			if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE id=? AND isMissing=1`, doomedID); n != 1 {
				t.Fatal("deleted item must be flagged missing, not removed")
			}
			wantRes := map[string]int{"Auth/Fresh": scanAdded, "Auth/Old Name": scanRemoved, "Auth/New Name": scanUpdated, "Auth/Doomed": scanRemoved}
			if !reflect.DeepEqual(res, wantRes) {
				t.Fatalf("results = %v, want %v", res, wantRes)
			}
		}
	}
}
