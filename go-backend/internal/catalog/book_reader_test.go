package catalog

import (
	"database/sql"
	"errors"
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
