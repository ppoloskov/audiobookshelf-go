package playback

import (
	"os"
	"path/filepath"

	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
)

// CleanupOrphanStreams removes prior-process stream directories at server
// startup, before a playback handler can own any sessions.
func CleanupOrphanStreams(root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ids.ValidV4(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			logging.Errorf("[PlaybackSessionManager] Failed to remove orphan stream %q: %v", path, err)
		}
	}
	return nil
}
