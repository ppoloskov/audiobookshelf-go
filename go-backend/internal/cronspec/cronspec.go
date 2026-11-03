// Package cronspec is the one cron dialect the server accepts: the five
// standard fields (minute, hour, day of month, month, day of week) and no
// descriptors, which is what the upstream node-cron schedules use.
package cronspec

import "github.com/robfig/cron/v3"

const fields = cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow

// Parser parses five-field cron expressions.
func Parser() cron.Parser { return cron.NewParser(fields) }

// Validate reports why expression is not a valid five-field cron expression.
func Validate(expression string) error {
	_, err := Parser().Parse(expression)
	return err
}

// Valid reports whether expression is a valid five-field cron expression.
func Valid(expression string) bool { return Validate(expression) == nil }

// New returns a scheduler that parses schedules with Parser.
func New() *cron.Cron { return cron.New(cron.WithParser(Parser())) }
