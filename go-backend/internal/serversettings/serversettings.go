// Package serversettings caches the decoded server-settings row per database
// handle. Every writer of that row calls Invalidate; a restored database is a
// new handle and starts empty.
package serversettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"audiobookshelf-compat/internal/libitem"
)

// Settings is the stored document. Doc is shared between callers: read only.
type Settings struct {
	JSON            []byte
	Doc             map[string]any
	SortingPrefixes []string
}

type entry struct {
	mu  sync.Mutex
	cur *Settings
	gen uint64
}

var entries sync.Map // *sql.DB -> *entry

func entryOf(db *sql.DB) *entry {
	e, _ := entries.LoadOrStore(db, &entry{})
	return e.(*entry)
}

// Get returns the cached settings, reading the row when it is not cached.
func Get(ctx context.Context, db *sql.DB) (Settings, error) {
	e := entryOf(db)
	e.mu.Lock()
	if e.cur != nil {
		defer e.mu.Unlock()
		return *e.cur, nil
	}
	gen := e.gen
	e.mu.Unlock()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		return Settings{}, err
	}
	s := Settings{JSON: raw, Doc: map[string]any{}, SortingPrefixes: libitem.SortingPrefixes(raw)}
	if json.Unmarshal(raw, &s.Doc) != nil || s.Doc == nil {
		s.Doc = map[string]any{}
	}
	e.mu.Lock()
	if e.gen == gen {
		e.cur = &s
	}
	e.mu.Unlock()
	return s, nil
}

// Load is Get with defaults (no keys, sortingPrefixes the, a) when the row
// cannot be read.
func Load(ctx context.Context, db *sql.DB) Settings {
	s, err := Get(ctx, db)
	if err != nil {
		return Settings{Doc: map[string]any{}, SortingPrefixes: libitem.SortingPrefixes(nil)}
	}
	return s
}

// Invalidate drops the cached value after the server-settings row is written.
func Invalidate(db *sql.DB) {
	e := entryOf(db)
	e.mu.Lock()
	e.cur, e.gen = nil, e.gen+1
	e.mu.Unlock()
}
