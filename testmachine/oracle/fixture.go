package oracle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	sourcePin           = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"
	maxFixtureMetaBytes = 1 << 20
)

type fixtureUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func fixtureTemplate(root string) string {
	return filepath.Join(root, "testmachine", "testdata", "install")
}

func copyTree(ctx context.Context, source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("relative path %s: %w", path, err)
		}
		target := filepath.Join(dest, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture symlink is not allowed: %s", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open source %s: %w", path, err)
		}
		defer in.Close() // read-only source file

		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create dest %s: %w", target, err)
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("copy %s: %w", rel, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", rel, closeErr)
		}
		return nil
	})
}

func freshInstall(ctx context.Context, root, dest string) error {
	for _, name := range []string{"config", "metadata"} {
		if err := os.MkdirAll(filepath.Join(dest, name), 0755); err != nil {
			return fmt.Errorf("fresh install %s: %w", name, err)
		}
	}
	if err := copyTree(ctx, filepath.Join(fixtureTemplate(root), "media"), filepath.Join(dest, "media")); err != nil {
		return fmt.Errorf("fresh install media: %w", err)
	}
	return nil
}

func treeDigest(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk %s: %w", dir, err)
	}
	slices.Sort(files)
	hash := sha256.New()
	for _, file := range files {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			return "", fmt.Errorf("relative %s: %w", file, err)
		}
		if _, err := io.WriteString(hash, filepath.ToSlash(rel)+"\x00"); err != nil {
			return "", err
		}
		in, err := os.Open(file)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", file, err)
		}
		_, copyErr := io.Copy(hash, in)
		closeErr := in.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", fmt.Errorf("hash %s: %w", rel, err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFixture(ctx context.Context, root, dest string) (err error) {
	source := fixtureTemplate(root)
	if err := copyTree(ctx, source, dest); err != nil {
		return fmt.Errorf("copy fixture: %w", err)
	}
	sourceDigest, err := treeDigest(source)
	if err != nil {
		return fmt.Errorf("digest source fixture: %w", err)
	}
	copiedDigest, err := treeDigest(dest)
	if err != nil {
		return fmt.Errorf("digest copied fixture: %w", err)
	}
	if sourceDigest != copiedDigest {
		return errors.New("fixture copy differs before path rewriting")
	}
	raw, err := readSmallFile(filepath.Join(dest, "fixture.json"))
	if err != nil {
		return fmt.Errorf("read fixture.json: %w", err)
	}
	var meta struct {
		Pin   string `json:"upstreamCommit"`
		Roots struct {
			Config   string `json:"config"`
			Metadata string `json:"metadata"`
			Media    string `json:"media"`
		} `json:"seedRoots"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fmt.Errorf("decode fixture.json: %w", err)
	}
	if meta.Pin != sourcePin {
		return fmt.Errorf("starting fixture source pin differs: got %q want %q", meta.Pin, sourcePin)
	}

	pairs := [][2]string{
		{meta.Roots.Config, filepath.Join(dest, "config")},
		{meta.Roots.Metadata, filepath.Join(dest, "metadata")},
		{meta.Roots.Media, filepath.Join(dest, "media")},
	}
	slices.SortFunc(pairs, func(a, b [2]string) int { return len(b[0]) - len(a[0]) })

	dbPath := filepath.Join(dest, "config", "absdatabase.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open copied sqlite: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	columns, err := textColumns(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect sqlite text columns: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin path rewrite tx: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, pair := range pairs {
		if pair[0] == "" {
			return errors.New("empty source fixture root in seedRoots")
		}
		for _, col := range columns {
			q := "UPDATE " + quoteIdentifier(col.table) + " SET " + quoteIdentifier(col.name) +
				"=replace(" + quoteIdentifier(col.name) + ",?,?) WHERE instr(" + quoteIdentifier(col.name) + ",?)>0"
			if _, err := tx.ExecContext(ctx, q, pair[0], pair[1], pair[0]); err != nil {
				return fmt.Errorf("rewrite %s.%s: %w", col.table, col.name, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit path rewrite tx: %w", err)
	}
	return nil
}

type tableColumn struct {
	table, name string
}

func textColumns(ctx context.Context, db *sql.DB) ([]tableColumn, error) {
	tables, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND substr(name,1,7)<>'sqlite_'")
	if err != nil {
		return nil, err
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			_ = tables.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := errors.Join(tables.Err(), tables.Close()); err != nil {
		return nil, err
	}

	var columns []tableColumn
	for _, table := range names {
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid, notNull, pk int
			var name, kind string
			var fallback sql.NullString
			if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &pk); err != nil {
				_ = rows.Close()
				return nil, err
			}
			upper := strings.ToUpper(kind)
			if upper == "" || strings.Contains(upper, "TEXT") || strings.Contains(upper, "CHAR") ||
				strings.Contains(upper, "CLOB") || strings.Contains(upper, "JSON") {
				columns = append(columns, tableColumn{table: table, name: name})
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return columns, nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func readSmallFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFixtureMetaBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxFixtureMetaBytes)
	}
	return os.ReadFile(path)
}

func loadInstallMeta(install string) (ids map[string]string, users map[string]fixtureUser, err error) {
	ids = map[string]string{}
	users = map[string]fixtureUser{}

	raw, err := readSmallFile(filepath.Join(install, "ids.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, nil, fmt.Errorf("read ids.json: %w", err)
	default:
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, nil, fmt.Errorf("decode ids.json: %w", err)
		}
	}

	raw, err = readSmallFile(filepath.Join(install, "fixture.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, nil, fmt.Errorf("read fixture.json: %w", err)
	default:
		var meta struct {
			Users map[string]fixtureUser `json:"users"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, nil, fmt.Errorf("decode fixture.json: %w", err)
		}
		if meta.Users != nil {
			users = meta.Users
		}
	}

	dbPath := filepath.Join(install, "config", "absdatabase.sqlite")
	if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
		return ids, users, nil
	}
	if err := discoverFixtureIDs(dbPath, ids, users); err != nil {
		return nil, nil, fmt.Errorf("discover fixture IDs: %w", err)
	}
	return ids, users, nil
}

func discoverFixtureIDs(dbPath string, ids map[string]string, users map[string]fixtureUser) (err error) {
	uri := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	setIfMissing := func(key, val string) {
		if val == "" {
			return
		}
		if _, exists := ids[key]; !exists {
			ids[key] = val
		}
	}

	// User IDs from fixture.json users (root, listener, finisher).
	for _, role := range []string{"root", "listener", "finisher"} {
		u, ok := users[role]
		if !ok || u.Username == "" {
			continue
		}
		if id, err := queryString(db, "SELECT id FROM users WHERE username = ? LIMIT 1", u.Username); err != nil {
			return err
		} else {
			setIfMissing(role, id)
		}
	}

	// First two authors ordered by name.
	authorRows, err := db.Query("SELECT id FROM authors ORDER BY name ASC LIMIT 2")
	if err != nil {
		return fmt.Errorf("query authors: %w", err)
	}
	var authorIDs []string
	for authorRows.Next() {
		var id string
		if err := authorRows.Scan(&id); err != nil {
			_ = authorRows.Close()
			return fmt.Errorf("scan author id: %w", err)
		}
		authorIDs = append(authorIDs, id)
	}
	if err := errors.Join(authorRows.Err(), authorRows.Close()); err != nil {
		return fmt.Errorf("close author rows: %w", err)
	}
	if len(authorIDs) > 0 {
		setIfMissing("author1", authorIDs[0])
	}
	if len(authorIDs) > 1 {
		setIfMissing("author2", authorIDs[1])
	}

	// First series ordered by name.
	if seriesID, err := queryString(db, "SELECT id FROM series ORDER BY name ASC LIMIT 1"); err != nil {
		return err
	} else {
		setIfMissing("series1", seriesID)
	}

	// Library folders for book library and podcastLibrary.
	if libID := ids["library"]; libID != "" {
		if folderID, err := queryString(db, "SELECT id FROM libraryFolders WHERE libraryId = ? ORDER BY rowid ASC LIMIT 1", libID); err != nil {
			return err
		} else {
			setIfMissing("folder1", folderID)
		}
	}
	if podLibID := ids["podcastLibrary"]; podLibID != "" {
		if folderID, err := queryString(db, "SELECT id FROM libraryFolders WHERE libraryId = ? ORDER BY rowid ASC LIMIT 1", podLibID); err != nil {
			return err
		} else {
			setIfMissing("podcastFolder1", folderID)
		}
	}

	// books.id for saga1.
	if saga1 := ids["saga1"]; saga1 != "" {
		if bookID, err := queryString(db, "SELECT mediaId FROM libraryItems WHERE id = ? LIMIT 1", saga1); err != nil {
			return err
		} else {
			setIfMissing("book1", bookID)
		}
	}

	// First mediaProgresses row and first bookmark for listener.
	if listenerID := ids["listener"]; listenerID != "" {
		if progID, err := queryString(db, "SELECT id FROM mediaProgresses WHERE userId = ? ORDER BY rowid ASC LIMIT 1", listenerID); err != nil {
			return err
		} else {
			setIfMissing("progress1", progID)
		}
		if rawBookmarks, err := queryString(db, "SELECT bookmarks FROM users WHERE id = ? LIMIT 1", listenerID); err != nil {
			return err
		} else if rawBookmarks != "" {
			if bmID, err := firstBookmarkID(rawBookmarks); err != nil {
				return err
			} else {
				setIfMissing("bookmark1", bmID)
			}
		}
	}

	return nil
}

func queryString(db *sql.DB, query string, args ...any) (string, error) {
	var val sql.NullString
	err := db.QueryRow(query, args...).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("query %q: %w", query, err)
	}
	return val.String, nil
}

func firstBookmarkID(raw string) (string, error) {
	var bookmarks []struct {
		ID            string `json:"id"`
		LibraryItemID string `json:"libraryItemId"`
	}
	if err := json.Unmarshal([]byte(raw), &bookmarks); err != nil {
		return "", fmt.Errorf("decode user bookmarks: %w", err)
	}
	if len(bookmarks) == 0 {
		return "", nil
	}
	if bookmarks[0].ID != "" {
		return bookmarks[0].ID, nil
	}
	return bookmarks[0].LibraryItemID, nil
}

func (tgt *Target) openReadOnly() (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: filepath.Join(tgt.Config, "absdatabase.sqlite"), RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	return sql.Open("sqlite", uri.String())
}

// Rows runs a read-only SQLite query against Config/absdatabase.sqlite and
// returns one map per row ([]byte values become strings).
func (tgt *Target) Rows(t *testing.T, query string, args ...any) []map[string]any {
	t.Helper()
	db, err := tgt.openReadOnly()
	if err != nil {
		t.Fatalf("open %s database: %v", tgt.Name, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close %s database: %v", tgt.Name, err)
		}
	}()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("%s query %q: %v", tgt.Name, query, err)
	}
	defer rows.Close() // rows.Err checked below

	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s query %q columns: %v", tgt.Name, query, err)
	}
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("%s query %q scan: %v", tgt.Name, query, err)
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			if raw, ok := values[i].([]byte); ok {
				values[i] = string(raw)
			}
			row[name] = values[i]
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s query %q: %v", tgt.Name, query, err)
	}
	return result
}

// Count runs a query returning a single integer column (such as SELECT COUNT(*)).
func (tgt *Target) Count(t *testing.T, query string, args ...any) int {
	t.Helper()
	rows := tgt.Rows(t, query, args...)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("%s count query %q: want 1 row with 1 column, got %v", tgt.Name, query, rows)
	}
	for _, value := range rows[0] {
		number, ok := value.(int64)
		if !ok {
			t.Fatalf("%s count query %q: value %v (%T) is not int64", tgt.Name, query, value, value)
		}
		return int(number)
	}
	return 0
}

// Exec executes a mutating SQL statement against Config/absdatabase.sqlite.
// The target server must be stopped first.
func (tgt *Target) Exec(t *testing.T, stmt string, args ...any) int64 {
	t.Helper()
	if tgt.proc != nil {
		t.Fatalf("%s Exec %q: stop the server first; a running server owns its database", tgt.Name, stmt)
	}
	uri := url.URL{Scheme: "file", Path: filepath.Join(tgt.Config, "absdatabase.sqlite"), RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatalf("open %s database: %v", tgt.Name, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close %s database: %v", tgt.Name, err)
		}
	}()
	res, err := db.Exec(stmt, args...)
	if err != nil {
		t.Fatalf("%s exec %q: %v", tgt.Name, stmt, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("%s exec %q rows affected: %v", tgt.Name, stmt, err)
	}
	return affected
}
