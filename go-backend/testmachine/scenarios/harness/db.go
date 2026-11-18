package harness

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

// openReadOnly opens the install's database read-only with a 5s busy timeout.
func (e *Env) openReadOnly() (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: filepath.Join(e.Config, "absdatabase.sqlite"), RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	return sql.Open("sqlite", uri.String())
}

// Rows runs a query against Config/absdatabase.sqlite (opened read-only for
// this call) and returns one map per row. []byte columns become strings; other
// values keep the driver's types (int64, float64, string, nil).
func (e *Env) Rows(t *testing.T, query string, args ...any) []map[string]any {
	t.Helper()
	db, err := e.openReadOnly()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fixture database: %v", err)
		}
	}()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close() // read-only; rows.Err is checked below
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("query %q: %v", query, err)
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
		t.Fatalf("query %q: %v", query, err)
	}
	return result
}

// Count runs a query that returns one integer, for example SELECT COUNT(*).
func (e *Env) Count(t *testing.T, query string, args ...any) int {
	t.Helper()
	rows := e.Rows(t, query, args...)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("query %q: want one row with one column, got %v", query, rows)
	}
	for _, value := range rows[0] {
		number, ok := value.(int64)
		if !ok {
			t.Fatalf("query %q: result %v (%T) is not an integer", query, value, value)
		}
		return int(number)
	}
	return 0
}

// Exec changes rows of the install's database directly. It is only for
// preparing persisted state that no API can produce (an expired session, a
// legacy row, a linked OpenID subject) and is the same for both targets. The
// server must be stopped: call Stop first, then Exec, then Restart. It returns
// the number of affected rows.
func (e *Env) Exec(t *testing.T, statement string, args ...any) int64 {
	t.Helper()
	if e.proc != nil {
		t.Fatalf("Exec %q: stop the server first; a running server owns its database", statement)
	}
	uri := url.URL{Scheme: "file", Path: filepath.Join(e.Config, "absdatabase.sqlite"), RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	result, err := db.Exec(statement, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
	return affected
}
