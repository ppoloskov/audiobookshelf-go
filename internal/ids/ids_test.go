package ids

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewReturnsVersion4UUIDs(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("New() = %q, not a lower-case version 4 UUID", id)
		}
	}
}

// Cases follow uuid@8.3.2 validate/version (dist/regex.js): versions 1-5, the
// 89ab variant nibble, the nil UUID, case-insensitive hex.
func TestValidFollowsUUIDPackage(t *testing.T) {
	valid := []string{
		"123e4567-e89b-12d3-a456-426614174000",
		"123E4567-E89B-52D3-B456-426614174000",
		"00000000-0000-0000-0000-000000000000",
	}
	invalid := []string{
		"", "not-a-uuid",
		"123e4567-e89b-62d3-a456-426614174000", // version 6 is outside 1-5 in v8.3.2
		"123e4567-e89b-12d3-c456-426614174000", // variant nibble
		"123e4567e89b12d3a456426614174000",
		"123e4567-e89b-12d3-a456-42661417400g",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
	}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	for _, s := range invalid {
		if Valid(s) || ValidV4(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
	if !ValidV4("123e4567-e89b-42d3-a456-426614174000") || ValidV4("123e4567-e89b-12d3-a456-426614174000") {
		t.Error("ValidV4 must accept only version 4")
	}
}
