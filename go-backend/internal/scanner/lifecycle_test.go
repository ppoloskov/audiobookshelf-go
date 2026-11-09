package scanner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

// gatedFFprobe installs a fake ffprobe that announces each call by creating a
// file in started and does not answer until release is called, so a test can
// hold a scan at a known point without sleeping.
func gatedFFprobe(t *testing.T) (started func(), release func()) {
	t.Helper()
	dir := t.TempDir()
	startFile, goFile := filepath.Join(dir, "started"), filepath.Join(dir, "go")
	script := "#!/bin/sh\ntouch '" + startFile + "'\nwhile [ ! -f '" + goFile + "' ]; do sleep 0.02; done\n" +
		`printf '%s' '{"format":{"format_name":"mp3","duration":"1"},"streams":[{"codec_type":"audio","codec_name":"mp3"}]}'` + "\n"
	path := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", path)
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := os.WriteFile(goFile, nil, 0o644); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return func() {
		t.Helper()
		eventually(t, "the scan to reach ffprobe", func() bool { _, err := os.Stat(startFile); return err == nil })
	}, release
}

// eventually polls cond until it holds; the wait is bounded, never a fixed sleep.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// One scan per library at a time, and Cancel stops a scan that is in progress:
// the scan reports Canceled, imports nothing, and frees the library for the next.
func TestCancelDuringScanStopsItAndFreesTheLibrary(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	writeBook(t, root, "Auth", "Book", "a.mp3")
	started, release := gatedFFprobe(t)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := s.Scan(context.Background(), "lib-1", false)
		done <- outcome{res, err}
	}()
	started()
	if _, err := s.Scan(context.Background(), "lib-1", false); err == nil {
		t.Fatal("a second scan of the same library ran concurrently")
	}
	s.Cancel("lib-1")
	release()
	out := <-done
	if !out.res.Canceled {
		t.Fatalf("canceled scan result = %+v, err %v", out.res, out.err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems`); n != 0 {
		t.Fatalf("canceled scan imported %d items", n)
	}
	res, err := s.Scan(context.Background(), "lib-1", false)
	if err != nil || res.Canceled || res.Added != 1 {
		t.Fatalf("scan after cancel = %+v, %v; want one item added", res, err)
	}
}

// Cancel during a watcher batch stops that batch; the batch imports nothing.
func TestCancelDuringWatcherBatchStopsIt(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	writeBook(t, root, "Auth", "Book", "a.mp3")
	started, release := gatedFFprobe(t)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	w := newFSWatcher(context.Background(), s)
	path := filepath.Join(root, "Auth", "Book", "a.mp3")
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.scanFilesChanged(context.Background(), []fileUpdate{{path: path, relPath: "Auth/Book/a.mp3", folderID: "folder-1", libraryID: "lib-1"}}, newWatcherTask(&libraryWatch{id: "lib-1", name: "Books"}), w)
	}()
	started()
	s.Cancel("lib-1")
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("canceled watcher batch did not return")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems`); n != 0 {
		t.Fatalf("canceled batch imported %d items", n)
	}
}

// Close stops the watcher: a batch still waiting for its quiet period is
// dropped, nothing is imported afterwards, and the watcher's goroutines exit.
func TestCloseStopsWatcherBatchesAndGoroutines(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeBook(t, root, "Auth", "Book", "a.mp3")
	eventually(t, "the watcher to queue the new file", func() bool {
		s.mu.Lock()
		fw := s.fw
		s.mu.Unlock()
		fw.mu.Lock()
		defer fw.mu.Unlock()
		return len(fw.pending) > 0
	})
	s.Close()
	eventually(t, "watcher goroutines to exit", func() bool { return runtime.NumGoroutine() <= before })
	// The pending delay has not elapsed yet; wait it out (a sleep is the only
	// way to observe that something does NOT happen).
	time.Sleep(3 * watcherPendingDelay)
	if n := countRows(t, db, `SELECT COUNT(*) FROM libraryItems`); n != 0 {
		t.Fatalf("a batch ran after Close: %d items imported", n)
	}
}

// A file created under a new nested folder is imported by the running watcher.
func TestWatcherImportsNewNestedFolders(t *testing.T) {
	root := t.TempDir()
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
	s, err := newScannerForTest(db, config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := runtime.NumGoroutine()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		eventually(t, "watcher goroutines to exit", func() bool { return runtime.NumGoroutine() <= before })
	}()
	writeBook(t, root, "New Author/New Series", "New Book", "track.mp3")
	eventually(t, "the nested book to be imported", func() bool {
		return countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE libraryId='lib-1'`) == 1
	})
}

// A library's cron schedule runs its scan.
func TestCronScheduleRunsLibraryScan(t *testing.T) {
	root := t.TempDir()
	writeBook(t, root, "", "Scheduled Book", "audio.mp3")
	db := scannerTestDB(t, root)
	fakeFFprobe(t)
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
	eventually(t, "the cron schedule to scan the library", func() bool {
		return countRows(t, db, `SELECT COUNT(*) FROM libraryItems WHERE libraryId='lib-1'`) == 1
	})
}
