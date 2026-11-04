package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	bookdomain "audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/wire"
)

func TestLoadBookNullsAndRelationOrder(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO books (id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES ('b-null',NULL,NULL,0,0,'[]','[]','null','[]','[]','[]')`,
		`INSERT INTO authors (id,name,libraryId) VALUES ('a2','Zed Author','l1'),('a3','Amy Author','l1')`,
		`INSERT INTO bookAuthors (id,bookId,authorId,createdAt) VALUES ('ba-late','b-null','a2','2024-01-02T00:00:00Z'),('ba-early','b-null','a3','2024-01-01T00:00:00Z')`,
		`INSERT INTO series (id,name,libraryId) VALUES ('s2','Nullable Series','l1')`,
		`INSERT INTO bookSeries (id,sequence,bookId,seriesId,createdAt) VALUES ('bs-null',NULL,'b-null','s2','2024-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	book, err := bookdomain.Load(t.Context(), db, "b-null")
	if err != nil {
		t.Fatal(err)
	}
	full, err := book.Full("item-null")
	if err != nil {
		t.Fatal(err)
	}
	if full.Metadata.Title != nil || full.Metadata.Description != nil {
		t.Fatalf("nullable fields title=%v description=%v", full.Metadata.Title, full.Metadata.Description)
	}
	if len(full.Metadata.Authors) != 2 || full.Metadata.Authors[0].ID != "a3" || full.Metadata.Authors[1].ID != "a2" {
		t.Fatalf("author relation order=%#v", full.Metadata.Authors)
	}
	if len(full.Metadata.Series) != 1 || full.Metadata.Series[0].ID != "s2" || full.Metadata.Series[0].Sequence != nil {
		t.Fatalf("nullable series relation=%#v", full.Metadata.Series)
	}
	var _ wire.BookFull = full
	if _, err := bookdomain.Load(t.Context(), db, "missing-book"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing book error=%v, want sql.ErrNoRows", err)
	}
}

func TestLoadManyPreservesIndependentBookValues(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO books (id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES ('b-null',NULL,NULL,0,0,'[]','[]','null','[]','[]','[]')`,
		`INSERT INTO authors (id,name,libraryId) VALUES ('a2','Zed Author','l1'),('a3','Amy Author','l1')`,
		`INSERT INTO bookAuthors (id,bookId,authorId,createdAt) VALUES ('ba-late','b-null','a2','2024-01-02T00:00:00Z'),('ba-early','b-null','a3','2024-01-01T00:00:00Z')`,
		`INSERT INTO series (id,name,libraryId) VALUES ('s2','Nullable Series','l1')`,
		`INSERT INTO bookSeries (id,sequence,bookId,seriesId,createdAt) VALUES ('bs-null',NULL,'b-null','s2','2024-01-01T00:00:00Z')`,
		`INSERT INTO books (id,title,description,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES ('b-empty','Empty Ebook','second book',0,0,'["Second Narrator"]','[{"ino":"second-audio","duration":2,"metadata":{"size":11}}]','{}','[{"id":0,"title":"Second Chapter"}]','["second-tag"]','["second-genre"]')`,
		`INSERT INTO authors (id,name,libraryId) VALUES ('a4','Second Author','l1')`,
		`INSERT INTO bookAuthors (id,bookId,authorId,createdAt) VALUES ('ba-second','b-empty','a4','2024-01-01T00:00:00Z')`,
		`INSERT INTO series (id,name,libraryId) VALUES ('s3','Second Series','l1')`,
		`INSERT INTO bookSeries (id,sequence,bookId,seriesId,createdAt) VALUES ('bs-second','4','b-empty','s3','2024-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	got, err := bookdomain.LoadMany(t.Context(), db, []string{"b-empty", "b-null", "b-empty", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadMany result has %d books, want 2", len(got))
	}
	empty := got["b-empty"]
	full, err := empty.Full("item-empty")
	if err != nil {
		t.Fatal(err)
	}
	if full.Metadata.Title == nil || *full.Metadata.Title != "Empty Ebook" || full.Metadata.Description == nil || *full.Metadata.Description != "second book" {
		t.Fatalf("second book metadata = %#v", full.Metadata)
	}
	if full.Tags == nil || len(full.Tags) != 1 || full.Tags[0] != "second-tag" || len(full.Metadata.Narrators) != 1 || full.Metadata.Narrators[0] != "Second Narrator" {
		t.Fatalf("second book JSON arrays tags=%#v narrators=%#v", full.Tags, full.Metadata.Narrators)
	}
	if string(full.EbookFile) != "null" {
		t.Fatalf("empty ebook object serialized as %s, want null", full.EbookFile)
	}
	if len(full.Metadata.Authors) != 1 || full.Metadata.Authors[0].ID != "a4" {
		t.Fatalf("second book authors = %#v", full.Metadata.Authors)
	}
	if len(full.Metadata.Series) != 1 || full.Metadata.Series[0].ID != "s3" || full.Metadata.Series[0].Sequence == nil || *full.Metadata.Series[0].Sequence != "4" {
		t.Fatalf("second book series = %#v", full.Metadata.Series)
	}

	first, err := got["b-null"].Full("item-null")
	if err != nil {
		t.Fatal(err)
	}
	if first.Metadata.Title != nil || first.Metadata.Description != nil || len(first.Metadata.Authors) != 2 || first.Metadata.Authors[0].ID != "a3" || first.Metadata.Authors[1].ID != "a2" {
		t.Fatalf("first book lost its independent nulls or relation order: %#v", first)
	}
	if len(first.Metadata.Series) != 1 || first.Metadata.Series[0].ID != "s2" || first.Metadata.Series[0].Sequence != nil {
		t.Fatalf("first book nullable series = %#v", first.Metadata.Series)
	}
}

func TestLoadManyQueryCountsByUniqueChunk(t *testing.T) {
	db, counter := catalogFixtureWithQueryCount(t)
	for _, statement := range []string{
		`INSERT INTO bookAuthors (id,bookId,authorId,createdAt) VALUES ('ba-orphan','missing-orphan','a1','2024-01-01T00:00:00Z')`,
		`INSERT INTO bookSeries (id,sequence,bookId,seriesId,createdAt) VALUES ('bs-orphan',NULL,'missing-orphan','s1','2024-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]string, 0, 252)
	ids = append(ids, "b1")
	for i := 0; i < 251; i++ {
		id := fmt.Sprintf("batch-%03d", i)
		ids = append(ids, id)
	}
	seedBooksForBatch(t, db, ids[1:])

	counter.reset()
	empty, err := bookdomain.LoadMany(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 || counter.reads() != 0 {
		t.Fatalf("empty LoadMany result/count = %d/%d, want 0/0", len(empty), counter.reads())
	}

	for _, size := range []int{1, 10, 50, 250, 251} {
		counter.reset()
		got, err := bookdomain.LoadMany(t.Context(), db, ids[:size])
		if err != nil {
			t.Fatalf("LoadMany(%d): %v", size, err)
		}
		wantReads := int64(3)
		if size == 251 {
			wantReads = 6
		}
		if len(got) != size || counter.reads() != wantReads {
			t.Fatalf("LoadMany(%d) returned %d books and %d reads, want %d and %d", size, len(got), counter.reads(), size, wantReads)
		}
		for _, id := range ids[:size] {
			book, ok := got[id]
			if !ok {
				t.Fatalf("LoadMany(%d) omitted ID %q", size, id)
			}
			full, err := book.Full("item-" + id)
			if err != nil {
				t.Fatalf("project %q: %v", id, err)
			}
			wantTitle := "The Book"
			if id != "b1" {
				wantTitle = "Title " + id
			}
			if full.ID != id || full.Metadata.Title == nil || *full.Metadata.Title != wantTitle {
				t.Fatalf("LoadMany(%d) book %q projected ID/title %q/%v, want %q/%q", size, id, full.ID, full.Metadata.Title, id, wantTitle)
			}
		}
	}

	counter.reset()
	duplicates, err := bookdomain.LoadMany(t.Context(), db, []string{ids[0], ids[0], ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 2 || counter.reads() != 3 {
		t.Fatalf("duplicate input returned %d books and %d reads, want 2 and 3", len(duplicates), counter.reads())
	}

	counter.reset()
	missing, err := bookdomain.LoadMany(t.Context(), db, []string{ids[0], "missing-one", "missing-orphan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || counter.reads() != 3 {
		t.Fatalf("missing IDs returned %d books and %d reads, want 1 and 3", len(missing), counter.reads())
	}
	if _, ok := missing["missing-orphan"]; ok {
		t.Fatal("LoadMany synthesized a missing book from orphan associations")
	}
	if _, err := bookdomain.Load(t.Context(), db, "missing-orphan"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Load orphan-associated ID error=%v, want sql.ErrNoRows", err)
	}
}

func TestLoadManyCancellationAndNoPartialResult(t *testing.T) {
	db, _ := catalogFixtureWithQueryCount(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := bookdomain.LoadMany(ctx, db, []string{"b1"})
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled LoadMany result/error = %#v/%v, want nil/context.Canceled", got, err)
	}

	if _, err := db.Exec(`DROP TABLE bookAuthors`); err != nil {
		t.Fatal(err)
	}
	got, err = bookdomain.LoadMany(t.Context(), db, []string{"b1", "missing"})
	if err == nil || got != nil {
		t.Fatalf("failed LoadMany result/error = %#v/%v, want nil/non-nil error", got, err)
	}
}

func seedBooksForBatch(t *testing.T, db *sql.DB, ids []string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(t.Context(), `INSERT INTO books (id,title,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres) VALUES (?,?,0,0,'[]','[]','null','[]','[]','[]')`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := stmt.ExecContext(t.Context(), id, "Title "+id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
