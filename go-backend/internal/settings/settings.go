// Package settings holds the three settings documents (server, email,
// notifications) as typed structs and the Store that reads and updates them.
//
// Each document is one row of the settings table: key, the JSON document in
// value, createdAt and updatedAt. Upstream rewrites the whole document from
// its own toJSON on every save, so a key it does not know is dropped on the
// next save, and so is here.
package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"audiobookshelf-compat/internal/db"
)

// Keys of the settings rows.
const (
	ServerKey        = "server-settings"
	EmailKey         = "email-settings"
	NotificationsKey = "notification-settings"
)

// Store reads and updates the settings documents of one database. The server
// settings are cached; email and notification settings are read on demand.
//
// Invariant: the cached server snapshot is replaced only after the transaction
// that wrote it committed, and writers are serialised by mu, so the cache
// never holds a document older than the last completed UpdateServer. A write
// made by other code (the old packages, a restore) needs Reload.
type Store struct {
	db         *db.DB
	getenv     func(string) string
	backupsDir string

	mu       sync.Mutex // serialises UpdateServer and Reload with their cache swap
	server   atomic.Pointer[Server]
	onChange []func(Server) // guarded by mu
}

// NewStore loads the server settings. It creates no row: a missing document
// reads as its defaults and the first update inserts it. getenv supplies
// ALLOW_IFRAME and BACKUP_PATH (os.Getenv in production); backupsDir is the
// default backupPath of a document that stores none.
func NewStore(ctx context.Context, d *db.DB, getenv func(string) string, backupsDir string) (*Store, error) {
	s := &Store{db: d, getenv: getenv, backupsDir: backupsDir}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Server returns the cached server settings. The caller owns the copy: its
// slices are not shared with the cache or with other callers.
func (s *Store) Server() Server { return s.server.Load().clone() }

// Reload re-reads the server settings row, for a write made by code that does
// not use the Store and for a restored database. It does not call OnChange
// functions.
func (s *Store) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, _, err := read(ctx, s.db, ServerKey, s.defaultServer, (*Server).normalize)
	if err != nil {
		return err
	}
	s.server.Store(s.withEnv(server))
	return nil
}

// UpdateServer applies fn to the stored server settings in one transaction and
// saves the result. When fn returns an error nothing is written and the cache
// is unchanged. It returns the new snapshot (with the environment overrides)
// and then calls the OnChange functions. Inside an outer db.Tx the cache is
// swapped before that transaction commits.
func (s *Store) UpdateServer(ctx context.Context, fn func(*Server) error) (Server, error) {
	s.mu.Lock()
	server, err := update(ctx, s.db, ServerKey, s.defaultServer, (*Server).normalize, fn)
	if err != nil {
		s.mu.Unlock()
		return Server{}, err
	}
	snapshot := s.withEnv(server)
	s.server.Store(snapshot)
	callbacks := slices.Clone(s.onChange)
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback(snapshot.clone())
	}
	return snapshot.clone(), nil
}

// OnChange registers fn to be called, with its own copy, after every
// successful UpdateServer.
func (s *Store) OnChange(fn func(Server)) {
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

// Email reads the email settings; a missing row reads as DefaultEmail.
func (s *Store) Email(ctx context.Context) (Email, error) {
	email, _, err := read(ctx, s.db, EmailKey, DefaultEmail, (*Email).normalize)
	return email, err
}

// UpdateEmail applies fn to the stored email settings in one transaction.
func (s *Store) UpdateEmail(ctx context.Context, fn func(*Email) error) (Email, error) {
	return update(ctx, s.db, EmailKey, DefaultEmail, (*Email).normalize, fn)
}

// Notifications reads the notification settings; a missing row reads as
// DefaultNotifications.
func (s *Store) Notifications(ctx context.Context) (Notifications, error) {
	settings, _, err := read(ctx, s.db, NotificationsKey, DefaultNotifications, (*Notifications).normalize)
	return settings, err
}

// UpdateNotifications applies fn to the stored notification settings in one
// transaction. Upstream creates this row on first write, so does this.
func (s *Store) UpdateNotifications(ctx context.Context, fn func(*Notifications) error) (Notifications, error) {
	return update(ctx, s.db, NotificationsKey, DefaultNotifications, (*Notifications).normalize, fn)
}

// read loads the document under key onto defaults(); exists reports whether a
// row with a document was found. A missing key in the document keeps the
// default because the defaults are in place before decoding.
func read[T any](ctx context.Context, d *db.DB, key string, defaults func() T, normalize func(*T)) (doc T, exists bool, err error) {
	doc = defaults()
	var row db.Setting
	err = d.Get(ctx, &row, "SELECT * FROM settings WHERE `key` = ?", key)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, false, nil
	}
	if err != nil {
		return doc, false, fmt.Errorf("read %s: %w", key, err)
	}
	raw := bytes.TrimSpace(row.Value.V)
	if len(raw) == 0 || string(raw) == "null" {
		return doc, true, nil
	}
	err = json.Unmarshal(raw, &doc)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		// A key of the wrong type keeps its default; the rest of the document
		// was decoded. The old code was as lenient; one bad key must not stop
		// the server from starting.
		slog.Warn("settings: ignoring a key of the wrong type", "key", key, "field", typeErr.Field)
	} else if err != nil {
		return defaults(), true, fmt.Errorf("decode %s: %w", key, err)
	}
	normalize(&doc)
	return doc, true, nil
}

// update is the atomic read-modify-write of one document: read, fn, upsert, in
// one transaction. An error from fn rolls back.
func update[T any](ctx context.Context, d *db.DB, key string, defaults func() T, normalize func(*T), fn func(*T) error) (T, error) {
	var doc T
	err := d.Tx(ctx, func(ctx context.Context) error {
		var err error
		if doc, _, err = read(ctx, d, key, defaults, normalize); err != nil {
			return err
		}
		if err := fn(&doc); err != nil {
			return err
		}
		normalize(&doc)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf) // like JSON.stringify, no HTML escaping
		enc.SetEscapeHTML(false)
		if err := enc.Encode(doc); err != nil {
			return fmt.Errorf("encode %s: %w", key, err)
		}
		now := db.Now()
		_, err = d.Exec(ctx, "INSERT INTO settings (`key`, `value`, createdAt, updatedAt) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(`key`) DO UPDATE SET `value` = excluded.`value`, updatedAt = excluded.updatedAt",
			key, string(bytes.TrimSpace(buf.Bytes())), now, now)
		if err != nil {
			return fmt.Errorf("write %s: %w", key, err)
		}
		return nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return doc, nil
}
