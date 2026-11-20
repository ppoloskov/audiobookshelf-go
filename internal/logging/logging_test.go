package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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

func newTestLogger(t *testing.T) (*Logger, *slog.Logger, *bytes.Buffer, *bytes.Buffer, *fakeClock) {
	t.Helper()
	var out, errBuf bytes.Buffer
	clock := &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 89_000_000, time.Local)}
	l := New(Options{Stdout: &out, Stderr: &errBuf, Now: clock.Now})
	t.Cleanup(l.Close)
	return l, slog.New(l), &out, &errBuf, clock
}

// logAt logs through slog at an upstream level.
func logAt(log *slog.Logger, level Level, msg string, args ...any) {
	log.Log(context.Background(), level.slog(), msg, args...)
}

func TestConsoleLineAndStreams(t *testing.T) {
	l, log, out, errBuf, _ := newTestLogger(t)
	l.SetLevel(LevelTrace)
	out.Reset()
	for level := LevelTrace; level <= LevelNote; level++ {
		logAt(log, level, "m"+level.String())
	}
	wantOut := "[2026-03-04 05:06:07.089] DEBUG: mDEBUG\n[2026-03-04 05:06:07.089] INFO: mINFO\n[2026-03-04 05:06:07.089] NOTE: mNOTE\n"
	wantErr := "[2026-03-04 05:06:07.089] TRACE: mTRACE\n[2026-03-04 05:06:07.089] WARN: mWARN\n[2026-03-04 05:06:07.089] ERROR: mERROR\n[2026-03-04 05:06:07.089] FATAL: mFATAL\n"
	if out.String() != wantOut || errBuf.String() != wantErr {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errBuf.String())
	}
}

func TestLevelFilteringAndRuntimeChange(t *testing.T) {
	l, log, out, errBuf, _ := newTestLogger(t)
	if l.Level() != LevelInfo {
		t.Fatalf("default level = %v", l.Level())
	}
	log.Debug("no")
	log.Info("yes")
	l.SetLevel(LevelError)
	if !strings.Contains(out.String(), "INFO: yes") || strings.Contains(out.String(), "no") {
		t.Fatalf("INFO filtering: %q", out.String())
	}
	log.Warn("hidden")
	log.Error("shown")
	if strings.Contains(errBuf.String(), "hidden") || !strings.Contains(errBuf.String(), "shown") {
		t.Fatalf("ERROR level: %q", errBuf.String())
	}
	// FATAL passes even above FATAL (server/Logger.js:112); NOTE is above it too.
	l.SetLevel(LevelNote)
	errBuf.Reset()
	log.Error("hidden")
	logAt(log, LevelFatal, "still")
	if strings.Contains(errBuf.String(), "hidden") || !strings.Contains(errBuf.String(), "FATAL: still") {
		t.Fatalf("NOTE level: %q", errBuf.String())
	}
	if l.Level() != LevelNote || Level(99).String() != "UNKNOWN" {
		t.Fatalf("level=%v unknown=%q", l.Level(), Level(99).String())
	}
}

func TestSetLevelLogsDebugLine(t *testing.T) {
	l, _, out, _, _ := newTestLogger(t)
	l.SetLevel(LevelDebug)
	if !strings.Contains(out.String(), "DEBUG: Set Log Level to DEBUG") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestLevelMapping(t *testing.T) {
	for level := LevelTrace; level <= LevelNote; level++ {
		if got := levelOf(level.slog()); got != level {
			t.Errorf("levelOf(%v.slog()) = %v", level, got)
		}
	}
	for slogLevel, want := range map[slog.Level]Level{-9: LevelTrace, -100: LevelTrace, -5: LevelTrace, -4: LevelDebug, -1: LevelDebug, 1: LevelInfo, 5: LevelWarn, 9: LevelError, 13: LevelFatal, 99: LevelNote} {
		if got := levelOf(slogLevel); got != want {
			t.Errorf("levelOf(%d) = %v, want %v", slogLevel, got, want)
		}
	}
	if slog.LevelDebug != LevelDebug.slog() || slog.LevelInfo != LevelInfo.slog() || slog.LevelError != LevelError.slog() {
		t.Fatal("standard slog levels do not line up with DEBUG, INFO, ERROR")
	}
}

func TestEntryShapeAndSource(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	log.Error("request failed", "path", "/api/x", "error", errors.New("boom"), "n", 3)
	got := l.Entries()
	if len(got) != 1 {
		t.Fatalf("entries = %+v", got)
	}
	e := got[0]
	if e.Timestamp != "2026-03-04 05:06:07.089" || e.Message != "request failed path=/api/x error=boom n=3" || e.LevelName != "ERROR" || e.Level != 4 ||
		!regexp.MustCompile(`^logging_test\.go:\d+$`).MatchString(e.Source) {
		t.Fatalf("entry = %+v", e)
	}
	b, _ := json.Marshal(e)
	if want := `{"timestamp":"2026-03-04 05:06:07.089","source":"` + e.Source + `","message":"request failed path=/api/x error=boom n=3","levelName":"ERROR","level":4}`; string(b) != want {
		t.Fatalf("json = %s", b)
	}
}

func TestAttrsAndGroups(t *testing.T) {
	l, _, _, _, _ := newTestLogger(t)
	log := slog.New(l).With("a", 1).WithGroup("g").With("b", "x")
	log.Info("m", "c", true, slog.Group("h", "d", 2.5), "err", error(nil))
	got := l.Entries()
	if len(got) != 1 || got[0].Message != "m a=1 g.b=x g.c=true g.h.d=2.5 g.err=<nil>" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestCompatHelpersSourceAndLevel(t *testing.T) {
	var out, errBuf bytes.Buffer
	old := defaultLogger
	defaultLogger = New(Options{Stdout: &out, Stderr: &errBuf})
	t.Cleanup(func() { defaultLogger = old })
	defaultLogger.SetLevel(LevelTrace)
	Tracef("t%d", 1)
	Debugf("d")
	Infof("i %s", "x")
	Warnf("w")
	Errorf("e")
	Fatalf("f")
	Info("plain")
	got := defaultLogger.Entries()[1:] // the first is "Set Log Level"
	var names []string
	for _, e := range got {
		names = append(names, e.LevelName+":"+e.Message)
		if !regexp.MustCompile(`^logging_test\.go:\d+$`).MatchString(e.Source) {
			t.Errorf("source = %q", e.Source)
		}
	}
	if strings.Join(names, "|") != "TRACE:t1|DEBUG:d|INFO:i x|WARN:w|ERROR:e|FATAL:f|INFO:plain" {
		t.Fatalf("entries = %v", names)
	}
	defaultLogger.SetLevel(LevelError)
	n := len(defaultLogger.Entries())
	Infof("hidden")
	if len(defaultLogger.Entries()) != n {
		t.Fatal("below-level helper call was logged")
	}
}

func TestRequestHelpers(t *testing.T) {
	old := defaultLogger
	defaultLogger = New(Options{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	t.Cleanup(func() { defaultLogger = old })
	defaultLogger.SetLevel(LevelDebug)
	r, _ := http.NewRequest(http.MethodPatch, "/api/items/li_1/media?token=secret", nil)
	RequestError(r, "database write failed", errors.New("disk I/O error"))
	RequestError(r, "internal error", nil)
	RequestError(nil, "no request", nil)
	RequestDebug(r, "responding 500: nope")
	RecoverRequest(r, "boom")
	var got []string
	for _, e := range defaultLogger.Entries()[1:] {
		got = append(got, e.LevelName+" "+e.Message)
	}
	want := []string{
		"ERROR [HTTP] PATCH /api/items/li_1/media: database write failed: disk I/O error",
		"ERROR [HTTP] PATCH /api/items/li_1/media: internal error",
		"ERROR [HTTP] ? ?: no request",
		"DEBUG [HTTP] PATCH /api/items/li_1/media: responding 500: nope",
	}
	if len(got) != 5 || strings.Join(got[:4], "\n") != strings.Join(want, "\n") || !strings.Contains(got[4], "panic: boom\ngoroutine") {
		t.Fatalf("logged:\n%s", strings.Join(got, "\n"))
	}
	for _, line := range got {
		if strings.Contains(line, "secret") {
			t.Fatal("query string leaked")
		}
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

func fileNames(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

func TestDailyFileLineShapeWithoutHTMLEscaping(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	if err := l.OpenFiles(dir, 7); err != nil {
		t.Fatal(err)
	}
	log.Info("a <b> & c")
	lines := readLines(t, filepath.Join(dir, "daily", "2026-03-04.txt"))
	if len(lines) != 1 || !strings.Contains(lines[0], `"message":"a <b> & c"`) {
		t.Fatalf("lines = %q", lines)
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e.LevelName != "INFO" || e.Level != 2 || e.Timestamp != "2026-03-04 05:06:07.089" {
		t.Fatalf("decoded = %+v, %v", e, err)
	}
}

func TestBufferedUntilOpenAndExistingFileLoaded(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"timestamp":"t1","source":"S.js:1","message":"old","levelName":"INFO","level":2}` + "\nnot json\n\n" +
		`{"timestamp":"t2","source":"S.js:2","message":"old2","levelName":"WARN","level":3}` + "\n"
	if err := os.WriteFile(filepath.Join(daily, "2026-03-04.txt"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	log.Info("before open")
	if got := l.Entries(); len(got) != 1 || got[0].Message != "before open" {
		t.Fatalf("pre-open entries = %+v", got)
	}
	if err := l.OpenFiles(dir, 7); err != nil {
		t.Fatal(err)
	}
	log.Info("after open")
	var msgs []string
	for _, e := range l.Entries() {
		msgs = append(msgs, e.Message)
	}
	if strings.Join(msgs, "|") != "old|old2|before open|after open" {
		t.Fatalf("messages = %v", msgs)
	}
	lines := readLines(t, filepath.Join(daily, "2026-03-04.txt"))
	if len(lines) != 6 || lines[0] != strings.Split(existing, "\n")[0] || !strings.Contains(lines[4], "before open") || !strings.Contains(lines[5], "after open") {
		t.Fatalf("file = %q", lines)
	}
}

func TestRetentionAtOpenAndRolloverAtMidnight(t *testing.T) {
	l, log, _, _, clock := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"2026-02-26.txt", "2026-02-27.txt", "2026-02-28.txt", "2026-03-01.txt", "notes.md"} {
		if err := os.WriteFile(filepath.Join(daily, n), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.OpenFiles(dir, 3); err != nil {
		t.Fatal(err)
	}
	log.Info("day one")
	if got := fileNames(t, daily); got != "2026-02-28.txt 2026-03-01.txt 2026-03-04.txt notes.md" {
		t.Fatalf("after open: %s", got)
	}
	clock.Set(time.Date(2026, 3, 4, 23, 59, 59, 0, time.Local))
	log.Info("late")
	clock.Set(time.Date(2026, 3, 5, 0, 0, 0, 1, time.Local))
	log.Info("day two")
	if got := l.Entries(); len(got) != 1 || got[0].Message != "day two" {
		t.Fatalf("ring after rollover = %+v", got)
	}
	if got := fileNames(t, daily); got != "2026-03-01.txt 2026-03-04.txt 2026-03-05.txt notes.md" {
		t.Fatalf("after rollover: %s", got)
	}
	if lines := readLines(t, filepath.Join(daily, "2026-03-05.txt")); len(lines) != 1 || !strings.Contains(lines[0], `"message":"day two"`) {
		t.Fatalf("day two file = %q", lines)
	}
	if lines := readLines(t, filepath.Join(daily, "2026-03-04.txt")); len(lines) != 2 {
		t.Fatalf("day one file = %q", lines)
	}
}

func TestDefaultRetentionIsSeven(t *testing.T) {
	l, _, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	daily := filepath.Join(dir, "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= 9; d++ {
		if err := os.WriteFile(filepath.Join(daily, "2026-02-0"+string(rune('0'+d))+".txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.OpenFiles(dir, 0); err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(fileNames(t, daily))); got != 7 {
		t.Fatalf("files = %d, want 7", got)
	}
}

func TestCrashLogReceivesFatalOnly(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	logAt(log, LevelFatal, "before open")
	if err := l.OpenFiles(dir, 7); err != nil {
		t.Fatal(err)
	}
	log.Error("not a crash")
	logAt(log, LevelFatal, "boom")
	lines := readLines(t, filepath.Join(dir, "crash_logs.txt"))
	if len(lines) != 2 || !strings.Contains(lines[0], `"message":"before open"`) || !strings.Contains(lines[1], `"levelName":"FATAL","level":5`) {
		t.Fatalf("crash log = %q", lines)
	}
	if daily := readLines(t, filepath.Join(dir, "daily", "2026-03-04.txt")); len(daily) != 3 {
		t.Fatalf("daily = %q", daily)
	}
}

func TestBelowLevelNotWritten(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	if err := l.OpenFiles(dir, 7); err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden")
	log.Info("kept")
	l.SetLevel(LevelDebug)
	log.Debug("now visible")
	var msgs []string
	for _, line := range readLines(t, filepath.Join(dir, "daily", "2026-03-04.txt")) {
		var e Entry
		_ = json.Unmarshal([]byte(line), &e) // lines are ours
		msgs = append(msgs, e.Message)
	}
	if strings.Join(msgs, "|") != "kept|Set Log Level to DEBUG|now visible" {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestOpenFilesFailureAndReopen(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.OpenFiles(filepath.Join(blocker, "logs"), 7); err == nil {
		t.Fatal("OpenFiles under a regular file succeeded")
	}
	log.Info("still logged")
	dir := t.TempDir()
	if err := l.OpenFiles(dir, 7); err != nil {
		t.Fatal(err)
	}
	l.Close()
	log.Info("after close") // Close only releases the handle; the next entry reopens it
	if lines := readLines(t, filepath.Join(dir, "daily", "2026-03-04.txt")); len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
}

func TestEntriesNeverNilAndCappedAt5000(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	if got := l.Entries(); got == nil || len(got) != 0 {
		t.Fatalf("empty entries = %#v", got)
	}
	b, _ := json.Marshal(struct {
		CurrentDailyLogs []Entry `json:"currentDailyLogs"`
	}{l.Entries()})
	if string(b) != `{"currentDailyLogs":[]}` {
		t.Fatalf("json = %s", b)
	}
	if err := l.OpenFiles(t.TempDir(), 7); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ringSize+25; i++ {
		log.Info("m", "i", i)
	}
	got := l.Entries()
	if len(got) != ringSize || got[0].Message != "m i=25" || got[len(got)-1].Message != "m i=5024" {
		t.Fatalf("len=%d first=%q last=%q", len(got), got[0].Message, got[len(got)-1].Message)
	}
}

func TestSocketListeners(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	type sent struct {
		id    string
		entry Entry
	}
	var mu sync.Mutex
	var got []sent
	l.SetSocketSink(func(id string, e Entry) {
		mu.Lock()
		got = append(got, sent{id, e})
		mu.Unlock()
	})
	l.AddListener("a", LevelWarn)
	l.AddListener("b", LevelInfo)
	log.Info("info line")
	log.Warn("warn line")
	log.Debug("debug line") // below the global level: nobody receives it
	counts := map[string]int{}
	for _, s := range got {
		counts[s.id]++
		if s.entry.Source == "" || s.entry.Timestamp == "" || s.entry.LevelName == "" {
			t.Fatalf("incomplete payload %+v", s.entry)
		}
	}
	if counts["a"] != 1 || counts["b"] != 2 {
		t.Fatalf("counts = %v", counts)
	}
	l.AddListener("a", LevelInfo) // replaces the level
	l.RemoveListener("b")
	got = nil
	log.Info("again")
	if len(got) != 1 || got[0].id != "a" || got[0].entry.Message != "again" {
		t.Fatalf("after replace/remove: %+v", got)
	}
}

func TestFatalAlwaysReachesListeners(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	n := 0
	l.SetSocketSink(func(string, Entry) { n++ })
	l.AddListener("a", LevelNote)
	log.Error("below listener level")
	logAt(log, LevelFatal, "fatal")
	logAt(log, LevelNote, "note")
	if n != 2 {
		t.Fatalf("deliveries = %d, want FATAL and NOTE", n)
	}
}

func TestStdlibLogAndHTTPErrorLogReachPipeline(t *testing.T) {
	l, _, _, errBuf, _ := newTestLogger(t)
	httpLog := slog.NewLogLogger(l, slog.LevelError)
	httpLog.Print("http: panic serving 1.2.3.4")
	got := l.Entries()
	if len(got) != 1 || got[0].LevelName != "ERROR" || got[0].Message != "http: panic serving 1.2.3.4" || !strings.Contains(errBuf.String(), "ERROR: http: panic") {
		t.Fatalf("entries = %+v stderr = %q", got, errBuf.String())
	}
}

func TestConcurrentLoggingIsRaceFree(t *testing.T) {
	l, log, _, _, _ := newTestLogger(t)
	if err := l.OpenFiles(t.TempDir(), 7); err != nil {
		t.Fatal(err)
	}
	l.SetSocketSink(func(string, Entry) {})
	l.AddListener("a", LevelInfo)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				log.Info("x", "j", j)
				_ = l.Entries()
				l.AddListener("a", LevelWarn)
			}
		}()
	}
	wg.Wait()
	if got := len(l.Entries()); got != 1600 {
		t.Fatalf("entries = %d, want 1600", got)
	}
}
