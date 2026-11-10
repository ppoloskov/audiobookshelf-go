package scanner

import (
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/mediafiles"
)

func planFixture() planInput {
	file := libraryFile{Ino: "11", FileType: "audio", Metadata: libraryFileMetadata{Path: "/lib/Book/a.mp3", RelPath: "a.mp3", Size: 100}}
	return planInput{
		lib:    library{id: "lib", mediaType: "book"},
		g:      &group{path: "/lib/Book", folder: folder{id: "f", path: "/lib"}},
		itemID: "item", mediaID: "media", rel: "Book", now: "2024-01-01 00:00:00.000 +00:00",
		fileDiff: libraryFileDiff{files: []libraryFile{file}},
		audio:    []mediafiles.AudioFile{{Duration: 60, Metadata: mediafiles.FileMetadata{Filename: "a.mp3"}}},
		title:    "Book", author: "Writer",
		itemIno: "7", mtime: "2023-05-05 00:00:00.000 +00:00", ctime: "2023-05-05 00:00:00.000 +00:00", birthtime: "2023-05-05 00:00:00.000 +00:00",
	}
}

// storedFor is the row an earlier import of the fixture left behind.
func storedFor(in planInput) *existingItem {
	return &existingItem{id: in.itemID, mediaID: in.mediaID, path: in.g.path, relPath: in.rel, ino: in.itemIno, mediaType: in.lib.mediaType,
		mtime: in.mtime, ctime: in.ctime, birthtime: in.birthtime}
}

func TestPlanNewItem(t *testing.T) {
	in := planFixture()
	in.md = bookMetadata{Authors: []string{"A", "B"}}
	p := plan(in)
	if p.skip || !p.added || !p.rescanMedia || p.updated || p.writeItem {
		t.Fatalf("new item plan = %+v", p)
	}
	if p.duration != 60 || p.size != 100 || p.itemIno != "7" || p.mtime != in.mtime {
		t.Fatalf("new item values: duration=%v size=%v ino=%q chapters=%v", p.duration, p.size, p.itemIno, p.chapters)
	}
	if !reflect.DeepEqual(p.authors, []string{"A", "B"}) || p.filesJSON == "" {
		t.Fatalf("authors=%v files=%q", p.authors, p.filesJSON)
	}
	// Without sidecar authors the folder author is the author.
	in.md = bookMetadata{}
	if p := plan(in); !reflect.DeepEqual(p.authors, []string{"Writer"}) {
		t.Fatalf("fallback authors = %v", p.authors)
	}
	// Times fall back to the import time; a new book without audio, or an
	// item without an inode, stores nothing.
	in.mtime, in.ctime, in.birthtime = "", "", ""
	if p := plan(in); p.mtime != in.now || p.birthtime != in.now {
		t.Fatalf("time fallback = %q", p.mtime)
	}
	in.audio = nil
	if !plan(in).skip {
		t.Fatal("new book without audio must be skipped")
	}
	in = planFixture()
	in.itemIno = ""
	if !plan(in).skip {
		t.Fatal("item without inode must be skipped")
	}
}

func TestPlanUnchangedItemWritesNothing(t *testing.T) {
	in := planFixture()
	in.old = storedFor(in)
	p := plan(in)
	if p.skip || p.added || p.rescanMedia || p.writeItem || p.itemChanged || p.updated || p.missing || p.coverChanged {
		t.Fatalf("unchanged plan = %+v", p)
	}
	in.force = true
	p = plan(in)
	if !p.rescanMedia || !p.writeItem || p.updated {
		t.Fatalf("forced plan = rescan %v write %v updated %v", p.rescanMedia, p.writeItem, p.updated)
	}
}

func TestPlanChangedAudioAndPathChange(t *testing.T) {
	in := planFixture()
	in.old = storedFor(in)
	in.fileDiff.modified = in.fileDiff.files
	p := plan(in)
	if !p.rescanMedia || !p.itemChanged || !p.writeItem || !p.updated || p.coverChanged {
		t.Fatalf("changed audio plan = %+v", p)
	}
	in = planFixture()
	in.old = storedFor(in)
	in.old.path = "/lib/Old"
	if p := plan(in); !p.rescanMedia || !p.itemChanged || !p.updated {
		t.Fatalf("moved item plan = %+v", p)
	}
	in = planFixture()
	in.old = storedFor(in)
	in.old.ino = "other"
	if p := plan(in); p.rescanMedia || !p.itemChanged || !p.writeItem {
		t.Fatalf("inode change must update the row but not the media: %+v", p)
	}
}

func TestPlanChangedMetadataSidecar(t *testing.T) {
	in := planFixture()
	in.old = storedFor(in)
	side := libraryFile{FileType: "metadata", Metadata: libraryFileMetadata{Path: "/lib/Book/metadata.json", RelPath: "metadata.json", Size: 10}}
	in.fileDiff.files = append(in.fileDiff.files, side)
	in.fileDiff.added = []libraryFile{side}
	in.md = bookMetadata{Title: "From Sidecar", Narrators: []string{"N"}}
	in.title = "From Sidecar"
	p := plan(in)
	if !p.rescanMedia || !p.itemChanged || !p.updated || p.coverChanged || p.size != 110 {
		t.Fatalf("sidecar plan = %+v", p)
	}
	if p.title != "From Sidecar" || p.md.Narrators[0] != "N" {
		t.Fatalf("sidecar metadata lost: %q %v", p.title, p.md.Narrators)
	}
	// A new cover image changes the cover without touching the audio.
	img := libraryFile{FileType: "image", Metadata: libraryFileMetadata{Path: "/lib/Book/cover.jpg", RelPath: "cover.jpg"}}
	in.fileDiff.files = append(in.fileDiff.files, img)
	in.fileDiff.added = append(in.fileDiff.added, img)
	if p := plan(in); p.coverPath != "/lib/Book/cover.jpg" || !p.coverChanged {
		t.Fatalf("cover = %q changed %v", p.coverPath, p.coverChanged)
	}
}

func TestPlanMissingThenFound(t *testing.T) {
	in := planFixture()
	in.old = storedFor(in)
	in.old.missing = true
	p := plan(in)
	if p.missing || !p.itemChanged || !p.writeItem || !p.updated || p.rescanMedia {
		t.Fatalf("found plan = %+v", p)
	}
	// And the reverse: no audio and no ebook marks an existing book missing;
	// an ebook keeps it present.
	in = planFixture()
	in.old = storedFor(in)
	in.audio = nil
	if p := plan(in); !p.missing || !p.itemChanged || !p.updated {
		t.Fatalf("missing plan = %+v", p)
	}
	in.hasEbook = true
	if p := plan(in); p.missing || p.itemChanged {
		t.Fatalf("ebook book plan = %+v", p)
	}
}

func TestPlanPodcast(t *testing.T) {
	in := planFixture()
	in.lib.mediaType = "podcast"
	in.g.path = "/lib/My Show"
	in.audio = []mediafiles.AudioFile{
		{Metadata: mediafiles.FileMetadata{Filename: "e1.mp3"}},
		{Metadata: mediafiles.FileMetadata{Filename: "e2.mp3"}},
	}
	in.md = bookMetadata{} // podcasts never read book metadata
	p := plan(in)
	if p.skip || !p.added || p.title != "My Show" || p.author != "" || p.series != "" {
		t.Fatalf("podcast plan = %+v", p)
	}
	if len(p.episodes) != 2 || p.episodes[0].title != "e1" || p.episodes[0].filename != "e1" || p.episodes[1].title != "e2" || p.episodes[1].offset != 1 {
		t.Fatalf("episodes = %+v", p.episodes)
	}
	// A new podcast folder with no audio is still an item; podcasts are never "missing" by plan.
	in.audio = nil
	if p := plan(in); p.skip || len(p.episodes) != 0 {
		t.Fatalf("empty podcast plan = %+v", p)
	}
	in.old = storedFor(in)
	in.old.mediaType = "podcast"
	if p := plan(in); p.missing || p.coverPath != "" {
		t.Fatalf("existing empty podcast plan = %+v", p)
	}
}
