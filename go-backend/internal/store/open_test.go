package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	if _, err := Open(path); err == nil {
		t.Fatal("expected missing database error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}

func TestOpenTransactionRollback(t *testing.T) {
	path := createFixture(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE users SET username='temporary' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var username string
	if err := s.DB().QueryRow(`SELECT username FROM users WHERE id='u1'`).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username != "alice" {
		t.Fatalf("rollback left username %q", username)
	}
}

// A database from another server version is refused: Go cannot migrate, and
// upstream would have migrated it when it reconnected after a restore.
func TestOpenRefusesOtherMigrationState(t *testing.T) {
	path := createFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`UPDATE migrationsMeta SET value = '2.36.0' WHERE key = 'version'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = Open(path); !errors.Is(err, ErrSchema) {
		t.Fatalf("Open = %v, want ErrSchema", err)
	}
	s, err := OpenUnchecked(path)
	if err != nil {
		t.Fatalf("OpenUnchecked must still open it: %v", err)
	}
	s.Close()
}
