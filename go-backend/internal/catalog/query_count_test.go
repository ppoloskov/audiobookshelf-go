package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	"modernc.org/sqlite"
)

// queryCounter records calls that return rows, including calls made through a
// prepared statement. It is deliberately test-only so endpoint tests can
// measure the SQL issued by the real database/sql path.
type queryCounter struct {
	count atomic.Int64
}

func (c *queryCounter) reset() { c.count.Store(0) }

func (c *queryCounter) reads() int64 { return c.count.Load() }

type countedConnector struct {
	driver  driver.Driver
	path    string
	counter *queryCounter
}

func (c *countedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.path)
	if err != nil {
		return nil, err
	}
	return &countedConn{Conn: conn, counter: c.counter}, nil
}

func (c *countedConnector) Driver() driver.Driver { return c.driver }

type countedConn struct {
	driver.Conn
	counter *queryCounter
}

func (c *countedConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &countedStmt{Stmt: stmt, counter: c.counter}, nil
}

func (c *countedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var stmt driver.Stmt
	var err error
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		stmt, err = c.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &countedStmt{Stmt: stmt, counter: c.counter}, nil
}

func (c *countedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (c *countedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return execer.ExecContext(ctx, query, args)
}

func (c *countedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err == nil {
		c.counter.count.Add(1)
	}
	return rows, err
}

func (c *countedConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.Queryer)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.Query(query, args)
	if err == nil {
		c.counter.count.Add(1)
	}
	return rows, err
}

type countedStmt struct {
	driver.Stmt
	counter *queryCounter
}

func (s *countedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, args)
	if err == nil {
		s.counter.count.Add(1)
	}
	return rows, err
}

func (s *countedStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args)
	if err == nil {
		s.counter.count.Add(1)
	}
	return rows, err
}

// catalogFixtureWithQueryCount wraps the already-seeded catalog fixture file,
// preserving its existing schema and data while measuring subsequent reads.
func catalogFixtureWithQueryCount(t *testing.T) (*sql.DB, *queryCounter) {
	t.Helper()
	seeded := catalogFixture(t)
	var seq int
	var name, path string
	if err := seeded.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		_ = seeded.Close()
		t.Fatal(err)
	}
	if path == "" {
		_ = seeded.Close()
		t.Fatal(errors.New("catalog fixture database has no file path"))
	}
	if err := seeded.Close(); err != nil {
		t.Fatal(err)
	}
	counter := &queryCounter{}
	db := sql.OpenDB(&countedConnector{driver: &sqlite.Driver{}, path: path, counter: counter})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	counter.reset()
	return db, counter
}

func TestQueryCounterIncludesPreparedReads(t *testing.T) {
	db, counter := catalogFixtureWithQueryCount(t)
	ctx := context.Background()

	var direct string
	if err := db.QueryRowContext(ctx, `SELECT title FROM books WHERE id = ?`, "b1").Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if direct != "The Book" {
		t.Fatalf("direct query title = %q, want %q", direct, "The Book")
	}
	if got := counter.reads(); got != 1 {
		t.Fatalf("direct query count = %d, want 1", got)
	}

	stmt, err := db.PrepareContext(ctx, `SELECT title FROM books WHERE id = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	var prepared string
	if err := stmt.QueryRowContext(ctx, "b1").Scan(&prepared); err != nil {
		t.Fatal(err)
	}
	if prepared != direct {
		t.Fatalf("prepared query title = %q, want %q", prepared, direct)
	}
	if got := counter.reads(); got != 2 {
		t.Fatalf("direct and prepared query count = %d, want 2", got)
	}
}

// A page with include= used to build each row with its own queries (83 reads
// for 20 rows); every page shape now loads through one libitem.Load.
func TestIncludePageQueryCountIndependentOfPageSize(t *testing.T) {
	counts := map[int]int64{}
	for _, size := range []int{10, 50} {
		db, counter := catalogFixtureWithQueryCount(t)
		seedBookPage(t, db, size)
		h := New(db, config.Config{}, nil)
		user := store.User{ID: "u", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
		counter.reset()
		page := requestBookPage(t, h, user, fmt.Sprintf("/api/libraries/l1/items?limit=%d&include=numepisodesincomplete", size))
		counts[size] = counter.reads()
		if got := len(page["results"].([]any)); got != size {
			t.Fatalf("page of %d returned %d rows", size, got)
		}
	}
	if counts[10] != counts[50] || counts[10] > 8 {
		t.Fatalf("include page reads 10=%d 50=%d, want equal and at most 8", counts[10], counts[50])
	}
}
