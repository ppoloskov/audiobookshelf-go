package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"

	_ "modernc.org/sqlite"
)

// openPinnedSchema opens a fresh database holding the pinned v2.37.2 schema on
// one connection, as production does.
func openPinnedSchema(t *testing.T) *sql.DB {
	t.Helper()
	ddl, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply pinned schema: %v", err)
	}
	return db
}

// TestUserLookupsSkipMalformedExtraData pins the behavior the Go-side scans had
// before: a row whose extraData is not JSON is skipped, not an error, and a
// lookup for an absent key is sql.ErrNoRows.
func TestUserLookupsSkipMalformedExtraData(t *testing.T) {
	db := openPinnedSchema(t)
	q := sqlc.New(db)
	ctx := context.Background()
	now := dbtime.Format(time.Now())

	if _, err := db.Exec(`INSERT INTO users (id, username, extraData, createdAt, updatedAt) VALUES ('bad', 'bad', 'not json', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertUser(ctx, sqlc.InsertUserParams{
		ID: "good", Username: sql.NullString{String: "good", Valid: true},
		Type:        sql.NullString{String: "user", Valid: true},
		Token:       sql.NullString{String: "tok", Valid: true},
		Permissions: sql.NullString{String: "{}", Valid: true},
		Extradata:   sql.NullString{String: `{"authOpenIDSub":"sub-1","oldUserId":"old-1"}`, Valid: true},
		Createdat:   now, Updatedat: now,
	}); err != nil {
		t.Fatal(err)
	}

	u, err := q.GetUserByOIDCSub(ctx, "sub-1")
	if err != nil || u.ID != "good" {
		t.Fatalf("GetUserByOIDCSub = %q, %v; want good", u.ID, err)
	}
	if _, err := q.GetUserByOIDCSub(ctx, "absent"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absent sub: err = %v, want sql.ErrNoRows", err)
	}
	id, err := q.GetUserIDByOldUserID(ctx, "old-1")
	if err != nil || id != "good" {
		t.Fatalf("GetUserIDByOldUserID = %q, %v; want good", id, err)
	}
}

// TestUserWritesAreText pins the storage class: JSON and DATETIME values bound
// by the generated writers must be TEXT, never BLOB, because TS reads them as
// text and the contract forbids BLOB text columns.
func TestUserWritesAreText(t *testing.T) {
	db := openPinnedSchema(t)
	q := sqlc.New(db)
	ctx := context.Background()
	now := dbtime.Format(time.Now())

	if err := q.InsertUser(ctx, sqlc.InsertUserParams{
		ID: "root", Username: sql.NullString{String: "root", Valid: true},
		Type:        sql.NullString{String: "root", Valid: true},
		Pash:        sql.NullString{String: "hash", Valid: true},
		Token:       sql.NullString{String: "tok", Valid: true},
		Permissions: sql.NullString{String: `{"download":true}`, Valid: true},
		Extradata:   sql.NullString{String: "{}", Valid: true},
		Createdat:   now, Updatedat: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateUserPermissions(ctx, sqlc.UpdateUserPermissionsParams{
		Permissions: sql.NullString{String: `{"update":true}`, Valid: true}, Updatedat: now, ID: "root",
	}); err != nil {
		t.Fatal(err)
	}
	var types string
	err := db.QueryRow(`SELECT typeof(permissions) || ',' || typeof(extraData) || ',' || typeof(createdAt) || ',' || typeof(updatedAt) FROM users WHERE id = 'root'`).Scan(&types)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(types, "blob") || strings.Count(types, "text") != 4 {
		t.Fatalf("stored types = %s, want text for every column", types)
	}
}
