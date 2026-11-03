package wire

// LogEntry is one upstream log object: the LogObject typedef in
// server/managers/LogManager.js:11, built by Logger.#logToFileAndListeners
// (server/Logger.js:73) and used verbatim as the Socket.IO "log" payload,
// as one line of logs/daily/YYYY-MM-DD.txt and as an element of
// LoggerData.CurrentDailyLogs. Field order matches the upstream literal.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	LevelName string `json:"levelName"`
	Level     int    `json:"level"`
}

// LoggerData is the GET /api/logger-data response
// (server/controllers/MiscController.js:783). The slice is the last 5000
// entries of the current daily log (LogManager.getMostRecentCurrentDailyLogs,
// server/managers/LogManager.js:179) and must be non-nil.
type LoggerData struct {
	CurrentDailyLogs []LogEntry `json:"currentDailyLogs"`
}
