package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func newTestLogger(t *testing.T) (*Logger, *bytes.Buffer, *bytes.Buffer, *fakeClock) {
	t.Helper()
	var out, errBuf bytes.Buffer
	clock := &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 89_000_000, time.Local)}
	l := New(Options{Stdout: &out, Stderr: &errBuf, Now: clock.Now})
	t.Cleanup(l.Close)
	return l, &out, &errBuf, clock
}

func TestConsoleLineCarriesLevelAndMessage(t *testing.T) {
	l, out, errBuf, _ := newTestLogger(t)
	l.Info("[Server]", "hello", 42)
	if !strings.Contains(out.String(), "INFO") || !strings.Contains(out.String(), "[Server] hello 42") {
		t.Fatalf("stdout = %q", out.String())
	}
	if errBuf.Len() != 0 {
		t.Fatalf("INFO wrote to stderr: %q", errBuf.String())
	}
	l.Errorf("failed: %v", errors.New("boom"))
	if !strings.Contains(errBuf.String(), "ERROR") || !strings.Contains(errBuf.String(), "failed: boom") {
		t.Fatalf("stderr = %q", errBuf.String())
	}
}

func TestConsoleStreamPerLevel(t *testing.T) {
	// Node: debug/info/log -> stdout; trace/warn/error -> stderr.
	l, out, errBuf, _ := newTestLogger(t)
	l.ChangeLevel(LevelTrace)
	out.Reset()
	l.Trace("t")
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	l.Fatal("f")
	l.Note("n")
	for level, msg := range map[string]string{"DEBUG": "d", "INFO": "i", "NOTE": "n"} {
		if !hasLine(out.String(), level, msg) || hasLine(errBuf.String(), level, msg) {
			t.Errorf("%s must go to stdout only: stdout=%q stderr=%q", level, out.String(), errBuf.String())
		}
	}
	for level, msg := range map[string]string{"TRACE": "t", "WARN": "w", "ERROR": "e", "FATAL": "f"} {
		if !hasLine(errBuf.String(), level, msg) || hasLine(out.String(), level, msg) {
			t.Errorf("%s must go to stderr only: stdout=%q stderr=%q", level, out.String(), errBuf.String())
		}
	}
}

// hasLine reports whether some console line names the level and ends with msg.
func hasLine(console, level, msg string) bool {
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, level) && strings.HasSuffix(line, msg) {
			return true
		}
	}
	return false
}

func TestLevelFilteringAndDefault(t *testing.T) {
	l, out, errBuf, _ := newTestLogger(t)
	if l.Level() != LevelInfo {
		t.Fatalf("default level = %v, want INFO", l.Level())
	}
	l.Trace("no")
	l.Debug("no")
	l.Info("yes")
	l.Warn("yes")
	if strings.Contains(out.String()+errBuf.String(), "no") {
		t.Fatalf("below-level lines were written: %q %q", out.String(), errBuf.String())
	}
	l.ChangeLevel(LevelError)
	out.Reset()
	errBuf.Reset()
	l.Warn("hidden")
	l.Error("shown")
	l.Fatal("always")
	if strings.Contains(errBuf.String(), "hidden") || !strings.Contains(errBuf.String(), "shown") {
		t.Fatalf("ERROR level filtering: %q", errBuf.String())
	}
	// FATAL is logged even when the level is above FATAL (server/Logger.js:112).
	l.ChangeLevel(LevelNote)
	errBuf.Reset()
	l.Error("hidden")
	l.Fatal("still")
	if strings.Contains(errBuf.String(), "hidden") || !strings.Contains(errBuf.String(), "FATAL: still") {
		t.Fatalf("NOTE level filtering: %q", errBuf.String())
	}
	if got := Level(99).String(); got != "UNKNOWN" {
		t.Fatalf("Level(99) = %q", got)
	}
}

func TestChangeLevelLogsDebugLine(t *testing.T) {
	l, out, _, _ := newTestLogger(t)
	l.ChangeLevel(LevelDebug)
	if !strings.Contains(out.String(), "DEBUG: Set Log Level to DEBUG") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestSourceIsCallerFileAndLine(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	l.Info("x")
	got := l.Recent()
	if len(got) != 1 || !regexp.MustCompile(`^logging_test\.go:\d+$`).MatchString(got[0].Source) {
		t.Fatalf("source = %+v", got)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestDailyFileJSONLineShapeAndName(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Info("a <b> & c")
	for _, d := range []string{"logs/daily", "logs/scans"} {
		if st, err := os.Stat(filepath.Join(dir, d)); err != nil || !st.IsDir() {
			t.Fatalf("%s not created: %v", d, err)
		}
	}
	lines := readLines(t, filepath.Join(dir, "logs", "daily", "2026-03-04.txt"))
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
	var e LogEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e.Level != 2 {
		t.Fatalf("round trip: %v %+v", err, e)
	}
	if e.Timestamp != "2026-03-04 05:06:07.089" || !regexp.MustCompile(`^logging_test\.go:\d+$`).MatchString(e.Source) || e.Message != "a <b> & c" || e.LevelName != "INFO" || e.Level != 2 {
		t.Fatalf("decoded log entry = %+v", e)
	}
}

func TestBufferedUntilOpenAndExistingFileLoaded(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "logs", "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"timestamp":"t1","source":"S.js:1","message":"old","levelName":"INFO","level":2}` + "\n" +
		"not json\n\n" +
		`{"timestamp":"t2","source":"S.js:2","message":"old2","levelName":"WARN","level":3}` + "\n"
	if err := os.WriteFile(filepath.Join(daily, "2026-03-04.txt"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	l.Info("before open")
	if got := l.Recent(); len(got) != 1 || got[0].Message != "before open" {
		t.Fatalf("pre-open Recent = %+v", got)
	}
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Info("after open")
	got := l.Recent()
	var msgs []string
	for _, e := range got {
		msgs = append(msgs, e.Message)
	}
	want := []string{"old", "old2", "before open", "after open"}
	if strings.Join(msgs, "|") != strings.Join(want, "|") {
		t.Fatalf("messages = %v, want %v", msgs, want)
	}
	// Bad lines were removed from the file (DailyLog.loadLogs rewrite).
	lines := readLines(t, filepath.Join(daily, "2026-03-04.txt"))
	if len(lines) != 4 {
		t.Fatalf("file lines = %q", lines)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("invalid line %q", line)
		}
	}
}

func TestRetentionPrunesOldDailyLogsAtOpen(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "logs", "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"2026-02-25.txt", "2026-02-26.txt", "2026-02-27.txt", "2026-02-28.txt", "2026-03-01.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(daily, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.ApplySettings(3)
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Info("today")
	entries, _ := os.ReadDir(daily)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	// Upstream prunes before the current day's file exists: 3 kept + today;
	// non-.txt files are never touched.
	want := "2026-02-27.txt 2026-02-28.txt 2026-03-01.txt 2026-03-04.txt notes.md"
	if strings.Join(names, " ") != want {
		t.Fatalf("files after prune = %v", names)
	}
	if _, err := os.Stat(filepath.Join(daily, "2026-02-25.txt")); !os.IsNotExist(err) {
		t.Fatal("oldest file survived")
	}
	if _, err := os.Stat(filepath.Join(daily, "2026-02-26.txt")); !os.IsNotExist(err) {
		t.Fatal("second oldest file survived")
	}
}

func TestRollToNextDayCreatesFileAndPrunes(t *testing.T) {
	l, _, _, clock := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "logs", "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"2026-03-02.txt", "2026-03-03.txt"} {
		if err := os.WriteFile(filepath.Join(daily, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.ApplySettings(2)
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Info("day one")
	clock.Set(clock.Now().Add(24 * time.Hour))
	l.Info("day two")
	if got := l.Recent(); len(got) != 1 || got[0].Message != "day two" {
		t.Fatalf("tail after roll = %+v", got)
	}
	entries, _ := os.ReadDir(daily)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "2026-03-04.txt 2026-03-05.txt" {
		t.Fatalf("files after roll = %v", names)
	}
	lines := readLines(t, filepath.Join(daily, "2026-03-05.txt"))
	if len(lines) != 1 || !strings.Contains(lines[0], `"message":"day two"`) {
		t.Fatalf("day two file = %q", lines)
	}
}

func TestCrashLogReceivesFatalOnly(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	l.Fatal("before open") // buffered, then flushed into the crash log
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Error("not a crash")
	l.Fatal("boom")
	lines := readLines(t, filepath.Join(dir, "logs", "crash_logs.txt"))
	if len(lines) != 2 {
		t.Fatalf("crash log lines = %q, want two fatal entries", lines)
	}
	wantMessages := []string{"before open", "boom"}
	for i, line := range lines {
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode crash log line %q: %v", line, err)
		}
		if entry.LevelName != "FATAL" || entry.Level != 5 {
			t.Errorf("crash log contains non-fatal entry: %+v", entry)
		}
		if entry.Message != wantMessages[i] {
			t.Errorf("crash log message[%d] = %q, want %q", i, entry.Message, wantMessages[i])
		}
	}
	// Fatal lines also reach the daily log.
	daily := readLines(t, filepath.Join(dir, "logs", "daily", "2026-03-04.txt"))
	if len(daily) != 3 {
		t.Fatalf("daily = %q", daily)
	}
}

func TestBelowLevelNotWrittenToFile(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	if err := l.OpenFiles(filepath.Join(dir, "logs")); err != nil {
		t.Fatal(err)
	}
	l.Debug("hidden")
	l.Info("kept")
	l.ChangeLevel(LevelDebug)
	l.Debug("now visible")
	var msgs []string
	for _, line := range readLines(t, filepath.Join(dir, "logs", "daily", "2026-03-04.txt")) {
		var e LogEntry
		_ = json.Unmarshal([]byte(line), &e)
		msgs = append(msgs, e.Message)
	}
	want := "kept|Set Log Level to DEBUG|now visible"
	if strings.Join(msgs, "|") != want {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestLoggerDataShape(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	b, _ := json.Marshal(l.LoggerData())
	var empty struct {
		CurrentDailyLogs []LogEntry `json:"currentDailyLogs"`
	}
	if err := json.Unmarshal(b, &empty); err != nil {
		t.Fatalf("decode empty LoggerData %s: %v", b, err)
	}
	if empty.CurrentDailyLogs == nil || len(empty.CurrentDailyLogs) != 0 {
		t.Fatalf("empty currentDailyLogs = %#v, want empty array", empty.CurrentDailyLogs)
	}
	l.Info("one")
	b, _ = json.Marshal(l.LoggerData())
	var nonempty struct {
		CurrentDailyLogs []LogEntry `json:"currentDailyLogs"`
	}
	if err := json.Unmarshal(b, &nonempty); err != nil {
		t.Fatalf("decode nonempty LoggerData %s: %v", b, err)
	}
	if len(nonempty.CurrentDailyLogs) != 1 {
		t.Fatalf("currentDailyLogs = %+v, want one entry", nonempty.CurrentDailyLogs)
	}
	e := nonempty.CurrentDailyLogs[0]
	if e.Timestamp != "2026-03-04 05:06:07.089" || !regexp.MustCompile(`^logging_test\.go:\d+$`).MatchString(e.Source) || e.Message != "one" || e.LevelName != "INFO" || e.Level != 2 {
		t.Fatalf("currentDailyLogs entry = %+v", e)
	}
}

func TestRecentIsLimitedToLast5000(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	if err := l.OpenFiles(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < RecentLimit+25; i++ {
		l.Infof("m%d", i)
	}
	got := l.Recent()
	if len(got) != RecentLimit || got[0].Message != "m25" || got[len(got)-1].Message != "m5024" {
		t.Fatalf("len=%d first=%q last=%q", len(got), got[0].Message, got[len(got)-1].Message)
	}
}

func TestSocketListenerEmission(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	type sent struct {
		id    string
		entry LogEntry
	}
	var mu sync.Mutex
	var got []sent
	l.BindSockets(func(id string, e LogEntry) {
		mu.Lock()
		got = append(got, sent{id, e})
		mu.Unlock()
	})
	l.AddSocketListener("a", LevelWarn)
	l.AddSocketListener("b", LevelInfo)
	l.Info("info line")
	l.Warn("warn line")
	l.Debug("debug line") // below the global level: nobody receives it
	mu.Lock()
	snapshot := append([]sent(nil), got...)
	mu.Unlock()
	counts := map[string]int{}
	for _, s := range snapshot {
		counts[s.id]++
		if s.entry.LevelName == "" || s.entry.Source == "" || s.entry.Timestamp == "" {
			t.Fatalf("incomplete payload %+v", s.entry)
		}
	}
	if counts["a"] != 1 || counts["b"] != 2 {
		t.Fatalf("counts = %v (%+v)", counts, snapshot)
	}
	// Replacing a listener changes its level; removing stops delivery.
	l.AddSocketListener("a", LevelInfo)
	l.RemoveSocketListener("b")
	mu.Lock()
	got = nil
	mu.Unlock()
	l.Info("again")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].id != "a" || got[0].entry.Message != "again" {
		t.Fatalf("after replace/remove: %+v", got)
	}
}

func TestFatalAlwaysReachesListeners(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	var n int
	l.BindSockets(func(string, LogEntry) { n++ })
	l.AddSocketListener("a", LevelNote)
	l.Error("below listener level")
	l.Fatal("fatal")
	if n != 1 {
		t.Fatalf("deliveries = %d, want only the FATAL one", n)
	}
}

func TestWriterAdaptsStdlibLines(t *testing.T) {
	l, _, errBuf, _ := newTestLogger(t)
	w := l.Writer(LevelError, "http")
	_, _ = w.Write([]byte("http: panic serving 1.2.3.4\nsecond\n"))
	got := l.Recent()
	if len(got) != 2 || got[0].Source != "http" || got[0].LevelName != "ERROR" {
		t.Fatalf("entries = %+v", got)
	}
	if strings.Count(errBuf.String(), "ERROR:") != 2 {
		t.Fatalf("stderr = %q", errBuf.String())
	}
}

func TestConcurrentLoggingIsRaceFree(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	if err := l.OpenFiles(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	l.BindSockets(func(string, LogEntry) {})
	l.AddSocketListener("a", LevelInfo)
	const writers, perWriter = 8, 200
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				l.Info("x", j)
				_ = l.Recent()
				l.AddSocketListener("a", LevelWarn)
			}
		}()
	}
	wg.Wait()
	if got := len(l.Recent()); got != writers*perWriter {
		t.Fatalf("recent entries = %d, want %d (every concurrent line kept)", got, writers*perWriter)
	}
}
