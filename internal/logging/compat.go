// Printf-style helpers over the slog handler kept for remaining callers in
// internal/server and internal/admin. New code logs with log/slog.
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

// Errorf logs at ERROR.
func Errorf(format string, args ...any) { output(LevelError, fmt.Sprintf(format, args...)) }

// Trace logs msg at TRACE.
func Trace(msg string) { output(LevelTrace, msg) }

// Debug logs msg at DEBUG.
func Debug(msg string) { output(LevelDebug, msg) }

// Info logs msg at INFO.
func Info(msg string) { output(LevelInfo, msg) }

// RecoverRequest logs a handler panic and its stack at ERROR.
func RecoverRequest(r *http.Request, recovered any) {
	path := "? ?"
	if r != nil && r.URL != nil {
		path = r.Method + " " + r.URL.Path
	}
	output(LevelError, fmt.Sprintf("[HTTP] %s: panic: %v\n%s", path, recovered, debug.Stack()))
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
