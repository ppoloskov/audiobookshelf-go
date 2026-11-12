package server

import (
	"fmt"
	"os"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/settings"
)

// OpenDatabase makes the config and metadata directories and opens the
// database in the config directory. A missing file is created as a fresh
// install; an existing one is validated and never modified.
func OpenDatabase(cfg config.Config) (*db.DB, error) {
	for _, dir := range []string{cfg.ConfigPath, cfg.MetadataPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory: %w", err)
		}
	}
	defaults, err := settings.Fresh(cfg.Dirs().Backups)
	if err != nil {
		return nil, err
	}
	return db.OpenOrCreate(db.FilePath(cfg.ConfigPath), defaults)
}
