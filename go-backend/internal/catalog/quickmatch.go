package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediafiles"
)

// SaveCoverFromURL is CoverManager.downloadCoverFromUrlNew (CoverManager.js:
// 300-345) for a quick match: the cover is stored with the item when withItem
// is set, otherwise under the metadata items folder. It returns the stored path.
func (h *Handler) SaveCoverFromURL(ctx context.Context, itemID, itemPath string, withItem bool, imageURL string) (string, error) {
	coverDir := filepath.Join(h.cfg.Dirs().Items, itemID)
	if withItem && itemPath != "" {
		coverDir = itemPath
	}
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return "", errors.New("Failed to fetch image from url")
	}
	tempPath := filepath.Join(coverDir, "cover")
	ext, message := downloadCoverFromURL(ctx, imageURL, tempPath)
	if message != "" {
		return "", errors.New(message)
	}
	coverFullPath := filepath.Join(coverDir, "cover."+ext)
	if err := os.Rename(tempPath, coverFullPath); err != nil {
		return "", errors.New("Failed to fetch image from url")
	}
	removeOldCovers(coverDir, "."+ext)
	mediafiles.PurgeCoverCache(h.coverCachePath(), itemID)
	logging.Infof(`[CoverManager] Downloaded libraryItem cover "%s" from url "%s"`, coverFullPath, imageURL)
	return coverFullPath, nil
}
