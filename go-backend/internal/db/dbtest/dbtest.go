// Package dbtest opens throwaway databases for tests of packages built on
// internal/db.
package dbtest

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"audiobookshelf-compat/internal/db"
)

// New returns an empty database holding the pinned schema (tables, indexes and
// triggers), in a temporary directory. It is closed when the test ends.
func New(t testing.TB) *db.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("dbtest: create database file: %v", err)
	}
	d := open(t, path)
	if _, err := d.SQL().Exec(db.Schema); err != nil {
		t.Fatalf("dbtest: apply pinned schema: %v", err)
	}
	return d
}

// Fixture returns a copy of the database that the upstream v2.37.2 server
// created for the contract tests
// (internal/contracttest/testdata/install/config/absdatabase.sqlite). It is
// closed when the test ends.
func Fixture(t testing.TB) *db.DB {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("dbtest: cannot locate the fixture database")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "contracttest", "testdata", "install", "config", "absdatabase.sqlite")
	dst := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	if err := copyFile(dst, src); err != nil {
		t.Fatalf("dbtest: copy fixture database: %v", err)
	}
	return open(t, dst)
}

// open connects to path as production does (db.Connect) and closes the
// handle when the test ends.
func open(t testing.TB, path string) *db.DB {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("dbtest: close: %v", err)
		}
	})
	return d
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() // the copy error is the one to report
		return err
	}
	return out.Close()
}
