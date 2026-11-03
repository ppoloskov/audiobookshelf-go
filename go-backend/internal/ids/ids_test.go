package ids

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewReturnsVersion4UUIDs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("New() = %q, not a lower-case version 4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("New() repeated %q", id)
		}
		seen[id] = true
	}
}
