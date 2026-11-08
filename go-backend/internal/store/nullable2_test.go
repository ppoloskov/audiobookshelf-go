package store

import (
	"database/sql"
	"testing"
)

func TestNullInt64(t *testing.T) {
	if got := NullInt64(sql.NullInt64{}); got != nil {
		t.Fatalf("NULL = %#v, want nil", got)
	}
	if got := NullInt64(sql.NullInt64{Int64: 0, Valid: true}); got != int64(0) {
		t.Fatalf("valid zero = %#v, want 0", got)
	}
}

func TestNullIfBlank(t *testing.T) {
	for _, blank := range []string{"", " ", "\t\n"} {
		if got := NullIfBlank(blank); got != nil {
			t.Fatalf("NullIfBlank(%q) = %#v, want nil", blank, got)
		}
	}
	if got := NullIfBlank(" a "); got != " a " {
		t.Fatalf("NullIfBlank keeps the original text, got %#v", got)
	}
}

func TestNullStringPtrAndStringPtrValue(t *testing.T) {
	if NullStringPtr(sql.NullString{}) != nil {
		t.Fatal("NULL must be a nil pointer")
	}
	got := NullStringPtr(sql.NullString{String: "", Valid: true})
	if got == nil || *got != "" {
		t.Fatalf("valid empty = %v, want pointer to empty string", got)
	}
	if StringPtrValue(nil) != nil {
		t.Fatal("nil pointer must be nil")
	}
	value := "x"
	if StringPtrValue(&value) != "x" {
		t.Fatal("pointer must unwrap")
	}
}
