// Package serversettings caches the server-settings row per database handle.
// Every writer of that row calls Invalidate; whoever closes a handle calls
// Forget, and a restored database is a new handle that starts empty.
package serversettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sync"

	"audiobookshelf-compat/internal/libitem"
)

// Settings is an immutable snapshot of the stored document.
type Settings struct {
	raw      []byte
	prefixes []string
}

// Doc decodes a fresh copy of the document; callers may change it freely.
func (s Settings) Doc() map[string]any {
	doc := map[string]any{}
	if json.Unmarshal(s.raw, &doc) != nil || doc == nil {
		return map[string]any{}
	}
	return doc
}

// Bool is the document's boolean key (false when absent or not a boolean).
func (s Settings) Bool(key string) bool { v, _ := s.Doc()[key].(bool); return v }

// SortingPrefixes returns a copy of the parsed sortingPrefixes.
func (s Settings) SortingPrefixes() []string { return slices.Clone(s.prefixes) }

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

// Get returns the cached settings, reading the row when it is not cached. A
// read that overlaps an Invalidate is discarded and repeated, so Get never
// returns a document older than the last completed write.
func Get(ctx context.Context, db *sql.DB) (Settings, error) {
	e := entryOf(db)
	for {
		e.mu.Lock()
		if e.cur != nil {
			s := *e.cur
			e.mu.Unlock()
			return s, nil
		}
		gen := e.gen
		e.mu.Unlock()
		var raw []byte
		if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
			return Settings{}, err
		}
		s := Settings{raw: raw, prefixes: libitem.SortingPrefixes(raw)}
		e.mu.Lock()
		if e.gen == gen {
			e.cur = &s
			e.mu.Unlock()
			return s, nil
		}
		e.mu.Unlock()
	}
}

// Load is Get with defaults (no keys, sortingPrefixes the, a) when the row
// cannot be read.
func Load(ctx context.Context, db *sql.DB) Settings {
	s, err := Get(ctx, db)
	if err != nil {
		return Settings{prefixes: libitem.SortingPrefixes(nil)}
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

// Forget drops the handle's entry; call it when the handle is closed.
func Forget(db *sql.DB) { entries.Delete(db) }
