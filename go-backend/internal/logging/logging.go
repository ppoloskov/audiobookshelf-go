// Package logging is the process-wide log pipeline: one slog.Handler that
// writes upstream's console line, keeps the last 5000 entries of the day for
// GET /api/logger-data, appends JSON lines to the daily files under
// <metadata>/logs and forwards entries to Socket.IO log listeners.
package logging

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is an upstream LogLevel (server/utils/constants.js:19), 0 to 6. Its
// slog value is (level-INFO)*4: TRACE and DEBUG sit below slog.LevelInfo,
// FATAL and NOTE above slog.LevelError.
type Level int

// Upstream's seven levels.
const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
	LevelNote
)

// String returns the upstream level name, or "UNKNOWN".
func (l Level) String() string {
	if l < LevelTrace || l > LevelNote {
		return "UNKNOWN"
	}
	return [...]string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL", "NOTE"}[l]
}

func (l Level) slog() slog.Level { return slog.Level((l - LevelInfo) * 4) }

// levelOf maps a slog level onto 0..6, rounding down between the steps.
func levelOf(level slog.Level) Level {
	return min(max(Level(math.Floor(float64(level)/4))+LevelInfo, LevelTrace), LevelNote)
}

// Entry is the persisted and client-visible log object: one line of the daily
// file, an element of currentDailyLogs and the Socket.IO "log" payload.
type Entry struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	LevelName string `json:"levelName"`
	Level     int    `json:"level"`
}

// SocketSink delivers an entry to the Socket.IO socket with the given id.
type SocketSink func(socketID string, entry Entry)

// Options configures New; nil fields mean os.Stdout, os.Stderr, time.Now.
type Options struct {
	Stdout, Stderr io.Writer
	Now            func() time.Time
}

// Logger is a slog.Handler. Loggers made by WithAttrs and WithGroup share the
// state of their parent, which one mutex guards. The minimum level starts at
// INFO; FATAL and NOTE are never filtered (server/Logger.js:112).
type Logger struct {
	*core
	prefix, attrs string // group prefix and rendered attrs of With/WithGroup
}

type core struct {
	level          slog.LevelVar
	now            func() time.Time
	stdout, stderr io.Writer

	mu        sync.Mutex
	listeners map[string]Level
	sink      SocketSink
	ring      []Entry // last ringSize entries of the current day
	files     fileSink
}

var defaultLogger = New(Options{})

// New returns an independent logger.
func New(opts Options) *Logger {
	c := &core{now: opts.Now, stdout: cmp.Or(opts.Stdout, io.Writer(os.Stdout)), stderr: cmp.Or(opts.Stderr, io.Writer(os.Stderr)), listeners: map[string]Level{}}
	if c.now == nil {
		c.now = time.Now
	}
	c.level.Set(LevelInfo.slog())
	return &Logger{core: c}
}

// Default returns the logger that the package-level helpers write to.
func Default() *Logger { return defaultLogger }

// Level returns the minimum level.
func (l *Logger) Level() Level { return levelOf(l.level.Level()) }

// SetLevel sets the minimum level, then logs "Set Log Level to X" at DEBUG
// (Logger.setLogLevel, server/Logger.js:95).
func (l *Logger) SetLevel(level Level) {
	l.level.Set(level.slog())
	slog.New(l).Debug("Set Log Level to " + level.String())
}

// SetSocketSink installs the receiver of entries for registered listeners
// (called outside the logger's lock); nil disables delivery.
func (l *Logger) SetSocketSink(sink SocketSink) {
	l.mu.Lock()
	l.sink = sink
	l.mu.Unlock()
}

// AddListener registers a socket for entries at or above level, replacing its
// earlier level. FATAL entries always reach it.
func (l *Logger) AddListener(socketID string, level Level) {
	l.mu.Lock()
	l.listeners[socketID] = level
	l.mu.Unlock()
}

// RemoveListener stops delivery to the socket.
func (l *Logger) RemoveListener(socketID string) {
	l.mu.Lock()
	delete(l.listeners, socketID)
	l.mu.Unlock()
}

// Entries returns a copy of the last 5000 entries of the current day, oldest
// first; never nil.
func (l *Logger) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry{}, l.ring...)
}

// Enabled implements slog.Handler.
func (l *Logger) Enabled(_ context.Context, level slog.Level) bool {
	return level >= l.level.Level() || levelOf(level) >= LevelFatal
}

// WithAttrs implements slog.Handler.
func (l *Logger) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(l.attrs)
	for _, attr := range attrs {
		appendAttr(&b, l.prefix, attr)
	}
	return &Logger{l.core, l.prefix, b.String()}
}

// WithGroup implements slog.Handler; group names prefix later keys.
func (l *Logger) WithGroup(name string) slog.Handler {
	if name == "" {
		return l
	}
	return &Logger{l.core, l.prefix + name + ".", l.attrs}
}

// Handle implements slog.Handler. The message is followed by the attrs as
// " key=value"; the source is the record's call site.
func (l *Logger) Handle(_ context.Context, record slog.Record) error {
	var b strings.Builder
	b.WriteString(record.Message + l.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		appendAttr(&b, l.prefix, attr)
		return true
	})
	level, now := levelOf(record.Level), l.now()
	entry := Entry{Timestamp: now.Format("2006-01-02 15:04:05.000"), Source: "unknown:0", Message: b.String(), LevelName: level.String(), Level: int(level)}
	if record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		entry.Source = filepath.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
	}

	l.mu.Lock()
	out := l.stdout
	if level != LevelDebug && level != LevelInfo && level != LevelNote {
		out = l.stderr // Node's console.trace/warn/error
	}
	_, _ = io.WriteString(out, "["+entry.Timestamp+"] "+entry.LevelName+": "+entry.Message+"\n") // a broken console is not actionable
	l.ring = append(l.ring, entry)
	l.ring = l.ring[max(0, len(l.ring)-ringSize):]
	l.files.write(l.core, entry, now)
	var ids []string
	for id, listenerLevel := range l.listeners {
		if level >= LevelFatal || level >= listenerLevel {
			ids = append(ids, id)
		}
	}
	sink := l.sink
	l.mu.Unlock()
	for _, id := range ids {
		if sink != nil {
			sink(id, entry)
		}
	}
	return nil
}

// appendAttr writes " key=value", expanding groups; errors print their text.
func appendAttr(b *strings.Builder, prefix string, attr slog.Attr) {
	value := attr.Value.Resolve()
	if attr.Key == "" && value.Kind() != slog.KindGroup {
		return // slog drops attrs without a key
	}
	if value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			prefix += attr.Key + "."
		}
		for _, member := range value.Group() {
			appendAttr(b, prefix, member)
		}
		return
	}
	b.WriteString(" " + prefix + attr.Key + "=")
	if err, ok := value.Any().(error); ok && err != nil {
		b.WriteString(err.Error())
		return
	}
	b.WriteString(value.String())
}
