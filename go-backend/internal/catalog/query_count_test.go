package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	"modernc.org/sqlite"
)

// queryCounter records calls that return rows, including calls made through a
// prepared statement. It wraps the sqlite driver in this test file only, so
// endpoint tests can measure the SQL issued by the real database/sql path.
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

// The book items page loads its rows in batches: the number of reads must not
// grow with the page size (an N+1 regression would make 50 rows cost more than
// 10). The bound is generous; the equality is the guard.
func TestBookItemsPageReadsDoNotGrowWithPageSize(t *testing.T) {
	counts := map[int]int64{}
	for _, size := range []int{10, 50} {
		db, counter := catalogFixtureWithQueryCount(t)
		seedBookPage(t, db, size)
		h := New(db, config.Config{}, nil, testDeps())
		user := store.User{ID: "u", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
		counter.reset()
		page := requestBookPage(t, h, user, fmt.Sprintf("/api/libraries/l1/items?limit=%d", size))
		counts[size] = counter.reads()
		if got := len(page["results"].([]any)); got != size {
			t.Fatalf("page of %d returned %d rows", size, got)
		}
	}
	if counts[10] != counts[50] || counts[10] > 12 {
		t.Fatalf("book page reads 10=%d 50=%d, want equal and at most 12", counts[10], counts[50])
	}
}

// ?include=numepisodesincomplete adds numEpisodes minus the requesting user's
// finished episodes to each podcast row (libraryItemsPodcastFilters.js:221-229),
// and LibraryItem.js:317 only copies it when non-zero, so a fully finished
// podcast has no key. Without the include no row has it.
func TestPodcastItemsIncludeNumEpisodesIncomplete(t *testing.T) {
	db := catalogFixture(t)
	for _, q := range []string{
		`CREATE TABLE settings (key TEXT PRIMARY KEY,value JSON)`,
		`CREATE TABLE podcasts (id TEXT PRIMARY KEY,title TEXT,titleIgnorePrefix TEXT,author TEXT,releaseDate TEXT,feedURL TEXT,imageURL TEXT,description TEXT,itunesPageURL TEXT,itunesId TEXT,itunesArtistId TEXT,language TEXT,podcastType TEXT,explicit BOOLEAN,autoDownloadEpisodes BOOLEAN,autoDownloadSchedule TEXT,lastEpisodeCheck DATETIME,maxEpisodesToKeep INTEGER,maxNewEpisodesToDownload INTEGER,coverPath TEXT,tags JSON,genres JSON,numEpisodes INTEGER,createdAt DATETIME,updatedAt DATETIME)`,
		`CREATE TABLE podcastEpisodes (id TEXT PRIMARY KEY,podcastId TEXT)`,
		`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,podcastId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// p1: 3 episodes, u1 finished 1 (and has one unfinished), u2 finished 2 -> 2 incomplete for u1.
	// p2: 4 episodes, none finished -> 4. p3: 1 episode, u1 finished it -> 0, key absent.
	for i, p := range []struct {
		id    string
		count int
	}{{"p1", 3}, {"p2", 4}, {"p3", 1}} {
		ts := fmt.Sprintf("2024-01-0%dT00:00:00Z", i+1)
		if _, err := db.Exec(`INSERT INTO podcasts VALUES (?,?,?,'Host','2024-01-01','https://example.test/feed',NULL,NULL,NULL,NULL,NULL,'en','episodic',0,0,NULL,NULL,0,3,NULL,'[]','[]',?,?,?)`, p.id, "Show "+p.id, "Show "+p.id, p.count, ts, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO libraryItems VALUES (?,?,?,?,?,'podcast',0,0,0,?,?,?,100,NULL,NULL,'[]','{}',?,?,NULL,NULL,'l2',NULL,?,?)`, "i"+p.id, "ino-"+p.id, "/media/podcasts/"+p.id, p.id, p.id, ts, ts, ts, "Show "+p.id, "Show "+p.id, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO mediaProgresses VALUES ('m1','u1','e1','p1',0,0,1,0,'2024-02-01T00:00:00Z','2024-02-01T00:00:00Z',NULL)`,
		`INSERT INTO mediaProgresses VALUES ('m2','u1','e2','p1',5,0,0,0,'2024-02-01T00:00:00Z','2024-02-01T00:00:00Z',NULL)`,
		`INSERT INTO mediaProgresses VALUES ('m3','u2','e1','p1',0,0,1,0,'2024-02-01T00:00:00Z','2024-02-01T00:00:00Z',NULL)`,
		`INSERT INTO mediaProgresses VALUES ('m4','u2','e2','p1',0,0,1,0,'2024-02-01T00:00:00Z','2024-02-01T00:00:00Z',NULL)`,
		`INSERT INTO mediaProgresses VALUES ('m5','u1','e9','p3',0,0,1,0,'2024-02-01T00:00:00Z','2024-02-01T00:00:00Z',NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h := New(db, config.Config{}, nil, testDeps())
	u1 := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	incomplete := func(path string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, path, nil), u1) || w.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, w.Code, w.Body.String())
		}
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		got := map[string]any{}
		for _, row := range payload.Results {
			v, present := row["numEpisodesIncomplete"]
			if present {
				got[row["id"].(string)] = v
			}
		}
		if len(payload.Results) != 3 {
			t.Fatalf("GET %s returned %d rows, want 3", path, len(payload.Results))
		}
		return got
	}
	got := incomplete("/api/libraries/l2/items?include=numepisodesincomplete")
	if len(got) != 2 || got["ip1"] != float64(2) || got["ip2"] != float64(4) {
		t.Fatalf("with include: numEpisodesIncomplete by item = %#v, want ip1=2 ip2=4 and none for the finished ip3", got)
	}
	if got := incomplete("/api/libraries/l2/items"); len(got) != 0 {
		t.Fatalf("without include: numEpisodesIncomplete = %#v, want none", got)
	}
}
