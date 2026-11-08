package catalog

import (
	"context"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/logging"
)

// pathAllowed reports whether target, with symlinks resolved, lies inside one
// of the library's folders.
func (h *Handler) pathAllowed(ctx context.Context, target, libraryID string) bool {
	roots, err := h.queryStrings(ctx, `SELECT path FROM libraryFolders WHERE libraryId=?`, libraryID)
	if err != nil {
		logging.Errorf("[catalog] folders of library %s: %v", libraryID, err)
		return false
	}
	return withinAny(target, roots)
}

// withinAny reports whether target, with symlinks resolved, lies inside one of
// roots.
func withinAny(target string, roots []string) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootAbs, e := filepath.Abs(root)
		if e != nil {
			continue
		}
		rootResolved, e := filepath.EvalSymlinks(rootAbs)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(rootResolved, resolved)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
