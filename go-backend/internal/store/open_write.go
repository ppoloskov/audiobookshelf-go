package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// OpenReadWrite opens an existing baseline database for explicit runtime
// operations. It never creates, migrates, or initializes a database.
func OpenReadWrite(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: database path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve database path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("store: database file %q: %w", abs, err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("store: open database read-write: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect to database: %w", err)
	}
	if err := s.validateSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("store: enable SQLite foreign_keys: %w", err)
		}
		return nil, fmt.Errorf("store: SQLite foreign_keys is not enabled")
	}
	return s, nil
}
