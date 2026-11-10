package items

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/podcasts/read"
	"modernc.org/sqlite"
)

// countingConnector counts queries that return rows.
type countingConnector struct {
	path  string
	count *atomic.Int64
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.path)
	if err != nil {
		return nil, err
	}
	return countingConn{Conn: conn, count: c.count}, nil
}
func (c countingConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type countingConn struct {
	driver.Conn
	count *atomic.Int64
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.count.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func testDB(t *testing.T) (*sql.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	schema, err := os.ReadFile("../../contracts/parity/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	count := &atomic.Int64{}
	db := sql.OpenDB(countingConnector{path: path, count: count})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db, count
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

const now = "2024-01-02 03:04:05.000 +00:00"

func addBook(t *testing.T, db *sql.DB, n int, title string) string {
	t.Helper()
	id, bid := fmt.Sprintf("li-b%d", n), fmt.Sprintf("book%d", n)
	exec(t, db, `INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES (?,?,0,0,10,'["t1"]','[]','[]','[{"ino":"a1","duration":4,"metadata":{"filename":"a.mp3","size":100}},{"ino":"a2","exclude":true,"duration":6,"metadata":{"size":50}}]','[]',?,?)`, bid, title, now, now)
	exec(t, db, `INSERT INTO libraryItems (id,ino,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES (?,?,?,?,?,'book',0,0,0,150,'[{"ino":"a1","metadata":{"ext":".mp3"}}]','{"oldLibraryItemId":"li_old"}',?,?,'lib')`, id, "ino"+id, "/p/"+id, id, bid, now, now)
	return id
}

func TestItemsMissingRowsAndOrder(t *testing.T) {
	db, _ := testDB(t)
	a, b := addBook(t, db, 1, "A"), addBook(t, db, 2, "B")
	exec(t, db, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-orphan','/o','o','nope','book','[]',?,?,'lib')`, now, now)
	ctx := context.Background()
	items, err := Load(ctx, db, []string{b, "missing", "li-orphan", a, b}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{b, a, b}) {
		t.Fatalf("order = %v", got)
	}
	for _, id := range []string{"missing", "li-orphan"} {
		if _, err := LoadOne(ctx, db, id, Options{}); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("LoadOne(%s) = %v, want ErrNoRows", id, err)
		}
	}
	if items, err := Load(ctx, db, nil, Options{}); err != nil || len(items) != 0 {
		t.Fatalf("empty Load = %v, %v", items, err)
	}
}

func TestItemsRowDecodingAndAccess(t *testing.T) {
	db, _ := testDB(t)
	it, err := LoadOne(context.Background(), db, addBook(t, db, 1, "The Hobbit"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	// addBook stores extraData.oldLibraryItemId and a .mp3 library file; both
	// must be decoded (oldLibraryItemId) and derived (fileType from ext).
	if it.OldLibraryItemID != "li_old" || it.LibraryFiles[0]["fileType"] != "audio" {
		t.Fatalf("row decode: oldLibraryItemID %q, libraryFiles %+v", it.OldLibraryItemID, it.LibraryFiles)
	}
	if a := it.Access(); a.LibraryID != "lib" || a.Explicit || len(a.Tags) != 1 || a.Tags[0] != "t1" {
		t.Fatalf("access = %+v", a)
	}
}

func TestItemsPodcastListRow(t *testing.T) {
	db, _ := testDB(t)
	exec(t, db, `INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('p1','A Show',1,0,'[]','[]',7,?,?)`, now, now)
	exec(t, db, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-p1','/p','p','p1','podcast',0,0,0,99,'[]',?,?,'lib')`, now, now)
	it, err := LoadOne(context.Background(), db, "li-p1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	row := it.ListRow([]string{"a"}).Media.(podcastread.PodcastMedia)
	if *row.NumEpisodes != 7 || *row.Size != 99 || row.Metadata.TitleIgnorePrefix == nil || *row.Metadata.TitleIgnorePrefix != "Show, A" {
		t.Fatalf("list row = %+v", row)
	}
}

func TestItemsLoadQueryCountIsConstant(t *testing.T) {
	db, count := testDB(t)
	var ids []string
	for i := 0; i < 120; i++ {
		ids = append(ids, addBook(t, db, i, "T"))
	}
	measure := func(ids []string) int64 {
		count.Store(0)
		if _, err := Load(context.Background(), db, ids, Options{}); err != nil {
			t.Fatal(err)
		}
		return count.Load()
	}
	// The loader batches by relation, so the number of queries must not grow with the
	// number of items (no per-row query). The absolute count is not a contract.
	one, many := measure(ids[:1]), measure(ids)
	if one == 0 || many > one {
		t.Fatalf("queries: 1 item = %d, 120 items = %d; loading must not query per item", one, many)
	}
}
