package settings

import (
	"encoding/json"
	"fmt"

	"audiobookshelf-compat/internal/db"
)

// Fresh returns the settings rows a new database starts with, for db.Create:
// the server settings at their defaults and nothing else, as upstream's first
// start persists. The email and notification documents appear on their first
// save; until then they read as their defaults. The row has no token secret
// (it reads as null) and carries no environment override, which the Store
// applies to its snapshot but never writes.
func Fresh(backupsDir string) ([]db.Setting, error) {
	doc, err := json.Marshal(DefaultServer(backupsDir))
	if err != nil {
		return nil, fmt.Errorf("settings: encode fresh server settings: %w", err)
	}
	return []db.Setting{{Key: ServerKey, Value: db.JSON[json.RawMessage]{V: doc}}}, nil
}
