package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// LibraryScanner.js:189-199 only queues a newly-missing item; :239-253 then
// stamps its lastScan and lastScanVersion. A later missing scan must not stamp
// it again because the row is not queued a second time.
func TestWaveGMissingScanStampsOnlyFirstMissingTransition(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	dir := filepath.Join(root, "Author", "Book")
	writeBook(t, root, "Author", "Book", "a.mp3")
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM libraryItems`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	const staleScan = "2000-01-01 00:00:00.000 +00:00"
	if _, err := db.Exec(`UPDATE libraryItems SET lastScan=?,lastScanVersion='old' WHERE id=?`, staleScan, id); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	first, err := s.Scan(ctx, "lib-1", false)
	if err != nil || first.Missing != 1 {
		t.Fatalf("first missing scan=%+v err=%v", first, err)
	}
	var lastScan, version string
	if err := db.QueryRow(`SELECT lastScan,lastScanVersion FROM libraryItems WHERE id=?`, id).Scan(&lastScan, &version); err != nil {
		t.Fatal(err)
	}
	if lastScan == staleScan || version != "2.37.2" {
		t.Fatalf("first missing bookkeeping lastScan=%q version=%q; want refreshed timestamp and pinned version", lastScan, version)
	}
	firstStamp := lastScan
	second, err := s.Scan(ctx, "lib-1", false)
	if err != nil || second.Missing != 1 {
		t.Fatalf("repeat missing scan=%+v err=%v", second, err)
	}
	if err := db.QueryRow(`SELECT lastScan,lastScanVersion FROM libraryItems WHERE id=?`, id).Scan(&lastScan, &version); err != nil {
		t.Fatal(err)
	}
	if lastScan != firstStamp || version != "2.37.2" {
		t.Fatalf("repeat missing bookkeeping lastScan=%q version=%q; want unchanged %q / 2.37.2", lastScan, version, firstStamp)
	}
}

// A failed TS bulk stamp happens after the item has already been saved as
// missing and its update emitted. A later scan does not retry that bulk write
// because the item is no longer newly missing.
func TestWaveGMissingStampFailureLeavesTheSavedMissingState(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	writeBook(t, root, "Author", "Book", "a.mp3")
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM libraryItems`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	const staleScan = "2000-01-01 00:00:00.000 +00:00"
	if _, err := db.Exec(`UPDATE libraryItems SET lastScan=?,lastScanVersion='old' WHERE id=?`, staleScan, id); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "Author", "Book")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_missing_stamp BEFORE UPDATE ON libraryItems WHEN NEW.lastScanVersion='2.37.2' BEGIN SELECT RAISE(ABORT, 'stamp-fail'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(ctx, "lib-1", false); err == nil {
		t.Fatal("missing stamp failure was ignored")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE id=? AND isMissing=1 AND lastScan=? AND lastScanVersion='old'`, id, staleScan); n != 1 {
		t.Fatal("failed stamp should retain the earlier missing-item save")
	}
	if got := eventsNamed(evs, "items_updated"); len(got) != 1 {
		t.Fatalf("item update events=%d, want event emitted before failed stamp", len(got))
	}
	if _, err := db.Exec(`DROP TRIGGER fail_missing_stamp`); err != nil {
		t.Fatal(err)
	}
	*evs = nil
	second, err := s.Scan(ctx, "lib-1", false)
	if err != nil || second.Missing != 1 {
		t.Fatalf("retry missing scan=%+v err=%v", second, err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE id=? AND isMissing=1 AND lastScan=? AND lastScanVersion='old'`, id, staleScan); n != 1 {
		t.Fatal("retry should not restamp an already-missing item, matching TS queue semantics")
	}
}

// A watcher deletion followed by a file reappearing at the same item path must
// clear isMissing on the existing row and keep its identity.
func TestWaveGWatcherMissingItemReappearsWithSameIdentity(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	writeBook(t, root, "Author", "Book", "a.mp3")
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	itemPath := filepath.Join(root, "Author", "Book")
	var id string
	if err := db.QueryRow(`SELECT id FROM libraryItems WHERE path=?`, itemPath).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(itemPath); err != nil {
		t.Fatal(err)
	}
	update := func(rel string) []fileUpdate {
		return []fileUpdate{{path: filepath.Join(root, filepath.FromSlash(rel)), relPath: rel, folderID: "folder-1", libraryID: "lib-1"}}
	}
	if _, err := s.scanFolderUpdates(ctx, "lib-1", "folder-1", update("Author/Book/a.mp3")); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE id=? AND isMissing=1`, id); n != 1 {
		t.Fatal("watcher deletion did not mark the stored item missing")
	}
	*evs = nil
	writeBook(t, root, "Author", "Book", "a.mp3")
	if _, err := s.scanFolderUpdates(ctx, "lib-1", "folder-1", update("Author/Book/a.mp3")); err != nil {
		t.Fatal(err)
	}
	var gotID string
	var missing int
	if err := db.QueryRow(`SELECT id,isMissing FROM libraryItems WHERE path=?`, itemPath).Scan(&gotID, &missing); err != nil {
		t.Fatal(err)
	}
	if gotID != id || missing != 0 {
		t.Fatalf("reappeared item id=%q missing=%d; want id=%q missing=0", gotID, missing, id)
	}
	updated := eventsNamed(evs, "item_updated")
	if len(updated) != 1 || payloadItems(t, updated[0].payload)[0]["id"] != id {
		t.Fatalf("reappearance events=%+v; want one item_updated for %s", updated, id)
	}
}
