// Package store owns the Audiobookshelf SQLite database: where the file lives,
// how it is opened (one DSN, one set of pragmas), and the nullable-column
// helpers. It deliberately does not initialize or migrate databases.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("store: record not found")
	ErrSchema   = errors.New("store: unsupported or incomplete Audiobookshelf schema")
)

// Store is a handle to an existing Audiobookshelf database.
type Store struct{ db *sql.DB }

// User is the database representation of the upstream Sequelize user model.
// JSON columns remain raw so callers can preserve the exact upstream values.
type User struct {
	ID          string          `json:"id"`
	Username    string          `json:"username"`
	Email       string          `json:"email"`
	Password    string          `json:"pash"`
	Type        string          `json:"type"`
	Token       string          `json:"token"`
	IsActive    bool            `json:"isActive"`
	IsLocked    bool            `json:"isLocked"`
	LastSeen    *int64          `json:"lastSeen"`
	Permissions json.RawMessage `json:"permissions"`
	Bookmarks   json.RawMessage `json:"bookmarks"`
	ExtraData   json.RawMessage `json:"extraData"`
	CreatedAt   *int64          `json:"createdAt"`
	UpdatedAt   *int64          `json:"updatedAt"`
}

// DatabasePath is the database file inside the configuration directory.
func DatabasePath(configPath string) string {
	return filepath.Join(configPath, "absdatabase.sqlite")
}

// Open opens an existing database and checks that it has the tables and
// columns this server uses. It never creates, migrates, or initializes one.
func Open(path string) (*Store, error) {
	s, err := OpenUnchecked(path)
	if err != nil {
		return nil, err
	}
	if err := s.validateSchema(context.Background()); err != nil {
		s.db.Close()
		return nil, err
	}
	return s, nil
}

// OpenUnchecked is Open without the schema check, for bootstrap, which opens
// a file that is still empty or whose migration state it is about to verify.
// It is the only place that builds the DSN and applies the connection pragmas.
func OpenUnchecked(path string) (*Store, error) {
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
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect to database: %w", err)
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("store: enable SQLite foreign_keys: %w", err)
		}
		return nil, fmt.Errorf("store: SQLite foreign_keys is not enabled")
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying database handle to the packages that query it.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) validateSchema(ctx context.Context) error {
	// These are the model columns used by this adapter at the v2.37.2 baseline.
	// Extra columns are accepted; missing model columns are not silently guessed.
	required := map[string][]string{
		"users":          {"id", "username", "email", "pash", "type", "token", "isActive", "isLocked", "lastSeen", "permissions", "bookmarks", "extraData", "createdAt", "updatedAt"},
		"libraries":      {"id", "name", "displayOrder", "icon", "mediaType", "provider", "lastScan", "lastScanVersion", "settings", "extraData", "createdAt", "updatedAt"},
		"libraryFolders": {"id", "path", "libraryId", "createdAt", "updatedAt"},
		"settings":       {"key", "value"},
	}
	for table, want := range required {
		got, err := tableColumns(ctx, s.db, table)
		if err != nil {
			return fmt.Errorf("%w: inspect table %s: %v", ErrSchema, table, err)
		}
		for _, col := range want {
			if _, ok := got[col]; !ok {
				return fmt.Errorf("%w: table %s is missing column %s", ErrSchema, table, col)
			}
		}
	}
	return nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	// table is selected exclusively from a fixed internal list.
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(\""+table+"\")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}

// UserFromRow maps a users row from the generated queries to User. It is the
// only place that converts user columns: NULL text becomes "", NULL flags
// become false, DATETIME values go through dbtime, and JSON text is kept raw.
func UserFromRow(r sqlc.User) (User, error) {
	u := User{
		ID:       r.ID,
		Username: r.Username.String,
		Email:    r.Email.String,
		Password: r.Pash.String,
		Type:     r.Type.String,
		Token:    r.Token.String,
		IsActive: r.Isactive.Int64 != 0,
		IsLocked: r.Islocked.Int64 != 0,
	}
	var err error
	if u.LastSeen, err = dbtime.OptionalMillis(r.Lastseen); err != nil {
		return User{}, fmt.Errorf("store: user lastSeen: %w", err)
	}
	if u.CreatedAt, err = dbtime.OptionalMillis(r.Createdat); err != nil {
		return User{}, fmt.Errorf("store: user createdAt: %w", err)
	}
	if u.UpdatedAt, err = dbtime.OptionalMillis(r.Updatedat); err != nil {
		return User{}, fmt.Errorf("store: user updatedAt: %w", err)
	}
	u.Permissions = lenientJSON(r.Permissions.String)
	u.Bookmarks = lenientJSON(r.Bookmarks.String)
	u.ExtraData = lenientJSON(r.Extradata.String)
	return u, nil
}

// NullString is a nullable text column as a JSON value: NULL becomes nil.
func NullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// NullStringPtr is a nullable text column as an optional string field: NULL
// becomes nil, which encodes as JSON null.
func NullStringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// StringPtrValue unwraps an optional string for a dynamic JSON value.
func StringPtrValue(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// NullInt64 is a nullable integer column as a JSON value: NULL becomes nil.
func NullInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// NullIfZero is an integer (for example epoch milliseconds) that reads as
// absent when it is 0.
func NullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// NullIfBlank is a text value that reads as absent when it is empty or only
// white space.
func NullIfBlank(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// Texts is Text for each element, for sqlc.slice parameters over nullable columns.
func Texts(values []string) []sql.NullString {
	out := make([]sql.NullString, len(values))
	for i, v := range values {
		out[i] = Text(v)
	}
	return out
}

// NullIfEmpty is a text value that is stored or serialized as NULL when empty.
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rawJSON(b []byte) (json.RawMessage, error) {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return append(json.RawMessage(nil), b...), nil
}

// lenientJSON reads a users JSON column. A malformed value reads as null
// instead of failing: one bad column must not lock a user out of login.
func lenientJSON(v string) json.RawMessage {
	raw, err := rawJSON([]byte(v))
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
