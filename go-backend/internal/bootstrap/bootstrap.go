// Package bootstrap creates only a new Audiobookshelf database from the
// pinned upstream fresh-init oracle. Existing databases are checked and never
// migrated or rewritten.
package bootstrap

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/config"
)

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
// the pinned migrationsMeta state without writes.
func Ensure(cfg config.Config) error {
	ensureMu.Lock()
	defer ensureMu.Unlock()
	dbPath := store.DatabasePath(cfg.ConfigPath)
	if _, err := os.Stat(dbPath); err == nil {
		if err := verifyRacedExisting(dbPath); err != nil {
			return err
		}
		logging.Infof("[Database] Existing database at %q matches the pinned migration state", dbPath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("bootstrap: inspect database: %w", err)
	}
	logging.Infof("[Database] absdatabase.sqlite not found at %s", dbPath) // Database.js:166
	if err := os.MkdirAll(cfg.ConfigPath, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create config directory: %w", err)
	}
	if err := os.MkdirAll(cfg.MetadataPath, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create metadata directory: %w", err)
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
	logging.Infof("[Database] Created new database at %s with the pinned schema and default settings", dbPath)
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
	st, err := store.OpenUnchecked(path)
	if err != nil {
		return fmt.Errorf("bootstrap: open existing database: %w", err)
	}
	defer st.Close()
	if err := st.Validate(context.Background()); err != nil {
		return fmt.Errorf("bootstrap: existing database: %w", err)
	}
	return nil
}

func initializeNew(path string, cfg config.Config) (retErr error) {
	st, err := store.OpenUnchecked(path)
	if err != nil {
		return fmt.Errorf("bootstrap: open new database: %w", err)
	}
	defer func() {
		if err := st.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("bootstrap: close initialized database: %w", err)
		}
	}()
	db := st.DB()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
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
			row.Value = serversettings.FreshDocument(cfg.Dirs().Backups)
		}
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

func sqlDate(t time.Time) string {
	return dbtime.Format(t)
}
