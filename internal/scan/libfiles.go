package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"monstrobook/internal/db"
	"monstrobook/internal/media"
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

func statDBTimes(path string, info os.FileInfo) (mtime, ctime, birthtime db.Time) {
	m, c, b := statTimes(path, info)
	return db.Time{Time: m.UTC()}, db.Time{Time: c.UTC()}, db.Time{Time: b.UTC()}
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

// newEbookFile builds books.ebookFile from a library file. Upstream writes two
// shapes (BookScanner.js:187-205): withFileType includes fileType and
// isSupplementary:null on initial pick; updates use EBookFile.toJSON without them.
func newEbookFile(lf db.LibraryFile, withFileType bool) *db.EbookFile {
	ef := &db.EbookFile{
		Ino:         lf.Ino,
		Metadata:    lf.Metadata,
		EbookFormat: strings.TrimPrefix(strings.ToLower(lf.Metadata.Ext), "."),
		AddedAt:     lf.AddedAt,
		UpdatedAt:   lf.UpdatedAt,
	}
	if withFileType {
		ef.FileType = media.KindEbook
		ef.IsSupplementary = json.RawMessage("null")
	}
	return ef
}

// pickEbookFile chooses the book's primary ebook, preferring .epub (BookScanner.js:199,453).
func pickEbookFile(files []db.LibraryFile) (db.LibraryFile, bool) {
	var first db.LibraryFile
	hasFirst := false
	for _, f := range files {
		if f.FileType != media.KindEbook {
			continue
		}
		if strings.EqualFold(f.Metadata.Ext, ".epub") {
			return f, true
		}
		if !hasFirst {
			first, hasFirst = f, true
		}
	}
	return first, hasFirst
}

// ebookRemoved reports whether eb is absent from files by both path and inode
// (LibraryItemScanData.checkEbookFileRemoved).
func ebookRemoved(eb *db.EbookFile, files []db.LibraryFile) bool {
	for _, f := range files {
		if f.FileType != media.KindEbook {
			continue
		}
		if f.Metadata.Path == eb.Metadata.Path || (eb.Ino != "" && f.Ino == eb.Ino) {
			return false
		}
	}
	return true
}

// syncSupplementaryEbooks updates isSupplementary on ebook entries in files:
// false for the primary ebook, true for all others. It reports whether any changed.
func syncSupplementaryEbooks(files []db.LibraryFile, primary *db.EbookFile) bool {
	changed := false
	for i := range files {
		if files[i].FileType != media.KindEbook {
			continue
		}
		want := primary == nil || files[i].Ino != primary.Ino
		if files[i].IsSupplementary == nil || *files[i].IsSupplementary != want {
			files[i].IsSupplementary = &want
			changed = true
		}
	}
	return changed
}
