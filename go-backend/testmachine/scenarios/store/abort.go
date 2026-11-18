// Direct database writes for the failure scenarios: the temporary-trigger
// technique from go-backend/handover.md. A BEFORE INSERT/UPDATE trigger on one
// table aborts the next statement that table receives, which surfaces inside
// pinned upstream as an unhandled rejection and exits the server with status 1
// while the persisted rows keep their previous values.
package store

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// rawJSONClient is one client for the raw requests that the harness helpers
// cannot make: they tolerate transport errors and are safe for concurrent use.
var rawJSONClient = &http.Client{Timeout: 30 * time.Second}

// rawJSONRequest issues one JSON request and tolerates a transport error,
// which the harness Env.Do would turn into a test failure. A server that dies
// before answering closes the connection instead of returning a status.
func rawJSONRequest(method, target, token string, body []byte) (int, error) {
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rawJSONClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() // fully drained below
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, err
}

// openRW opens the install database for writing while the server runs, to
// install the abort triggers before the request under test. Env.Exec refuses
// to write while the server is alive; only these failure scenarios need that.
func openRW(t *testing.T, e *harness.Env) *sql.DB {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: filepath.Join(e.Config, "absdatabase.sqlite"), RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatalf("open database for writing: %v", err)
	}
	return db
}

// abortTriggers installs BEFORE INSERT and BEFORE UPDATE triggers that abort
// every statement on table, and returns the DROP statements that remove them.
func abortTriggers(t *testing.T, db *sql.DB, table string) (drop []string) {
	t.Helper()
	for _, action := range []string{"INSERT", "UPDATE"} {
		name := "store_abort_" + table + "_" + strings.ToLower(action)
		statement := fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'store abort trigger'); END", name, action, table)
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create trigger %s: %v", name, err)
		}
		drop = append(drop, "DROP TRIGGER IF EXISTS "+name)
	}
	return drop
}
