package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

func countRows(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func writeBook(t *testing.T, root, author, book string, tracks ...string) {
	t.Helper()
	dir := filepath.Join(root, author, book)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tr := range tracks {
		if err := os.WriteFile(filepath.Join(dir, tr), []byte(book+tr), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A failing libraryItems write (the last statement of an item's transaction)
// rolls back that item's books/authors rows. Pinned current behaviour: Scan
// returns the error unwrapped and stops, so items already imported stay
// committed, later items are not attempted, and no items_added event or
// library lastScan update is emitted for the aborted scan.
func TestScanNewItemFailureRollsBackAndAbortsScan(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	writeBook(t, root, "Author A", "Book A", "01.mp3")
	writeBook(t, root, "Author B", "Book B", "01.mp3")
	writeBook(t, root, "Author C", "Book C", "01.mp3")
	if _, err := db.Exec(`CREATE TRIGGER fail_b BEFORE INSERT ON libraryItems WHEN NEW.path LIKE '%Book B' BEGIN SELECT RAISE(ABORT, 'boom-b'); END`); err != nil {
		t.Fatal(err)
	}
	res, err := s.Scan(context.Background(), "lib-1")
	if err == nil || !strings.Contains(err.Error(), "boom-b") || strings.HasPrefix(err.Error(), "scanner:") {
		t.Fatalf("err = %v, want the raw sqlite error containing boom-b", err)
	}
	if res.Added != 1 {
		t.Fatalf("result = %+v, want only Book A counted", res)
	}
	var titles []string
	rows, err := db.Query(`SELECT title FROM libraryItems ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		titles = append(titles, title)
	}
	rows.Close()
	if strings.Join(titles, ",") != "Book A" {
		t.Fatalf("committed items = %v, want [Book A] (B rolled back, C never attempted)", titles)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM books`); n != 1 {
		t.Fatalf("books = %d, want 1 (Book B's insert rolled back)", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM authors`); n != 1 {
		t.Fatalf("authors = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM bookAuthors`); n != 1 {
		t.Fatalf("bookAuthors = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraries WHERE id='lib-1' AND lastScan IS NOT NULL`); n != 0 {
		t.Fatal("aborted scan must not stamp libraries.lastScan")
	}
	if got := eventsNamed(evs, "items_added"); len(got) != 0 {
		t.Fatalf("items_added emitted %d times; the pending chunk is dropped on abort", len(got))
	}
}

// Same seam on a rescan: the failing item's books row (updated earlier in the
// transaction) is rolled back, items before it keep their update, items after
// it are not rescanned, and the error is returned unwrapped.
func TestScanExistingItemFailureRollsBackAndAbortsScan(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, n := range []string{"A", "B", "C"} {
		writeBook(t, root, "Author "+n, "Book "+n, "01.mp3")
	}
	ctx := context.Background()
	if _, err := s.Scan(ctx, "lib-1"); err != nil {
		t.Fatal(err)
	}
	// Stored order is insertion (path) order: A, B, C.
	for _, n := range []string{"A", "B", "C"} {
		writeBook(t, root, "Author "+n, "Book "+n, "02.mp3")
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_b BEFORE UPDATE ON libraryItems WHEN NEW.path LIKE '%Book B' BEGIN SELECT RAISE(ABORT, 'boom-b'); END`); err != nil {
		t.Fatal(err)
	}
	*evs = nil
	res, err := s.Scan(ctx, "lib-1")
	if err == nil || !strings.Contains(err.Error(), "boom-b") {
		t.Fatalf("err = %v, want boom-b", err)
	}
	if res.Updated != 1 {
		t.Fatalf("result = %+v, want only Book A counted", res)
	}
	tracks := func(book string) int {
		return countRows(t, db, `SELECT json_array_length(b.audioFiles) FROM books b WHERE b.title=?`, book)
	}
	files := func(book string) int {
		return countRows(t, db, `SELECT json_array_length(libraryFiles) FROM libraryItems WHERE title=?`, book)
	}
	for book, want := range map[string]int{"Book A": 2, "Book B": 1, "Book C": 1} {
		if got := tracks(book); got != want {
			t.Errorf("%s audioFiles = %d, want %d", book, got, want)
		}
		if got := files(book); got != want {
			t.Errorf("%s libraryFiles = %d, want %d", book, got, want)
		}
	}
	if got := eventsNamed(evs, "items_updated"); len(got) != 0 {
		t.Fatalf("items_updated emitted %d times; the pending chunk is dropped on abort", len(got))
	}
	// The failure is not sticky: once the cause is gone the next scan imports B and C.
	if _, err := db.Exec(`DROP TRIGGER fail_b`); err != nil {
		t.Fatal(err)
	}
	res, err = s.Scan(ctx, "lib-1")
	if err != nil {
		t.Fatal(fmt.Errorf("retry: %w", err))
	}
	if res.Updated != 2 || tracks("Book B") != 2 || tracks("Book C") != 2 {
		t.Fatalf("retry result = %+v, B=%d C=%d", res, tracks("Book B"), tracks("Book C"))
	}
}
