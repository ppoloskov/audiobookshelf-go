package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "internal", "demo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "demo.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "internal")
}

func findingsFor(t *testing.T, source string) []string {
	t.Helper()
	root := writeFixture(t, source)
	got, err := run(root, false)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, f := range got {
		msgs = append(msgs, f.msg)
	}
	return msgs
}

const header = `package demo

import (
	"context"
	"database/sql"
)

type H struct{ db *sql.DB }

func (h *H) byID(ctx context.Context, id string) (string, error) {
	var out string
	err := h.db.QueryRowContext(ctx, "SELECT x FROM t WHERE id=?", id).Scan(&out)
	return out, err
}
`

func TestFlagsQueryInsideRowsLoop(t *testing.T) {
	got := findingsFor(t, header+`
func (h *H) bad(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, "SELECT id FROM t")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if _, err := h.byID(ctx, id); err != nil {
			return err
		}
	}
	return rows.Err()
}
`)
	if len(got) != 1 || !strings.Contains(got[0], "h.byID") {
		t.Fatalf("findings = %v, want one for h.byID", got)
	}
}

func TestFlagsPoolUseInsideTransaction(t *testing.T) {
	got := findingsFor(t, header+`
func (h *H) bad(ctx context.Context) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := h.db.ExecContext(ctx, "UPDATE t SET x=1"); err != nil {
		return err
	}
	return tx.Commit()
}
`)
	if len(got) != 1 || !strings.Contains(got[0], "h.db.ExecContext") {
		t.Fatalf("findings = %v, want one for h.db.ExecContext", got)
	}
}

func TestAllowsMaterializedRowsAndTxQueries(t *testing.T) {
	got := findingsFor(t, header+`
func (h *H) good(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, "SELECT id FROM t")
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := h.byID(ctx, id); err != nil {
			return err
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE t SET x=1"); err != nil {
		return err
	}
	return tx.Commit()
}
`)
	if len(got) != 0 {
		t.Fatalf("findings = %v, want none", got)
	}
}

func TestBreakKeepsCursorOpenAfterLoop(t *testing.T) {
	got := findingsFor(t, header+`
func (h *H) bad(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, "SELECT id FROM t")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		break
	}
	_, err = h.byID(ctx, "x")
	return err
}
`)
	if len(got) != 1 {
		t.Fatalf("findings = %v, want one", got)
	}
}

func TestSuppressionComment(t *testing.T) {
	got := findingsFor(t, header+`
func (h *H) ok(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, "SELECT id FROM t")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		//dbnest:ok demonstration of the escape hatch
		if _, err := h.byID(ctx, "x"); err != nil {
			return err
		}
	}
	return nil
}
`)
	if len(got) != 0 {
		t.Fatalf("findings = %v, want none", got)
	}
}

func TestFlagsSQLiteFixtureWithoutSingleConnection(t *testing.T) {
	root := writeFixture(t, `package demo

import "database/sql"

func open() (*sql.DB, error) {
	return sql.Open("sqlite", ":memory:")
}
`)
	got, err := run(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].msg, "SetMaxOpenConns(1)") {
		t.Fatalf("findings = %v, want one SetMaxOpenConns finding", got)
	}
}

// The repository itself must stay clean: this is the regression guard for the
// one-connection pool deadlocks (ROADMAP C22).
func TestInternalPackagesAreClean(t *testing.T) {
	got, err := run(filepath.Join("..", "..", "internal"), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got {
		t.Errorf("%s:%d: %s", f.pos.Filename, f.pos.Line, f.msg)
	}
}
