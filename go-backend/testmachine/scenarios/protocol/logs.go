package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// logLevels is the upstream LogLevel constant map.
var logLevels = map[string]float64{"TRACE": 0, "DEBUG": 1, "INFO": 2, "WARN": 3, "ERROR": 4, "FATAL": 5, "NOTE": 6}

var (
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$`)
	logSource    = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.js:\d+$`)
)

// logRecord is one daily log object and one `log` socket payload.
type logRecord struct {
	Timestamp string  `json:"timestamp"`
	Source    string  `json:"source"`
	Message   string  `json:"message"`
	LevelName string  `json:"levelName"`
	Level     float64 `json:"level"`
}

// dailyDir is the daily log directory of the install.
func dailyDir(e *harness.Env) string {
	return filepath.Join(e.Metadata, "logs", "daily")
}

// todayLogName is the UTC daily log file name.
func todayLogName() string {
	return time.Now().UTC().Format("2006-01-02") + ".txt"
}

// logLines reads a daily file's non-empty lines.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// wantValidLogLine asserts one line is an upstream log object.
func wantValidLogLine(t *testing.T, line, where string) logRecord {
	t.Helper()
	var record logRecord
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("%s: line is not JSON: %v: %.120s", where, err, line)
	}
	if !logTimestamp.MatchString(record.Timestamp) {
		t.Fatalf("%s: timestamp %q is not YYYY-MM-DD HH:mm:ss.SSS", where, record.Timestamp)
	}
	if !logSource.MatchString(record.Source) {
		t.Fatalf("%s: source %q is not File.js:line", where, record.Source)
	}
	if record.Message == "" {
		t.Fatalf("%s: empty message", where)
	}
	wantLevel, ok := logLevels[record.LevelName]
	if !ok {
		t.Fatalf("%s: levelName %q is not a LogLevel", where, record.LevelName)
	}
	if record.Level != wantLevel {
		t.Fatalf("%s: level %v does not match levelName %s", where, record.Level, record.LevelName)
	}
	return record
}

// waitDailyFile waits until the file exists and satisfies valid.
func waitDailyFile(t *testing.T, path, what string, valid func(lines []string) bool) []string {
	t.Helper()
	var lines []string
	harness.Eventually(t, 15*time.Second, what, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		lines = lines[:0]
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		return valid(lines)
	})
	return lines
}

// containsMessage reports whether any line carries the message text.
func containsMessage(lines []string, text string) bool {
	for _, line := range lines {
		var record logRecord
		if json.Unmarshal([]byte(line), &record) == nil && strings.Contains(record.Message, text) {
			return true
		}
	}
	return false
}

// loggerData reads the admin logger-data payload as records.
func loggerData(t *testing.T, e *harness.Env, token string) []logRecord {
	t.Helper()
	resp := e.Get(t, token, "/api/logger-data")
	wantStatus(t, resp, 200, "logger-data")
	var body struct {
		CurrentDailyLogs []logRecord `json:"currentDailyLogs"`
	}
	resp.JSON(t, &body)
	return body.CurrentDailyLogs
}

// waitLoggerMessage waits until the in-memory daily logs contain text.
func waitLoggerMessage(t *testing.T, e *harness.Env, token, text string) logRecord {
	t.Helper()
	var found logRecord
	harness.Eventually(t, 15*time.Second, "logger-data record "+text, func() bool {
		for _, record := range loggerData(t, e, token) {
			if strings.Contains(record.Message, text) {
				found = record
				return true
			}
		}
		return false
	})
	return found
}

// dailyLogScenarios covers the daily log file format and the admin read.
func dailyLogScenarios() []harness.Scenario {
	const req = "format-daily-logs"
	const inv = "INV-FORMAT-DAILY-LOGS"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "daily-log-file-and-admin-read-keep-the-record-shape",
			Sources: []string{"server/Logger.js:73-115", "server/managers/LogManager.js:1-120", "server/objects/DailyLog.js:1-110", "server/controllers/MiscController.js:777-786"},
			Claim:   "A logged request appends one line to metadata/logs/daily/<UTC-date>.txt; every line is a JSON object with string timestamp YYYY-MM-DD HH:mm:ss.SSS, string source File.js:line, non-empty string message, levelName in the LogLevel set and the matching numeric level; GET /api/logger-data returns currentDailyLogs as an array of records with that shape, newest last.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				e.Get(t, "", "/ping")
				path := filepath.Join(dailyDir(e), todayLogName())
				lines := waitDailyFile(t, path, "ping log line", func(lines []string) bool {
					return containsMessage(lines, "Received ping")
				})
				for i, line := range lines {
					wantValidLogLine(t, line, fmt.Sprintf("%s line %d", path, i+1))
				}
				records := waitLoggerMessage(t, e, token, "Received ping")
				if records.LevelName != "INFO" || records.Level != logLevels["INFO"] {
					t.Fatalf("ping record = %+v, want INFO/2", records)
				}
				all := loggerData(t, e, token)
				if len(all) < 2 {
					t.Fatalf("logger-data returned %d records", len(all))
				}
				for i, record := range all {
					wantValidLogLine(t, fmt.Sprintf(`{"timestamp":%q,"source":%q,"message":%q,"levelName":%q,"level":%v}`,
						record.Timestamp, record.Source, record.Message, record.LevelName, record.Level), fmt.Sprintf("logger-data record %d", i))
				}
				if all[len(all)-1].Timestamp < all[0].Timestamp {
					t.Fatalf("logger-data is not oldest-first: first %q last %q", all[0].Timestamp, all[len(all)-1].Timestamp)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "repeat", Assertion: "repeat-requests-append-lines-and-records",
			Sources: []string{"server/managers/LogManager.js:118-160", "server/objects/DailyLog.js:60-80"},
			Claim:   "Three more logged requests append three more valid lines to the same daily file and three more records to logger-data in the same oldest-first order; the file is appended, not replaced.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				path := filepath.Join(dailyDir(e), todayLogName())
				before := logLines(t, path)
				beforePings := countMessages(before, "Received ping")
				for range 3 {
					e.Get(t, "", "/ping")
				}
				lines := waitDailyFile(t, path, "three more ping lines", func(lines []string) bool {
					return countMessages(lines, "Received ping") >= beforePings+3
				})
				if len(lines) < len(before) {
					t.Fatalf("daily log shrank from %d to %d lines", len(before), len(lines))
				}
				for i, line := range lines {
					wantValidLogLine(t, line, fmt.Sprintf("appended line %d", i+1))
				}
				harness.Eventually(t, 15*time.Second, "logger-data pings", func() bool {
					return countRecords(loggerData(t, e, token), "Received ping") >= beforePings+3
				})
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "non-admin-logger-data-is-forbidden",
			Sources: []string{"server/controllers/MiscController.js:777-786"},
			Claim:   "GET /api/logger-data as the non-admin listener answers 403 with no currentDailyLogs records.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "listener"), "/api/logger-data")
				wantStatus(t, resp, 403, "non-admin logger-data")
				if strings.Contains(string(resp.Body), "currentDailyLogs") || strings.Contains(string(resp.Body), `"timestamp"`) {
					t.Fatalf("403 body carried log records: %.200s", resp.Body)
				}
			},
		},
	}
}

// dailyLogLifecycleScenarios covers loading, retention and corrupt lines.
func dailyLogLifecycleScenarios() []harness.Scenario {
	const req = "format-daily-logs"
	const inv = "INV-FORMAT-DAILY-LOGS-LIFECYCLE-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "failure", Assertion: "restart-drops-corrupt-lines-and-keeps-valid-ones",
			Sources: []string{"server/objects/DailyLog.js:96-135", "server/managers/LogManager.js:52-88"},
			Claim:   "On start the manager reads today's existing file, keeps the valid JSON records (they appear in currentDailyLogs) and rewrites the file without an unparsable line, then continues appending valid records.",
			Run: func(t *testing.T, e *harness.Env) {
				path := filepath.Join(dailyDir(e), todayLogName())
				seeded := `{"timestamp":"2001-02-03 04:05:06.789","source":"Seeded.js:1","message":"seeded valid record","levelName":"INFO","level":2}`
				e.Stop(t)
				appendLine(t, path, seeded)
				appendLine(t, path, "{not json at all")
				e.Restart(t)
				token := e.Token(t, "root")
				e.Get(t, "", "/ping")
				lines := waitDailyFile(t, path, "rewritten daily log", func(lines []string) bool {
					if !containsMessage(lines, "seeded valid record") || !containsMessage(lines, "Received ping") {
						return false
					}
					for _, line := range lines {
						var record logRecord
						if json.Unmarshal([]byte(line), &record) != nil {
							return false
						}
					}
					return true
				})
				if slices.Contains(lines, "{not json at all") {
					t.Fatal("restart kept the corrupt log line")
				}
				for i, line := range lines {
					wantValidLogLine(t, line, fmt.Sprintf("reloaded line %d", i+1))
				}
				record := waitLoggerMessage(t, e, token, "seeded valid record")
				if record.Source != "Seeded.js:1" || record.Timestamp != "2001-02-03 04:05:06.789" {
					t.Fatalf("loaded record = %+v", record)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "changed", Assertion: "retention-keeps-the-newest-daily-files",
			Sources: []string{"server/managers/LogManager.js:35-70", "server/objects/settings/ServerSettings.js:62"},
			Claim:   "Starting with eleven daily files keeps the seven newest, removes the four oldest, and creates/appends today's file for new records; every kept file still holds valid records.",
			Run: func(t *testing.T, e *harness.Env) {
				e.Stop(t)
				dir := dailyDir(e)
				names, err := dailyLogNames(dir)
				if err != nil {
					t.Fatalf("list daily logs: %v", err)
				}
				for _, name := range names {
					if err := os.Remove(filepath.Join(dir, name)); err != nil {
						t.Fatalf("remove daily log %s: %v", name, err)
					}
				}
				seeded := make([]string, 0, 11)
				for day := 1; day <= 11; day++ {
					name := fmt.Sprintf("2000-01-%02d.txt", day)
					appendLine(t, filepath.Join(dir, name), `{"timestamp":"2000-01-`+fmt.Sprintf("%02d", day)+` 00:00:00.000","source":"Seeded.js:1","message":"old record `+strconv.Itoa(day)+`","levelName":"INFO","level":2}`)
					seeded = append(seeded, name)
				}
				e.Restart(t)
				e.Get(t, "", "/ping")
				today := filepath.Join(dir, todayLogName())
				waitDailyFile(t, today, "today's log after retention", func(lines []string) bool {
					return containsMessage(lines, "Received ping")
				})
				kept, err := dailyLogNames(dir)
				if err != nil {
					t.Fatalf("list kept daily logs: %v", err)
				}
				want := append([]string{todayLogName()}, seeded[len(seeded)-7:]...)
				slices.Sort(want)
				if !slices.Equal(kept, want) {
					t.Fatalf("kept daily logs = %v, want %v", kept, want)
				}
				for _, name := range kept {
					for i, line := range logLines(t, filepath.Join(dir, name)) {
						wantValidLogLine(t, line, fmt.Sprintf("%s line %d", name, i+1))
					}
				}
			},
		},
	}
}

// appendLine appends one line to a file, creating it.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.WriteString(line + "\n"); err != nil {
		_ = file.Close() // the write error is the failure under test
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// dailyLogNames lists the .txt files of a directory, sorted.
func dailyLogNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".txt") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// countMessages counts parsed lines whose message contains text.
func countMessages(lines []string, text string) int {
	count := 0
	for _, line := range lines {
		var record logRecord
		if json.Unmarshal([]byte(line), &record) == nil && strings.Contains(record.Message, text) {
			count++
		}
	}
	return count
}

// countRecords counts in-memory records whose message contains text.
func countRecords(records []logRecord, text string) int {
	count := 0
	for _, record := range records {
		if strings.Contains(record.Message, text) {
			count++
		}
	}
	return count
}
