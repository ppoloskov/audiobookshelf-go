package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// chapterProbe fakes ffprobe: *.m4b files report two chapters.
func chapterProbe(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	script := `#!/bin/sh
for a; do f="$a"; done
case "$f" in
*.m4b) printf '%s' '{"format":{"format_name":"mov,mp4","duration":"20"},"streams":[{"codec_type":"audio","codec_name":"aac"}],"chapters":[{"start_time":"0.0","end_time":"10.0","tags":{"title":"One"}},{"start_time":"10.0","end_time":"20.0","tags":{"title":"Two"}}]}';;
*) printf '%s' '{"format":{"format_name":"mp3","duration":"1"},"streams":[{"codec_type":"audio","codec_name":"mp3"}]}';;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", path)
}

// Stored audio entries and freshly probed ones are the same type, so a rescan
// keeps the chapters of an unchanged file even when no metadata.json is read
// back (BookScanner.js:68-96 only rescans added or modified audio files).
func TestForcedRescanKeepsEmbeddedChaptersWithoutSidecar(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "book.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	chapterProbe(t)
	db := scannerTestDB(t, root)
	if _, err := db.Exec(`UPDATE libraries SET settings='{"metadataPrecedence":["folderStructure","audioMetatags"]}' WHERE id='lib-1'`); err != nil {
		t.Fatal(err)
	}
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	chapterCount := func() int {
		var raw string
		if err := db.QueryRow(`SELECT chapters FROM books`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var chapters []map[string]any
		if err := json.Unmarshal([]byte(raw), &chapters); err != nil {
			t.Fatal(err)
		}
		return len(chapters)
	}
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	if n := chapterCount(); n != 2 {
		t.Fatalf("first scan chapters=%d, want 2", n)
	}
	if _, err := s.Scan(ctx, "lib-1", true); err != nil {
		t.Fatal(err)
	}
	if n := chapterCount(); n != 2 {
		t.Fatalf("forced rescan chapters=%d, want 2", n)
	}
}

// A file added to an existing book is ordered with the stored entries
// (AudioFileScanner.runSmartTrackOrder over the whole book).
func TestAddedAudioFileIsOrderedWithStoredEntries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Book")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chapterProbe(t)
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-2.mp3")
	db := scannerTestDB(t, root)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	write("b-1.mp3")
	if _, err := s.Scan(ctx, "lib-1", false); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT audioFiles FROM books`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var files []struct {
		Index    int `json:"index"`
		Metadata struct {
			Filename string `json:"filename"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, f := range files {
		got = append(got, f.Metadata.Filename)
		if f.Index != i+1 {
			t.Fatalf("index of %s = %d, want %d", f.Metadata.Filename, f.Index, i+1)
		}
	}
	if strings.Join(got, ",") != "b-1.mp3,a-2.mp3" {
		t.Fatalf("order = %v, want b-1.mp3 then a-2.mp3", got)
	}
}
