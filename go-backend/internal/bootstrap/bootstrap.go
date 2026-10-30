// Package bootstrap creates only a new Audiobookshelf database from the
// pinned upstream fresh-init oracle. Existing databases are checked and never
// migrated or rewritten.
package bootstrap

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
	_ "modernc.org/sqlite"
)

const baseline = "2.37.2"

// ErrReadOnlyDatabaseMissing is returned when read-only mode is requested but
// the configured database file does not exist.
var ErrReadOnlyDatabaseMissing = errors.New("bootstrap: database is absent in read-only mode")

var ensureMu sync.Mutex

type migrationAsset struct {
	Tables []struct {
		Table string `json:"table"`
		Rows  []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"rows"`
	} `json:"tables"`
}

type settingsAsset struct {
	Rows []struct {
		Key                  string         `json:"key"`
		Value                map[string]any `json:"value"`
		PersistedAtFreshInit bool           `json:"persistedAtFreshInit"`
	} `json:"rows"`
}

// Ensure creates config and metadata directories and initializes a database
// only when absdatabase.sqlite is absent. Existing files are verified against
// the pinned migrationsMeta state without writes. ABS_GO_READ_ONLY=1 prevents
// all filesystem/database initialization.
func Ensure(cfg config.Config) error {
	ensureMu.Lock()
	defer ensureMu.Unlock()
	readOnly := strings.TrimSpace(os.Getenv("ABS_GO_READ_ONLY")) == "1"
	dbPath := filepath.Join(cfg.ConfigPath, "absdatabase.sqlite")
	if _, err := os.Stat(dbPath); err == nil {
		return verifyRacedExisting(dbPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bootstrap: inspect database: %w", err)
	}
	if readOnly {
		return ErrReadOnlyDatabaseMissing
	}
	if err := os.MkdirAll(cfg.ConfigPath, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create config directory: %w", err)
	}
	if cfg.MetadataPath != "" {
		if err := os.MkdirAll(cfg.MetadataPath, 0o755); err != nil {
			return fmt.Errorf("bootstrap: create metadata directory: %w", err)
		}
	}
	file, err := os.OpenFile(dbPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process won the create race. Treat it as an existing DB and
			// verify it; never modify a file that this process did not create.
			return verifyRacedExisting(dbPath)
		}
		return fmt.Errorf("bootstrap: create database exclusively: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dbPath)
		return fmt.Errorf("bootstrap: close new database file: %w", err)
	}
	if err := initializeNew(dbPath, cfg); err != nil {
		_ = os.Remove(dbPath)
		return err
	}
	return nil
}

func verifyRacedExisting(path string) error {
	var lastErr error
	for attempt := 0; attempt < 300; attempt++ {
		lastErr = verifyExisting(path)
		if lastErr == nil {
			return nil
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return lastErr
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lastErr
}

func verifyExisting(path string) error {
	db, err := openReadOnly(path)
	if err != nil {
		return fmt.Errorf("bootstrap: open existing database: %w", err)
	}
	defer db.Close()
	var version, maxVersion string
	if err := db.QueryRow(`SELECT value FROM migrationsMeta WHERE key = 'version' LIMIT 1`).Scan(&version); err != nil {
		return fmt.Errorf("bootstrap: read migrationsMeta version: %w", err)
	}
	if err := db.QueryRow(`SELECT value FROM migrationsMeta WHERE key = 'maxVersion' LIMIT 1`).Scan(&maxVersion); err != nil {
		return fmt.Errorf("bootstrap: read migrationsMeta maxVersion: %w", err)
	}
	if version != baseline || maxVersion != baseline {
		return fmt.Errorf("bootstrap: existing database migration state is %q/%q; pinned baseline is %s", version, maxVersion, baseline)
	}
	return nil
}

func initializeNew(path string, cfg config.Config) (retErr error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("bootstrap: open new database: %w", err)
	}
	defer func() {
		if err := db.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("bootstrap: close initialized database: %w", err)
		}
	}()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON; PRAGMA user_version = 0;`); err != nil {
		return fmt.Errorf("bootstrap: set SQLite pragmas: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bootstrap: begin schema transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, schemaassets.SchemaSQL); err != nil {
		return fmt.Errorf("bootstrap: apply pinned schema: %w", err)
	}
	var migrations migrationAsset
	if err := json.Unmarshal(schemaassets.MigrationStateJSON, &migrations); err != nil {
		return fmt.Errorf("bootstrap: decode migration oracle: %w", err)
	}
	for _, table := range migrations.Tables {
		if table.Table != "migrationsMeta" {
			continue
		}
		for _, row := range table.Rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO migrationsMeta (key, value) VALUES (?, ?)`, row.Key, row.Value); err != nil {
				return fmt.Errorf("bootstrap: insert migration state: %w", err)
			}
		}
	}
	var defaults settingsAsset
	if err := json.Unmarshal(schemaassets.DefaultSettingsJSON, &defaults); err != nil {
		return fmt.Errorf("bootstrap: decode default settings oracle: %w", err)
	}
	now := sqlDate(time.Now())
	for _, row := range defaults.Rows {
		if !row.PersistedAtFreshInit {
			continue
		}
		if row.Key == "server-settings" {
			backupPath := os.Getenv("BACKUP_PATH")
			if backupPath == "" {
				backupPath = filepath.Join(cfg.MetadataPath, "backups")
			}
			row.Value["backupPath"] = backupPath
			row.Value["authOpenIDTokenSigningAlgorithm"] = "RS256"
			if os.Getenv("ALLOW_IFRAME") == "1" {
				row.Value["allowIframe"] = true
			}
		}
		delete(row.Value, "tokenSecret")
		data, err := json.Marshal(row.Value)
		if err != nil {
			return fmt.Errorf("bootstrap: encode default setting %s: %w", row.Key, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, createdAt, updatedAt) VALUES (?, ?, ?, ?)`, row.Key, string(data), now, now); err != nil {
			return fmt.Errorf("bootstrap: insert default setting %s: %w", row.Key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bootstrap: commit new database: %w", err)
	}
	committed = true
	return nil
}

func openReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func sqlDate(t time.Time) string {
	return dbtime.Format(t)
}
