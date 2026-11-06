//go:build tsdiagnostic

package logging

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTSRepresentationR05 retains former JSON member ordering and adjacent
// fatal-log fingerprints as an opt-in diagnostic.
func TestTSRepresentationR05(t *testing.T) {
	l, _, _, _ := newTestLogger(t)
	b, _ := json.Marshal(l.LoggerData())
	if string(b) != `{"currentDailyLogs":[]}` {
		t.Fatalf("empty = %s", b)
	}
	l.Info("one")
	b, _ = json.Marshal(l.LoggerData())
	re := regexp.MustCompile(`^\{"currentDailyLogs":\[\{"timestamp":"[^\"]+","source":"[^\"]+","message":"one","levelName":"INFO","level":2\}\]\}$`)
	if !re.Match(b) {
		t.Fatalf("one = %s", b)
	}

	daily, _, _, _ := newTestLogger(t)
	dir := t.TempDir()
	if err := daily.OpenFiles(dir); err != nil {
		t.Fatal(err)
	}
	daily.Info("a <b> & c")
	line := readLines(t, filepath.Join(dir, "logs", "daily", "2026-03-04.txt"))[0]
	dailyRE := regexp.MustCompile(`^\{"timestamp":"2026-03-04 05:06:07\.089","source":"[^\"]+","message":"a <b> & c","levelName":"INFO","level":2\}$`)
	if !dailyRE.MatchString(line) {
		t.Fatalf("line = %s", line)
	}

	crash, _, _, _ := newTestLogger(t)
	crashDir := t.TempDir()
	crash.Fatal("before open")
	if err := crash.OpenFiles(crashDir); err != nil {
		t.Fatal(err)
	}
	crash.Error("not a crash")
	crash.Fatal("boom")
	crashLines := readLines(t, filepath.Join(crashDir, "logs", "crash_logs.txt"))
	if len(crashLines) != 2 || !strings.Contains(crashLines[0], "before open") || !strings.Contains(crashLines[1], `"levelName":"FATAL","level":5`) {
		t.Fatalf("crash log = %q", crashLines)
	}
}
