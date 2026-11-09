package itemwrite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/filterdata"

	_ "modernc.org/sqlite"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

// openDB opens a file database with the real schema, foreign keys on, one
// library (l1) and one book (b1).
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "db.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		schemaassets.SchemaSQL,
		`INSERT INTO libraries(id,name,displayOrder,icon,mediaType,provider,settings,extraData,createdAt,updatedAt) VALUES('l1','Lib',0,'books','book','google','{}','{}','` + stamp + `','` + stamp + `')`,
		`INSERT INTO books(id,title,createdAt,updatedAt) VALUES('b1','Book','` + stamp + `','` + stamp + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(stmt, err)
		}
	}
	return db
}

// mustExec inserts fixture rows; they carry the NOT NULL timestamps.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

type recorder struct {
	mu     sync.Mutex
	events []string
	names  []string
}

func (r *recorder) emit(_ string, event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, event)
	id := any("")
	if m, ok := payload.(map[string]any); ok {
		id = m["id"]
	}
	r.events = append(r.events, fmt.Sprintf("%s %v", event, id))
}

func TestSweepAuthorsRemovesOnlyUnguardedOrphans(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `INSERT INTO authors(id,name,asin,description,libraryId,createdAt,updatedAt) VALUES ('orphan','O',NULL,'','l1',?,?),('withAsin','W','B00X',NULL,'l1',?,?),('linked','L',NULL,NULL,'l1',?,?)`, stamp, stamp, stamp, stamp, stamp, stamp)
	mustExec(t, db, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES ('ba1','b1','linked',?)`, stamp)
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
	mustExec(t, db, `INSERT INTO series(id,name,libraryId,createdAt,updatedAt) VALUES ('s1','S','l1',?,?)`, stamp, stamp)
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
		if _, err := db.Exec(`INSERT INTO authors(id,name,libraryId,createdAt,updatedAt) VALUES (?,?,'l1',?,?)`, author, author, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var linkErr error
		wg.Add(2)
		go func() { defer wg.Done(); e.SweepAuthors(context.Background(), []string{author}) }()
		go func() {
			defer wg.Done()
			_, linkErr = db.Exec(`INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES (?,'b1',?,?)`, link, author, stamp)
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
	mustExec(t, db, `INSERT INTO authors(id,name,libraryId,createdAt,updatedAt) VALUES ('orphan','O','l1',?,?)`, stamp, stamp)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Env{DB: db, Filters: filterdata.NewCache()}.SweepAuthors(ctx, []string{"orphan"})
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM authors`).Scan(&n)
	if n != 0 {
		t.Fatal("a cancelled request context left the orphan behind")
	}
}
