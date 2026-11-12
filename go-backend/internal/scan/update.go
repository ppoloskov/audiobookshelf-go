package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
)

// change is what a rescan of one item folder did.
type change int

const (
	nothing  change = iota // no files found
	added                  // a new item
	updated                // an existing item changed
	removed                // an existing item's folder is gone
	uptodate               // rescanned, nothing changed
)

// rescan reimports a stored item from its candidate folder, forced, and
// announces item_updated when it changed.
func (s *Scanner) rescan(ctx context.Context, lib library, ex *db.LibraryItem, c *candidate) (change, error) {
	out, err := s.importItem(ctx, lib, c, ex, true)
	if err != nil {
		return nothing, err
	}
	if !out.updated {
		return uptodate, nil
	}
	if v, err := s.view(ctx, out.id); err == nil {
		s.emit.ToAll("item_updated", v)
	}
	var tally scanTally
	tally.add(out.links)
	s.finalize(ctx, lib.id, tally)
	return updated, nil
}

// RescanItem refreshes one existing item, as POST /api/items/{id}/scan does,
// and leaves the rest of its library alone.
func (s *Scanner) RescanItem(ctx context.Context, itemID string) (Result, error) {
	var ex db.LibraryItem
	if err := s.db.Get(ctx, &ex, `SELECT * FROM libraryItems WHERE id = ?`, itemID); err != nil {
		return Result{}, fmt.Errorf("item %q: %w", itemID, err)
	}
	libraryID := db.Val(ex.LibraryID)
	ctx, end, err := s.begin(ctx, libraryID)
	if err != nil {
		return Result{}, err
	}
	defer end()
	res, err := s.rescanStored(ctx, libraryID, &ex)
	res.Canceled = ctx.Err() != nil
	return res, err
}

func (s *Scanner) rescanStored(ctx context.Context, libraryID string, ex *db.LibraryItem) (Result, error) {
	var folder db.LibraryFolder
	if err := s.db.Get(ctx, &folder, `SELECT * FROM libraryFolders WHERE id = ?`, db.Val(ex.LibraryFolderID)); err != nil {
		return Result{}, err
	}
	lib, err := s.loadLibrary(ctx, libraryID)
	if err != nil {
		return Result{}, err
	}
	c, err := lib.collect(ctx, folder, db.Val(ex.Path), bool(ex.IsFile))
	if err != nil || c == nil {
		return Result{Nothing: err == nil}, err
	}
	kind, err := s.rescan(ctx, lib, ex, c)
	res := Result{Nothing: kind == nothing}
	if kind == updated {
		res.Updated = 1
	}
	return res, err
}

// target is an item folder a batch of file events touched.
type target struct {
	path   string
	single bool
}

// updateTargets maps the changed paths (slash paths relative to the library
// folder) to the item folders to rescan: the folders the scan would claim for
// them, lifted to the stored item that contains them.
func updateTargets(lib library, folder db.LibraryFolder, rels []string, stored []db.LibraryItem) []target {
	var media, other []string
	for _, rel := range rels {
		rel = strings.TrimPrefix(rel, "/")
		ext := filepath.Ext(rel)
		if !(strings.Contains(rel, "/") || (lib.mediaType == "book" && lib.mediaExt(ext))) {
			continue
		}
		if lib.keeps(ext) {
			media = append(media, rel)
		} else {
			other = append(other, rel)
		}
	}
	root := filepath.ToSlash(db.Val(folder.Path))
	hasChild := func(dir string) bool {
		return slices.ContainsFunc(stored, func(it db.LibraryItem) bool {
			return strings.HasPrefix(filepath.ToSlash(db.Val(it.Path)), joinSlash(root, dir)+"/")
		})
	}
	byPath := map[string]db.LibraryItem{}
	for _, it := range stored {
		byPath[filepath.ToSlash(db.Val(it.Path))] = it
	}
	var targets, moved []target
	for _, cl := range groupClaims(media, other).order {
		// Files moved out of an item folder into its parent (LibraryScanner.js:501-545):
		// the stored item lives below the folder, not below the nested one.
		if i := slices.IndexFunc(cl.files, func(rel string) bool { return strings.Contains(rel, "/") }); !cl.single && i >= 0 {
			alt := cl.dir + "/" + strings.SplitN(cl.files[i], "/", 2)[0]
			if hasChild(cl.dir) && !hasChild(alt) {
				moved = append(moved, target{path: joinSlash(root, alt)})
				continue
			}
		}
		targets = append(targets, target{path: joinSlash(root, cl.dir), single: cl.single})
	}
	var out []target
	for _, t := range append(targets, moved...) {
		for dir := t.path; len(dir) > len(root); dir = filepath.ToSlash(filepath.Dir(dir)) {
			if it, ok := byPath[dir]; ok {
				t = target{path: dir, single: bool(it.IsFile)}
				break
			}
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// scanFolderUpdates rescans the item folders that the changed paths of one
// library folder touched (LibraryScanner.scanFolderUpdates): the folders are
// re-walked and paired with the stored items like a library scan does.
func (s *Scanner) scanFolderUpdates(ctx context.Context, libraryID, folderID string, rels []string) ([]change, error) {
	lib, err := s.loadLibrary(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	var folder db.LibraryFolder
	if err := s.db.Get(ctx, &folder, `SELECT * FROM libraryFolders WHERE id = ? AND libraryId = ?`, folderID, libraryID); err != nil {
		return nil, err
	}
	stored, err := s.storedItems(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	targets := updateTargets(lib, folder, rels, stored)
	cands := make([]*candidate, len(targets))
	gone := make([]bool, len(targets))
	var found []*candidate
	for i, t := range targets {
		c, err := lib.collect(ctx, folder, t.path, t.single)
		gone[i] = errors.Is(err, fs.ErrNotExist)
		if err != nil && !gone[i] {
			return nil, err
		}
		if cands[i] = c; c != nil {
			found = append(found, c)
		}
	}
	matched, _ := matchItems(stored, found)
	owner := map[*candidate]*db.LibraryItem{}
	for i, c := range matched {
		if c != nil {
			owner[c] = &stored[i]
		}
	}
	var results []change
	for i, t := range targets {
		var result change
		switch ex, c := owner[cands[i]], cands[i]; {
		case gone[i]:
			if j := slices.IndexFunc(stored, func(it db.LibraryItem) bool { return db.Val(it.Path) == t.path }); j >= 0 {
				if err := s.markMissing(ctx, &stored[j]); err != nil {
					return results, err
				}
				if v, err := s.view(ctx, stored[j].ID); err == nil {
					s.emit.ToAll("item_updated", v)
				}
				result = removed
			}
		case c == nil:
		case ex != nil:
			result, err = s.rescan(ctx, lib, ex, c)
		default:
			result, err = s.addFolder(ctx, lib, c, stored)
		}
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// addFolder imports a folder no stored item claims, unless a stored item
// already lives below it.
func (s *Scanner) addFolder(ctx context.Context, lib library, c *candidate, stored []db.LibraryItem) (change, error) {
	if lib.audiobooksOnly && len(c.media) == 0 {
		return nothing, nil
	}
	if slices.ContainsFunc(stored, func(it db.LibraryItem) bool {
		return strings.HasPrefix(db.Val(it.Path), c.path+string(filepath.Separator))
	}) {
		return nothing, nil
	}
	out, err := s.importItem(ctx, lib, c, nil, false)
	if err != nil || !out.added {
		return nothing, err
	}
	if v, err := s.view(ctx, out.id); err == nil {
		s.emit.ToAll("item_added", v)
	}
	// Only the author book counts are announced: the watcher does not sweep.
	s.finalize(ctx, lib.id, scanTally{authorsChanged: out.links.AuthorsChanged})
	return added, nil
}
