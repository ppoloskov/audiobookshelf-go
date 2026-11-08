package scanner

import (
	"audiobookshelf-compat/internal/filekind"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// walkFiles lists the files under root that keep accepts (it receives the
// OS-separated path relative to root). It is the only directory walk of the
// scanner: ignored directories and files (shouldIgnore), directories holding
// a .ignore file and vanished entries are skipped here, so the library scan,
// the single-item rescan and the watcher agree on what exists.
func walkFiles(ctx context.Context, root string, keep func(rel string) bool) ([]file, error) {
	ignoredDirs := map[string]bool{}
	var out []file
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			if errors.Is(e, os.ErrNotExist) {
				return nil
			}
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := mustRel(root, p)
		if d.IsDir() {
			if p != root && (shouldIgnore(rel) || ignoredDirs[p]) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".ignore" {
			if dir := filepath.Dir(p); dir != root {
				ignoredDirs[dir] = true
				return filepath.SkipDir
			}
			return nil
		}
		for ignored := range ignoredDirs {
			if strings.HasPrefix(p, ignored+string(filepath.Separator)) {
				return nil
			}
		}
		if shouldIgnore(rel) || !keep(rel) {
			return nil
		}
		if info, e := d.Info(); e == nil {
			out = append(out, file{path: p, info: info, relPath: filepath.ToSlash(rel)})
		}
		return nil
	})
	return out, err
}

// wantFile reports whether the library scan imports a file: media and
// companions, but only books may sit directly in the folder root.
func (l library) wantFile(rel string) bool {
	ext := filepath.Ext(rel)
	if filepath.Dir(rel) == "." && !(l.mediaType == "book" && l.mediaExt(ext)) {
		return false
	}
	return l.mediaExt(ext) || filekind.IsCompanion(ext)
}

// mediaExt reports whether a file of this extension is an item's media file
// (audio, plus ebooks in a book library that is not audiobooks-only).
func (l library) mediaExt(ext string) bool {
	return filekind.IsAudio(ext) || (l.mediaType == "book" && !l.audiobooksOnly && filekind.IsEbook(ext))
}

// updateGroup holds the files claimed by each item directory, in the
// insertion order JavaScript object keys have. A nil file list marks a
// single-file item.
type updateGroup struct {
	keys  []string
	files map[string][]string
}

func (g *updateGroup) add(key, rel string, single bool) {
	if _, ok := g.files[key]; !ok {
		g.keys = append(g.keys, key)
	}
	if single {
		g.files[key] = nil
		return
	}
	g.files[key] = append(g.files[key], rel)
}

func (g *updateGroup) isSingle(key string) bool {
	files, ok := g.files[key]
	return ok && files == nil
}

func (g *updateGroup) drop(key string) {
	delete(g.files, key)
	for i, k := range g.keys {
		if k == key {
			g.keys = append(g.keys[:i:i], g.keys[i+1:]...)
			break
		}
	}
}

// claimGroups is scandir.groupFileItemsIntoLibraryItemDirs (scandir.js:37-110),
// the one grouping rule: each media path claims the first directory on its
// way down that is already claimed, holds the file itself, or is a parent of a
// single CD folder; a root-level media file is its own item. Other paths join
// the nearest claimed ancestor. Paths are slash-separated relative to the
// library folder; the files of a claim are relative to its directory.
func claimGroups(media, other []string) *updateGroup {
	out := &updateGroup{files: map[string][]string{}}
	for _, rel := range media {
		dirparts := splitNonEmpty(filepath.ToSlash(filepath.Dir(rel)))
		name := filepath.Base(rel)
		if len(dirparts) == 0 {
			out.add(name, name, true)
			continue
		}
		cur := ""
		for i := range dirparts {
			cur = joinSlash(cur, dirparts[i])
			rest := dirparts[i+1:]
			if _, ok := out.files[cur]; ok {
				out.add(cur, joinSlash(strings.Join(rest, "/"), name), false)
				break
			} else if len(rest) == 0 {
				out.add(cur, name, false)
				break
			} else if len(rest) == 1 && cdDirectory.MatchString(rest[0]) {
				out.add(cur, joinSlash(rest[0], name), false)
				break
			}
		}
	}
	for _, rel := range other {
		dirparts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
		name := filepath.Base(rel)
		cur := ""
		for i := range dirparts {
			cur = joinSlash(cur, dirparts[i])
			if _, ok := out.files[cur]; ok {
				out.add(cur, joinSlash(strings.Join(dirparts[i+1:], "/"), name), false)
				break
			}
		}
	}
	return out
}

// groupFiles turns a folder's walked files into item groups ordered by path.
// Media files are claimed shallowest first (rra order: depth, then path) so a
// parent directory wins over a nested one.
func groupFiles(f folder, files []file, isMedia func(ext string) bool) []*group {
	sort.SliceStable(files, func(i, j int) bool {
		di, dj := strings.Count(files[i].relPath, "/"), strings.Count(files[j].relPath, "/")
		if di != dj {
			return di < dj
		}
		return files[i].path < files[j].path
	})
	byRel := make(map[string]file, len(files))
	var media, other []string
	for _, fl := range files {
		byRel[fl.relPath] = fl
		if isMedia(filepath.Ext(fl.path)) {
			media = append(media, fl.relPath)
		} else {
			other = append(other, fl.relPath)
		}
	}
	claimed := claimGroups(media, other)
	isMediaRel := make(map[string]bool, len(media))
	for _, rel := range media {
		isMediaRel[rel] = true
	}
	out := make([]*group, 0, len(claimed.keys))
	for _, key := range claimed.keys {
		g := &group{path: filepath.Join(f.path, filepath.FromSlash(key)), folder: f}
		if claimed.isSingle(key) {
			fl := byRel[key]
			g.isFile, g.files = true, []file{fl}
		}
		for _, rel := range claimed.files[key] {
			fl := byRel[joinSlash(key, rel)]
			fl.relPath = rel
			if isMediaRel[joinSlash(key, rel)] {
				g.files = append(g.files, fl)
			} else {
				g.companions = append(g.companions, fl)
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func walkVisible(root, from string, fn func(p string, d os.DirEntry) error) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if d.IsDir() && p != root && shouldIgnore(mustRel(root, p)) {
			return filepath.SkipDir
		}
		return fn(p, d)
	})
}
