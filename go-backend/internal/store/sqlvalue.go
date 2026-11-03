package store

import "database/sql"

// Parameter helpers for sqlc. Each binds the storage class the handwritten
// statements bound: TEXT for strings and JSON, REAL for floats, 0/1 for flags.

// Text binds a value that is never NULL.
func Text(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// TextOrNull binds "" as NULL.
func TextOrNull(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// TextAny binds a string, and NULL for any other value.
func TextAny(v any) sql.NullString {
	s, ok := v.(string)
	return sql.NullString{String: s, Valid: ok}
}

// TextPtr binds nil as NULL.
func TextPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return Text(*s)
}

// Float binds a value that is never NULL.
func Float(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }

// FloatPtr binds nil as NULL.
func FloatPtr(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return Float(*f)
}

// Flag binds a boolean as 0 or 1.
func Flag(b bool) sql.NullInt64 {
	if b {
		return sql.NullInt64{Int64: 1, Valid: true}
	}
	return sql.NullInt64{Valid: true}
}
