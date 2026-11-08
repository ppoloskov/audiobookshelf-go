package scanner

import (
	"audiobookshelf-compat/internal/filekind"
	"encoding/json"
	"path/filepath"
	"regexp"
)

// libraryFileMetadata is the stored FileMetadata of a library file
// (server/objects/metadata/FileMetadata.js). Numbers are float64 so rows with
// fractional values still decode; whole numbers encode without a fraction.
type libraryFileMetadata struct {
	Filename    string  `json:"filename"`
	Ext         string  `json:"ext"`
	Path        string  `json:"path"`
	RelPath     string  `json:"relPath"`
	Size        float64 `json:"size"`
	MtimeMs     float64 `json:"mtimeMs"`
	CtimeMs     float64 `json:"ctimeMs"`
	BirthtimeMs float64 `json:"birthtimeMs"`
}

// libraryFile is one entry of libraryItems.libraryFiles
// (server/objects/files/LibraryFile.js:22-30).
type libraryFile struct {
	Ino             string              `json:"ino"`
	Metadata        libraryFileMetadata `json:"metadata"`
	IsSupplementary *bool               `json:"isSupplementary"`
	AddedAt         float64             `json:"addedAt"`
	UpdatedAt       float64             `json:"updatedAt"`
	FileType        string              `json:"fileType"`
}

// newLibraryFile describes a file found on disk.
func newLibraryFile(root string, f file) libraryFile {
	meta := makeFileMeta(root, f)
	flat, _ := meta["metadata"].(map[string]any)
	mtime, ctime, birthtime := statTimes(f.path, f.info)
	rel, _ := flat["relPath"].(string)
	ext, _ := flat["ext"].(string)
	return libraryFile{
		Ino: inode(f.info),
		Metadata: libraryFileMetadata{
			Filename: filepath.Base(f.path), Ext: ext, Path: f.path, RelPath: rel, Size: float64(f.info.Size()),
			MtimeMs: float64(mtime.UnixMilli()), CtimeMs: float64(ctime.UnixMilli()), BirthtimeMs: float64(birthtime.UnixMilli()),
		},
		FileType: filekind.Of(ext),
	}
}

// libraryFileDiff is the outcome of reconciling an item's stored libraryFiles
// with the files found on disk (LibraryItemScanData.checkLibraryItemData,
// server/scanner/LibraryItemScanData.js:169-247).
type libraryFileDiff struct {
	files    []libraryFile // stored entries (in stored order) followed by new ones
	removed  []libraryFile
	modified []libraryFile // updated stored entries (new state)
	// oldPathOf maps a modified entry's new path to the path it had before.
	oldPathOf map[string]string
	added     []libraryFile
}

func (d libraryFileDiff) changed() bool {
	return len(d.removed)+len(d.modified)+len(d.added) > 0
}

// touchedPaths returns the paths whose entries were updated or created.
func (d libraryFileDiff) touchedPaths() map[string]bool {
	out := map[string]bool{}
	for _, list := range [][]libraryFile{d.modified, d.added} {
		for _, entry := range list {
			out[entry.Metadata.Path] = true
		}
	}
	return out
}

func parseStoredLibraryFiles(raw []byte) []libraryFile {
	var out []libraryFile
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// parseStoredObjects decodes a JSON column holding an array of objects.
func parseStoredObjects(raw []byte) []map[string]any {
	var out []map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// mergeLibraryFiles keeps stored library file entries (their order, addedAt and
// fileType), updates the ones whose stat data changed and appends files that
// are new, as upstream does. A stored file is matched to a scanned file by path
// first and by inode second.
func mergeLibraryFiles(stored []libraryFile, found []file, itemPath string, nowMs int64) libraryFileDiff {
	var diff libraryFileDiff
	consumed := make([]bool, len(found))
	fresh := make([]libraryFile, len(found))
	for i, f := range found {
		fresh[i] = newLibraryFile(itemPath, f)
	}
	for _, entry := range stored {
		match := -1
		for i := range found {
			if !consumed[i] && found[i].path == entry.Metadata.Path {
				match = i
				break
			}
		}
		if match < 0 && entry.Ino != "" {
			for i := range found {
				if !consumed[i] && fresh[i].Ino == entry.Ino {
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
		scanned := fresh[match]
		oldPath := entry.Metadata.Path
		changed := entry.Ino != scanned.Ino || entry.Metadata != scanned.Metadata
		entry.Ino, entry.Metadata = scanned.Ino, scanned.Metadata
		if changed {
			entry.UpdatedAt = float64(nowMs)
			diff.modified = append(diff.modified, entry)
			if diff.oldPathOf == nil {
				diff.oldPathOf = map[string]string{}
			}
			diff.oldPathOf[entry.Metadata.Path] = oldPath
		}
		diff.files = append(diff.files, entry)
	}
	for i := range found {
		if consumed[i] {
			continue
		}
		entry := fresh[i]
		entry.AddedAt, entry.UpdatedAt = float64(nowMs), float64(nowMs)
		if entry.FileType == "ebook" {
			supplementary := true
			entry.IsSupplementary = &supplementary
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
func pickCoverPath(files []libraryFile) string {
	first := ""
	for _, entry := range files {
		if entry.FileType != "image" || entry.Metadata.Path == "" {
			continue
		}
		if coverNamePattern.MatchString(filepath.ToSlash(entry.Metadata.Path)) {
			return entry.Metadata.Path
		}
		if first == "" {
			first = entry.Metadata.Path
		}
	}
	return first
}

func libraryFileSize(files []libraryFile) int64 {
	var size int64
	for _, entry := range files {
		size += int64(entry.Metadata.Size)
	}
	return size
}

func libraryFileHasPath(files []libraryFile, p string) bool {
	for _, entry := range files {
		if entry.Metadata.Path == p {
			return true
		}
	}
	return false
}
