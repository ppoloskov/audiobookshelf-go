package server

import "audiobookshelf-compat/internal/logging"

// ApplyLoggingSettings applies the server setting `logLevel` to the logger and
// returns `loggerDailyLogsToKeep` (0 when absent) for logging.OpenFiles.
// Upstream reads logLevel when ServerSettings is constructed from the database
// row (server/objects/settings/ServerSettings.js:150,215): a falsy value (0, or
// absent) keeps the Logger's own default, and a different value calls
// Logger.setLogLevel.
func ApplyLoggingSettings(settings map[string]any) (dailyLogsToKeep int) {
	l := logging.Default()
	if v, ok := settings["logLevel"].(float64); ok && v != 0 && logging.Level(int(v)) != l.Level() {
		l.SetLevel(logging.Level(int(v)))
	}
	keep, _ := settings["loggerDailyLogsToKeep"].(float64)
	return int(keep)
}
