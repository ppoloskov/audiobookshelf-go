package store

import (
	"database/sql"
	"testing"
)

func TestNullStringMapsInvalidToNil(t *testing.T) {
	if got := NullString(sql.NullString{}); got != nil {
		t.Fatalf("NULL = %#v, want nil", got)
	}
	if got := NullString(sql.NullString{String: "", Valid: true}); got != "" {
		t.Fatalf("empty but valid = %#v, want empty string", got)
	}
	if got := NullString(sql.NullString{String: "x", Valid: true}); got != "x" {
		t.Fatalf("value = %#v, want x", got)
	}
}

func TestNullIfEmptyMapsEmptyToNil(t *testing.T) {
	if got := NullIfEmpty(""); got != nil {
		t.Fatalf("empty = %#v, want nil", got)
	}
	if got := NullIfEmpty(" "); got != " " {
		t.Fatalf("blank = %#v, want a single space", got)
	}
}
