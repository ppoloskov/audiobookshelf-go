package items

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/libitem"
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

func TestItemsBookWithoutAuthorsOrSeries(t *testing.T) {
	db, _ := testDB(t)
	id := addBook(t, db, 1, "The Hobbit")
	it, err := LoadOne(context.Background(), db, id, Options{})
	if err != nil {
		t.Fatal(err)
	}
	min := it.Minified([]string{"the"})
	raw, _ := json.Marshal(min)
	var got struct {
		Media struct {
			Metadata                 map[string]any `json:"metadata"`
			NumTracks, NumAudioFiles int
			Size                     int64
		} `json:"media"`
		Files []map[string]any `json:"libraryFiles"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	md := got.Media.Metadata
	if md["authorName"] != "" || md["seriesName"] != "" || md["titleIgnorePrefix"] != "Hobbit, The" || got.Media.Size != 150 {
		t.Fatalf("minified metadata %v size %d", md, got.Media.Size)
	}
	if full := it.Full().Media.(libitem.BookFull); full.Metadata.Authors == nil || full.Metadata.Series == nil || full.EbookFile != nil {
		t.Fatalf("authors/series must be empty arrays and ebook null: %+v", full)
	}
	tracks := it.Expanded(nil).Media.(libitem.BookExpanded).Tracks
	if len(tracks) != 1 || tracks[0]["contentUrl"] != "/api/items/li-b1/file/a1" || tracks[0]["title"] != "a.mp3" {
		t.Fatalf("tracks = %v", tracks)
	}
	if it.OldLibraryItemID != "li_old" || it.LibraryFiles[0]["fileType"] != "audio" {
		t.Fatalf("row decode: %+v", it)
	}
	if a := it.Access(); a.LibraryID != "lib" || a.Explicit || len(a.Tags) != 1 {
		t.Fatalf("access = %+v", a)
	}
}

func TestItemsPodcastWithoutEpisodes(t *testing.T) {
	db, _ := testDB(t)
	exec(t, db, `INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('p1','A Show',1,0,'[]','[]',7,?,?)`, now, now)
	exec(t, db, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-p1','/p','p','p1','podcast',0,0,0,99,'[]',?,?,'lib')`, now, now)
	ctx := context.Background()
	it, err := LoadOne(ctx, db, "li-p1", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if it.Podcast.Episodes != nil {
		t.Fatal("episodes loaded without Options.Episodes")
	}
	if a := it.Access(); !a.Explicit {
		t.Fatal("podcast explicit flag missing from Access")
	}
	list := it.ListRow([]string{"a"}).Media.(libitem.PodcastMedia)
	if *list.NumEpisodes != 7 || *list.Size != 99 || *list.Metadata.TitleIgnorePrefix != "Show, A" {
		t.Fatalf("list row %+v", list)
	}
	if min := it.Minified(nil).Media.(libitem.PodcastMedia); *min.NumEpisodes != 0 || *min.Size != 0 {
		t.Fatalf("minified counts %d %d", *min.NumEpisodes, *min.Size)
	}
	it, err = LoadOne(ctx, db, "li-p1", Options{Episodes: true})
	if err != nil {
		t.Fatal(err)
	}
	exp := it.Expanded(nil).Media.(libitem.PodcastMedia)
	raw, _ := json.Marshal(exp)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if eps, ok := got["episodes"].([]any); !ok || len(eps) != 0 || got["numEpisodes"] != float64(0) {
		t.Fatalf("expanded empty podcast: %s", raw)
	}
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

func TestItemsAuthorsAndSeriesOrder(t *testing.T) {
	db, _ := testDB(t)
	id := addBook(t, db, 1, "B")
	exec(t, db, `INSERT INTO authors (id,name,createdAt,updatedAt) VALUES ('a1','Jane Doe',?,?),('a2','John Roe',?,?)`, now, now, now, now)
	exec(t, db, `INSERT INTO bookAuthors (id,createdAt,bookId,authorId) VALUES ('x1','2024-01-02 03:04:06.000 +00:00','book1','a2'),('x2',?,'book1','a1')`, now)
	exec(t, db, `INSERT INTO series (id,name,createdAt,updatedAt) VALUES ('s1','Saga',?,?)`, now, now)
	exec(t, db, `INSERT INTO bookSeries (id,sequence,createdAt,bookId,seriesId) VALUES ('y1','2',?,'book1','s1')`, now)
	it, err := LoadOne(context.Background(), db, id, Options{})
	if err != nil {
		t.Fatal(err)
	}
	md := it.Minified(nil).Media.(libitem.BookMinified).Metadata
	if md.AuthorName != "Jane Doe, John Roe" || md.AuthorNameLF != "Doe, Jane, Roe, John" || md.SeriesName != "Saga #2" {
		t.Fatalf("metadata %+v", md)
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
