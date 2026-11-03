package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"audiobookshelf-compat/internal/wire"
)

// TimestampLayout is upstream's date.format(new Date(), 'YYYY-MM-DD HH:mm:ss.SSS')
// (server/Logger.js:22) in the server's local time zone.
const TimestampLayout = "2006-01-02 15:04:05.000"

// Options configures New. The zero value logs to the process's stdout and
// stderr at INFO using the wall clock.
type Options struct {
	Level  Level
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

// SocketEmitter delivers a "log" event to one Socket.IO socket.
type SocketEmitter func(socketID string, entry wire.LogEntry)

// Logger is upstream's Logger singleton plus its LogManager.
type Logger struct {
	level atomic.Int64
	now   func() time.Time

	outMu  sync.Mutex
	stdout io.Writer
	stderr io.Writer

	sockMu    sync.Mutex
	listeners map[string]Level
	emit      SocketEmitter

	files fileStore
}

// New returns an independent logger. Level defaults to INFO (upstream's
// production default, server/Logger.js:12) when opts.Level is zero; pass
// LevelTrace through ChangeLevel to get TRACE.
func New(opts Options) *Logger {
	l := &Logger{now: opts.Now, stdout: opts.Stdout, stderr: opts.Stderr, listeners: map[string]Level{}}
	if l.now == nil {
		l.now = time.Now
	}
	if l.stdout == nil {
		l.stdout = os.Stdout
	}
	if l.stderr == nil {
		l.stderr = os.Stderr
	}
	level := opts.Level
	if level == LevelTrace {
		level = LevelInfo
	}
	l.level.Store(int64(level))
	l.files.init(l.now)
	return l
}

var defaultLogger = New(Options{})

// Default is the process-wide logger (see the package comment).
func Default() *Logger { return defaultLogger }

// Level returns the current minimum level.
func (l *Logger) Level() Level { return Level(l.level.Load()) }

// ChangeLevel is Logger.setLogLevel (server/Logger.js:95): it stores the level
// and then logs `Set Log Level to X` at DEBUG.
func (l *Logger) ChangeLevel(level Level) {
	l.level.Store(int64(level))
	l.write(LevelDebug, "", "Set Log Level to "+level.String(), 2)
}

// BindSockets installs the function used to deliver "log" events to sockets
// registered with AddSocketListener. A rebuilt runtime rebinds it.
func (l *Logger) BindSockets(emit SocketEmitter) {
	l.sockMu.Lock()
	l.emit = emit
	l.sockMu.Unlock()
}

// AddSocketListener is Logger.addSocketListener (server/Logger.js:44); a
// second call for the same socket replaces its level.
func (l *Logger) AddSocketListener(socketID string, level Level) {
	l.sockMu.Lock()
	l.listeners[socketID] = level
	l.sockMu.Unlock()
}

// RemoveSocketListener is Logger.removeSocketListener (server/Logger.js:61).
func (l *Logger) RemoveSocketListener(socketID string) {
	l.sockMu.Lock()
	delete(l.listeners, socketID)
	l.sockMu.Unlock()
}

// write is Logger.#log (server/Logger.js:109) followed by
// #logToFileAndListeners (server/Logger.js:73). depth is the number of stack
// frames between write and the call site reported as the entry's source
// (upstream reports the caller as "File.js:123").
func (l *Logger) write(level Level, source, msg string, depth int) {
	if level < LevelFatal && level < l.Level() {
		return
	}
	now := l.now()
	ts := now.Format(TimestampLayout)

	// Console: console.<method>(`[ts] LEVEL:`, ...args) (server/Logger.js:114).
	line := "[" + ts + "] " + level.String() + ": " + msg + "\n"
	l.outMu.Lock()
	if level == LevelTrace || level == LevelWarn || level == LevelError || level == LevelFatal {
		// Node's console.trace/warn/error write to stderr.
		_, _ = io.WriteString(l.stderr, line)
	} else {
		_, _ = io.WriteString(l.stdout, line)
	}
	l.outMu.Unlock()

	if source == "" {
		source = callerSource(depth)
	}
	entry := wire.LogEntry{Timestamp: ts, Source: source, Message: msg, LevelName: level.String(), Level: int(level)}

	// Sockets listening for log events (server/Logger.js:85).
	l.sockMu.Lock()
	emit := l.emit
	var targets []string
	if emit != nil {
		for id, min := range l.listeners {
			if level >= LevelFatal || level >= min {
				targets = append(targets, id)
			}
		}
	}
	l.sockMu.Unlock()
	for _, id := range targets {
		emit(id, entry)
	}

	// Daily log file and crash log (server/Logger.js:90).
	l.addFile(entry, now)
}

func callerSource(depth int) string {
	_, file, line, ok := runtime.Caller(depth + 1)
	if !ok {
		return "unknown:0"
	}
	return filepath.Base(file) + ":" + strconv.Itoa(line)
}

// join formats args the way upstream does: strings as-is, everything else
// stringified, separated by one space (server/Logger.js:74 and console's
// util.format). Errors contribute their message.
func join(args []any) string {
	if len(args) == 1 {
		if s, ok := args[0].(string); ok {
			return s
		}
	}
	var b bytes.Buffer
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch v := a.(type) {
		case string:
			b.WriteString(v)
		case error:
			b.WriteString(v.Error())
		default:
			fmt.Fprint(&b, v)
		}
	}
	return b.String()
}

// Trace, Debug, ... mirror Logger.trace/debug/info/warn/error/fatal/note.
// Arguments are joined with a space like upstream's variadic API.

func (l *Logger) Trace(args ...any) { l.write(LevelTrace, "", join(args), 2) }
func (l *Logger) Debug(args ...any) { l.write(LevelDebug, "", join(args), 2) }
func (l *Logger) Info(args ...any)  { l.write(LevelInfo, "", join(args), 2) }
func (l *Logger) Warn(args ...any)  { l.write(LevelWarn, "", join(args), 2) }
func (l *Logger) Error(args ...any) { l.write(LevelError, "", join(args), 2) }
func (l *Logger) Fatal(args ...any) { l.write(LevelFatal, "", join(args), 2) }
func (l *Logger) Note(args ...any)  { l.write(LevelNote, "", join(args), 2) }

func (l *Logger) Tracef(f string, a ...any) { l.write(LevelTrace, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Debugf(f string, a ...any) { l.write(LevelDebug, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Infof(f string, a ...any)  { l.write(LevelInfo, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Warnf(f string, a ...any)  { l.write(LevelWarn, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Errorf(f string, a ...any) { l.write(LevelError, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Fatalf(f string, a ...any) { l.write(LevelFatal, "", fmt.Sprintf(f, a...), 2) }
func (l *Logger) Notef(f string, a ...any)  { l.write(LevelNote, "", fmt.Sprintf(f, a...), 2) }

// Package-level shorthands for Default().

func Tracef(f string, a ...any) { defaultLogger.write(LevelTrace, "", fmt.Sprintf(f, a...), 2) }
func Debugf(f string, a ...any) { defaultLogger.write(LevelDebug, "", fmt.Sprintf(f, a...), 2) }
func Infof(f string, a ...any)  { defaultLogger.write(LevelInfo, "", fmt.Sprintf(f, a...), 2) }
func Warnf(f string, a ...any)  { defaultLogger.write(LevelWarn, "", fmt.Sprintf(f, a...), 2) }
func Errorf(f string, a ...any) { defaultLogger.write(LevelError, "", fmt.Sprintf(f, a...), 2) }
func Fatalf(f string, a ...any) { defaultLogger.write(LevelFatal, "", fmt.Sprintf(f, a...), 2) }
func Notef(f string, a ...any)  { defaultLogger.write(LevelNote, "", fmt.Sprintf(f, a...), 2) }

func Trace(args ...any) { defaultLogger.write(LevelTrace, "", join(args), 2) }
func Debug(args ...any) { defaultLogger.write(LevelDebug, "", join(args), 2) }
func Info(args ...any)  { defaultLogger.write(LevelInfo, "", join(args), 2) }
func Warn(args ...any)  { defaultLogger.write(LevelWarn, "", join(args), 2) }
func Error(args ...any) { defaultLogger.write(LevelError, "", join(args), 2) }
func Fatal(args ...any) { defaultLogger.write(LevelFatal, "", join(args), 2) }

// Writer returns an io.Writer that logs each written line at level with the
// given source tag. It adapts the standard library's log package and
// http.Server.ErrorLog to this logger.
func (l *Logger) Writer(level Level, source string) io.Writer {
	return &lineWriter{l: l, level: level, source: source}
}

type lineWriter struct {
	l      *Logger
	level  Level
	source string
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimRight(p, "\r\n"), []byte("\n")) {
		if len(line) > 0 {
			w.l.write(w.level, w.source, string(line), 0)
		}
	}
	return len(p), nil
}
