package scan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/media"
)

// newLibraryFile is the libraryFiles entry of a file found on disk.
func newLibraryFile(f fileEntry) db.LibraryFile {
	ext := strings.ToLower(filepath.Ext(f.path))
	mtime, ctime, birthtime := statTimes(f.path, f.info)
	return db.LibraryFile{
		Ino: inode(f.info),
		Metadata: db.FileMetadata{Filename: filepath.Base(f.path), Ext: ext, Path: f.path, RelPath: f.rel, Size: f.info.Size(),
			MtimeMs: float64(mtime.UnixMilli()), CtimeMs: float64(ctime.UnixMilli()), BirthtimeMs: float64(birthtime.UnixMilli())},
		AddedAt: mtime.UnixMilli(), UpdatedAt: mtime.UnixMilli(), FileType: media.KindOf(ext),
	}
}

func statTimes(path string, info os.FileInfo) (mtime, ctime, birthtime time.Time) {
	return info.ModTime(), statChangeTime(info), statBirthTime(path, info)
}

// fileDiff is the outcome of reconciling an item's stored libraryFiles with
// the files found on disk (LibraryItemScanData.checkLibraryItemData).
type fileDiff struct {
	files    []db.LibraryFile // stored entries in stored order, then new ones
	removed  []db.LibraryFile
	modified []db.LibraryFile // stored entries whose stat data changed, in their new state
	added    []db.LibraryFile
	oldPath  map[string]string // path of a modified entry now -> its path before
}

func (d fileDiff) changed() bool { return len(d.removed)+len(d.modified)+len(d.added) > 0 }

// touched are the paths of the files that are new or modified.
func (d fileDiff) touched() map[string]bool {
	out := map[string]bool{}
	for _, list := range [][]db.LibraryFile{d.modified, d.added} {
		for _, f := range list {
			out[f.Metadata.Path] = true
		}
	}
	return out
}

// diffFiles keeps the stored entries (their order, addedAt and fileType),
// updates the ones whose stat data changed and appends the files that are new.
// Ebooks added to a book are supplementary.
func diffFiles(stored []db.LibraryFile, found []fileEntry, nowMs int64) fileDiff {
	fresh := make([]db.LibraryFile, len(found))
	foundKeys := make([]keyed, len(found))
	for i, f := range found {
		fresh[i] = newLibraryFile(f)
		foundKeys[i] = keyed{fresh[i].Metadata.Path, []string{fresh[i].Ino}}
	}
	storedKeys := make([]keyed, len(stored))
	for i, f := range stored {
		storedKeys[i] = keyed{f.Metadata.Path, []string{f.Ino}}
	}
	var diff fileDiff
	taken := make([]bool, len(found))
	for i, j := range pair(storedKeys, foundKeys) {
		entry := stored[i]
		if j < 0 {
			diff.removed = append(diff.removed, entry)
			continue
		}
		taken[j] = true
		scanned, oldPath := fresh[j], entry.Metadata.Path
		changed := entry.Ino != scanned.Ino || entry.Metadata != scanned.Metadata
		entry.Ino, entry.Metadata = scanned.Ino, scanned.Metadata
		if changed {
			entry.UpdatedAt = nowMs
			diff.modified = append(diff.modified, entry)
			if diff.oldPath == nil {
				diff.oldPath = map[string]string{}
			}
			diff.oldPath[entry.Metadata.Path] = oldPath
		}
		diff.files = append(diff.files, entry)
	}
	for j, entry := range fresh {
		if taken[j] {
			continue
		}
		entry.AddedAt, entry.UpdatedAt = nowMs, nowMs
		if entry.FileType == media.KindEbook {
			entry.IsSupplementary = new(true)
		}
		diff.added = append(diff.added, entry)
		diff.files = append(diff.files, entry)
	}
	return diff
}

var coverName = regexp.MustCompile(`/cover\.[^./]*$`)

// coverPath chooses the cover (BookScanner.js:106-161): a removed cover file
// clears it, a renamed one is followed, and a missing cover becomes the image
// named "cover" or the first image. It reports whether the cover changed.
func coverPath(current string, diff fileDiff) (string, bool) {
	cover := current
	if cover != "" {
		for _, f := range diff.removed {
			if f.Metadata.Path == cover && f.FileType == media.KindImage {
				if _, err := os.Stat(cover); err != nil {
					cover = ""
				}
			}
		}
	}
	for _, f := range diff.files {
		if cover != "" && diff.oldPath[f.Metadata.Path] == cover && f.Metadata.Path != cover && f.FileType == media.KindImage {
			cover = f.Metadata.Path
		}
	}
	if cover == "" {
		first := ""
		for _, f := range diff.files {
			if f.FileType != media.KindImage || f.Metadata.Path == "" {
				continue
			}
			if coverName.MatchString(filepath.ToSlash(f.Metadata.Path)) {
				first = f.Metadata.Path
				break
			}
			if first == "" {
				first = f.Metadata.Path
			}
		}
		cover = first
	}
	return cover, cover != current
}
