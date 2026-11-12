package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/reflectx"
)

// Schema is the pinned upstream v2.37.2 schema (tables, indexes and triggers),
// the text of schema.sql. Create executes it to make a fresh database.
//
//go:embed schema.sql
var Schema string

// DB is the data-access handle: typed row scanning, slice expansion for IN
// lists and context-carried transactions over one SQLite connection pool.
//
// The pool has ONE connection. A query made on the pool while a transaction is
// open waits for the transaction's connection and therefore deadlocks. Tx puts
// the transaction in the context it gives its callback, and every method here
// uses the transaction it finds in its ctx, so the deadlock cannot be written
// as long as callers pass the ctx they were given.
type DB struct {
	x    *sqlx.DB
	cols sync.Map // reflect.Type -> []column, the memoized writable columns of a row struct
}

// New wraps an already-open handle that follows the Connect settings (one
// connection, foreign keys on). Production code uses Open; New is for tests
// that build the handle themselves.
func New(sqlDB *sql.DB) *DB {
	x := sqlx.NewDb(sqlDB, "sqlite")
	// Columns are camelCase db tags used exactly; an untagged field keeps its name.
	x.Mapper = reflectx.NewMapperFunc("db", func(name string) string { return name })
	return &DB{x: x}
}

// SQL returns the underlying *sql.DB, for the filter-data cache, which still
// takes one, and for tests. Queries on it ignore transactions started by Tx.
func (d *DB) SQL() *sql.DB { return d.x.DB }

type txKey struct{ db *DB }

func (d *DB) tx(ctx context.Context) *sqlx.Tx {
	tx, _ := ctx.Value(txKey{d}).(*sqlx.Tx)
	return tx
}

// querier is the part of *sqlx.DB and *sqlx.Tx that DB uses.
type querier interface {
	sqlx.QueryerContext
	sqlx.ExecerContext
}

func (d *DB) conn(ctx context.Context) querier {
	if tx := d.tx(ctx); tx != nil {
		return tx
	}
	return d.x
}

// Get scans the first row of the query into dest, a pointer to a struct or to a
// scalar. It returns sql.ErrNoRows, unwrapped, when there is no row. A struct
// that lacks a selected column fails the scan.
func (d *DB) Get(ctx context.Context, dest any, query string, args ...any) error {
	query, args, err := expand(query, args)
	if err != nil {
		return err
	}
	return sqlx.GetContext(ctx, d.conn(ctx), dest, query, args...)
}

// Select scans every row of the query into dest, a pointer to a slice. Slice
// arguments expand for IN (?), see Exec.
func (d *DB) Select(ctx context.Context, dest any, query string, args ...any) error {
	query, args, err := expand(query, args)
	if err != nil {
		return err
	}
	return sqlx.SelectContext(ctx, d.conn(ctx), dest, query, args...)
}

// Exec runs a statement. A slice argument (other than []byte) expands to a
// comma-separated placeholder list for IN (?); an empty slice binds a single
// NULL, so `x IN (?)` matches nothing. (`x NOT IN (?)` then matches nothing
// too, because NULL comparisons are never true.) Chunk long lists with
// slices.Chunk, SQLite limits the number of variables.
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, args, err := expand(query, args)
	if err != nil {
		return nil, err
	}
	return d.conn(ctx).ExecContext(ctx, query, args...)
}

// expand applies sqlx.In when an argument is a slice.
func expand(query string, args []any) (string, []any, error) {
	var expanded []any
	for i, arg := range args {
		if !isSlice(arg) {
			continue
		}
		if expanded == nil {
			expanded = append([]any(nil), args...)
		}
		if reflect.ValueOf(arg).Len() == 0 {
			expanded[i] = []any{nil}
		}
	}
	if expanded == nil {
		return query, args, nil
	}
	return sqlx.In(query, expanded...)
}

// isSlice reports whether arg is a list to expand: a slice that is neither bytes
// nor a driver.Valuer such as JSON.
func isSlice(arg any) bool {
	if arg == nil {
		return false
	}
	if _, ok := arg.(driver.Valuer); ok {
		return false
	}
	t := reflect.TypeOf(arg)
	return t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8
}

// Tx runs fn in a transaction: it commits when fn returns nil and rolls back
// when fn returns an error or panics (the panic continues). The ctx given to fn
// carries the transaction; every DB method called with it joins the
// transaction. A Tx called with such a ctx joins the open transaction and runs
// fn directly, so the outermost Tx decides the outcome.
func (d *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	if d.tx(ctx) != nil {
		return fn(ctx)
	}
	tx, err := d.x.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	done := false
	defer func() {
		if done {
			return
		}
		_ = tx.Rollback() // the panic in flight is the error that matters
	}()
	if err := fn(context.WithValue(ctx, txKey{d}, tx)); err != nil {
		done = true
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
		}
		return err
	}
	done = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// column is one db-tagged field of a row struct.
type column struct {
	name     string
	index    int
	writable bool // false for columns a trigger maintains after the insert, tagged dbwrite:"insert"
}

// columnsOf lists the db-tagged fields of struct type t in declaration order.
func (d *DB) columnsOf(t reflect.Type) []column {
	if cached, ok := d.cols.Load(t); ok {
		return cached.([]column)
	}
	var cols []column
	for i := range t.NumField() {
		field := t.Field(i)
		name := field.Tag.Get("db")
		if name == "" || name == "-" || !field.IsExported() {
			continue
		}
		cols = append(cols, column{name: name, index: i, writable: field.Tag.Get("dbwrite") != "insert"})
	}
	d.cols.Store(t, cols)
	return cols
}

// rowOf returns the struct value of row, a struct or a pointer to one.
func rowOf(row any) (reflect.Value, error) {
	v := reflect.Indirect(reflect.ValueOf(row))
	if v.Kind() != reflect.Struct {
		return v, fmt.Errorf("row must be a struct or a pointer to one, got %T", row)
	}
	return v, nil
}

// quote quotes an SQL identifier with backticks, which SQLite never falls back
// to reading as a string literal (double quotes would).
func quote(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// Insert inserts row, a struct or pointer to one, into table: every db-tagged
// column, including those tagged dbwrite:"insert".
func (d *DB) Insert(ctx context.Context, table string, row any) error {
	v, err := rowOf(row)
	if err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	var names, marks []string
	var args []any
	for _, col := range d.columnsOf(v.Type()) {
		names = append(names, quote(col.name))
		marks = append(marks, "?")
		args = append(args, v.Field(col.index).Interface())
	}
	query := "INSERT INTO " + quote(table) + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	if _, err := d.conn(ctx).ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}

// Update writes columns of row, a struct or pointer to one, to the table row
// whose id equals the struct's id column. With no columns named it writes every
// column except id and those tagged dbwrite:"insert", which triggers maintain
// once the row exists. An unknown, insert-only or id column name is an error.
// Whether a row matched is not checked.
func (d *DB) Update(ctx context.Context, table string, row any, columns ...string) error {
	v, err := rowOf(row)
	if err != nil {
		return fmt.Errorf("update %s: %w", table, err)
	}
	all := d.columnsOf(v.Type())
	byName := make(map[string]column, len(all))
	for _, col := range all {
		byName[col.name] = col
	}
	idCol, ok := byName["id"]
	if !ok {
		return fmt.Errorf("update %s: %T has no id column", table, row)
	}
	var set []column
	if len(columns) == 0 {
		for _, col := range all {
			if col.writable && col.name != "id" {
				set = append(set, col)
			}
		}
	}
	for _, name := range columns {
		col, ok := byName[name]
		switch {
		case !ok:
			return fmt.Errorf("update %s: unknown column %q", table, name)
		case !col.writable || name == "id":
			return fmt.Errorf("update %s: column %q is not writable", table, name)
		}
		set = append(set, col)
	}
	if len(set) == 0 {
		return fmt.Errorf("update %s: no columns to write", table)
	}
	assignments := make([]string, len(set))
	args := make([]any, 0, len(set)+1)
	for i, col := range set {
		assignments[i] = quote(col.name) + " = ?"
		args = append(args, v.Field(col.index).Interface())
	}
	args = append(args, v.Field(idCol.index).Interface())
	query := "UPDATE " + quote(table) + " SET " + strings.Join(assignments, ", ") + " WHERE `id` = ?"
	if _, err := d.conn(ctx).ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("update %s: %w", table, err)
	}
	return nil
}
