package scanner

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// libraryFileDiff is the outcome of reconciling an item's stored libraryFiles
// with the files found on disk (LibraryItemScanData.checkLibraryItemData,
// server/scanner/LibraryItemScanData.js:169-247).
type libraryFileDiff struct {
	files    []map[string]any // stored entries (in stored order) followed by new ones
	removed  []map[string]any
	modified []map[string]any // updated stored entries (new state)
	// oldPathOf maps a modified entry's new path to the path it had before.
	oldPathOf map[string]string
	added     []map[string]any
}

func (d libraryFileDiff) changed() bool {
	return len(d.removed)+len(d.modified)+len(d.added) > 0
}

// modifiedOrAddedPaths returns the paths whose entries were updated or created.
func (d libraryFileDiff) touchedPaths() map[string]bool {
	out := map[string]bool{}
	for _, list := range [][]map[string]any{d.modified, d.added} {
		for _, entry := range list {
			if meta, ok := entry["metadata"].(map[string]any); ok {
				if p, ok := meta["path"].(string); ok {
					out[p] = true
				}
			}
		}
	}
	return out
}

func parseStoredLibraryFiles(raw []byte) []map[string]any {
	var out []map[string]any
	if len(raw) == 0 {
		return nil
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func numberEquals(value any, want int64) bool {
	switch v := value.(type) {
	case float64:
		return int64(v) == want && v == float64(want)
	case int64:
		return v == want
	case int:
		return int64(v) == want
	case json.Number:
		n, err := v.Int64()
		return err == nil && n == want
	}
	return false
}

// mergeLibraryFiles keeps stored library file entries (their order, addedAt and
// fileType), updates the ones whose stat data changed and appends files that
// are new, as upstream does. A stored file is matched to a scanned file by path
// first and by inode second.
func mergeLibraryFiles(stored []map[string]any, found []file, itemPath string, nowMs int64) libraryFileDiff {
	var diff libraryFileDiff
	consumed := make([]bool, len(found))
	fresh := make([]map[string]any, len(found))
	for i, f := range found {
		fresh[i] = makeFileMeta(itemPath, f)
	}
	for _, entry := range stored {
		meta, _ := entry["metadata"].(map[string]any)
		storedPath, _ := meta["path"].(string)
		storedIno, _ := entry["ino"].(string)
		match := -1
		for i := range found {
			if !consumed[i] && found[i].path == storedPath {
				match = i
				break
			}
		}
		if match < 0 && storedIno != "" {
			for i := range found {
				if !consumed[i] && fresh[i]["ino"] == storedIno {
					match = i
					break
				}
			}
		}
		if match < 0 {
			diff.removed = append(diff.removed, entry)
			continue
		}
		consumed[match] = true
		scannedMeta, _ := fresh[match]["metadata"].(map[string]any)
		changed := false
		if entry["ino"] != fresh[match]["ino"] {
			entry["ino"] = fresh[match]["ino"]
			changed = true
		}
		if meta == nil {
			meta = map[string]any{}
			entry["metadata"] = meta
		}
		for key, want := range scannedMeta {
			have, present := meta[key]
			same := present
			switch w := want.(type) {
			case int64:
				same = same && numberEquals(have, w)
			default:
				same = same && have == want
			}
			if !same {
				meta[key] = want
				changed = true
			}
		}
		if changed {
			entry["updatedAt"] = nowMs
			diff.modified = append(diff.modified, entry)
			if diff.oldPathOf == nil {
				diff.oldPathOf = map[string]string{}
			}
			newPath, _ := meta["path"].(string)
			diff.oldPathOf[newPath] = storedPath
		}
		diff.files = append(diff.files, entry)
	}
	for i := range found {
		if consumed[i] {
			continue
		}
		entry := fresh[i]
		entry["addedAt"] = nowMs
		entry["updatedAt"] = nowMs
		if entry["fileType"] == "ebook" {
			entry["isSupplementary"] = true
		}
		diff.added = append(diff.added, entry)
		diff.files = append(diff.files, entry)
	}
	return diff
}

var coverNamePattern = regexp.MustCompile(`/cover\.[^./]*$`)

// pickCoverPath chooses the cover image among library files the way
// BookScanner does for new and existing items: an image named "cover" wins,
// otherwise the first image (BookScanner.js:158-162).
func pickCoverPath(files []map[string]any) string {
	first := ""
	for _, entry := range files {
		if entry["fileType"] != "image" {
			continue
		}
		meta, _ := entry["metadata"].(map[string]any)
		p, _ := meta["path"].(string)
		if p == "" {
			continue
		}
		if coverNamePattern.MatchString(filepath.ToSlash(p)) {
			return p
		}
		if first == "" {
			first = p
		}
	}
	return first
}

func libraryFileSize(files []map[string]any) int64 {
	var size int64
	for _, entry := range files {
		meta, _ := entry["metadata"].(map[string]any)
		switch v := meta["size"].(type) {
		case float64:
			size += int64(v)
		case int64:
			size += v
		}
	}
	return size
}

func libraryFileHasPath(files []map[string]any, p string) bool {
	for _, entry := range files {
		if meta, ok := entry["metadata"].(map[string]any); ok && meta["path"] == p {
			return true
		}
	}
	return false
}

func nowMillis() int64 { return time.Now().UnixMilli() }

func lowerExt(p string) string { return strings.ToLower(filepath.Ext(p)) }
