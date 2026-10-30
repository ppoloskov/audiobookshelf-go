// Package dbtime formats timestamps for SQLite DATETIME columns.
//
// These helpers are only for SQLite DATETIME values. Do not use them for API
// JSON, RSS, millisecond, or display formats. SQL NULL must remain nil; never
// replace a NULL timestamp with an empty string.
package dbtime

import "time"

const layout = "2006-01-02 15:04:05.000 -07:00"

// Now returns the current time in the canonical SQLite DATETIME format.
func Now() string { return Format(time.Now()) }

// Format returns t in the canonical SQLite DATETIME format, normalized to UTC.
func Format(t time.Time) string { return t.UTC().Format(layout) }
