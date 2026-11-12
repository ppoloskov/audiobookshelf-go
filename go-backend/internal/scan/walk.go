package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/media"
)

// fileEntry is one file found on disk. rel is slash-separated and relative to
// the walk root, or to the item folder once the file belongs to a candidate.
type fileEntry struct {
	path, rel string
	info      os.FileInfo
}

// inode is the file's inode as the stored entries spell it ("" when unknown).
func inode(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(st.Ino, 10)
	}
	return ""
}

// candidate is an item folder (or a single file) found on disk, with the files
// that belong to it.
type candidate struct {
	path         string
	folder       db.LibraryFolder
	isFile       bool
	info         os.FileInfo // of path
	media, other []fileEntry // audio and ebooks; images, text and metadata
}

func (c *candidate) files() []fileEntry { return slices.Concat(c.media, c.other) }

// key is the candidate's path and every inode it can be recognised by.
func (c *candidate) key() keyed {
	k := keyed{path: c.path}
	if c.info != nil {
		k.inos = append(k.inos, inode(c.info))
	}
	for _, f := range c.files() {
		k.inos = append(k.inos, inode(f.info))
	}
	return k
}

// ignoredPath reports whether a scan skips the path (relative to the library
// folder): dot-prefixed names, Synology @eaDir and temporary extensions.
func ignoredPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") || strings.Contains(part, "@eaDir") {
			return true
		}
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".part", ".tmp", ".crdownload", ".download", ".bak", ".old", ".temp", ".tempfile", ".tempfile~":
		return true
	}
	return false
}

// walkFiles lists the files under root that keep accepts (it receives the
// slash path relative to root). It is the only directory walk of the scanner:
// ignored names, directories holding a .ignore file and vanished entries are
// skipped, so the library scan, the item rescan and the watcher agree.
func walkFiles(ctx context.Context, root string, keep func(rel string) bool) ([]fileEntry, error) {
	var out []fileEntry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == root {
				return nil
			}
			if _, statErr := os.Lstat(filepath.Join(p, ".ignore")); ignoredPath(rel) || statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if ignoredPath(rel) || !keep(rel) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out = append(out, fileEntry{path: p, rel: rel, info: info})
		}
		return nil
	})
	return out, err
}

// claim is the set of files of one item folder.
type claim struct {
	dir    string   // relative to the library folder, slash-separated
	single bool     // a media file at the folder root is its own item
	files  []string // relative to dir; nil for a single
}

// claims keeps the claims in the order they were made.
type claims struct {
	order []*claim
	byDir map[string]*claim
}

func (cs *claims) add(dir, rel string, single bool) {
	c := cs.byDir[dir]
	if c == nil {
		c = &claim{dir: dir}
		cs.byDir[dir] = c
		cs.order = append(cs.order, c)
	}
	c.single = single
	if single {
		c.files = nil
		return
	}
	c.files = append(c.files, rel)
}

// groupClaims is scandir.groupFileItemsIntoLibraryItemDirs (scandir.js:37-110):
// each media path claims the first directory on its way down that is already
// claimed, holds the file itself, or is the parent of a single CD folder; a
// media file in the folder root is its own item. Other paths join the nearest
// claimed ancestor. Paths are slash-separated and relative to the library folder.
func groupClaims(media, other []string) *claims {
	cs := &claims{byDir: map[string]*claim{}}
	for _, rel := range media {
		dirs := splitDirs(rel)
		name := filepath.Base(rel)
		if len(dirs) == 0 {
			cs.add(name, name, true)
			continue
		}
		cur := ""
		for i := range dirs {
			cur = joinSlash(cur, dirs[i])
			rest := dirs[i+1:]
			if _, ok := cs.byDir[cur]; ok {
				cs.add(cur, joinSlash(strings.Join(rest, "/"), name), false)
				break
			} else if len(rest) == 0 {
				cs.add(cur, name, false)
				break
			} else if len(rest) == 1 && cdDirectory.MatchString(rest[0]) {
				cs.add(cur, joinSlash(rest[0], name), false)
				break
			}
		}
	}
	for _, rel := range other {
		dirs := splitDirs(rel)
		cur := ""
		for i := range dirs {
			cur = joinSlash(cur, dirs[i])
			if _, ok := cs.byDir[cur]; ok {
				cs.add(cur, joinSlash(strings.Join(dirs[i+1:], "/"), filepath.Base(rel)), false)
				break
			}
		}
	}
	return cs
}

// splitDirs is the directory components of a slash path.
func splitDirs(rel string) []string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		return nil
	}
	return strings.Split(dir, "/")
}

func joinSlash(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "/" + b
}

// candidates turns the files walked under a library folder into item
// candidates ordered by path. Media files are claimed shallowest first so a
// parent directory wins over a nested one.
func (l library) candidates(folder db.LibraryFolder, files []fileEntry) []*candidate {
	slices.SortStableFunc(files, func(a, b fileEntry) int {
		if x, y := strings.Count(a.rel, "/"), strings.Count(b.rel, "/"); x != y {
			return x - y
		}
		return strings.Compare(a.path, b.path)
	})
	byRel := make(map[string]fileEntry, len(files))
	var media, other []string
	for _, f := range files {
		byRel[f.rel] = f
		if l.mediaExt(filepath.Ext(f.path)) {
			media = append(media, f.rel)
		} else {
			other = append(other, f.rel)
		}
	}
	var out []*candidate
	for _, cl := range groupClaims(media, other).order {
		c := &candidate{path: filepath.Join(db.Val(folder.Path), filepath.FromSlash(cl.dir)), folder: folder, isFile: cl.single}
		c.info, _ = os.Stat(c.path) // nil when it vanished: the candidate then has no inode of its own
		if cl.single {
			c.media = []fileEntry{byRel[cl.dir]}
		}
		for _, rel := range cl.files {
			f := byRel[joinSlash(cl.dir, rel)]
			f.rel = rel
			if l.mediaExt(filepath.Ext(f.path)) {
				c.media = append(c.media, f)
			} else {
				c.other = append(c.other, f)
			}
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *candidate) int { return strings.Compare(a.path, b.path) })
	return out
}

// collect lists one item folder (or single file) into a candidate; it is nil
// when no file the scan keeps is there. A path that cannot be read is an error.
func (l library) collect(ctx context.Context, folder db.LibraryFolder, path string, isFile bool) (*candidate, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	c := &candidate{path: path, folder: folder, isFile: isFile, info: info}
	var files []fileEntry
	if info.IsDir() && !isFile {
		files, err = walkFiles(ctx, path, func(rel string) bool { return l.keeps(filepath.Ext(rel)) })
	} else if l.mediaExt(filepath.Ext(path)) {
		files = []fileEntry{{path: path, rel: filepath.Base(path), info: info}}
	}
	if err != nil || len(files) == 0 {
		return nil, err
	}
	for _, f := range files {
		if l.mediaExt(filepath.Ext(f.path)) {
			c.media = append(c.media, f)
		} else {
			c.other = append(c.other, f)
		}
	}
	return c, nil
}

// wantFile reports whether the library scan imports a file: media and
// companions, but only books may sit directly in the folder root.
func (l library) wantFile(rel string) bool {
	ext := filepath.Ext(rel)
	if !strings.Contains(rel, "/") && !(l.mediaType == "book" && l.mediaExt(ext)) {
		return false
	}
	return l.keeps(ext)
}

// keeps reports whether a scan keeps files of this extension: an item's media
// files and the companions that describe it.
func (l library) keeps(ext string) bool { return l.mediaExt(ext) || media.IsCompanion(ext) }

// mediaExt reports whether a file of this extension is an item's media file
// (audio, plus ebooks in a book library that is not audiobooks-only).
func (l library) mediaExt(ext string) bool {
	return media.IsAudio(ext) || (l.mediaType == "book" && !l.audiobooksOnly && media.IsEbook(ext))
}
