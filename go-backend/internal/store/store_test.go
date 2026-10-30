package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestReadOnlyReadsUpstreamModels(t *testing.T) {
	path := createFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	s, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	u, err := s.user(ctx, "username", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "u1" || u.Password != "bcrypt-hash" || !u.IsActive || u.IsLocked {
		t.Fatalf("unexpected user: %#v", u)
	}
	if u.LastSeen == nil || *u.LastSeen != 1704164645006 {
		t.Fatalf("unexpected lastSeen: %v", u.LastSeen)
	}
	var permissions map[string]any
	if err := json.Unmarshal(u.Permissions, &permissions); err != nil || permissions["librariesAccessible"] == nil {
		t.Fatalf("permissions not preserved: %s (%v)", u.Permissions, err)
	}
	settings, err := s.ServerSettings(ctx)
	if err != nil || settings["scannerFindCovers"] != true {
		t.Fatalf("settings=%v err=%v", settings, err)
	}
	libraries, err := s.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libraries) != 1 || libraries[0]["displayOrder"] != int64(2) {
		t.Fatalf("unexpected libraries: %#v", libraries)
	}
	folders := libraries[0]["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["fullPath"] != "/media/books" {
		t.Fatalf("unexpected folders: %#v", folders)
	}
	if _, err := s.UserByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user err=%v", err)
	}
	legacy, err := s.UserByIDOrOldID(ctx, "old-u2")
	if err != nil || legacy.ID != "u2" {
		t.Fatalf("legacy lookup=%#v err=%v", legacy, err)
	}
	if _, err := s.DB().Exec(`UPDATE users SET username='changed' WHERE id='u1'`); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterHash := sha256.Sum256(after)
	if !bytes.Equal(beforeHash[:], afterHash[:]) {
		t.Fatal("read-only store changed the SQLite database bytes")
	}
}

func TestOpenReadOnlyDoesNotCreateAndRequiresSchema(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := OpenReadOnly(missing); err == nil {
		t.Fatal("expected missing file error")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "wrong.sqlite")
	db, err := sql.Open("sqlite", bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := OpenReadOnly(bad); !errors.Is(err, ErrSchema) {
		t.Fatalf("schema error=%v", err)
	}
}

func TestNullableMillisTreatsNumericDatesAsMilliseconds(t *testing.T) {
	got, err := nullableMillis(int64(1234))
	if err != nil || got == nil || *got != 1234 {
		t.Fatalf("nullableMillis(1234) = %v, %v; want 1234ms", got, err)
	}
}

func TestLibrariesMatchPinnedUpstreamGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "golden.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fixtureDDL); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO libraries VALUES
('book-library','Fiction Shelf',1,'books','book','google','2024-03-04T05:06:07.000Z','2.37.2',
'{"coverAspectRatio":1,"disableWatcher":false,"skipMatchingMediaWithAsin":true,"skipMatchingMediaWithIsbn":false,"audiobooksOnly":false,"hideSingleBookSeries":true,"metadataPrecedence":["folderStructure","audioMetatags","absMetadata"],"markAsFinishedTimeRemaining":12,"markAsFinishedPercentComplete":null}', '{}','2023-01-02T03:04:05.000Z','2024-04-05T06:07:08.000Z'),
('podcast-library','Science Podcasts',2,'podcast','podcast','podcastIndex',NULL,NULL,
'{"coverAspectRatio":1,"disableWatcher":true,"autoScanCronExpression":null,"podcastSearchRegion":"ca","markAsFinishedPercentComplete":95,"markAsFinishedTimeRemaining":null}', '{}','2023-06-07T08:09:10.000Z','2024-07-08T09:10:11.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO libraryFolders VALUES
('book-folder-1','/media/books/fiction','book-library','2023-01-02T03:04:05.000Z','2024-04-05T06:07:08.000Z'),
('book-folder-2','/media/books/mystery','book-library','2023-02-03T04:05:06.000Z','2024-04-05T06:07:08.000Z'),
('podcast-folder-1','/media/podcasts/science','podcast-library','2023-06-07T08:09:10.000Z','2024-07-08T09:10:11.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotLibraries, err := s.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(gotLibraries)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("..", "..", "contracts", "library-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Expected struct {
			Unrestricted struct {
				Libraries any `json:"libraries"`
			} `json:"unrestricted"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(golden, &fixture); err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(fixture.Expected.Unrestricted.Libraries)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("libraries differ from pinned upstream golden\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}
