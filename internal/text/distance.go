// File note: measures how far apart two strings are.

package text

import "strings"

// Levenshtein is the edit distance between a and b, counted in runes.
func Levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	previous := make([]int, len(br)+1)
	for j := range previous {
		previous[j] = j
	}
	for i, ra := range ar {
		current := make([]int, len(br)+1)
		current[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			current[j+1] = min(current[j]+1, previous[j+1]+1, previous[j]+cost)
		}
		previous = current
	}
	return previous[len(br)]
}

// LevenshteinFold is Levenshtein after strings.ToLower on both strings.
func LevenshteinFold(a, b string) int {
	return Levenshtein(strings.ToLower(a), strings.ToLower(b))
}
