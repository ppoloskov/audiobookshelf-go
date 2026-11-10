package access

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	_ "modernc.org/sqlite"
)

func itemAccessDB(t *testing.T) *sql.DB {
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

func TestLoadAccessReadsLibraryExplicitAndTags(t *testing.T) {
	db := itemAccessDB(t)
	const now = "2024-01-02 03:04:05.000 +00:00"
	for _, row := range []string{
		`INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES ('book1','Book',0,0,1,'["tag"]','[]','[]','[]','[]','` + now + `','` + now + `')`,
		`INSERT INTO podcasts (id,title,explicit,autoDownloadEpisodes,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('pod1','Show',1,0,'[]','[]',1,'` + now + `','` + now + `')`,
		`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-book','/b','b','book1','book',0,0,0,1,'[]','` + now + `','` + now + `','lib')`,
		`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-pod','/p','p','pod1','podcast',0,0,0,1,'[]','` + now + `','` + now + `','lib')`,
		`INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,createdAt,updatedAt,libraryId) VALUES ('li-orphan','/o','o','missing','podcast',0,0,0,1,'[]','` + now + `','` + now + `','lib')`,
	} {
		if _, err := db.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id       string
		explicit bool
		tags     []string
	}{
		{"li-book", false, []string{"tag"}},
		{"li-pod", true, []string{}},
	} {
		got, err := LoadAccess(context.Background(), db, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.LibraryID != "lib" || got.Explicit != tc.explicit || !slices.Equal(got.Tags, tc.tags) {
			t.Fatalf("%s access = %+v", tc.id, got)
		}
	}
	for _, id := range []string{"missing", "li-orphan"} {
		if _, err := LoadAccess(context.Background(), db, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
