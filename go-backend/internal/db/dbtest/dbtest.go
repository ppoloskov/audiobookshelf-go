// Package dbtest opens throwaway databases for tests of packages built on
// internal/db.
package dbtest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
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
	applySchema(t, d)
	return d
}

func applySchema(t testing.TB, d *db.DB) {
	t.Helper()
	if _, err := d.Exec(context.Background(), db.Schema); err != nil {
		t.Fatalf("dbtest: apply pinned schema: %v", err)
	}
}

// Counting is New with a counter of the queries that return rows (the
// QueryContext calls of the driver), for tests that pin how many reads an
// operation makes.
func Counting(t testing.TB) (*db.DB, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "absdatabase.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("dbtest: create database file: %v", err)
	}
	count := &atomic.Int64{}
	d, err := db.ConnectWrapped(path, func(conn driver.Conn) driver.Conn { return countingConn{Conn: conn, count: count} })
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("dbtest: close: %v", err)
		}
	})
	applySchema(t, d)
	return d, count
}

type countingConn struct {
	driver.Conn
	count *atomic.Int64
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.count.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
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

// OpenRaw opens a second, independent database/sql handle on the database file
// at path, for tests that read rows of unknown shape (every column of every
// table) or run statements beside the server. It is not the handle production
// code uses and it does not follow a file the server replaces. It is closed
// when the test ends.
func OpenRaw(t testing.TB, path string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(5000)"}}.Encode()}
	raw, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatalf("dbtest: open raw handle: %v", err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	return raw
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
