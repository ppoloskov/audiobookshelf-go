package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const ringSize = 5000

// fileSink is the daily file and crash log, guarded by the logger's mutex.
// The zero value is closed: entries only reach the ring until OpenFiles.
type fileSink struct {
	dir  string // <metadata>/logs; empty while closed
	keep int
	day  string // YYYY-MM-DD of file
	file *os.File
}

// OpenFiles creates <logsDir>/daily, opens today's file, keeps the newest
// keep daily files (keep <= 0 means 7, upstream's `|| 7`) and flushes the
// entries logged so far. Call it once; today's existing entries fill the ring
// first. The file rolls over on the first write after local midnight.
func (l *Logger) OpenFiles(logsDir string, keep int) error {
	if err := os.MkdirAll(filepath.Join(logsDir, "daily"), 0o755); err != nil {
		return fmt.Errorf("create log directories: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.files = fileSink{dir: logsDir, keep: keep}
	if keep <= 0 {
		l.files.keep = 7
	}
	if err := l.files.open(l.now()); err != nil {
		l.files.dir = ""
		return err
	}
	pending := l.ring
	l.ring = loadTail(filepath.Join(logsDir, "daily", l.files.day+".txt"))
	for _, entry := range pending {
		l.files.append(entry)
		l.ring = append(l.ring, entry)
	}
	l.ring = l.ring[max(0, len(l.ring)-ringSize):]
	return nil
}

// Close closes the files; a later entry reopens today's.
func (l *Logger) Close() {
	l.mu.Lock()
	l.files.close()
	l.mu.Unlock()
}

// write appends entry (already in the ring), rolling to a new day first.
func (f *fileSink) write(c *core, entry Entry, now time.Time) {
	if f.dir == "" {
		return
	}
	if day := now.Format("2006-01-02"); f.file == nil || day != f.day {
		if day != f.day {
			c.ring = c.ring[len(c.ring)-1:] // new day, new tail
		}
		f.close()
		if err := f.open(now); err != nil {
			fmt.Fprintln(c.stderr, "[LogManager] cannot open daily log:", err)
			return
		}
	}
	f.append(entry)
}

// append writes one JSON line to the daily file, and FATAL ones to
// crash_logs.txt. A write error is dropped: the console line already exists.
func (f *fileSink) append(entry Entry) {
	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	enc.SetEscapeHTML(false) // JSON.stringify does not escape < > &
	if enc.Encode(entry) != nil {
		return
	}
	_, _ = f.file.Write(line.Bytes())
	if entry.Level == int(LevelFatal) {
		if crash, err := os.OpenFile(filepath.Join(f.dir, "crash_logs.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = crash.Write(line.Bytes())
			_ = crash.Close()
		}
	}
}

// open opens the file of now's day for append and prunes the oldest days.
func (f *fileSink) open(now time.Time) (err error) {
	f.day = now.Format("2006-01-02")
	daily := filepath.Join(f.dir, "daily")
	if f.file, err = os.OpenFile(filepath.Join(daily, f.day+".txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
		return fmt.Errorf("open daily log: %w", err)
	}
	names, _ := filepath.Glob(filepath.Join(daily, "*.txt")) // the pattern is valid
	sort.Strings(names)
	for ; len(names) > f.keep; names = names[1:] {
		_ = os.Remove(names[0]) // a file that stays is retried at the next roll
	}
	return nil
}

func (f *fileSink) close() {
	if f.file != nil {
		_ = f.file.Close() // append-only handle, nothing buffered
		f.file = nil
	}
}

// loadTail returns the last ringSize entries of a daily file; lines that are
// not JSON are skipped, as DailyLog.loadLogs does.
func loadTail(path string) (entries []Entry) {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			entries = append(entries, entry)
		}
	}
	return entries[max(0, len(entries)-ringSize):]
}
