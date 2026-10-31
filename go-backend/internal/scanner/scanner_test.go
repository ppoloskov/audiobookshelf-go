package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/catalog"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
	_ "modernc.org/sqlite"
)

var scannerTestMetadataRoot string

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "abs-scanner-test-metadata-")
	if err != nil {
		panic(err)
	}
	scannerTestMetadataRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}

func newScannerForTest(db *sql.DB, cfg config.Config, emit EmitFunc) (*Scanner, error) {
	if cfg.MetadataPath == "" {
		cfg.MetadataPath = scannerTestMetadataRoot
	}
	return New(db, cfg, emit, catalog.New(db, cfg, nil))
}

// These DDL statements are copied from the pinned v2.37.2 baseline schema.sql.
func scannerTestDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "scanner.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	statements := []string{
		`CREATE TABLE libraries (id UUID PRIMARY KEY, name VARCHAR(255), displayOrder INTEGER, icon VARCHAR(255), mediaType VARCHAR(255), provider VARCHAR(255), lastScan DATETIME, lastScanVersion VARCHAR(255), settings JSON, extraData JSON, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL)`,
		`CREATE TABLE libraryFolders (id UUID PRIMARY KEY, path VARCHAR(255), createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, libraryId UUID)`,
		`CREATE TABLE libraryItems (id UUID PRIMARY KEY, ino VARCHAR(255), path VARCHAR(255), relPath VARCHAR(255), mediaId UUID, mediaType VARCHAR(255), isFile TINYINT(1), isMissing TINYINT(1), isInvalid TINYINT(1), mtime DATETIME, ctime DATETIME, birthtime DATETIME, size BIGINT, lastScan DATETIME, lastScanVersion VARCHAR(255), libraryFiles JSON, extraData JSON, title VARCHAR(255), titleIgnorePrefix VARCHAR(255), authorNamesFirstLast VARCHAR(255), authorNamesLastFirst VARCHAR(255), createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, libraryId UUID, libraryFolderId UUID)`,
		`CREATE TABLE books (id UUID PRIMARY KEY, title VARCHAR(255), titleIgnorePrefix VARCHAR(255), subtitle VARCHAR(255), publishedYear VARCHAR(255), publishedDate VARCHAR(255), publisher VARCHAR(255), description TEXT, isbn VARCHAR(255), asin VARCHAR(255), language VARCHAR(255), explicit TINYINT(1), abridged TINYINT(1), coverPath VARCHAR(255), duration FLOAT, narrators JSON, audioFiles JSON, ebookFile JSON, chapters JSON, tags JSON, genres JSON, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL)`,
		`CREATE TABLE authors (id UUID PRIMARY KEY, name VARCHAR(255), lastFirst VARCHAR(255), asin VARCHAR(255), description TEXT, imagePath VARCHAR(255), createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, libraryId UUID)`,
		`CREATE TABLE bookAuthors (id UUID PRIMARY KEY, createdAt DATETIME NOT NULL, bookId UUID, authorId UUID, UNIQUE(bookId, authorId))`,
		`CREATE TABLE series (id UUID PRIMARY KEY, name VARCHAR(255), nameIgnorePrefix VARCHAR(255), description TEXT, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, libraryId UUID)`,
		`CREATE TABLE bookSeries (id UUID PRIMARY KEY, sequence VARCHAR(255), createdAt DATETIME NOT NULL, bookId UUID, seriesId UUID, UNIQUE(bookId, seriesId))`,
		`CREATE TABLE mediaProgresses (id UUID PRIMARY KEY, mediaItemId UUID, mediaItemType VARCHAR(255), duration FLOAT, currentTime FLOAT, isFinished TINYINT(1), createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL, userId UUID)`,
		`CREATE TABLE settings (key VARCHAR(255) PRIMARY KEY, value JSON, createdAt DATETIME NOT NULL, updatedAt DATETIME NOT NULL)`,
		`INSERT INTO libraries(id,name,mediaType,settings,createdAt,updatedAt) VALUES('lib-1','Books','book','{}','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-1',?,'lib-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,currentTime,isFinished,createdAt,updatedAt,userId) VALUES('progress-1','pending','book',42,0,'2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00','user-1')`,
	}
	for i, statement := range statements {
		if strings.Contains(statement, "INSERT INTO libraryFolders") {
			_, err = db.Exec(statement, root)
		} else {
			_, err = db.Exec(statement)
		}
		if err != nil {
			_ = db.Close()
			t.Fatalf("baseline DDL %d: %v", i, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAudioMetadataSourceMapping(t *testing.T) {
	m := bookMetadata{Title: "Folder"}
	applyAudioMetadata(map[string]any{"tagTitle": "Tagged", "tagArtist": "A Writer", "tagGenre": "Mystery/Fiction"}, &m)
	if m.Title != "Tagged" || strings.Join(m.Authors, ",") != "A Writer" || strings.Join(m.Genres, ",") != "Mystery,Fiction" {
		t.Fatalf("mapped audio metadata = %+v", m)
	}
}

func TestReaderAndDescriptionSourceRules(t *testing.T) {
	d := t.TempDir()
	for name, body := range map[string]string{"desc.txt": "  a description \n", "reader.txt": "First Reader\nIgnored Reader\n"} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	infos := []file{}
	for _, n := range []string{"desc.txt", "reader.txt"} {
		p := filepath.Join(d, n)
		st, _ := os.Stat(p)
		infos = append(infos, file{path: p, info: st})
	}
	m := bookMetadata{}
	applyTextFiles(&group{companions: infos}, &m)
	if m.Description != "a description" || strings.Join(m.Narrators, ",") != "First Reader" {
		t.Fatalf("text source metadata = %+v", m)
	}
}

func TestFolderExtractorsSourcePatterns(t *testing.T) {
	if base, asin := extractASIN("A [B0015T963C]"); base != "A" || asin != "B0015T963C" {
		t.Fatalf("ASIN = %q,%q", base, asin)
	}
	if title, seq := extractSequence("Book 2 - Title"); title != "Title" || seq != "2" {
		t.Fatalf("sequence = %q,%q", title, seq)
	}
}

func TestFolderStructureExtractorOrderAndOptionalSubtitle(t *testing.T) {
	tests := []struct{ path, title, author, series, seq string }{
		{"Author/Series/Vol. 3 Title - Part", "Title - Part", "Author", "Series", "3"},
		{"Author/Series/(2020) - Title - Extra", "Title - Extra", "Author", "Series", ""},
	}
	for _, tc := range tests {
		title, author, series, seq := bookPathMetadata(filepath.FromSlash(tc.path), "", false)
		if title != tc.title || author != tc.author || series != tc.series || seq != tc.seq {
			t.Fatalf("bookPathMetadata(%q)=%q/%q/%q/%q", tc.path, title, author, series, seq)
		}
	}
	title, _, _, _ := bookPathMetadata("Author/Series/Title - Subtitle", "", true)
	if title != "Title" {
		t.Fatalf("subtitle-enabled title=%q", title)
	}
}

func TestMetadataSourcesFollowConfiguredPrecedence(t *testing.T) {
	dir := t.TempDir()
	nfo := filepath.Join(dir, "book.nfo")
	if err := os.WriteFile(nfo, []byte("title: NFO Title\n"), 0644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(nfo)
	g := &group{files: []file{{path: filepath.Join(dir, "audio.mp3")}}, companions: []file{{path: nfo, info: st}}}
	order := []string{"folderStructure", "nfoFile", "audioMetatags"}
	got := mergeBookMetadata("Folder Title", "", "", "", g, order, map[string]any{"tagTitle": "Audio Title"})
	if got.Title != "Audio Title" {
		t.Fatalf("later audio source title=%q", got.Title)
	}
	order = []string{"audioMetatags", "nfoFile", "folderStructure"}
	got = mergeBookMetadata("Folder Title", "", "", "", g, order, map[string]any{"tagTitle": "Audio Title"})
	if got.Title != "NFO Title" {
		t.Fatalf("later NFO source title=%q", got.Title)
	}
}

func TestExternalABSMetadataFallbackOnlyWithoutReadableInItemFile(t *testing.T) {
	root := t.TempDir()
	item := filepath.Join(root, "Book")
	if err := os.MkdirAll(item, 0755); err != nil {
		t.Fatal(err)
	}
	metadataRoot := t.TempDir()
	external := filepath.Join(metadataRoot, "items", "existing-id")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "metadata.json"), []byte(`{"title":"External title"}`), 0644); err != nil {
		t.Fatal(err)
	}
	g := &group{path: item}
	got, ok := readABSMetadata(g, "existing-id", metadataRoot)
	if !ok || got.Title != "External title" {
		t.Fatalf("external metadata=%+v readable=%v", got, ok)
	}
	inside := filepath.Join(item, "metadata.json")
	if err := os.WriteFile(inside, []byte(`{"title":"In-item title"}`), 0644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(inside)
	g.companions = []file{{path: inside, info: st}}
	got, ok = readABSMetadata(g, "existing-id", metadataRoot)
	if !ok || got.Title != "In-item title" {
		t.Fatalf("in-item metadata=%+v readable=%v", got, ok)
	}
}

func TestABSMetadataEmptyStringOverwritesButEmptyArraysDoNot(t *testing.T) {
	var src bookMetadata
	parseAbsMetadata([]byte(`{"title":"","authors":[],"genres":[],"tags":[],"chapters":[]}`), &src)
	dst := bookMetadata{Title: "Existing", Authors: []string{"A"}, Genres: []string{"G"}}
	applyABSMetadata(src, &dst)
	if dst.Title != "" || strings.Join(dst.Authors, ",") != "A" || strings.Join(dst.Genres, ",") != "G" {
		t.Fatalf("ABS merge=%+v", dst)
	}
}

func TestPrecedenceChangeForcesAndPersistsSnapshot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("FFPROBE_PATH")
	_ = os.Setenv("FFPROBE_PATH", ffprobe)
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", old) })
	if err := os.WriteFile(filepath.Join(dir, "track.mp3"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	defaultOrder := `{"metadataPrecedence":["folderStructure","audioMetatags","nfoFile","txtFiles","opfFile","absMetadata"]}`
	defaultExtra := `{"lastScanMetadataPrecedence":["folderStructure","audioMetatags","nfoFile","txtFiles","opfFile","absMetadata"]}`
	if _, err := db.Exec(`UPDATE libraries SET settings=?,extraData=? WHERE id='lib-1'`, defaultOrder, defaultExtra); err != nil {
		t.Fatal(err)
	}
	s, _ := newScannerForTest(db, config.Config{}, nil)
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE books SET title='Persisted title',updatedAt='2020-01-01'`); err != nil {
		t.Fatal(err)
	}
	changed := `{"metadataPrecedence":["audioMetatags","folderStructure","nfoFile","txtFiles","opfFile","absMetadata"]}`
	if _, err := db.Exec(`UPDATE libraries SET settings=? WHERE id='lib-1'`, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var updated, title string
	if err := db.QueryRow(`SELECT updatedAt,title FROM books`).Scan(&updated, &title); err != nil {
		t.Fatal(err)
	}
	if updated == "2020-01-01" {
		t.Fatal("precedence change did not force media update")
	}
	var extra string
	if err := db.QueryRow(`SELECT extraData FROM libraries WHERE id='lib-1'`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(extra), &data)
	if strings.Join(anyStringSlice(data["lastScanMetadataPrecedence"]), ",") != strings.Join([]string{"audioMetatags", "folderStructure", "nfoFile", "txtFiles", "opfFile", "absMetadata"}, ",") {
		t.Fatalf("persisted snapshot=%s", extra)
	}
}

// LibraryScanner.js:171-220 matches items by path then inode, counts an
// item update when LibraryItemScanData reports changed metadata, and does not
// rewrite unchanged items (LibraryItemScanData.js:264-280).
func TestScanRenameCompanionChangeAndUnchangedBookkeeping(t *testing.T) {
	root := t.TempDir()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	oldProbe := os.Getenv("FFPROBE_PATH")
	_ = os.Setenv("FFPROBE_PATH", ffprobe)
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", oldProbe) })
	dir := filepath.Join(root, "Old Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "track.mp3"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	companion := filepath.Join(dir, "desc.txt")
	if err := os.WriteFile(companion, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Scan(context.Background(), "lib-1")
	if err != nil || first.Added != 1 {
		t.Fatalf("initial scan=%+v err=%v", first, err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM libraryItems`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	stamp := func() string {
		var v string
		if e := db.QueryRow(`SELECT lastScan FROM libraryItems WHERE id=?`, id).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	prior := stamp()
	newDir := filepath.Join(root, "Renamed Book")
	if err := os.Rename(dir, newDir); err != nil {
		t.Fatal(err)
	}
	moved, err := s.Scan(context.Background(), "lib-1")
	if err != nil || moved.Added != 0 || moved.Updated != 1 {
		t.Fatalf("rename scan=%+v err=%v", moved, err)
	}
	var gotID, path, rel string
	var count int
	if err := db.QueryRow(`SELECT id,path,relPath FROM libraryItems`).Scan(&gotID, &path, &rel); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM libraryItems`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if gotID != id || path != newDir || rel != "Renamed Book" || count != 1 {
		t.Fatalf("renamed row=%s %s %s count=%d", gotID, path, rel, count)
	}
	prior = stamp()
	if err := os.WriteFile(filepath.Join(newDir, "desc.txt"), []byte("changed description"), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Scan(context.Background(), "lib-1")
	if err != nil || changed.Updated != 1 {
		t.Fatalf("companion update=%+v err=%v", changed, err)
	}
	var version string
	if err := db.QueryRow(`SELECT lastScanVersion FROM libraryItems WHERE id=?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if stamp() == prior || version != "2.37.2" {
		t.Fatalf("changed scan bookkeeping lastScan=%q prior=%q version=%q", stamp(), prior, version)
	}
	prior = stamp()
	unchanged, err := s.Scan(context.Background(), "lib-1")
	if err != nil || unchanged.Updated != 0 {
		t.Fatalf("unchanged scan=%+v err=%v", unchanged, err)
	}
	if stamp() != prior {
		t.Fatalf("no-op scan rewrote lastScan: %q -> %q", prior, stamp())
	}
}

// LibraryScanner.js:182-199 counts missing rows on every scan but only saves
// the newly-missing row; podcast folders still present are explicitly exempt.
func TestScanMissingIsStableAndExistingPodcastFolderIsNotMissing(t *testing.T) {
	root := t.TempDir()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("FFPROBE_PATH")
	_ = os.Setenv("FFPROBE_PATH", ffprobe)
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", old) })
	dir := filepath.Join(root, "Missing Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM libraryItems`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	first, err := s.Scan(context.Background(), "lib-1")
	if err != nil || first.Missing != 1 {
		t.Fatalf("first missing=%+v err=%v", first, err)
	}
	var missing int
	var last string
	if err := db.QueryRow(`SELECT isMissing,lastScan FROM libraryItems WHERE id=?`, id).Scan(&missing, &last); err != nil {
		t.Fatal(err)
	}
	if missing != 1 || last == "" {
		t.Fatalf("missing row state=%d lastScan=%q", missing, last)
	}
	savedLast := last
	second, err := s.Scan(context.Background(), "lib-1")
	if err != nil || second.Missing != 1 {
		t.Fatalf("second missing=%+v err=%v", second, err)
	}
	if err := db.QueryRow(`SELECT isMissing,lastScan FROM libraryItems WHERE id=?`, id).Scan(&missing, &last); err != nil {
		t.Fatal(err)
	}
	if missing != 1 || last != savedLast {
		t.Fatalf("repeat missing state=%d lastScan=%q want %q", missing, last, savedLast)
	}

	podRoot := t.TempDir()
	show := filepath.Join(podRoot, "Show")
	if err := os.MkdirAll(show, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE libraries SET mediaType='podcast' WHERE id='lib-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE libraryFolders SET path=? WHERE id='folder-1'`, podRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO libraryItems(id,path,relPath,mediaId,mediaType,isFile,isMissing,libraryId,libraryFolderId,createdAt,updatedAt) VALUES('pod-item',?,'Show','pod-media','podcast',0,0,'lib-1','folder-1','2020-01-01','2020-01-01')`, show); err != nil {
		t.Fatal(err)
	}
	// The removed book row belongs to a separate library for this podcast check.
	if _, err := db.Exec(`UPDATE libraryItems SET libraryId='other' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	res, err := s.Scan(context.Background(), "lib-1")
	if err != nil || res.Missing != 0 {
		t.Fatalf("existing podcast folder scan=%+v err=%v", res, err)
	}
	if err := db.QueryRow(`SELECT isMissing FROM libraryItems WHERE id='pod-item'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("existing podcast folder marked missing=%d", missing)
	}
}

// BookScanner.js:85-147 reconciles audio entries and recomputes duration;
// AudioFile.js:171-196 retains existing entry timestamps when unchanged.
func TestAudioEntryIdentityPreservedOnCompanionUpdateAndRemoval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("FFPROBE_PATH")
	_ = os.Setenv("FFPROBE_PATH", ffprobe)
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", old) })
	for _, name := range []string{"a.mp3", "b.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := scannerTestDB(t, root)
	s, _ := newScannerForTest(db, config.Config{}, nil)
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var mediaID string
	if err := db.QueryRow(`SELECT mediaId FROM libraryItems`).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	var initial string
	_ = db.QueryRow(`SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&initial)
	var before []map[string]any
	_ = json.Unmarshal([]byte(initial), &before)
	if len(before) != 2 {
		t.Fatalf("initial audio=%s", initial)
	}
	byName := map[string]map[string]any{}
	for _, a := range before {
		m := a["metadata"].(map[string]any)
		byName[m["filename"].(string)] = a
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("new content with different size"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "b.mp3")); err != nil {
		t.Fatal(err)
	}
	res, err := s.Scan(context.Background(), "lib-1")
	if err != nil || res.Updated != 1 {
		t.Fatalf("rescan=%+v err=%v", res, err)
	}
	var updated string
	_ = db.QueryRow(`SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&updated)
	var after []map[string]any
	_ = json.Unmarshal([]byte(updated), &after)
	if len(after) != 1 {
		t.Fatalf("removed audio retained: %s", updated)
	}
	got := after[0]
	if got["ino"] != byName["a.mp3"]["ino"] || got["addedAt"] != byName["a.mp3"]["addedAt"] {
		t.Fatalf("changed entry identity/timestamp not preserved: before=%v after=%v", byName["a.mp3"], got)
	}
	var dur float64
	if err := db.QueryRow(`SELECT duration FROM books WHERE id=?`, mediaID).Scan(&dur); err != nil || dur < .99 || dur > 1.01 {
		t.Fatalf("recomputed duration=%f err=%v", dur, err)
	}
}

func TestScanPersistsConfiguredTitleIgnorePrefixes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"The Martian", "Quiet Story"} {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "track.mp3"), []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("FFPROBE_PATH")
	_ = os.Setenv("FFPROBE_PATH", ffprobe)
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", old) })
	db := scannerTestDB(t, root)
	if _, err := db.Exec(`INSERT INTO settings(key,value,createdAt,updatedAt) VALUES('server-settings',?,'2020','2020')`, `{"sortingPrefixes":["the","a"]}`); err != nil {
		t.Fatal(err)
	}
	s, _ := newScannerForTest(db, config.Config{}, nil)
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT b.title,b.titleIgnorePrefix,li.titleIgnorePrefix FROM books b JOIN libraryItems li ON li.mediaId=b.id ORDER BY b.title`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][2]string{}
	for rows.Next() {
		var title, bp, ip string
		if err := rows.Scan(&title, &bp, &ip); err != nil {
			t.Fatal(err)
		}
		got[title] = [2]string{bp, ip}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["The Martian"] != [2]string{"Martian", "Martian"} || got["Quiet Story"] != [2]string{"Quiet Story", "Quiet Story"} {
		t.Fatalf("title prefix values=%v", got)
	}
}

func anyStringSlice(v any) []string {
	values, _ := v.([]any)
	out := make([]string, 0, len(values))
	for _, x := range values {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestScanImportsFFmpegMediaAndSidecarsIdempotently(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("test requires FFmpeg to exercise a generated audio file")
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Fatal("test requires ffprobe to inspect generated audio")
	}
	root := t.TempDir()
	itemDir := filepath.Join(root, "Author", "Series", "02 - Disk Book")
	if err := os.MkdirAll(itemDir, 0755); err != nil {
		t.Fatal(err)
	}
	audioPath := filepath.Join(itemDir, "01 - Track.mp3")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1.25", "-metadata", "title=Embedded Title", "-metadata", "artist=Embedded Author", "-q:a", "9", audioPath)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate test audio: %v: %s", e, output)
	}
	metadata := map[string]any{"title": "Sidecar Title", "authors": []string{"Sidecar Author"}, "genres": []string{"Mystery"}, "chapters": []map[string]any{{"start": 0, "end": 1.25, "title": "Opening"}}}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(itemDir, "metadata.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "metadata.opf"), []byte(`<package><metadata><dc:title xmlns:dc="urn:dc">OPF Title</dc:title><dc:publisher xmlns:dc="urn:dc">Test Publisher</dc:publisher></metadata></package>`), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	metadataRoot := filepath.Join(t.TempDir(), "metadata-root")
	cfg := config.Config{MetadataPath: metadataRoot}
	var events []struct {
		scope, name string
		payload     any
	}
	s, err := newScannerForTest(db, cfg, func(scope, event string, payload any) {
		events = append(events, struct {
			scope, name string
			payload     any
		}{scope, event, payload})
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Scan(context.Background(), "lib-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 1 || first.Updated != 0 {
		t.Fatalf("first scan = %+v, want one addition", first)
	}
	// First-scan authors are announced by author_added; BookScanner.js:225-245
	// does not add them to the count-changed set.
	if len(events) != 5 || events[0].name != "task_started" || events[1].scope != "*" || events[1].name != "author_added" || events[2].scope != "*" || events[2].name != "series_added" || events[3].name != "items_added" || events[3].scope != "*" || events[4].name != "task_finished" {
		t.Fatalf("source-scoped metadata events = %+v", events)
	}
	addedItems, ok := events[3].payload.([]map[string]any)
	if !ok || len(addedItems) != 1 || addedItems[0]["id"] == nil || addedItems[0]["path"] == nil || addedItems[0]["mediaType"] != "book" {
		t.Fatalf("expanded items_added payload = %#v", events[3].payload)
	}
	mediaRaw, _ := json.Marshal(addedItems[0]["media"])
	var media map[string]any
	_ = json.Unmarshal(mediaRaw, &media)
	md, _ := media["metadata"].(map[string]any)
	if md["title"] != "Sidecar Title" {
		t.Fatalf("expanded event metadata title = %#v", md["title"])
	}
	var itemID, mediaID, itemTitle, authorNames string
	if err := db.QueryRow(`SELECT id,mediaId,title,authorNamesFirstLast FROM libraryItems WHERE libraryId='lib-1'`).Scan(&itemID, &mediaID, &itemTitle, &authorNames); err != nil {
		t.Fatal(err)
	}
	if itemTitle != "Sidecar Title" || authorNames != "Sidecar Author" {
		t.Fatalf("sidecar precedence/title/author = %q/%q", itemTitle, authorNames)
	}
	defaultSidecarPath := filepath.Join(metadataRoot, "items", itemID, "metadata.json")
	defaultSidecar, err := os.ReadFile(defaultSidecarPath)
	if err != nil {
		t.Fatalf("default scan sidecar %s: %v", defaultSidecarPath, err)
	}
	var defaultSidecarJSON map[string]any
	if err := json.Unmarshal(defaultSidecar, &defaultSidecarJSON); err != nil || defaultSidecarJSON["title"] != "Sidecar Title" {
		t.Fatalf("default sidecar payload=%s err=%v", defaultSidecar, err)
	}
	itemRes := httptest.NewRecorder()
	itemReq := httptest.NewRequest(http.MethodPost, "/api/items/"+itemID+"/scan", nil)
	if !s.Handle(itemRes, itemReq, store.User{Type: "root"}) {
		t.Fatal("item scan route was not handled")
	}
	var itemScanResponse map[string]string
	if err := json.Unmarshal(itemRes.Body.Bytes(), &itemScanResponse); err != nil {
		t.Fatalf("item scan response %q: %v", itemRes.Body.String(), err)
	}
	if itemScanResponse["result"] != "UPTODATE" {
		t.Fatalf("item scan response = %v", itemScanResponse)
	}
	var title, publisher string
	var duration float64
	var chaptersJSON string
	var audioJSON string
	var genres string
	if err := db.QueryRow(`SELECT title,publisher,duration,chapters,audioFiles,genres FROM books WHERE id=?`, mediaID).Scan(&title, &publisher, &duration, &chaptersJSON, &audioJSON, &genres); err != nil {
		t.Fatal(err)
	}
	if title != "Sidecar Title" || publisher != "Test Publisher" || duration < 1.0 || duration > 1.5 {
		t.Fatalf("book metadata = title %q publisher %q duration %.3f", title, publisher, duration)
	}
	var chapters []map[string]any
	if err := json.Unmarshal([]byte(chaptersJSON), &chapters); err != nil || len(chapters) != 1 || chapters[0]["title"] != "Opening" {
		t.Fatalf("chapters %s (%v)", chaptersJSON, err)
	}
	var audio []map[string]any
	if err := json.Unmarshal([]byte(audioJSON), &audio); err != nil || len(audio) != 1 {
		t.Fatalf("audio files %s (%v)", audioJSON, err)
	}
	if len(audio) > 0 {
		tags, _ := audio[0]["metaTags"].(map[string]any)
		if tags["tagTitle"] != "Embedded Title" {
			t.Fatalf("ffprobe tags not stored: %s", audioJSON)
		}
	}
	if genres != "[\"Mystery\"]" {
		t.Fatalf("metadata.json genres = %s", genres)
	}
	if _, err := db.Exec(`UPDATE mediaProgresses SET mediaItemId=? WHERE id='progress-1'`, mediaID); err != nil {
		t.Fatal(err)
	}
	second, err := s.Scan(context.Background(), "lib-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Added != 0 || second.Updated != 0 || second.Missing != 0 {
		t.Fatalf("repeat scan = %+v, want no changes", second)
	}
	var itemIDAgain, mediaIDAgain string
	if err := db.QueryRow(`SELECT id,mediaId FROM libraryItems WHERE libraryId='lib-1'`).Scan(&itemIDAgain, &mediaIDAgain); err != nil {
		t.Fatal(err)
	}
	if itemIDAgain != itemID || mediaIDAgain != mediaID {
		t.Fatalf("rescan changed ids: item %s->%s media %s->%s", itemID, itemIDAgain, mediaID, mediaIDAgain)
	}
	var progressMedia string
	var currentTime float64
	if err := db.QueryRow(`SELECT mediaItemId,currentTime FROM mediaProgresses WHERE id='progress-1'`).Scan(&progressMedia, &currentTime); err != nil {
		t.Fatal(err)
	}
	if progressMedia != mediaID || currentTime != 42 {
		t.Fatalf("progress changed during scan: %s %.1f", progressMedia, currentTime)
	}
	metadata["title"] = "Updated Sidecar"
	updatedMetadata, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(itemDir, "metadata.json"), updatedMetadata, 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Scan(context.Background(), "lib-1")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Updated != 1 {
		t.Fatalf("scan after sidecar change = %+v", changed)
	}
	if err := db.QueryRow(`SELECT title FROM books WHERE id=?`, mediaID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Updated Sidecar" {
		t.Fatalf("metadata update title = %q", title)
	}
	if _, err := db.Exec(`UPDATE settings SET value=? WHERE key='server-settings'`, `{"storeMetadataWithItem":true,"metadataFileFormat":"json"}`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audioPath, append(mustReadFile(t, audioPath), []byte(" ")...), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	inItemSidecar, err := os.ReadFile(filepath.Join(itemDir, "metadata.json"))
	if err != nil {
		t.Fatalf("in-item sidecar was not written: %v", err)
	}
	var inItemJSON map[string]any
	if err := json.Unmarshal(inItemSidecar, &inItemJSON); err != nil || inItemJSON["title"] != "Updated Sidecar" {
		t.Fatalf("in-item sidecar=%s err=%v", inItemSidecar, err)
	}
	var newBookUpdated string
	var newSize int64
	var filesJSON string
	if err := db.QueryRow(`SELECT updatedAt,size,libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&newBookUpdated, &newSize, &filesJSON); err != nil {
		t.Fatal(err)
	}
	if newBookUpdated == "" || newSize <= 0 || !strings.Contains(filesJSON, "metadata.json") {
		t.Fatalf("in-item rescan did not refresh bookkeeping: updatedAt=%q size=%d files=%s", newBookUpdated, newSize, filesJSON)
	}
	if err := os.Remove(audioPath); err != nil {
		t.Fatal(err)
	}
	third, err := s.Scan(context.Background(), "lib-1")
	if err != nil {
		t.Fatal(err)
	}
	if third.Missing != 1 {
		t.Fatalf("scan after media removal = %+v", third)
	}
	if _, err := db.Exec(`INSERT INTO settings(key,value,createdAt,updatedAt) VALUES('server-settings',?,'2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`, `{"id":"server-settings","storeMetadataWithItem":true,"metadataFileFormat":"json"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMetadata(context.Background(), itemID); err != nil {
		t.Fatalf("write item metadata: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(itemDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar map[string]any
	if err := json.Unmarshal(written, &sidecar); err != nil {
		t.Fatal(err)
	}
	if sidecar["title"] != "Updated Sidecar" || len(anySlice(sidecar["authors"])) != 1 || len(anySlice(sidecar["series"])) != 1 {
		t.Fatalf("saved metadata sidecar = %s", written)
	}
	var libraryFiles string
	if err := db.QueryRow(`SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryFiles); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(libraryFiles, "metadata.json") {
		t.Fatalf("stored sidecar not added to libraryFiles: %s", libraryFiles)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Regression: probe failures must be filtered from the scanned audio set, not
// persisted as placeholder audio records beside valid media.
func TestScanDropsProbeFailedAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("test requires ffmpeg")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=0.6", "-q:a", "9", filepath.Join(dir, "good.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.mp3"), []byte("not audio"), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT audioFiles FROM books`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var audio []map[string]any
	if err := json.Unmarshal([]byte(raw), &audio); err != nil {
		t.Fatal(err)
	}
	if len(audio) != 1 {
		t.Fatalf("audioFiles length=%d, want 1 (failed probe must be dropped): %s", len(audio), raw)
	}
	if audio[0]["error"] != nil {
		t.Fatalf("valid audio has non-null error: %s", raw)
	}
	duration, _ := audio[0]["duration"].(float64)
	if duration <= 0 {
		t.Fatalf("valid audio duration=%v, want positive", duration)
	}
}

// LibraryScanner.js:224-279 emits expanded item updates/additions in ordered
// batches of ten; this catches a lost tail or incorrect membership at boundary.
func TestScanEmitsAddedAndUpdatedItemsInChunksOfTen(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	makeAudio := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.15", "-q:a", "9", filepath.Join(dir, "track.mp3"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate audio: %v: %s", err, out)
		}
	}
	for i := 0; i < 11; i++ {
		makeAudio(filepath.Join(root, fmt.Sprintf("Book %02d", i)))
	}
	db := scannerTestDB(t, root)
	var events []struct {
		name    string
		payload any
	}
	s, err := newScannerForTest(db, config.Config{}, func(_ string, name string, payload any) {
		events = append(events, struct {
			name    string
			payload any
		}{name, payload})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	assertChunks := func(event string) [][]string {
		t.Helper()
		var chunks [][]string
		for _, e := range events {
			if e.name == event {
				items, ok := e.payload.([]map[string]any)
				if !ok {
					t.Fatalf("%s payload type %T", event, e.payload)
				}
				ids := make([]string, len(items))
				for i, item := range items {
					ids[i], _ = item["id"].(string)
					if ids[i] == "" {
						t.Fatalf("%s item missing id: %#v", event, item)
					}
				}
				chunks = append(chunks, ids)
			}
		}
		if len(chunks) != 2 || len(chunks[0]) != 10 || len(chunks[1]) != 1 {
			t.Fatalf("%s chunks sizes = %v", event, chunkSizes(chunks))
		}
		return chunks
	}
	added := assertChunks("items_added")
	if len(added[0])+len(added[1]) != 11 {
		t.Fatal("addition chunk omitted an item")
	}
	var addedIDs, addedPaths []string
	addedRows, err := db.Query(`SELECT id,path FROM libraryItems WHERE libraryId='lib-1' ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	for addedRows.Next() {
		var id, path string
		if err := addedRows.Scan(&id, &path); err != nil {
			t.Fatal(err)
		}
		addedIDs = append(addedIDs, id)
		addedPaths = append(addedPaths, path)
	}
	if err := addedRows.Close(); err != nil {
		t.Fatal(err)
	}
	gotAdded := append(append([]string{}, added[0]...), added[1]...)
	if strings.Join(gotAdded, ",") != strings.Join(addedIDs, ",") {
		t.Fatalf("added item IDs=%v want path order %v", gotAdded, addedIDs)
	}
	for i, path := range addedPaths {
		wantName := fmt.Sprintf("Book %02d", i)
		if filepath.Base(path) != wantName {
			t.Fatalf("added path[%d]=%q want %q", i, filepath.Base(path), wantName)
		}
	}
	// Updating each item's own audio file changes its file snapshot; source scan order is path-sorted.
	for i := 0; i < 11; i++ {
		p := filepath.Join(root, fmt.Sprintf("Book %02d", i), "track.mp3")
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(" "))
		_ = f.Close()
	}
	events = nil
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	updated := assertChunks("items_updated")
	var persisted []string
	rows, err := db.Query(`SELECT id FROM libraryItems WHERE libraryId='lib-1' ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		persisted = append(persisted, id)
	}
	want := append(append([]string{}, persisted[:10]...), persisted[10:]...)
	got := append(append([]string{}, updated[0]...), updated[1]...)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("updated ids=%v want ordered %v", got, want)
	}
}

func chunkSizes(chunks [][]string) []int {
	out := make([]int, len(chunks))
	for i := range chunks {
		out[i] = len(chunks[i])
	}
	return out
}

// LibraryItemScanner.js finalization and BookScanner.js:926-930 retain
// associated authors and emit their final book counts while deleting empty rows.
func TestScanCleansOrphanAuthorsAndEmptySeriesAndReportsBookCounts(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Healthy Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=0.2", "-q:a", "9", filepath.Join(dir, "track.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"title":"Healthy","authors":["Healthy Author"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	for _, q := range []string{
		`INSERT INTO authors(id,name,createdAt,updatedAt,libraryId) VALUES('orphan','Orphan','2020-01-01','2020-01-01','lib-1')`,
		`INSERT INTO series(id,name,createdAt,updatedAt,libraryId) VALUES('empty-series','Empty','2020-01-01','2020-01-01','lib-1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var events []struct {
		name    string
		payload any
	}
	s, err := newScannerForTest(db, config.Config{}, func(_ string, name string, payload any) {
		events = append(events, struct {
			name    string
			payload any
		}{name, payload})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM authors WHERE id='orphan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orphan count=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM series WHERE id='empty-series'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("empty series count=%d err=%v", n, err)
	}
	var healthyID string
	if err := db.QueryRow(`SELECT id FROM authors WHERE name='Healthy Author'`).Scan(&healthyID); err != nil {
		t.Fatal(err)
	}
	removed := false
	newAuthorCountUpdate := false
	for _, e := range events {
		switch e.name {
		case "author_removed":
			p, _ := e.payload.(map[string]any)
			if p["id"] == "orphan" && p["libraryId"] == "lib-1" {
				removed = true
			}
		case "authors_num_books_updated":
			p, _ := e.payload.(map[string]any)
			if p["libraryId"] != "lib-1" {
				continue
			}
			authors, _ := p["authors"].([]map[string]any)
			for _, a := range authors {
				if a["id"] == healthyID && a["numBooks"] == 1 {
					newAuthorCountUpdate = true
				}
			}
		}
	}
	if !removed {
		t.Fatal("author_removed with orphan id/libraryId not emitted")
	}
	if newAuthorCountUpdate {
		// The only author is created during this initial scan; upstream emits
		// author_added rather than a count update (BookScanner.js:235-245).
		t.Fatal("newly created author's count must not emit authors_num_books_updated")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM authors WHERE id=?`, healthyID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("healthy author row count=%d err=%v", n, err)
	}
}

// BookScanner.js:483-508 assigns a shared millisecond timestamp plus the
// author position, so new author rows remain ordered even within one scan.
func TestScanCreatesAuthorsInMetadataOrder(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=0.2", "-q:a", "9", filepath.Join(dir, "track.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"title":"Book","authors":["A","B","C"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name,createdAt FROM authors WHERE libraryId='lib-1' ORDER BY createdAt ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{"A", "B", "C"}
	var got []string
	previous := ""
	for rows.Next() {
		var name, created string
		if err := rows.Scan(&name, &created); err != nil {
			t.Fatal(err)
		}
		if previous != "" && created < previous {
			t.Fatalf("createdAt decreased: %s then %s", previous, created)
		}
		previous = created
		got = append(got, name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("author order=%v want %v", got, want)
	}
}

func TestFilenameTrackDiscParsingMatchesUpstream(t *testing.T) {
	track, disc := trackNumbersFromFilename("Author - Book CD2 - 003 Chapter.mp3")
	if track != 3 || disc != 2 {
		t.Fatalf("track/disc=%v/%v, want 3/2", track, disc)
	}
}

func TestFilenameTrackParsingRemovesOnlyFirstBookTitleOccurrence(t *testing.T) {
	track, _ := filenameNumbers("01 My Title 2.mp3", "", "My Title", "", "", "")
	if track != 1 {
		t.Fatalf("track=%v, want 1 after removing first title occurrence", track)
	}
}

func TestSmartAudioOrderKeepsMissingTrackStableAndSetsOneBasedIndex(t *testing.T) {
	makeAudio := func(name string, track any) map[string]any {
		return map[string]any{"trackNumFromMeta": track, "trackNumFromFilename": nil, "discNumFromMeta": nil, "discNumFromFilename": nil, "metadata": map[string]any{"filename": name}}
	}
	audio := smartAudioOrder([]map[string]any{makeAudio("a.mp3", 1), makeAudio("b.mp3", 2), makeAudio("zz.mp3", nil)})
	want := []string{"a.mp3", "b.mp3", "zz.mp3"}
	for i, af := range audio {
		name := af["metadata"].(map[string]any)["filename"]
		if name != want[i] || af["index"] != i+1 {
			t.Fatalf("audio[%d]=%v index=%v, want %q index %d", i, name, af["index"], want[i], i+1)
		}
	}
}

func TestProbeAudioDropsFilesWithoutAudioStreams(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Video Only")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "silent.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=0.5", "-q:a", "9", path)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate video-only mp4: %v: %s", e, out)
	}
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
		t.Fatal(err)
	}
	var items int
	if err := db.QueryRow(`SELECT COUNT(*) FROM libraryItems`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if items != 0 {
		t.Fatalf("libraryItems=%d, want 0 for video-only file", items)
	}
	var audio int
	if err := db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&audio); err != nil {
		t.Fatal(err)
	}
	if audio != 0 {
		t.Fatalf("books=%d, want no audio entry", audio)
	}
}

func TestBuildChaptersMetaTitleAndSingleFileRules(t *testing.T) {
	makeAudio := func(name, title string, duration float64) map[string]any {
		return map[string]any{"duration": duration, "chapters": []map[string]any{}, "metadata": map[string]any{"filename": name}, "metaTags": map[string]any{"tagTitle": title}}
	}
	got := buildChapters([]map[string]any{makeAudio("a.mp3", "First", 2), makeAudio("b.mp3", "Second", 3)}, "Book")
	if len(got) != 2 || got[0]["title"] != "First" || got[1]["title"] != "Second" {
		t.Fatalf("unique tag titles: %v", got)
	}
	got = buildChapters([]map[string]any{makeAudio("a.mp3", "Book", 2), makeAudio("b.mp3", "Book", 3)}, "Book")
	if len(got) != 2 || got[0]["title"] != "a" || got[1]["title"] != "b" {
		t.Fatalf("book-title tags should use filenames: %v", got)
	}
	got = buildChapters([]map[string]any{makeAudio("a.mp3", "Only", 2)}, "Book")
	if len(got) != 0 {
		t.Fatalf("single unchaptered file chapters=%v, want none", got)
	}
}

func TestProbeAudioMissingBitRateIsNull(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	old := os.Getenv("FFPROBE_PATH")
	if err := os.Setenv("FFPROBE_PATH", ffprobe); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("FFPROBE_PATH", old)
	// A deterministic fake emits valid probe JSON with no usable stream/format bitrate.
	// Use a short-lived executable script so the production parser sees the real wire shape.
	script := filepath.Join(t.TempDir(), "probe.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\",\"bit_rate\":\"-1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\",\"bit_rate\":\"0\"}]} '\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("FFPROBE_PATH", script); err != nil {
		t.Fatal(err)
	}
	// Flat FileMetadata is the caller contract since the audio-shape fix.
	meta := map[string]any{"filename": "x.mp3", "ext": ".mp3"}
	af, err := probeAudio(context.Background(), "ignored", meta, "1")
	if err != nil {
		t.Fatal(err)
	}
	if af["bitRate"] != nil {
		t.Fatalf("bitRate=%v, want null", af["bitRate"])
	}
}

func TestParseOPFMetadata(t *testing.T) {
	var got bookMetadata
	parseOPF([]byte(`<package xmlns:dc="urn:dc" xmlns:opf="urn:opf"><metadata><dc:title>OPF Book</dc:title><dc:creator opf:role="aut">A. Writer</dc:creator><dc:identifier opf:scheme="ISBN">123</dc:identifier><meta name="calibre:series" content="Tales"/></metadata></package>`), &got)
	if got.Title != "OPF Book" || got.ISBN != "123" || len(got.Authors) != 1 || got.Authors[0] != "A. Writer" || len(got.Series) != 1 || got.Series[0].Name != "Tales" {
		t.Fatalf("OPF parse = %+v", got)
	}
}

func TestScanCancellationAndRouteOwnership(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	var emitted []string
	s, err := newScannerForTest(db, config.Config{}, func(_ string, event string, _ any) { emitted = append(emitted, event) })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.Scan(ctx, "lib-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled scan error = %v, want context.Canceled", err)
	}
	if !result.Canceled {
		t.Fatalf("pre-canceled scan result = %+v", result)
	}
	defer s.Close()
	for _, tc := range []struct {
		path   string
		want   bool
		status int
	}{{"/api/items/id/scan", true, http.StatusNotFound}, {"/api/items/id/progress", false, 0}, {"/api/libraries/lib-1/scan", true, http.StatusOK}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, tc.path, nil)
		if got := s.Handle(recorder, request, storeUserRoot()); got != tc.want {
			t.Fatalf("Handle(%s) = %v, want %v", tc.path, got, tc.want)
		}
		if tc.want && tc.status != 0 && recorder.Code != tc.status {
			t.Fatalf("Handle(%s) status = %d want %d", tc.path, recorder.Code, tc.status)
		}
		if tc.path == "/api/libraries/lib-1/scan" && (recorder.Body.String() != "OK" || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8") {
			t.Fatalf("library scan response = %d %q %q", recorder.Code, recorder.Body.String(), recorder.Header().Get("Content-Type"))
		}
	}
	if len(emitted) != 2 || emitted[0] != "task_started" || emitted[1] != "task_finished" {
		t.Fatalf("library scan task events = %v", emitted)
	}
}

func storeUserRoot() store.User { return store.User{Type: "root"} }

func TestScannedFileRelPathsAreItemRelative(t *testing.T) {
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(ffprobe, []byte("#!/bin/sh\nprintf '%s' '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"1\"},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\"}]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("FFPROBE_PATH")
	if err := os.Setenv("FFPROBE_PATH", ffprobe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("FFPROBE_PATH", old) })
	cases := []struct {
		name  string
		files map[string]string
		want  map[string]string
	}{
		{"normal directory", map[string]string{"Author/Series/Book/audio.mp3": "audio.mp3"}, map[string]string{"audio.mp3": "audio.mp3"}},
		{"CD folded", map[string]string{"Author/Series/Book/CD1/t1.mp3": "CD1/t1.mp3"}, map[string]string{"t1.mp3": "CD1/t1.mp3"}},
		{"nested claim", map[string]string{"Author/album.mp3": "album.mp3", "Author/Book2/x.mp3": "Book2/x.mp3"}, map[string]string{"album.mp3": "album.mp3", "x.mp3": "Book2/x.mp3"}},
		{"root single file", map[string]string{"book.mp3": "book.mp3"}, map[string]string{"book.mp3": "book.mp3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel := range tc.files {
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			db := scannerTestDB(t, root)
			s, err := newScannerForTest(db, config.Config{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
				t.Fatal(err)
			}
			var libraryRaw, audioRaw string
			if err := db.QueryRow(`SELECT libraryFiles FROM libraryItems LIMIT 1`).Scan(&libraryRaw); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT audioFiles FROM books LIMIT 1`).Scan(&audioRaw); err != nil {
				t.Fatal(err)
			}
			var libraryFiles, audio []map[string]any
			if err := json.Unmarshal([]byte(libraryRaw), &libraryFiles); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(audioRaw), &audio); err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, f := range libraryFiles {
				m := f["metadata"].(map[string]any)
				got[m["filename"].(string)] = m["relPath"].(string)
			}
			for name, want := range tc.want {
				if got[name] != want {
					t.Errorf("libraryFiles %s relPath=%q want %q", name, got[name], want)
				}
			}
			for _, f := range audio {
				m := f["metadata"].(map[string]any)
				name, _ := m["filename"].(string)
				if want, exists := tc.want[name]; exists && m["relPath"] != want {
					t.Errorf("audioFiles %s relPath=%v want %q", name, m["relPath"], want)
				}
			}
		})
	}
}

func TestScannerDiscoveryRepairCharacterization(t *testing.T) {
	t.Run("shallowest-first claim captures CD companion and nested media", func(t *testing.T) {
		root := t.TempDir()
		book := filepath.Join(root, "A")
		paths := []string{filepath.Join(book, "B", "C.mp3"), filepath.Join(book, "z.mp3"), filepath.Join(book, "notes.txt")}
		for _, p := range paths {
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("fixture"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		cd := filepath.Join(book, "CD1", "track.mp3")
		if err := os.MkdirAll(filepath.Dir(cd), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cd, []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
		if !scannableCompanion(filepath.Ext(paths[2])) {
			t.Fatal("notes.txt is not a recognized companion")
		}
		groups := discoverGroupsForTest(t, root, "book")
		g := groups[book]
		if g == nil || len(g.files) != 3 || len(g.companions) != 1 || g.companions[0].path != paths[2] {
			t.Fatalf("A claim group=%+v", g)
		}
		if len(groups) != 1 {
			t.Fatalf("groups=%v, want only shallow A", groups)
		}
	})
	t.Run("podcast drops root-level files", func(t *testing.T) {
		root := t.TempDir()
		rootAudio := filepath.Join(root, "root.mp3")
		nested := filepath.Join(root, "Show", "episode.mp3")
		for _, p := range []string{rootAudio, nested} {
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("fixture"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		groups := discoverGroupsForTest(t, root, "podcast")
		if len(groups) != 1 || groups[filepath.Join(root, "Show")] == nil {
			t.Fatalf("podcast groups=%v, want Show only (root file dropped)", groups)
		}
	})
	t.Run("root media is one file item and unrelated companion ignored", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "book.mp3")
		for _, name := range []string{"book.mp3", "unrelated.txt"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		groups := discoverGroupsForTest(t, root, "book")
		g := groups[p]
		if g == nil || !g.isFile || len(g.files) != 1 || len(g.companions) != 0 {
			t.Fatalf("root group=%+v", g)
		}
	})
	t.Run("CD folder folds into book", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "Author", "Series", "Book", "CD1", "track.mp3")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}
		if got := groupKey(root, p, "book"); got != filepath.Join(root, "Author", "Series", "Book") {
			t.Fatalf("groupKey=%q", got)
		}
	})
	t.Run("dot component and temporary extension ignored", func(t *testing.T) {
		for _, p := range []string{"Author/.hidden/book.mp3", "Author/book.TMP"} {
			if !shouldIgnore(p) {
				t.Errorf("%s not ignored", p)
			}
		}
		if shouldIgnore("Author/.Hidden/book.mp3") != true {
			t.Fatal("dot-name ignore must apply regardless of component case")
		}
		if !shouldIgnore("Author/Book/.ignore") {
			t.Fatal("dotfile should be ignored")
		}
		if shouldIgnore("Author/book.tmpx") {
			t.Fatal("unlisted extension ignored")
		}
		if !scannableCompanion(".opf") || scannableCompanion(".pdf") {
			t.Fatal("companion extension mismatch")
		}
	})
	t.Run("CD folder matcher source case-insensitive", func(t *testing.T) {
		if !cdDirectory.MatchString("Disc12") || !cdDirectory.MatchString("CD 001") {
			t.Fatal("CD folder regex mismatch")
		}
		if cdDirectory.MatchString("CD1234") {
			t.Fatal("four-digit CD folder matched")
		}
	})
	t.Run("root item classification", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "Standalone.mp3")
		if err := os.WriteFile(p, []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}
		g := &group{path: p, folder: folder{path: root}, files: []file{{path: p}}, isFile: true}
		got := append([]file(nil), g.files...)
		if len(got) != 1 || got[0].path != p {
			t.Fatalf("single-file libraryFiles=%v", got)
		}
		if boolInt(g.isFile) != 1 {
			t.Fatal("single-file isFile not set")
		}
	})
	t.Run("unreadable/missing folder degrades", func(t *testing.T) {
		root := t.TempDir()
		db := scannerTestDB(t, filepath.Join(root, "missing"))
		if _, err := db.Exec(`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-2',?,'lib-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`, root); err != nil {
			t.Fatal(err)
		}
		book := filepath.Join(root, "Book")
		if err := os.MkdirAll(book, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(book, "track.mp3"), []byte("not audio"), 0644); err != nil {
			t.Fatal(err)
		}
		s, err := newScannerForTest(db, config.Config{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Scan(context.Background(), "lib-1"); err != nil {
			t.Fatalf("scan error=%v", err)
		}
	})
	t.Run("root single media is file item", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "Standalone.mp3")
		if err := os.WriteFile(p, []byte("not audio"), 0644); err != nil {
			t.Fatal(err)
		}
		g := &group{path: p, folder: folder{path: root}, files: []file{{path: p}}, isFile: true}
		got := append([]file(nil), g.files...)
		if len(got) != 1 {
			t.Fatalf("groupFiles len=%d", len(got))
		}
	})
	t.Run("missing inode is not fabricated", func(t *testing.T) {
		if got := inode(fakeFileInfo{}); got != "" {
			t.Fatalf("inode=%q", got)
		}
	})
}

// discoverGroupsForTest mirrors scanLibrary's depth-ordered media claims and
// subsequent shallowest-ancestor companion claims without probing audio.
func discoverGroupsForTest(t *testing.T, root, mediaType string) map[string]*group {
	t.Helper()
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	var discovered []file
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			discovered = append(discovered, file{path: p, info: info})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(discovered, func(i, j int) bool {
		a, b := mustRel(root, discovered[i].path), mustRel(root, discovered[j].path)
		da, db := strings.Count(a, string(filepath.Separator)), strings.Count(b, string(filepath.Separator))
		if da != db {
			return da < db
		}
		return discovered[i].path < discovered[j].path
	})
	groups := map[string]*group{}
	for _, item := range discovered {
		rel, _ := filepath.Rel(root, item.path)
		if audioExt(filepath.Ext(item.path)) {
			if mediaType == "podcast" && filepath.Dir(rel) == "." {
				continue
			}
			parts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
			if filepath.Dir(rel) == "." {
				groups[item.path] = &group{path: item.path, files: []file{item}, isFile: true}
				continue
			}
			keyRel := ""
			for i, part := range parts {
				keyRel = filepath.Join(keyRel, part)
				key := filepath.Join(root, keyRel)
				if g := groups[key]; g != nil {
					g.files = append(g.files, item)
					break
				}
				remaining := len(parts) - i - 1
				if remaining == 0 || (remaining == 1 && cdDirectory.MatchString(parts[i+1])) {
					groups[key] = &group{path: key, files: []file{item}}
					break
				}
			}
		}
	}
	for _, item := range discovered {
		if audioExt(filepath.Ext(item.path)) || !scannableCompanion(filepath.Ext(item.path)) {
			continue
		}
		rel, _ := filepath.Rel(root, item.path)
		dirrel := filepath.ToSlash(filepath.Dir(rel))
		parts := strings.Split(dirrel, "/")
		if dirrel == "." {
			parts = nil
		}
		keyRel := ""
		for _, part := range parts {
			if part == "." {
				continue
			}
			keyRel = filepath.Join(keyRel, part)
			if g := groups[filepath.Join(root, keyRel)]; g != nil {
				g.companions = append(g.companions, item)
				break
			}
		}
	}
	return groups
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "x" }
func (fakeFileInfo) Size() int64        { return 1 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func TestStartRejectsInvalidPerLibraryCron(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	if _, err := db.Exec(`UPDATE libraries SET settings=? WHERE id='lib-1'`, `{"autoScanCronExpression":"not a cron"}`); err != nil {
		t.Fatal(err)
	}
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("Start accepted invalid library cron expression")
	}
	s.Close()
}

func TestWatcherDiscoversNewNestedFoldersAndCloseStopsIt(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("test requires FFmpeg")
	}
	root := t.TempDir()
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	itemDir := filepath.Join(root, "New Author", "New Series", "New Book")
	if err := os.MkdirAll(itemDir, 0755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(itemDir, "track.mp3")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=330:duration=0.5", "-q:a", "9", mediaPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create watcher audio: %v: %s", err, out)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryId='lib-1'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("recursive fsnotify watcher did not import media in newly created nested directories")
}

func TestCronScheduleRunsLibraryScan(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("test requires FFmpeg")
	}
	root := t.TempDir()
	itemDir := filepath.Join(root, "Scheduled Book")
	if err := os.MkdirAll(itemDir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=510:duration=0.5", "-q:a", "9", filepath.Join(itemDir, "audio.mp3"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create scheduled scan audio: %v: %s", err, out)
	}
	db := scannerTestDB(t, root)
	if _, err := db.Exec(`UPDATE libraries SET settings=? WHERE id='lib-1'`, `{"autoScanCronExpression":"@every 1s"}`); err != nil {
		t.Fatal(err)
	}
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM libraryItems WHERE libraryId='lib-1'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("configured cron schedule did not scan the library")
}

func TestQuickMatchPodcastEpisodesPersistsFeedFields(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	for _, ddl := range []string{
		`CREATE TABLE podcasts(id UUID PRIMARY KEY,feedURL TEXT)`,
		`CREATE TABLE podcastEpisodes(id UUID PRIMARY KEY,podcastId UUID,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`INSERT INTO podcasts(id,feedURL) VALUES('pod-1','https://feed.test/rss')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO libraryItems(id,mediaId,mediaType,isFile,isMissing,path,libraryId,libraryFolderId,createdAt,updatedAt) VALUES('item-podcast','pod-1','podcast',0,0,?,'lib-1','folder-1','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO podcastEpisodes(id,podcastId,title,createdAt,updatedAt) VALUES('ep-1','pod-1','The Story','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPodcastFeedFetcher(func(_ context.Context, feedURL string) ([]map[string]any, error) {
		if feedURL != "https://feed.test/rss" {
			t.Fatalf("feed URL=%q", feedURL)
		}
		return []map[string]any{{"title": "The Story", "subtitle": "Episode One", "description": "Details", "episode": "1", "episodeType": "full", "season": "1", "pubDate": "Wed, 01 Jan 2025 00:00:00 GMT", "publishedAt": int64(1735689600000), "enclosure": map[string]any{"url": "https://feed.test/audio.mp3", "length": "1234", "type": "audio/mpeg"}}}, nil
	})
	updated, err := s.QuickMatchPodcastEpisodes(context.Background(), "item-podcast", false)
	if err != nil || updated != 1 {
		t.Fatalf("quick match updated=%d err=%v", updated, err)
	}
	var title, subtitle, audioURL string
	var size int64
	if err := db.QueryRow(`SELECT title,subtitle,enclosureURL,enclosureSize FROM podcastEpisodes WHERE id='ep-1'`).Scan(&title, &subtitle, &audioURL, &size); err != nil {
		t.Fatal(err)
	}
	if title != "The Story" || subtitle != "Episode One" || audioURL != "https://feed.test/audio.mp3" || size != 1234 {
		t.Fatalf("matched episode=%q/%q/%q/%d", title, subtitle, audioURL, size)
	}
}
