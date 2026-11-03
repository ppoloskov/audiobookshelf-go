// Package dbtime formats timestamps for SQLite DATETIME columns.
//
// Format and Parse are only for SQLite DATETIME values; the Millis family in
// millis.go converts stored DATETIME values to the epoch milliseconds the API
// reports. Do not use any of them for RSS or display formats. SQL NULL must
// remain nil; never replace a NULL timestamp with an empty string.
package dbtime

import "time"

const layout = "2006-01-02 15:04:05.000 -07:00"

// Now returns the current time in the canonical SQLite DATETIME format.
func Now() string { return Format(time.Now()) }

// Format returns t in the canonical SQLite DATETIME format, normalized to UTC.
func Format(t time.Time) string { return t.UTC().Format(layout) }

// parseLayouts are the DATETIME spellings found in upstream-written and
// Go-written databases: Sequelize's "YYYY-MM-DD HH:mm:ss.SSS +00:00", its
// variants without milliseconds or offset, and RFC 3339.
var parseLayouts = []string{
	layout,
	"2006-01-02 15:04:05.999999999 -07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	time.RFC3339Nano,
}

// Parse reads a SQLite DATETIME value written by upstream or by this server.
// Values without an offset are UTC, as Sequelize stores them.
func Parse(value string) (time.Time, bool) {
	for _, l := range parseLayouts {
		if t, err := time.Parse(l, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// JSDate formats t as JavaScript Date.prototype.toJSON does, which is how
// upstream serializes a Sequelize Date that reaches res.json unconverted.
func JSDate(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
