package store

import "database/sql"

// Parameter helpers for sqlc. Each binds the storage class the handwritten
// statements bound: TEXT for strings and JSON, REAL for floats, 0/1 for flags.

func Text(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func TextOrNull(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// TextAny binds a string, and NULL for any other value.
func TextAny(v any) sql.NullString {
	s, ok := v.(string)
	return sql.NullString{String: s, Valid: ok}
}

func TextPtr(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return Text(*s)
}

func Float(f float64) sql.NullFloat64 { return sql.NullFloat64{Float64: f, Valid: true} }

func FloatPtr(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return Float(*f)
}

func Flag(b bool) sql.NullInt64 {
	if b {
		return sql.NullInt64{Int64: 1, Valid: true}
	}
	return sql.NullInt64{Valid: true}
}
