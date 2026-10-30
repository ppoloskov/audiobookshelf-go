package audiotools

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

func TestFFMetadataEscapingAndChapterOrder(t *testing.T) {
	x := item{title: "A; #book=one", author: "Writer\\Name", chapters: []chapter{{0, 1.25, "First"}, {1.25, 2.5, "Second"}}}
	got := ffmetadata(x, true)
	for _, want := range []string{"title=A\\; \\#book\\=one", "artist=Writer\\\\Name", "START=0", "END=1250", "title=First", "START=1250", "END=2500", "title=Second"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ffmetadata missing %q in:\n%s", want, got)
		}
	}
}

func TestMergeCreatesM4BAndKeepsSourceBackup(t *testing.T) {
	if _, err := exec.LookPath(ffmpegPath()); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	one := filepath.Join(root, "01.wav")
	two := filepath.Join(root, "02.wav")
	writeTone(t, one, 440, 0.35)
	writeTone(t, two, 880, 0.35)
	h := New(nil, configForTest(filepath.Join(root, "metadata")), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &task{ID: "task-merge", Action: "encode-m4b", Data: map[string]any{}, ctx: ctx, cancel: cancel, cache: filepath.Join(root, "metadata", "cache", "items", "item")}
	libDir := filepath.Join(root, "Merged Book")
	if err := os.Mkdir(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	lib := item{id: "item", path: libDir, title: "Merged Book", author: "Writer", files: []audio{{ino: "a", path: one, duration: .35}, {ino: "b", path: two, duration: .35}}, chapters: []chapter{{0, .35, "First"}, {.35, .7, "Second"}}}
	if err := h.merge(task, lib, map[string][]string{}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "Merged Book.m4b")
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("missing m4b output: %v", err)
	}
	for _, src := range []string{one, two} {
		if _, err := os.Stat(filepath.Join(task.cache, filepath.Base(src))); err != nil {
			t.Fatalf("source backup not retained: %v", err)
		}
	}
	probe := exec.Command(ffprobePath(), "-v", "error", "-show_entries", "format=duration:format_tags=title", "-show_chapters", "-of", "json", out)
	b, err := probe.Output()
	if err != nil {
		t.Fatalf("ffprobe output: %v", err)
	}
	if !strings.Contains(string(b), "Merged Book") || !strings.Contains(string(b), "First") || !strings.Contains(string(b), "Second") {
		t.Fatalf("output metadata/chapters absent: %s", b)
	}
	if strings.Count(string(b), `"id"`) < 2 {
		t.Fatalf("expected ordered chapters in output: %s", b)
	}
}

func TestMergeFFmpegFailureDoesNotInstallOutput(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "a.wav")
	writeTone(t, src, 440, .2)
	fake := filepath.Join(root, "ffmpeg-fail")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fail >&2\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", fake)
	h := New(nil, configForTest(filepath.Join(root, "meta")), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &task{ctx: ctx, cancel: cancel, cache: filepath.Join(root, "cache")}
	it := item{path: root, title: "Failure", files: []audio{{path: src, duration: .2}}}
	if err := h.merge(task, it, map[string][]string{}); err == nil {
		t.Fatal("expected ffmpeg failure")
	}
	if _, err := os.Stat(filepath.Join(root, "Failure.m4b")); !os.IsNotExist(err) {
		t.Fatalf("failed encode installed output: %v", err)
	}
}

func writeTone(t *testing.T, path string, hz float64, duration float64) {
	t.Helper()
	const rate = 8000
	n := int(float64(rate) * duration)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("RIFF")); err != nil {
		t.Fatal(err)
	}
	_ = binary.Write(f, binary.LittleEndian, uint32(36+n*2))
	_, _ = f.Write([]byte("WAVEfmt "))
	_ = binary.Write(f, binary.LittleEndian, uint32(16))
	_ = binary.Write(f, binary.LittleEndian, uint16(1))
	_ = binary.Write(f, binary.LittleEndian, uint16(1))
	_ = binary.Write(f, binary.LittleEndian, uint32(rate))
	_ = binary.Write(f, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(f, binary.LittleEndian, uint16(2))
	_ = binary.Write(f, binary.LittleEndian, uint16(16))
	_, _ = f.Write([]byte("data"))
	_ = binary.Write(f, binary.LittleEndian, uint32(n*2))
	for i := 0; i < n; i++ {
		v := int16(math.Sin(2*math.Pi*hz*float64(i)/rate) * 8000)
		_ = binary.Write(f, binary.LittleEndian, v)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func configForTest(metadata string) config.Config { return config.Config{MetadataPath: metadata} }
func ffprobePath() string {
	if p := strings.TrimSpace(os.Getenv("FFPROBE_PATH")); p != "" {
		return p
	}
	return "ffprobe"
}

func TestCancelEncodeKillsConfiguredFFmpeg(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "ffmpeg-sleep")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", fake)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &task{ctx: ctx, cancel: cancel, cache: filepath.Join(root, "cache")}
	h := New(nil, configForTest(filepath.Join(root, "meta")), nil)
	it := item{id: "id", path: root, title: "cancel", files: []audio{{path: filepath.Join(root, "in.wav"), duration: 1}}}
	_ = os.WriteFile(it.files[0].path, []byte("placeholder"), 0600)
	done := make(chan struct{})
	go func() { h.runEncode(task, it, map[string][]string{}); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("encode did not stop after cancellation")
	}
	if _, err := os.Stat(filepath.Join(root, "cancel.m4b")); !os.IsNotExist(err) {
		t.Fatalf("canceled encode left output: %v", err)
	}
}
