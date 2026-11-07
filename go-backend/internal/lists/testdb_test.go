package lists

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

const stamp = "2024-01-02 03:04:05.000 +00:00"

// schemaDB opens an in-memory database with the real schema.
func schemaDB(t *testing.T) *sql.DB {
	t.Helper()
	schema, err := os.ReadFile("../../contracts/parity/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	exec(t, db, string(schema))
	return db
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func addBook(t *testing.T, db *sql.DB, itemID, mediaID, title string) {
	t.Helper()
	exec(t, db, `INSERT INTO books (id,title,explicit,abridged,duration,tags,genres,narrators,audioFiles,chapters,createdAt,updatedAt) VALUES (?,?,0,0,10,'[]','[]','[]','[]','[]',?,?)`, mediaID, title, stamp, stamp)
	exec(t, db, `INSERT INTO libraryItems (id,path,relPath,mediaId,mediaType,isFile,isMissing,isInvalid,size,libraryFiles,extraData,createdAt,updatedAt,libraryId) VALUES (?,?,?,?, 'book',0,0,0,1,'[]','{}',?,?,'library-1')`, itemID, "/"+itemID, itemID, mediaID, stamp, stamp)
}

// asJSON round-trips v through encoding/json.
func asJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
