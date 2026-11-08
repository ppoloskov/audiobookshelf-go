// Package serversettings caches the server-settings row per database handle.
// Save is the only writer of that row and invalidates; whoever closes a handle
// calls Forget, and a restored database is a new handle that starts empty.
package serversettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/textsort"
)

// Settings is an immutable snapshot of the stored document.
type Settings struct {
	doc      map[string]any
	prefixes []string
}

func parse(raw []byte) Settings {
	doc := map[string]any{}
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		doc = map[string]any{}
	}
	return Settings{doc: doc, prefixes: textsort.ParsePrefixes(raw)}
}

// Doc decodes a fresh copy of the document; callers may change it freely.
func (s Settings) Doc() map[string]any {
	raw, _ := json.Marshal(s.doc)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Bool is the document's boolean key (false when absent or not a boolean).
func (s Settings) Bool(key string) bool { v, _ := s.doc[key].(bool); return v }

// String is the document's string key ("" when absent or not a string).
func (s Settings) String(key string) string { v, _ := s.doc[key].(string); return v }

// SortingPrefixes returns a copy of the parsed sortingPrefixes.
func (s Settings) SortingPrefixes() []string { return slices.Clone(s.prefixes) }

// IgnorePrefix is sortingIgnorePrefix: sort titles without their prefix.
func (s Settings) IgnorePrefix() bool { return s.Bool("sortingIgnorePrefix") }

// EnvAllowIframe reports ALLOW_IFRAME=1, which forces allowIframe on.
func EnvAllowIframe() bool { return os.Getenv("ALLOW_IFRAME") == "1" }

// EnvBackupPath is BACKUP_PATH, which overrides the stored backupPath.
func EnvBackupPath() string { return os.Getenv("BACKUP_PATH") }

// AllowIframe is allowIframe as the server enforces it (stored or forced).
func (s Settings) AllowIframe() bool { return s.Bool("allowIframe") || EnvAllowIframe() }

// Defaults is the server-settings document of a fresh install.
func Defaults() map[string]any {
	var defaults struct {
		Rows []struct {
			Key   string         `json:"key"`
			Value map[string]any `json:"value"`
		} `json:"rows"`
	}
	out := map[string]any{}
	if json.Unmarshal(schemaassets.DefaultSettingsJSON, &defaults) == nil {
		for _, row := range defaults.Rows {
			if row.Key == "server-settings" {
				for k, v := range row.Value {
					out[k] = v
				}
			}
		}
	}
	return out
}

// Save writes the whole document and drops the cached value; it is the only
// writer of the server-settings row.
func Save(ctx context.Context, db *sql.DB, doc map[string]any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE settings SET value=?,updatedAt=? WHERE key='server-settings'`, string(b), dbtime.Now())
	if err != nil {
		return err
	}
	Invalidate(db)
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("serversettings: server-settings row missing")
	}
	return nil
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
		s := parse(raw)
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
		return parse(nil)
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
