package itemwrite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/filterdata"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "db.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		"CREATE TABLE books (id TEXT PRIMARY KEY)",
		"CREATE TABLE authors (id TEXT PRIMARY KEY, libraryId TEXT, asin TEXT, description TEXT, imagePath TEXT)",
		"CREATE TABLE series (id TEXT PRIMARY KEY, libraryId TEXT)",
		"CREATE TABLE bookAuthors (id TEXT PRIMARY KEY, bookId TEXT REFERENCES books(id) ON DELETE CASCADE, authorId TEXT REFERENCES authors(id) ON DELETE CASCADE)",
		"CREATE TABLE bookSeries (id TEXT PRIMARY KEY, bookId TEXT REFERENCES books(id) ON DELETE CASCADE, seriesId TEXT REFERENCES series(id) ON DELETE CASCADE)",
		"CREATE TABLE feeds (id TEXT PRIMARY KEY, entityType TEXT, entityId TEXT, feedUrl TEXT, title TEXT, description TEXT, preventIndexing INTEGER, ownerName TEXT, ownerEmail TEXT)",
		"INSERT INTO books VALUES ('b1')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(stmt, err)
		}
	}
	return db
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) emit(_ string, event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("%s %v", event, payload.(map[string]any)["id"]))
}

func TestSweepAuthorsRemovesOnlyUnguardedOrphans(t *testing.T) {
	db := openDB(t)
	db.Exec(`INSERT INTO authors VALUES ('orphan','l1',NULL,'',NULL),('withAsin','l1','B00X',NULL,NULL),('linked','l1',NULL,NULL,NULL)`)
	db.Exec(`INSERT INTO bookAuthors VALUES ('ba1','b1','linked')`)
	var r recorder
	e := Env{DB: db, Filters: filterdata.NewCache(), Emit: r.emit}
	e.SweepAuthors(context.Background(), []string{"orphan", "withAsin", "linked", "orphan", ""})
	var left []string
	rows, _ := db.Query(`SELECT id FROM authors ORDER BY id`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if fmt.Sprint(left) != "[linked withAsin]" || fmt.Sprint(r.events) != "[author_removed orphan]" {
		t.Fatalf("left %v events %v", left, r.events)
	}
}

func TestOverlappingSweepsAnnounceOnce(t *testing.T) {
	db := openDB(t)
	db.Exec(`INSERT INTO series VALUES ('s1','l1')`)
	var r recorder
	e := Env{DB: db, Filters: filterdata.NewCache(), Emit: r.emit}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.SweepSeries(context.Background(), []string{"s1"}) }()
	}
	wg.Wait()
	if fmt.Sprint(r.events) != "[series_removed s1]" {
		t.Fatalf("events %v, want one series_removed", r.events)
	}
}

// TestSweepNeverDropsAConcurrentLink races a sweep against a request that
// links a book to the author. If the link was inserted it must survive: a
// check-then-delete sweep would delete the author and, through ON DELETE
// CASCADE, the new link.
func TestSweepNeverDropsAConcurrentLink(t *testing.T) {
	db := openDB(t)
	e := Env{DB: db, Filters: filterdata.NewCache()}
	for i := 0; i < 1500; i++ {
		author, link := fmt.Sprintf("a%d", i), fmt.Sprintf("ba%d", i)
		if _, err := db.Exec(`INSERT INTO authors VALUES (?,'l1',NULL,NULL,NULL)`, author); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var linkErr error
		wg.Add(2)
		go func() { defer wg.Done(); e.SweepAuthors(context.Background(), []string{author}) }()
		go func() {
			defer wg.Done()
			_, linkErr = db.Exec(`INSERT INTO bookAuthors VALUES (?,'b1',?)`, link, author)
		}()
		wg.Wait()
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM bookAuthors WHERE id=?`, link).Scan(&n)
		if linkErr == nil && n != 1 {
			t.Fatalf("iteration %d: the link was inserted but the sweep dropped it", i)
		}
	}
}

func TestSweepIgnoresRequestCancellation(t *testing.T) {
	db := openDB(t)
	db.Exec(`INSERT INTO authors VALUES ('orphan','l1',NULL,NULL,NULL)`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Env{DB: db, Filters: filterdata.NewCache()}.SweepAuthors(ctx, []string{"orphan"})
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM authors`).Scan(&n)
	if n != 0 {
		t.Fatal("a cancelled request context left the orphan behind")
	}
}
