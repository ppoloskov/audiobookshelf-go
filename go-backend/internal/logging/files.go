package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/wire"
)

const (
	// RecentLimit is how many entries GET /api/logger-data returns
	// (LogManager.getMostRecentCurrentDailyLogs, server/managers/LogManager.js:180).
	RecentLimit = 5000
	// DefaultDailyLogsToKeep is upstream's fallback when the setting is falsy
	// (server/managers/LogManager.js:30).
	DefaultDailyLogsToKeep = 7

	dayLayout = "2006-01-02"
	// A log file line is one JSON object; entries can carry long messages.
	maxLineBytes = 16 << 20
)

// fileStore is LogManager plus DailyLog: the daily log directory, the current
// day's file and its in-memory tail, and the crash log.
type fileStore struct {
	mu  sync.Mutex
	now func() time.Time

	root     string // METADATA_PATH once OpenFiles ran
	dailyDir string
	opened   bool
	keep     int

	day   string   // current daily log id, YYYY-MM-DD
	file  *os.File // append handle for day's file
	names []string // sorted *.txt names in the daily dir (LogManager.dailyLogFiles)

	tail    []wire.LogEntry // last RecentLimit entries of the current day
	pending []wire.LogEntry // entries logged before OpenFiles (dailyLogBuffer)

	writeFailed bool
}

func (f *fileStore) init(now func() time.Time) {
	f.now = now
	f.keep = DefaultDailyLogsToKeep
}

// ApplySettings applies the server settings that LogManager reads live:
// loggerDailyLogsToKeep (`|| 7`, LogManager.js:30).
func (l *Logger) ApplySettings(dailyLogsToKeep int) {
	l.files.mu.Lock()
	defer l.files.mu.Unlock()
	if dailyLogsToKeep <= 0 {
		dailyLogsToKeep = DefaultDailyLogsToKeep
	}
	l.files.keep = dailyLogsToKeep
}

// OpenFiles is LogManager.init (server/managers/LogManager.js:48): create
// logs/daily and logs/scans, remove daily logs beyond the keep count, open
// today's file (loading what it already holds) and flush entries logged
// earlier. Fatal entries logged earlier go to the crash log now.
func (l *Logger) OpenFiles(metadataPath string) error {
	f := &l.files
	f.mu.Lock()
	defer f.mu.Unlock()
	dailyDir := filepath.Join(metadataPath, "logs", "daily")
	scanDir := filepath.Join(metadataPath, "logs", "scans")
	for _, dir := range []string{dailyDir, scanDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			err = fmt.Errorf("[LogManager] Failed to create log directories at %q: %w", dailyDir, err)
			fmt.Fprintln(l.stderr, err.Error())
			return err
		}
	}
	f.root, f.dailyDir = metadataPath, dailyDir

	// scanLogFiles: *.txt files, sorted by name.
	f.names = f.names[:0]
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		return fmt.Errorf("read daily log dir: %w", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".txt" {
			f.names = append(f.names, e.Name())
		}
	}
	sort.Strings(f.names)

	// Remove the oldest files beyond the keep count (LogManager.js:56).
	if len(f.names) > f.keep {
		for _, name := range append([]string(nil), f.names[:len(f.names)-f.keep]...) {
			l.removeLogLocked(name)
		}
	}

	now := f.now()
	f.day = now.Format(dayLayout)
	current := f.day + ".txt"
	f.tail = f.tail[:0]
	if containsString(f.names, current) {
		f.tail = loadDailyLog(filepath.Join(dailyDir, current), l)
	} else {
		f.names = append(f.names, current)
	}
	f.opened = true

	pending := f.pending
	f.pending = nil
	for _, e := range pending {
		if e.Level == int(LevelFatal) {
			f.appendCrashLocked(e, l)
		}
		f.appendEntryLocked(e, l)
	}
	return nil
}

// add is LogManager.logToFile (server/managers/LogManager.js:134).
func (l *Logger) addFile(e wire.LogEntry, now time.Time) {
	// Callers hold no logger locks, so file I/O here cannot deadlock the console.
	f := &l.files
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Level == int(LevelFatal) {
		f.appendCrashLocked(e, l)
	}
	if !f.opened {
		f.pending = appendBounded(f.pending, e)
		return
	}
	if today := now.Format(dayLayout); today != f.day {
		// The log rolls to the next day: new file, new in-memory tail.
		f.closeFileLocked()
		f.day = today
		f.tail = f.tail[:0]
		if name := today + ".txt"; !containsString(f.names, name) {
			f.names = append(f.names, name)
			sort.Strings(f.names)
		}
		for len(f.names) > f.keep {
			l.removeLogLocked(f.names[0])
		}
	}
	f.appendEntryLocked(e, l)
}

func (f *fileStore) appendEntryLocked(e wire.LogEntry, l *Logger) {
	f.tail = appendBounded(f.tail, e)
	line, err := marshalLine(e)
	if err == nil {
		err = f.writeLocked(line)
	}
	l.reportWrite(f, "[DailyLog] Append log failed", err)
}

func (f *fileStore) writeLocked(line []byte) error {
	if f.file == nil {
		file, err := os.OpenFile(filepath.Join(f.dailyDir, f.day+".txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		f.file = file
	}
	_, err := f.file.Write(line)
	if err != nil {
		f.closeFileLocked()
	}
	return err
}

func (f *fileStore) closeFileLocked() {
	if f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
}

// appendCrashLocked is LogManager.logCrashToFile (LogManager.js:157).
func (f *fileStore) appendCrashLocked(e wire.LogEntry, l *Logger) {
	if f.root == "" {
		return
	}
	line, err := marshalLine(e)
	if err == nil {
		dir := filepath.Join(f.root, "logs")
		if err = os.MkdirAll(dir, 0o755); err == nil {
			var file *os.File
			file, err = os.OpenFile(filepath.Join(dir, "crash_logs.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				_, err = file.Write(line)
				if cerr := file.Close(); err == nil {
					err = cerr
				}
			}
		}
	}
	l.reportWrite(f, "[LogManager] Appended crash log", err)
}

// removeLogLocked is LogManager.removeLogFile (LogManager.js:96).
func (l *Logger) removeLogLocked(name string) {
	f := &l.files
	full := filepath.Join(f.dailyDir, name)
	drop := func() {
		for i, n := range f.names {
			if n == name {
				f.names = append(f.names[:i], f.names[i+1:]...)
				return
			}
		}
	}
	if _, err := os.Stat(full); err != nil {
		drop()
		l.consoleOnly(LevelError, "[LogManager] Invalid log dne "+full)
		return
	}
	if err := os.Remove(full); err != nil {
		l.consoleOnly(LevelError, "[LogManager] Failed to unlink log file "+full)
		return
	}
	drop()
	l.consoleOnly(LevelInfo, "[LogManager] Removed daily log: "+name)
}

// consoleOnly prints a line while the file store is locked. The same lines are
// logged through Logger.error/info upstream; here they skip the file so that a
// failing log directory cannot recurse into itself.
func (l *Logger) consoleOnly(level Level, msg string) {
	if level < l.Level() {
		return
	}
	line := "[" + l.now().Format(TimestampLayout) + "] " + level.String() + ": " + msg + "\n"
	l.outMu.Lock()
	defer l.outMu.Unlock()
	if level == LevelError {
		_, _ = l.stderr.Write([]byte(line))
		return
	}
	_, _ = l.stdout.Write([]byte(line))
}

// reportWrite prints a failure to the console once per outage, like
// upstream's console.log('[DailyLog] Append log failed', error) but without
// flooding the output.
func (l *Logger) reportWrite(f *fileStore, what string, err error) {
	if err == nil {
		f.writeFailed = false
		return
	}
	if f.writeFailed {
		return
	}
	f.writeFailed = true
	l.outMu.Lock()
	defer l.outMu.Unlock()
	fmt.Fprintf(l.stderr, "%s %v\n", what, err)
}

// Recent is LogManager.getMostRecentCurrentDailyLogs
// (server/managers/LogManager.js:179): the last RecentLimit entries of the
// current daily log, oldest first. The slice is never nil.
func (l *Logger) Recent() []wire.LogEntry {
	f := &l.files
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.tail
	if !f.opened {
		src = f.pending
	}
	out := make([]wire.LogEntry, len(src))
	copy(out, src)
	return out
}

func (l *Logger) LoggerData() wire.LoggerData {
	return wire.LoggerData{CurrentDailyLogs: l.Recent()}
}

// Close releases the open daily file handle; later entries reopen it.
func (l *Logger) Close() {
	l.files.mu.Lock()
	l.files.closeFileLocked()
	l.files.mu.Unlock()
}

func appendBounded(s []wire.LogEntry, e wire.LogEntry) []wire.LogEntry {
	if len(s) >= RecentLimit {
		copy(s, s[len(s)-RecentLimit+1:])
		s = s[:RecentLimit-1]
	}
	return append(s, e)
}

func marshalLine(e wire.LogEntry) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // JSON.stringify does not escape <, > or &
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode appends the newline
}

// loadDailyLog is DailyLog.loadLogs (server/objects/DailyLog.js:96): parse
// the file's JSON lines, and rewrite the file without unparseable lines.
func loadDailyLog(path string, l *Logger) []wire.LogEntry {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	var logs []wire.LogEntry
	bad := false
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			bad = true
			continue
		}
		var e wire.LogEntry
		if json.Unmarshal([]byte(text), &e) != nil {
			bad = true
			continue
		}
		logs = append(logs, e)
	}
	_ = file.Close()
	if bad {
		var buf bytes.Buffer
		for _, e := range logs {
			line, _ := marshalLine(e)
			buf.Write(line)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err == nil {
			l.consoleOnly(LevelInfo, "Re-Saved log file to remove bad lines")
		}
	}
	if len(logs) > RecentLimit {
		logs = append([]wire.LogEntry(nil), logs[len(logs)-RecentLimit:]...)
	}
	return logs
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
