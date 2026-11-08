package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const fixtureDDL = `
CREATE TABLE users (
 id TEXT PRIMARY KEY, username TEXT, email TEXT, pash TEXT, type TEXT, token TEXT,
 isActive BOOLEAN, isLocked BOOLEAN, lastSeen DATETIME, permissions JSON, bookmarks JSON,
 extraData JSON, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL
);
CREATE TABLE libraries (
 id TEXT PRIMARY KEY, name TEXT, displayOrder INTEGER, icon TEXT, mediaType TEXT, provider TEXT,
 lastScan DATETIME, lastScanVersion TEXT, settings JSON, extraData JSON,
 createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL
);
CREATE TABLE libraryFolders (
 id TEXT PRIMARY KEY, path TEXT, libraryId TEXT REFERENCES libraries(id),
 createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL
);
CREATE TABLE settings (key TEXT PRIMARY KEY, value JSON);
`

func createFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	if _, err = db.Exec(fixtureDDL); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"u1", "alice", "a@example.test", "bcrypt-hash", "user", "legacy-token", 1, 0,
		"2024-01-02T03:04:05.006Z", `{"librariesAccessible":["l1"]}`,
		`[{"libraryItemId":"i1","time":3}]`, `{"custom":true}`,
		"2020-01-02T03:04:05.000Z", "2024-01-03T03:04:05.000Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"u2", "legacy", "", "", "user", "", 0, 0, nil, `{}`, `[]`, `{"oldUserId":"old-u2"}`,
		"2020-01-02T03:04:05.000Z", "2024-01-03T03:04:05.000Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO libraries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"l1", "Books", 2, "books", "book", "audible", "2024-01-02T03:04:05.006Z", "2.37.2",
		`{"audiobooksOnly":true}`, `{}`, "2020-01-02T03:04:05.000Z", "2024-01-03T03:04:05.000Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO libraryFolders VALUES (?, ?, ?, ?, ?)`, "f1", "/media/books", "l1", "2020-01-03T03:04:05.000Z", "2020-01-03T03:04:05.000Z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO settings VALUES (?, ?)`, "server-settings", `{"scannerFindCovers":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenRequiresSchema(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "wrong.sqlite")
	db, err := sql.Open("sqlite", bad)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // production pool size (store.Open)
	if _, err := db.Exec(`CREATE TABLE users (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(bad); !errors.Is(err, ErrSchema) {
		t.Fatalf("schema error=%v", err)
	}
	s, err := OpenUnchecked(bad)
	if err != nil {
		t.Fatalf("OpenUnchecked skips the schema check: %v", err)
	}
	s.Close()
}
