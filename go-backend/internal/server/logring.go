package server

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// runtimeLogBuffer records actual Go logger output for the log-data API and
// socket listeners. It is bounded so long-running servers do not grow memory
// with every message.
var runtimeLogBuffer = newLogRing(2000)
var installLogOutput sync.Once

type logRing struct {
	mu      sync.RWMutex
	entries []map[string]any
	limit   int
	notify  func(map[string]any)
}

func newLogRing(limit int) *logRing { return &logRing{limit: limit} }

func (b *logRing) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\r\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		entry := map[string]any{"timestamp": time.Now().Format(time.RFC3339Nano), "source": "go-backend", "message": strings.TrimSpace(line), "levelName": "info", "level": 3}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "fatal") {
			entry["levelName"], entry["level"] = "fatal", 0
		} else if strings.Contains(lower, "error") {
			entry["levelName"], entry["level"] = "error", 1
		} else if strings.Contains(lower, "warn") {
			entry["levelName"], entry["level"] = "warning", 2
		}
		b.mu.Lock()
		b.entries = append(b.entries, entry)
		if len(b.entries) > b.limit {
			copy(b.entries, b.entries[len(b.entries)-b.limit:])
			b.entries = b.entries[:b.limit]
		}
		notify := b.notify
		b.mu.Unlock()
		if notify != nil {
			notify(entry)
		}
	}
	return len(p), nil
}

func (b *logRing) Snapshot() any {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]any, len(b.entries))
	for i, e := range b.entries {
		copyEntry := make(map[string]any, len(e))
		for k, v := range e {
			copyEntry[k] = v
		}
		result[i] = copyEntry
	}
	return result
}

func (b *logRing) SetNotify(fn func(map[string]any)) { b.mu.Lock(); b.notify = fn; b.mu.Unlock() }

func installLogRing() {
	installLogOutput.Do(func() {
		old := log.Writer()
		if old == nil {
			old = os.Stderr
		}
		log.SetOutput(io.MultiWriter(old, runtimeLogBuffer))
	})
}
