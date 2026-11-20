package text

import "testing"

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"same", "same", 0},
		{"Case", "case", 1},
		{"héllo", "hello", 1},
		{"日本語", "日本", 1},
	}
	for _, c := range cases {
		if got := Levenshtein(c.a, c.b); got != c.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Levenshtein(c.b, c.a); got != c.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d (symmetry)", c.b, c.a, got, c.want)
		}
	}
}

func TestLevenshteinFoldIgnoresCase(t *testing.T) {
	if got := LevenshteinFold("The Hobbit", "the HOBBIT"); got != 0 {
		t.Fatalf("LevenshteinFold = %d, want 0", got)
	}
	if got := LevenshteinFold("Dune", "Dunk"); got != 1 {
		t.Fatalf("LevenshteinFold = %d, want 1", got)
	}
	if got := LevenshteinFold("Éclair", "éCLAIR"); got != 0 {
		t.Fatalf("LevenshteinFold on accented capitals = %d, want 0", got)
	}
}
