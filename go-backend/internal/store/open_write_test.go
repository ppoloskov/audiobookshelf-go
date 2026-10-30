package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadWriteDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	if _, err := OpenReadWrite(path); err == nil {
		t.Fatal("expected missing database error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}

func TestOpenReadWriteTransactionRollback(t *testing.T) {
	path := createFixture(t)
	s, err := OpenReadWrite(path)
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
