package server

import (
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/settings"
)

// ApplyLoggingSettings applies the server setting `logLevel` to the logger and
// returns `loggerDailyLogsToKeep` for logging.OpenFiles. Upstream reads logLevel
// when ServerSettings is constructed from the database row
// (server/objects/settings/ServerSettings.js:150,215): a falsy value (0) keeps
// the Logger's own default, and a different value calls Logger.setLogLevel.
func ApplyLoggingSettings(server settings.Server) (dailyLogsToKeep int) {
	l := logging.Default()
	if level := logging.Level(server.LogLevel); level != 0 && level != l.Level() {
		l.SetLevel(level)
	}
	return server.LoggerDailyLogsToKeep
}
