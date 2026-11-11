// Transitional printf helpers for packages not yet rewritten; delete when unused.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// Tracef logs at TRACE to the default logger; the source is the caller.
func Tracef(format string, args ...any) { output(LevelTrace, fmt.Sprintf(format, args...)) }

// Debugf logs at DEBUG.
func Debugf(format string, args ...any) { output(LevelDebug, fmt.Sprintf(format, args...)) }

// Infof logs at INFO.
func Infof(format string, args ...any) { output(LevelInfo, fmt.Sprintf(format, args...)) }

// Warnf logs at WARN.
func Warnf(format string, args ...any) { output(LevelWarn, fmt.Sprintf(format, args...)) }

// Errorf logs at ERROR.
func Errorf(format string, args ...any) { output(LevelError, fmt.Sprintf(format, args...)) }

// Fatalf logs at FATAL (also written to the crash log).
func Fatalf(format string, args ...any) { output(LevelFatal, fmt.Sprintf(format, args...)) }

// Trace logs msg at TRACE.
func Trace(msg string) { output(LevelTrace, msg) }

// Debug logs msg at DEBUG.
func Debug(msg string) { output(LevelDebug, msg) }

// Info logs msg at INFO.
func Info(msg string) { output(LevelInfo, msg) }

// Warn logs msg at WARN.
func Warn(msg string) { output(LevelWarn, msg) }

// Error logs msg at ERROR.
func Error(msg string) { output(LevelError, msg) }

// RequestError logs a 5xx cause at ERROR with the route (path only: the query
// can carry a token).
func RequestError(r *http.Request, what string, err error) {
	if err != nil {
		what += ": " + err.Error()
	}
	output(LevelError, route(r)+": "+what)
}

// RequestDebug logs at DEBUG why a handler deliberately answers 500.
func RequestDebug(r *http.Request, why string) { output(LevelDebug, route(r)+": "+why) }

// RecoverRequest logs a handler panic and its stack at ERROR.
func RecoverRequest(r *http.Request, recovered any) {
	output(LevelError, fmt.Sprintf("%s: panic: %v\n%s", route(r), recovered, debug.Stack()))
}

func route(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "[HTTP] ? ?"
	}
	return "[HTTP] " + r.Method + " " + r.URL.Path
}

// output logs to the default logger; the source is the caller of the helper.
func output(level Level, msg string) {
	if !defaultLogger.Enabled(context.Background(), level.slog()) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(3, pcs[:])                                                                             // Callers, output, helper
	_ = defaultLogger.Handle(context.Background(), slog.NewRecord(time.Time{}, level.slog(), msg, pcs[0])) // Handle never fails
}
