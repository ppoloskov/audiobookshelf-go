package store

import (
	"database/sql"
	"testing"
)

// The API reports SQL NULL as null and ” as "" (User.toOldJSONForBrowser), so
// the converters must keep the two apart.
func TestNullableColumnsKeepNullDistinctFromEmpty(t *testing.T) {
	if NullString(sql.NullString{}) != nil || NullString(sql.NullString{Valid: true}) != "" {
		t.Error("NullString must map NULL to nil and '' to an empty string")
	}
	if p := NullStringPtr(sql.NullString{}); p != nil {
		t.Error("NullStringPtr(NULL) must be nil")
	}
	if p := NullStringPtr(sql.NullString{Valid: true}); p == nil || *p != "" {
		t.Error("NullStringPtr('') must point at the empty string")
	}
	if NullInt64(sql.NullInt64{}) != nil || NullInt64(sql.NullInt64{Valid: true}) != int64(0) {
		t.Error("NullInt64 must map NULL to nil and 0 to 0")
	}
	if NullIfEmpty("") != nil || NullIfBlank("\t ") != nil {
		t.Error("empty (and, for NullIfBlank, blank) text must be stored as NULL")
	}
	if NullIfBlank(" a ") != " a " {
		t.Error("NullIfBlank must keep the original text")
	}
}
