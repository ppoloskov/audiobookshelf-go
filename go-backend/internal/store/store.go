// Package store provides read-only access to the pinned Audiobookshelf SQLite
// schema. It deliberately does not initialize or migrate databases.
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

// Store is a read-only handle to an existing Audiobookshelf database.
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

// OpenReadOnly opens an existing database without creating files or applying
// schema changes. A schema check is performed before the handle is returned.
func OpenReadOnly(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
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
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("store: open database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: connect to database: %w", err)
	}
	var queryOnly int
	if err := db.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("store: enable SQLite query_only: %w", err)
		}
		return nil, fmt.Errorf("store: SQLite query_only is not enabled")
	}
	if err := s.validateSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying database for integrations that need additional
// read-only queries. SQLite query_only remains enabled on the connection.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	row, err := sqlc.New(s.db).GetUserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: read user: %w", err)
	}
	return UserFromRow(row)
}

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

// UserByIDOrOldID mirrors the upstream token lookup for IDs created before
// the 2.3.0 user migration. It scans the stored extraData JSON in Go so it does
// not depend on a SQLite JSON extension being available.
func (s *Store) UserByIDOrOldID(ctx context.Context, id string) (User, error) {
	u, err := s.UserByID(ctx, id)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return u, err
	}
	matched, err := sqlc.New(s.db).GetUserIDByOldUserID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: search legacy user id: %w", err)
	}
	return s.UserByID(ctx, matched)
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

func (s *Store) ServerSettings(ctx context.Context) (map[string]any, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'server-settings' LIMIT 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read server settings: %w", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("store: decode server settings: %w", err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// Libraries returns old-client Library.toOldJSON-compatible maps, ordered by
// displayOrder. Permission filtering belongs to the caller.
func (s *Store) Libraries(ctx context.Context) ([]map[string]any, error) {
	selectCol := func(name string) string { return `"` + name + `"` }
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectCol("id")+`, `+selectCol("name")+`, `+selectCol("displayOrder")+`, `+selectCol("icon")+`, `+selectCol("mediaType")+`, `+selectCol("provider")+`, `+selectCol("lastScan")+`, `+
		selectCol("lastScanVersion")+`, `+selectCol("settings")+`, `+selectCol("createdAt")+`, `+selectCol("updatedAt")+` FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list libraries: %w", err)
	}
	type libraryRow struct {
		item map[string]any
		id   string
	}
	var libraries []libraryRow
	for rows.Next() {
		var id, name, icon, mediaType, provider, version sql.NullString
		var order sql.NullInt64
		var lastScan, created, updated any
		var settings []byte
		if err := rows.Scan(&id, &name, &order, &icon, &mediaType, &provider, &lastScan, &version, &settings, &created, &updated); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan library: %w", err)
		}
		last, err := dbtime.OptionalMillis(lastScan)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: library lastScan: %w", err)
		}
		if last != nil && *last == 0 {
			last = nil
		} // upstream emits valueOf() || null
		createdMs, err := dbtime.OptionalMillis(created)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: library createdAt: %w", err)
		}
		updatedMs, err := dbtime.OptionalMillis(updated)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: library updatedAt: %w", err)
		}
		settingsObject := map[string]any{}
		if len(settings) > 0 && string(settings) != "null" {
			if err := json.Unmarshal(settings, &settingsObject); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: decode library settings: %w", err)
			}
		}
		item := map[string]any{"id": id.String, "name": name.String, "folders": []any{}, "displayOrder": order.Int64,
			"icon": NullString(icon), "mediaType": NullString(mediaType), "provider": NullString(provider), "settings": settingsObject,
			"lastScan": last, "lastScanVersion": version.String, "createdAt": createdMs, "lastUpdate": updatedMs}
		if !version.Valid {
			item["lastScanVersion"] = nil
		}
		libraries = append(libraries, libraryRow{item: item, id: id.String})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: list libraries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, lib := range libraries {
		folders, err := s.libraryFolders(ctx, lib.id)
		if err != nil {
			return nil, err
		}
		lib.item["folders"] = folders
	}
	result := make([]map[string]any, len(libraries))
	for i, lib := range libraries {
		result[i] = lib.item
	}
	return result, nil
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

// NullIfEmpty is a text value that is stored or serialized as NULL when empty.
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) libraryFolders(ctx context.Context, libraryID string) ([]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, libraryId, createdAt FROM libraryFolders WHERE libraryId = ?`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("store: list library folders: %w", err)
	}
	defer rows.Close()
	folders := make([]any, 0)
	for rows.Next() {
		var id, path, parent string
		var added any
		if err := rows.Scan(&id, &path, &parent, &added); err != nil {
			return nil, err
		}
		addedMs, err := dbtime.OptionalMillis(added)
		if err != nil {
			return nil, fmt.Errorf("store: folder createdAt: %w", err)
		}
		var addedAt any
		if addedMs != nil {
			addedAt = *addedMs
		}
		folders = append(folders, map[string]any{"id": id, "fullPath": path, "libraryId": parent, "addedAt": addedAt})
	}
	return folders, rows.Err()
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
