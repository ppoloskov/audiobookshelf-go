// Package logging ports upstream's Logger (server/Logger.js) and LogManager
// (server/managers/LogManager.js, server/objects/DailyLog.js): leveled console
// output, daily JSON-line log files under METADATA_PATH/logs, the crash log,
// the in-memory tail served by GET /api/logger-data, and the Socket.IO "log"
// event for sockets that called set_log_listener.
//
// It is a leaf package (standard library and wire only).
//
// # Why a package-level default logger
//
// Upstream's Logger is a process-wide singleton that every module imports.
// Almost every Go feature package is constructed as New(db, cfg, emit) without
// a deps struct, and logging is a cross-cutting concern with exactly one
// owner, so threading a logger through ~20 constructors would add a parameter
// to each without adding any isolation. Feature packages therefore call the
// package-level functions (logging.Infof, logging.Errorf, ...), which write to
// Default(). Composition roots (cmd, server.Runtime) configure that single
// instance once at boot; tests build private instances with New.
package logging

import "strings"

// Level is an upstream LogLevel (server/utils/constants.js:19).
type Level int

const (
	LevelTrace Level = 0
	LevelDebug Level = 1
	LevelInfo  Level = 2
	LevelWarn  Level = 3
	LevelError Level = 4
	LevelFatal Level = 5
	LevelNote  Level = 6
)

var levelNames = [...]string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL", "NOTE"}

// String returns the upstream level name, or "UNKNOWN" (Logger.getLogLevelString).
func (l Level) String() string {
	if l < LevelTrace || l > LevelNote {
		return "UNKNOWN"
	}
	return levelNames[l]
}

func (l Level) Valid() bool { return l >= LevelTrace && l <= LevelNote }

// ParseLevel maps an upstream level name (case-insensitive) to a Level.
func ParseLevel(name string) (Level, bool) {
	for i, n := range levelNames {
		if strings.EqualFold(n, name) {
			return Level(i), true
		}
	}
	return 0, false
}
