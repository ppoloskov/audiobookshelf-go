package admin_files

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	loggerPath = "/api/logger-data"

	loggerRoute = "server/routers/ApiRouter.js:361"
	loggerSrc   = "server/controllers/MiscController.js:777-786"
	loggerStore = "server/managers/LogManager.js:179-183"
	dailyLogSrc = "server/objects/DailyLog.js:29-72"
	loggerUISrc = "client/pages/config/log.vue:144-158"

	// fixtureDailyLogDate is the only daily log file in the fixture; the route
	// must list the running server's current log, not that older file.
	fixtureDailyLogDate = "2026-11-03"
)

// timestampLayout is the format the logger writes for every entry.
const timestampLayout = "2006-01-02 15:04:05.000"

var timestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}$`)

// dailyLogLine is one entry of the current daily log.
type dailyLogLine struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	LevelName string `json:"levelName"`
	Level     int    `json:"level"`
}

// loggerData reads GET /api/logger-data and returns the entries.
func loggerData(t *testing.T, e *harness.Env, token string) (harness.Response, []dailyLogLine) {
	t.Helper()
	resp := e.Get(t, token, loggerPath)
	var payload struct {
		CurrentDailyLogs []dailyLogLine `json:"currentDailyLogs"`
	}
	if resp.Status == http.StatusOK {
		resp.JSON(t, &payload)
	}
	return resp, payload.CurrentDailyLogs
}

// wantLogLines checks the shape of every listed entry.
func wantLogLines(t *testing.T, what string, lines []dailyLogLine) {
	t.Helper()
	for i, line := range lines {
		if !timestampPattern.MatchString(line.Timestamp) {
			t.Fatalf("%s[%d] timestamp = %q, want %q", what, i, line.Timestamp, timestampLayout)
		}
		if line.Source == "" || line.LevelName == "" || line.Level <= 0 {
			t.Fatalf("%s[%d] is not a complete log entry: %+v", what, i, line)
		}
		if i > 0 && line.Timestamp < lines[i-1].Timestamp {
			t.Fatalf("%s[%d] timestamp %q is older than %q", what, i, line.Timestamp, lines[i-1].Timestamp)
		}
	}
}

func loggerScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario("route-r218", "INV-ROUTE-R218-LISTING", "normal", "stateless", "logger-data-lists-the-current-daily-log",
			"GET /api/logger-data as an admin answers 200 with one key, currentDailyLogs, an array of log entries (timestamp, source, message, levelName, level) that is not empty and grows after a request that logs an error.",
			[]string{loggerRoute, loggerSrc, loggerStore, dailyLogSrc, loggerUISrc}, func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				resp, lines := loggerData(t, e, root)
				wantStatus(t, "logger data", resp, http.StatusOK)
				exactKeys(t, decode(resp), "currentDailyLogs")
				if len(lines) == 0 {
					t.Fatal("currentDailyLogs is empty; the running server must have logged")
				}
				wantLogLines(t, "currentDailyLogs", lines)
				before := len(lines)
				// An invalid year makes StatsController log an error.
				wantStatus(t, "error request", e.Get(t, root, statsYearPath+"nope"), http.StatusBadRequest)
				var grown []dailyLogLine
				harness.Eventually(t, 10*time.Second, "the error to appear in the daily log", func() bool {
					_, grown = loggerData(t, e, root)
					return len(grown) > before
				})
				wantLogLines(t, "currentDailyLogs after an error", grown)
				errLogged := false
				for _, line := range grown[before:] {
					if line.LevelName == "ERROR" && line.Level == 4 {
						errLogged = true
					}
				}
				if !errLogged {
					t.Fatalf("no ERROR entry appended after the rejected request: %+v", grown[before:])
				}
			}),
		scenario("route-r218", "INV-ROUTE-R218-LISTING", "edge", "stateless", "logger-data-lists-only-one-day",
			"currentDailyLogs holds entries of a single day and none of the fixture's "+fixtureDailyLogDate+" log, and two consecutive reads return the same prefix.",
			[]string{loggerRoute, loggerSrc, loggerStore, dailyLogSrc}, func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				resp, lines := loggerData(t, e, root)
				wantStatus(t, "logger data", resp, http.StatusOK)
				if len(lines) == 0 {
					t.Fatal("currentDailyLogs is empty")
				}
				day := trimLogDay(lines[len(lines)-1].Timestamp)
				for _, line := range lines {
					if got := trimLogDay(line.Timestamp); got != day {
						t.Fatalf("currentDailyLogs mixes days: %s and %s", got, day)
					}
					if trimLogDay(line.Timestamp) == fixtureDailyLogDate {
						t.Fatalf("currentDailyLogs lists the fixture's old daily log: %+v", line)
					}
				}
				second := e.Get(t, root, loggerPath)
				wantStatus(t, "logger data again", second, http.StatusOK)
				_, later := loggerData(t, e, root)
				if len(later) < len(lines) {
					t.Fatalf("the log shrank from %d to %d entries", len(lines), len(later))
				}
				for i, line := range lines {
					if later[i] != line {
						t.Fatalf("entry %d changed between reads: %+v then %+v", i, line, later[i])
					}
				}
			}),
		scenario("route-r218", "INV-ROUTE-R218-ROUTE", "error", "stateless", "logger-data-rejects-non-admins",
			"GET /api/logger-data answers 401 without a token and 403 to the non-admin fixture user.",
			[]string{loggerRoute, loggerSrc}, func(t *testing.T, e *harness.Env) {
				anonymousAndListener(t, e, "GET", loggerPath, nil)
			}),
	}
}

// trimLogDay returns the date part of a log timestamp.
func trimLogDay(timestamp string) string {
	day, _, _ := strings.Cut(timestamp, " ")
	return day
}
