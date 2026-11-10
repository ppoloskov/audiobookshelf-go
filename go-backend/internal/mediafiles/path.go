package mediafiles

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/logging"
)

// PathAllowed reports whether target, with symlinks resolved, lies inside one
// of the library's folders.
func PathAllowed(ctx context.Context, db *sql.DB, target, libraryID string) bool {
	rows, err := db.QueryContext(ctx, `SELECT path FROM libraryFolders WHERE libraryId=?`, libraryID)
	if err != nil {
		logging.Errorf("[mediafiles] folders of library %s: %v", libraryID, err)
		return false
	}
	defer rows.Close()
	roots := make([]string, 0)
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			logging.Errorf("[mediafiles] folders of library %s: %v", libraryID, err)
			return false
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		logging.Errorf("[mediafiles] folders of library %s: %v", libraryID, err)
		return false
	}
	return WithinAny(target, roots)
}

// WithinAny reports whether target, with symlinks resolved, lies inside one of
// roots.
func WithinAny(target string, roots []string) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rootResolved, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootResolved, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// PurgeCoverCache removes the cached cover variants of itemID.
func PurgeCoverCache(cacheDir, itemID string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), itemID+"_") {
			_ = os.Remove(filepath.Join(cacheDir, entry.Name()))
		}
	}
}
