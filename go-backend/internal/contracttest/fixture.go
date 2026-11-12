// Package contracttest boots the contract-test fixture: an installation that
// pinned upstream (TS v2.37.2) created and scanned, copied from testdata/install
// and served in-process. Nothing outside tests imports this package. The fixture
// is rebuilt by tools/contractfixture/build.cjs.
package contracttest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/server"
)

const (
	fixtureDir   = "testdata/install"
	fixtureFile  = "fixture.json"
	idsFile      = "ids.json"
	dbRelPath    = "config/absdatabase.sqlite"
	routerBase   = "/audiobookshelf"
	requestLimit = 30 * time.Second
)

// Credentials is a seeded user from the trial.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Install is a running copy of the fixture.
type Install struct {
	// BaseURL is the server root, for example http://127.0.0.1:41234.
	BaseURL string
	// IDs maps the seeded entities (library, saga1, listener, ...) to their ids.
	IDs map[string]string
	// Users maps "root", "listener" and "finisher" to the seeded credentials.
	Users map[string]Credentials
	// Dir is the temp directory holding config/, metadata/ and media/.
	Dir string
	// DB is the database the server runs on, for persistence assertions.
	DB *sql.DB

	client *http.Client
}

// fixtureMeta is the subset of testdata/install/fixture.json that Boot reads.
type fixtureMeta struct {
	UpstreamCommit string `json:"upstreamCommit"`
	SeedRoots      struct {
		Config   string `json:"config"`
		Metadata string `json:"metadata"`
		Media    string `json:"media"`
	} `json:"seedRoots"`
	Users map[string]Credentials `json:"users"`
}

// Boot copies the fixture into t.TempDir(), rewrites the absolute seed paths
// stored in the database, and serves it with the production open sequence
// (server.OpenDatabase, server.NewManager). Everything is
// closed by t.Cleanup.
func Boot(t testing.TB) *Install {
	t.Helper()
	return boot(t, true)
}

// BootEmpty boots an uninitialized install: empty config and metadata
// directories, so bootstrap creates a database with no root user. It is the
// starting point of the first-run flow (POST /init). Its IDs and Users are empty.
func BootEmpty(t testing.TB) *Install {
	t.Helper()
	return boot(t, false)
}

// boot is Boot and BootEmpty: seeded copies the fixture and rewrites its seed
// paths; otherwise the install starts empty.
func boot(t testing.TB, seeded bool) *Install {
	dir := t.TempDir()
	var meta fixtureMeta
	var ids map[string]string
	if seeded {
		if err := copyTree(fixtureDir, dir); err != nil {
			t.Fatalf("contracttest: copy fixture: %v", err)
		}
		meta = readJSON[fixtureMeta](t, filepath.Join(dir, fixtureFile))
		ids = readJSON[map[string]string](t, filepath.Join(dir, idsFile))
	}

	cfg := config.Config{
		ConfigPath:     filepath.Join(dir, "config"),
		MetadataPath:   filepath.Join(dir, "metadata"),
		Host:           "127.0.0.1",
		RouterBasePath: routerBase,
		Source:         "docker",
	}
	st, err := server.OpenDatabase(cfg)
	if err != nil {
		t.Fatalf("contracttest: open database: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if seeded {
		pairs := []pathPair{
			{from: meta.SeedRoots.Media, to: filepath.Join(dir, "media")},
			{from: meta.SeedRoots.Metadata, to: filepath.Join(dir, "metadata")},
			{from: meta.SeedRoots.Config, to: filepath.Join(dir, "config")},
		}
		if err := rewriteSeedPaths(context.Background(), dbtest.OpenRaw(t, db.FilePath(cfg.ConfigPath)), pairs); err != nil {
			t.Fatalf("contracttest: rewrite seed paths: %v", err)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("contracttest: listen: %v", err)
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port

	manager, err := server.NewManager(st, cfg)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("contracttest: start server: %v", err)
	}
	httpServer := &http.Server{Handler: manager, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = httpServer.Serve(ln) }()
	// LIFO: the HTTP server stops before the manager closes the database.
	t.Cleanup(manager.Close)
	t.Cleanup(func() { _ = httpServer.Close() })
	// Runs first (LIFO), while the server still runs: TS stores every text and
	// JSON column as TEXT, so a BLOB anywhere is a Go write bug. It opens the
	// file afresh through a second handle: a backup apply reopens the database,
	// which closes st.
	t.Cleanup(func() {
		assertNoBlobText(t, dbtest.OpenRaw(t, db.FilePath(cfg.ConfigPath)))
	})

	return &Install{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		IDs:     ids,
		Users:   meta.Users,
		Dir:     dir,
		DB:      dbtest.OpenRaw(t, db.FilePath(cfg.ConfigPath)),
		client:  &http.Client{Timeout: requestLimit},
	}
}

// Login posts /login the way the mobile apps do (X-Return-Tokens: true) and
// returns the access token.
func (in *Install) Login(t testing.TB, username, password string) string {
	t.Helper()
	resp, body := in.request(t, http.MethodPost, "/login", "", map[string]string{"X-Return-Tokens": "true"},
		Credentials{Username: username, Password: password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("contracttest: login %s: status %d: %s", username, resp.StatusCode, body)
	}
	var out struct {
		User struct {
			AccessToken string `json:"accessToken"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.User.AccessToken == "" {
		t.Fatalf("contracttest: login %s: no access token in %s", username, body)
	}
	return out.User.AccessToken
}

// Do sends one request to path (for example "/api/libraries") with an optional
// bearer token. A non-nil body is sent as JSON. The body is read and returned,
// and resp.Body is replaced with a reader over it.
func (in *Install) Do(t testing.TB, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	return in.request(t, method, path, token, nil, body)
}

// rawBody is a request body sent as it is, with its own media type (multipart
// uploads). Any other non-nil body is sent as JSON.
type rawBody struct {
	contentType string
	data        []byte
}

func (in *Install) request(t testing.TB, method, path, token string, headers map[string]string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case rawBody:
		reader, contentType = bytes.NewReader(b.data), b.contentType
	default:
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("contracttest: marshal %s %s: %v", method, path, err)
		}
		reader, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(context.Background(), method, in.BaseURL+path, reader)
	if err != nil {
		t.Fatalf("contracttest: build %s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := in.client.Do(req)
	if err != nil {
		t.Fatalf("contracttest: %s %s: %v", method, path, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("contracttest: read %s %s: %v", method, path, err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, data
}

type pathPair struct{ from, to string }

// rewriteSeedPaths replaces every seed path in every text-like column of every
// table. Matching is by instr (case-sensitive, no LIKE wildcards), which is the
// same rule replace() applies. Pairs are applied longest first.
func rewriteSeedPaths(ctx context.Context, db *sql.DB, pairs []pathPair) error {
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) })
	cols, err := textColumns(ctx, db)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range pairs {
		if p.from == "" {
			return fmt.Errorf("empty seed path")
		}
		for _, c := range cols {
			stmt := fmt.Sprintf(`UPDATE %s SET %s = replace(%s, ?, ?) WHERE instr(%s, ?) > 0`, c.table, c.column, c.column, c.column)
			if _, err := tx.ExecContext(ctx, stmt, p.from, p.to, p.from); err != nil {
				return fmt.Errorf("rewrite %s.%s: %w", c.table, c.column, err)
			}
		}
	}
	return tx.Commit()
}

// countSeedPaths counts text cells that still contain a seed path. Boot tests
// use it to show that the rewrite left nothing behind.
func countSeedPaths(ctx context.Context, db *sql.DB, roots ...string) (int, error) {
	cols, err := textColumns(ctx, db)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, c := range cols {
		for _, root := range roots {
			var n int
			q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE instr(%s, ?) > 0`, c.table, c.column)
			if err := db.QueryRowContext(ctx, q, root).Scan(&n); err != nil {
				return 0, fmt.Errorf("count %s.%s: %w", c.table, c.column, err)
			}
			total += n
		}
	}
	return total, nil
}

type column struct{ table, column string }

// textColumns lists every column whose declared type is TEXT-like (TEXT,
// VARCHAR, CHAR, CLOB, JSON) or empty, across all user tables.
func textColumns(ctx context.Context, db *sql.DB) ([]column, error) {
	var tables []string
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND substr(name, 1, 7) <> 'sqlite_'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []column
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		info, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoted+`)`)
		if err != nil {
			return nil, err
		}
		for info.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt sql.NullString
			if err := info.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				_ = info.Close()
				return nil, err
			}
			upper := strings.ToUpper(typ)
			textLike := upper == "" || strings.Contains(upper, "TEXT") || strings.Contains(upper, "CHAR") ||
				strings.Contains(upper, "CLOB") || strings.Contains(upper, "JSON")
			if textLike {
				out = append(out, column{table: quoted, column: `"` + strings.ReplaceAll(name, `"`, `""`) + `"`})
			}
		}
		if err := info.Close(); err != nil {
			return nil, err
		}
		if err := info.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readJSON[T any](t testing.TB, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("contracttest: read %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("contracttest: parse %s: %v", path, err)
	}
	return v
}

// copyTree copies regular files only.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported fixture entry %s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

// assertNoBlobText fails the test when any TEXT-affinity column holds a BLOB
// value. database/sql binds []byte as BLOB, so json.Marshal output must be
// written as string.
func assertNoBlobText(t testing.TB, db *sql.DB) {
	t.Helper()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Errorf("list tables: %v", err)
		return
	}
	var names []string
	for tables.Next() {
		var n string
		if tables.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	_ = tables.Close()
	for _, table := range names {
		cols, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Errorf("columns of %s: %v", table, err)
			continue
		}
		var text []string
		for cols.Next() {
			var name, typ string
			if cols.Scan(&name, &typ) != nil {
				continue
			}
			u := strings.ToUpper(typ)
			if u == "" || strings.Contains(u, "TEXT") || strings.Contains(u, "CHAR") || strings.Contains(u, "CLOB") || strings.Contains(u, "JSON") || strings.Contains(u, "DATE") || strings.Contains(u, "UUID") {
				text = append(text, name)
			}
		}
		_ = cols.Close()
		for _, col := range text {
			var n int
			q := fmt.Sprintf(`SELECT count(*) FROM %q WHERE typeof(%q) = 'blob'`, table, col)
			if err := db.QueryRow(q).Scan(&n); err != nil {
				t.Errorf("%s.%s: %v", table, col, err)
				continue
			}
			if n > 0 {
				t.Errorf("%s.%s holds %d BLOB value(s); TS stores TEXT", table, col, n)
			}
		}
	}
}
