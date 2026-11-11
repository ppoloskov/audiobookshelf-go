// Package dbtest opens throwaway databases for tests of packages built on
// internal/db.
package dbtest

import (
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"audiobookshelf-compat/internal/db"

	_ "modernc.org/sqlite"
)

// New returns an empty database holding the pinned schema (tables, indexes and
// triggers), in a temporary directory. It is closed when the test ends.
func New(t testing.TB) *db.DB {
	t.Helper()
	d := open(t, filepath.Join(t.TempDir(), "absdatabase.sqlite"))
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

// open opens path with the production connection settings (see
// internal/store): foreign keys on, a 5 s busy timeout, a single connection.
func open(t testing.TB, path string) *db.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	sqlDB, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatalf("dbtest: open %s: %v", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("dbtest: close: %v", err)
		}
	})
	return db.New(sqlDB)
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
