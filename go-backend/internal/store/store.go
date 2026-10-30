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
	"time"

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
	return s.user(ctx, "id", id)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, extraData FROM users`)
	if err != nil {
		return User{}, fmt.Errorf("store: search legacy user id: %w", err)
	}
	type candidate struct{ id string }
	var candidates []candidate
	for rows.Next() {
		var userID string
		var raw []byte
		if err := rows.Scan(&userID, &raw); err != nil {
			rows.Close()
			return User{}, err
		}
		var extra struct {
			OldUserID string `json:"oldUserId"`
		}
		if len(raw) != 0 && json.Unmarshal(raw, &extra) == nil && extra.OldUserID == id {
			candidates = append(candidates, candidate{id: userID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return User{}, err
	}
	if err := rows.Close(); err != nil {
		return User{}, err
	}
	if len(candidates) == 0 {
		return User{}, ErrNotFound
	}
	return s.UserByID(ctx, candidates[0].id)
}

func (s *Store) user(ctx context.Context, col, value string) (User, error) {
	if col != "id" && col != "username" {
		return User{}, fmt.Errorf("store: invalid user lookup")
	}
	selectCol := func(name string) string { return `"` + name + `"` }
	var u User
	var email, pash, typ, token sql.NullString
	var lastSeen, created, updated any
	var active, locked int64
	var permissions, bookmarks, extra []byte
	query := `SELECT ` + selectCol("id") + `, ` + selectCol("username") + `, ` + selectCol("email") + `, ` + selectCol("pash") + `, ` + selectCol("type") + `, ` + selectCol("token") + `, ` + selectCol("isActive") + `, ` + selectCol("isLocked") + `, ` +
		selectCol("lastSeen") + `, ` + selectCol("permissions") + `, ` + selectCol("bookmarks") + `, ` + selectCol("extraData") + `, ` + selectCol("createdAt") + `, ` + selectCol("updatedAt") + ` FROM users WHERE ` + col + ` = ? LIMIT 1`
	err := s.db.QueryRowContext(ctx, query, value).Scan(&u.ID, &u.Username, &email, &pash, &typ, &token, &active, &locked,
		&lastSeen, &permissions, &bookmarks, &extra, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: read user: %w", err)
	}
	u.Email, u.Password, u.Type, u.Token = email.String, pash.String, typ.String, token.String
	u.IsActive, u.IsLocked = active != 0, locked != 0
	u.LastSeen, err = nullableMillis(lastSeen)
	if err != nil {
		return User{}, fmt.Errorf("store: user lastSeen: %w", err)
	}
	u.CreatedAt, err = nullableMillis(created)
	if err != nil {
		return User{}, fmt.Errorf("store: user createdAt: %w", err)
	}
	u.UpdatedAt, err = nullableMillis(updated)
	if err != nil {
		return User{}, fmt.Errorf("store: user updatedAt: %w", err)
	}
	u.Permissions, err = rawJSON(permissions)
	if err != nil {
		return User{}, fmt.Errorf("store: user permissions: %w", err)
	}
	u.Bookmarks, err = rawJSON(bookmarks)
	if err != nil {
		return User{}, fmt.Errorf("store: user bookmarks: %w", err)
	}
	u.ExtraData, err = rawJSON(extra)
	if err != nil {
		return User{}, fmt.Errorf("store: user extraData: %w", err)
	}
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
		last, err := nullableMillis(lastScan)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: library lastScan: %w", err)
		}
		if last != nil && *last == 0 {
			last = nil
		} // upstream emits valueOf() || null
		createdMs, err := nullableMillis(created)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: library createdAt: %w", err)
		}
		updatedMs, err := nullableMillis(updated)
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
			"icon": nullString(icon), "mediaType": nullString(mediaType), "provider": nullString(provider), "settings": settingsObject,
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

func nullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
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
		addedMs, err := nullableMillis(added)
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

func nullableMillis(v any) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	var n int64
	switch x := v.(type) {
	case int64:
		n = x
	case int:
		n = int64(x)
	case float64:
		n = int64(x)
	case []byte:
		return parseDate(string(x))
	case string:
		return parseDate(x)
	case time.Time:
		n = x.UnixMilli()
	default:
		return nil, fmt.Errorf("unsupported date value %T", v)
	}
	// Numeric date values in fixtures and integrations are interpreted as Unix
	// milliseconds. The pinned Sequelize SQLite schema stores dates as text.
	return &n, nil
}

func parseDate(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if n, err := time.Parse(time.RFC3339Nano, s); err == nil {
		ms := n.UnixMilli()
		return &ms, nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -07:00",
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if n, err := time.Parse(layout, s); err == nil {
			ms := n.UnixMilli()
			return &ms, nil
		}
	}
	return nil, fmt.Errorf("unrecognized timestamp %q", s)
}
