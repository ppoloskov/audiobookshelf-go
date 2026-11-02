package textsort

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

type fixture struct {
	Pairs [][4]json.RawMessage `json:"pairs"`
}

// TestMatchesNode compares every recorded pair with the sign Node's
// localeCompare / numeric-base Intl.Collator produced.
func TestMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/collation.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Pairs) == 0 {
		t.Fatal("empty fixture")
	}
	bad := 0
	for _, p := range f.Pairs {
		var a, b string
		var locale, natural int
		_ = json.Unmarshal(p[0], &a)
		_ = json.Unmarshal(p[1], &b)
		_ = json.Unmarshal(p[2], &locale)
		_ = json.Unmarshal(p[3], &natural)
		if got := LocaleCompare(a, b); got != locale {
			bad++
			t.Errorf("LocaleCompare(%q, %q) = %d, node %d", a, b, got, locale)
		}
		if got := NaturalBaseCompare(a, b); got != natural {
			bad++
			t.Errorf("NaturalBaseCompare(%q, %q) = %d, node %d", a, b, got, natural)
		}
		if bad > 30 {
			t.Fatal("too many mismatches")
		}
	}
}

// TestPlaylistNameOrder pins an ordering produced by
// ['a','B','b','Á','á','A', ...].sort((x, y) => x.localeCompare(y)) in Node 22.
func TestPlaylistNameOrder(t *testing.T) {
	names := []string{"b", "B", "a", "A", "Á", "á", "10", "9", "Zoe", "zoe", "Éclair", "eclair", "_x", "-x", "Commute"}
	// node -e 'console.log([...].sort((x,y)=>x.localeCompare(y)))'
	want := []string{"_x", "-x", "10", "9", "a", "A", "á", "Á", "b", "B", "Commute", "eclair", "Éclair", "zoe", "Zoe"}
	sort.SliceStable(names, func(i, j int) bool { return LocaleCompare(names[i], names[j]) < 0 })
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %q, want %q", names, want)
		}
	}
}
