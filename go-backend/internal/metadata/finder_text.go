package metadata

import (
	"math"
	"regexp"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/textdist"

	"golang.org/x/text/unicode/norm"
)

// This file holds the string cleaning behind the fuzzy book search. The
// rules follow upstream BookFinder so that the same library item finds the
// same book.

var (
	accentMarks = regexp.MustCompile(`[\x{0300}-\x{036f}]`)
	spaceRuns   = regexp.MustCompile(`\s+`)
	parenText   = regexp.MustCompile(`\([^)]*\)`)

	titleTransformers = []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`(: |[,;_]| by ).*`), ""},
		{regexp.MustCompile(`(^| )\d+k(bps)?( |$)`), " "},
		{regexp.MustCompile(` (2nd|3rd|\d+th)\s+ed(\.|ition)?`), ""},
		{regexp.MustCompile(`(^| |\.)(m4b|m4a|mp3)( |$)`), ""},
		{regexp.MustCompile(` a novel.*$`), ""},
		{regexp.MustCompile(`(^| )(un)?abridged( |$)`), " "},
		{regexp.MustCompile(`^\d+ | \d+$`), ""},
	}
)

func stripRedundantSpaces(s string) string {
	return strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))
}

func replaceAccentedChars(s string) string {
	return accentMarks.ReplaceAllString(norm.NFD.String(s), "")
}

// stripSubtitle cuts a title at its first ": " or " - ".
func stripSubtitle(title string) string {
	for _, separator := range []string{": ", " - "} {
		if strings.Contains(title, separator) {
			return strings.TrimSpace(before(title, separator))
		}
	}
	return title
}

// cleanTitleForCompares lower-cases a title without subtitle, parenthesised
// text, apostrophes and accents.
func cleanTitleForCompares(title string) string {
	if title == "" {
		return ""
	}
	cleaned := stripSubtitle(stripRedundantSpaces(title))
	cleaned = strings.TrimSpace(spaceRuns.ReplaceAllString(parenText.ReplaceAllString(cleaned, ""), " "))
	cleaned = strings.ReplaceAll(cleaned, "'", "")
	return strings.ToLower(replaceAccentedChars(cleaned))
}

// cleanAuthorForCompares lower-cases an author without accents, middle
// initials and "et al".
func cleanAuthorForCompares(author string) string {
	if author == "" {
		return ""
	}
	clean := strings.ToLower(replaceAccentedChars(stripRedundantSpaces(author)))
	clean = initialDots.ReplaceAllString(clean, "$1. $2")
	// Each pass removes the initials of one gap; a name with several gaps
	// needs more than one because a match consumes the context of the next.
	for {
		next := middleInitials.ReplaceAllString(clean, "$1$2")
		if next == clean {
			break
		}
		clean = next
	}
	return etAl.ReplaceAllString(clean, "$1")
}

// filterByDistance keeps the books whose title and author are close to the
// searched ones (or contain them) and orders them by total distance.
func filterByDistance(books []BookResult, title, author string) []BookResult {
	searchTitle, searchAuthor := cleanTitleForCompares(title), cleanAuthorForCompares(author)
	type scored struct {
		book  BookResult
		total int
	}
	var kept []scored
	for _, b := range books {
		cleanedTitle := cleanTitleForCompares(b.Title)
		titleDistance := textdist.LevenshteinFold(cleanedTitle, title)
		totalPossible := len([]rune(b.Title))
		authorDistance, includesAuthor := 0, false
		if author != "" {
			if b.Author == "" {
				authorDistance = len([]rune(author))
			} else {
				totalPossible += len([]rune(b.Author))
				cleanedAuthor := cleanAuthorForCompares(b.Author)
				authorDistance = int(math.Min(float64(textdist.LevenshteinFold(cleanedAuthor, searchAuthor)), float64(textdist.LevenshteinFold(b.Author, author))))
				includesAuthor = len(searchAuthor) > 4 && strings.Contains(cleanedAuthor, searchAuthor) || len(author) > 4 && strings.Contains(b.Author, author)
			}
		}
		includesTitle := len(searchTitle) > 4 && strings.Contains(cleanedTitle, searchTitle) || len(title) > 4 && strings.Contains(b.Title, title)
		total := titleDistance + authorDistance
		switch {
		case !includesTitle && titleDistance > maxTitleDistance,
			author != "" && !includesAuthor && authorDistance > maxFilterAuthorDist,
			totalPossible < 5 && total > 0:
			continue
		}
		kept = append(kept, scored{b, total})
	}
	slices.SortStableFunc(kept, func(a, b scored) int { return a.total - b.total })
	out := make([]BookResult, len(kept))
	for i, k := range kept {
		out[i] = k.book
	}
	return out
}

// titleCandidates are the cleaned title parts and their simplified variants
// (bitrate, edition, "a novel" and format words removed), best first: variants
// of a cleaned part before the part itself, earlier parts first, and
// all-digit candidates last.
func titleCandidates(cleanAuthor string, titleParts []string) []string {
	var order []string
	priority, position := map[string]int{}, map[string]int{}
	note := func(candidate string, prio, pos int) {
		if _, seen := priority[candidate]; !seen {
			order = append(order, candidate)
		}
		priority[candidate], position[candidate] = prio, pos
	}
	for pos, part := range titleParts {
		if cleanAuthor != "" {
			part = removeAuthorFromTitle(cleanAuthor, part)
		}
		clean := strings.TrimSpace(cleanTitleForCompares(part))
		if clean == "" {
			continue
		}
		note(clean, 0, pos)
		simplified := clean
		for _, t := range titleTransformers {
			simplified = strings.TrimSpace(t.re.ReplaceAllString(simplified, t.with))
		}
		if simplified != clean {
			if simplified != "" {
				note(simplified, 0, pos)
			}
			priority[clean] = 1
		}
	}
	slices.SortStableFunc(order, func(a, b string) int {
		if da, db := onlyDigits.MatchString(a), onlyDigits.MatchString(b); da != db {
			if da {
				return 1
			}
			return -1
		}
		if priority[a] != priority[b] {
			return priority[a] - priority[b]
		}
		return position[a] - position[b]
	})
	return order
}

// removeAuthorFromTitle drops the author's name from a title that contains it.
func removeAuthorFromTitle(cleanAuthor, title string) string {
	re := regexp.MustCompile(`(^| | by |)` + regexp.QuoteMeta(cleanAuthor) + `( |$)`)
	cleaned := cleanAuthorForCompares(title)
	if without := re.ReplaceAllString(cleaned, "$2"); without != cleaned {
		return strings.TrimSpace(without)
	}
	return title
}
