package books

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const testNow = "2024-01-02 03:04:05.000 +00:00"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema, err := os.ReadFile("../../contracts/parity/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func addTestBook(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES ('book1','The Hobbit',0,0,10,'["t1"]','[]','[]','[{"ino":"a1","duration":4,"metadata":{"filename":"a.mp3","size":100}},{"ino":"a2","exclude":true,"duration":6,"metadata":{"size":50}}]','[]',?,?)`, testNow, testNow); err != nil {
		t.Fatal(err)
	}
}

func loadTestBook(t *testing.T, db *sql.DB) *Book {
	t.Helper()
	loaded, err := LoadMany(context.Background(), db, []string{"book1"})
	if err != nil {
		t.Fatal(err)
	}
	book := loaded["book1"]
	if book == nil {
		t.Fatal("book1 was not loaded")
	}
	return book
}

func TestBookWithoutAuthorsOrSeries(t *testing.T) {
	db := testDB(t)
	addTestBook(t, db)
	book := loadTestBook(t, db)
	raw, _ := json.Marshal(book.Minified([]string{"the"}))
	var min struct {
		Metadata struct {
			AuthorName        string
			SeriesName        string
			TitleIgnorePrefix string
		}
		NumTracks int
		Size      int64
	}
	if err := json.Unmarshal(raw, &min); err != nil {
		t.Fatal(err)
	}
	if min.Metadata.AuthorName != "" || min.Metadata.SeriesName != "" || min.Metadata.TitleIgnorePrefix != "Hobbit, The" || min.NumTracks != 1 || min.Size != 150 {
		t.Fatalf("minified book = %#v", min)
	}
	if full := book.Full("li-b1"); full.Metadata.Authors == nil || full.Metadata.Series == nil || full.EbookFile != nil {
		t.Fatalf("authors/series must be empty arrays and ebook null: %+v", full)
	}
	expanded := book.Expanded("li-b1", nil)
	if len(expanded.Tracks) != 1 || expanded.Tracks[0]["contentUrl"] != "/api/items/li-b1/file/a1" || expanded.Tracks[0]["title"] != "a.mp3" {
		t.Fatalf("tracks = %v", expanded.Tracks)
	}
}

func TestBookAuthorsAndSeriesOrder(t *testing.T) {
	db := testDB(t)
	addTestBook(t, db)
	for _, query := range []string{
		`INSERT INTO authors (id,name,createdAt,updatedAt) VALUES ('a1','Jane Doe','` + testNow + `','` + testNow + `'),('a2','John Roe','` + testNow + `','` + testNow + `')`,
		`INSERT INTO bookAuthors (id,createdAt,bookId,authorId) VALUES ('x1','2024-01-02 03:04:06.000 +00:00','book1','a2'),('x2','` + testNow + `','book1','a1')`,
		`INSERT INTO series (id,name,createdAt,updatedAt) VALUES ('s1','Saga','` + testNow + `','` + testNow + `')`,
		`INSERT INTO bookSeries (id,sequence,createdAt,bookId,seriesId) VALUES ('y1','2','` + testNow + `','book1','s1')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	book := loadTestBook(t, db)
	md := book.Minified(nil).Metadata
	if md.AuthorName != "Jane Doe, John Roe" || md.AuthorNameLF != "Doe, Jane, Roe, John" || md.SeriesName != "Saga #2" {
		t.Fatalf("metadata %+v", md)
	}
}
