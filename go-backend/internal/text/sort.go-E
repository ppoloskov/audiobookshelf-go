// Package textsort is the Go counterpart of the ICU collation behind
// JavaScript's String.prototype.localeCompare and Intl.Collator, as Node
// uses them for the root/"en-US" locale. It is a leaf package: it imports
// only the standard library and golang.org/x/text (CLDR root collation).
//
// Node 22 ships a newer ICU/Unicode than the CLDR tables vendored in
// golang.org/x/text/collate. Orderings of the characters that appear in
// titles, names and sequences (ASCII, Latin with diacritics, digits, common
// punctuation, Cyrillic, Greek) match; characters added to Unicode after the
// x/text tables were generated compare as unassigned code points.
// testdata/collation.json is generated from Node by
// tools/gen-collation-fixture.cjs and checked by TestMatchesNode.
package textsort

import (
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// A collate.Collator keeps scratch buffers and is not safe for concurrent
// use, so every comparison holds mu.
var (
	mu sync.Mutex
	// variant is `a.localeCompare(b)` with no options: tertiary strength, so
	// case and accents break ties (lowercase before uppercase).
	variant = collate.New(language.Und)
	// naturalBase is `new Intl.Collator(undefined, {numeric: true,
	// sensitivity: 'base'}).compare`, the comparer of upstream's naturalSort
	// (server/utils/libraryHelpers.js:3-5).
	naturalBase = collate.New(language.Und, collate.Numeric, collate.Loose)
)

// quotes folds the typographic single quotes onto the ASCII apostrophe: ICU
// 78 (CLDR 48) gives U+2018 and U+2019 the apostrophe's primary weight, the
// older tables in x/text give them their own.
var quotes = strings.NewReplacer("\u2018", "'", "\u2019", "'")

// LocaleCompare reports -1, 0 or 1 like `a.localeCompare(b)`.
func LocaleCompare(a, b string) int {
	mu.Lock()
	defer mu.Unlock()
	fa, fb := quotes.Replace(a), quotes.Replace(b)
	if r := variant.CompareString(fa, fb); r != 0 || (fa == a && fb == b) {
		return r
	}
	// Equal apart from the quote variants, which differ at tertiary level.
	return strings.Compare(a, b)
}

// NaturalBaseCompare reports -1, 0 or 1 like the comparer of
// `new Intl.Collator(undefined, {numeric: true, sensitivity: 'base'})`:
// digit runs compare by value and case and accents are ignored.
func NaturalBaseCompare(a, b string) int {
	mu.Lock()
	defer mu.Unlock()
	return naturalBase.CompareString(quotes.Replace(a), quotes.Replace(b))
}

// SequenceLess orders series sequences like the comparator of seriesFilters.js
// (:200-206): an empty sequence sorts last, the rest by NaturalBaseCompare.
func SequenceLess(a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	return NaturalBaseCompare(a, b) < 0
}
