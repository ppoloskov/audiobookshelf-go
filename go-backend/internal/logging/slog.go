package logging

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// SlogHandler returns a slog.Handler that forwards every record to the
// package's default logger: console, daily log file, in-memory tail and socket
// listeners. The message is followed by the attributes as " key=value" pairs
// (an error prints its Error() text, group names prefix their keys with "."),
// and the entry's source is the slog call site. Enabled follows the logger's
// current level.
func SlogHandler() slog.Handler { return &slogHandler{logger: defaultLogger} }

type slogHandler struct {
	logger *Logger
	prefix string // "group." for the groups opened with WithGroup
	attrs  string // preformatted " key=value" pairs from WithAttrs
}

// slogLevel maps a slog level onto the logger's DEBUG, INFO, WARN or ERROR.
func slogLevel(level slog.Level) Level {
	switch {
	case level < slog.LevelInfo:
		return LevelDebug
	case level < slog.LevelWarn:
		return LevelInfo
	case level < slog.LevelError:
		return LevelWarn
	}
	return LevelError
}

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return slogLevel(level) >= h.logger.Level()
}

func (h *slogHandler) Handle(_ context.Context, record slog.Record) error {
	var b strings.Builder
	b.WriteString(record.Message)
	b.WriteString(h.attrs)
	record.Attrs(func(attr slog.Attr) bool {
		appendAttr(&b, h.prefix, attr)
		return true
	})
	h.logger.write(slogLevel(record.Level), recordSource(record), b.String(), 0)
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, attr := range attrs {
		appendAttr(&b, h.prefix, attr)
	}
	return &slogHandler{logger: h.logger, prefix: h.prefix, attrs: b.String()}
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &slogHandler{logger: h.logger, prefix: h.prefix + name + ".", attrs: h.attrs}
}

// appendAttr writes " key=value", expanding a group attribute into its members.
func appendAttr(b *strings.Builder, prefix string, attr slog.Attr) {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			prefix += attr.Key + "."
		}
		for _, member := range value.Group() {
			appendAttr(b, prefix, member)
		}
		return
	}
	if attr.Key == "" {
		return
	}
	b.WriteByte(' ')
	b.WriteString(prefix + attr.Key)
	b.WriteByte('=')
	if err, ok := value.Any().(error); ok && err != nil {
		b.WriteString(err.Error())
		return
	}
	b.WriteString(value.String())
}

// recordSource is the "file.go:123" of the slog call site (the record's PC).
func recordSource(record slog.Record) string {
	if record.PC == 0 {
		return "unknown:0"
	}
	frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
	return filepath.Base(frame.File) + ":" + strconv.Itoa(frame.Line)
}
