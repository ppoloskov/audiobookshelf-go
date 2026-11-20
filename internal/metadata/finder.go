package metadata

import (
	"cmp"
	"context"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"

	"monstrobook/internal/text"

	"golang.org/x/text/unicode/norm"
)

// Search distances: how far (Levenshtein) an Open Library result may be from
// the searched title and author, and the longest title or author searched.
const (
	maxTitleDistance      = 4
	maxFilterAuthorDist   = 4
	maxSearchTermRunes    = 500
	validateAuthorMaxDist = 2
)

var (
	bracketedText  = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\{[^}]*\}|_`)
	titleSplit     = regexp.MustCompile(` - | -|- `)
	etAl           = regexp.MustCompile(` et al\.?( |$)`)
	initialDots    = regexp.MustCompile(`([a-z])\.([a-z])`)
	middleInitials = regexp.MustCompile(`(\w\w)(?:\s+[a-z]\.?)+(\s+\w\w)`)
	aggressiveTail = regexp.MustCompile(`[,/-].*$`)
	accentMarks    = regexp.MustCompile(`[\x{0300}-\x{036f}]`)
	spaceRuns      = regexp.MustCompile(`\s+`)
	parenText      = regexp.MustCompile(`\([^)]*\)`)

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

// fuzzySearch searches q.Provider and, while it finds nothing, up to q.MaxFuzzy
// more times with cleaned title and author candidates.
func (h *Handler) fuzzySearch(ctx context.Context, q BookQuery) []BookResult {
	title, author := truncateRunes(q.Title, maxSearchTermRunes), truncateRunes(q.Author, maxSearchTermRunes)
	try := func(title, author string) []BookResult {
		attempt := q
		attempt.Title, attempt.Author, attempt.Region = title, author, ""
		return h.runSearch(ctx, attempt)
	}
	books := try(title, author)
	if len(books) > 0 || q.MaxFuzzy <= 0 {
		return books
	}
	lowerTitle := strings.ToLower(strings.TrimSpace(title))
	cleanAuthor := cleanAuthorForCompares(strings.ToLower(strings.TrimSpace(author)))
	titleParts := titleSplit.Split(bracketedText.ReplaceAllString(lowerTitle, " - "), -1)
	tries := 0
	for _, authorCandidate := range h.authorCandidates(ctx, cleanAuthor, titleParts) {
		for _, titleCandidate := range titleCandidates(authorCandidate, titleParts) {
			if titleCandidate == title && authorCandidate == author {
				continue // already tried
			}
			if tries++; tries > q.MaxFuzzy {
				return nil
			}
			title, author = titleCandidate, authorCandidate
			if books = try(title, author); len(books) > 0 {
				return books
			}
		}
	}
	return nil
}

// runSearch is one provider search; Open Library results are also filtered by
// their distance to the query and ordered by it.
func (h *Handler) runSearch(ctx context.Context, q BookQuery) []BookResult {
	slog.Debug("metadata: book search", "title", q.Title, "author", q.Author, "provider", q.Provider)
	books, _ := h.searchBookOnce(ctx, q) // built-ins answer failure as no results
	if q.Provider == "openlibrary" {
		books = filterByDistance(books, q.Title, q.Author)
	}
	return books
}

// sortByDuration orders books by how close their duration is to minutes;
// books without a duration come last.
func sortByDuration(books []BookResult, minutes float64) {
	distance := func(b BookResult) float64 {
		if b.Duration == 0 {
			return math.Inf(1)
		}
		return math.Abs(b.Duration - minutes)
	}
	slices.SortStableFunc(books, func(a, b BookResult) int {
		return cmp.Compare(distance(a), distance(b))
	})
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (h *Handler) authorCandidates(ctx context.Context, cleanAuthor string, titleParts []string) []string {
	candidates := appendUnique(nil, cleanAuthor)
	for _, part := range titleParts {
		candidates = appendUnique(candidates, strings.TrimSpace(cleanAuthorForCompares(part)))
	}
	var validated []string
	for _, candidate := range candidates {
		validated = appendUnique(validated, h.validateAuthor(ctx, candidate))
	}
	if len(validated) == 0 && cleanAuthor != "" {
		aggressive := strings.TrimSpace(aggressiveTail.ReplaceAllString(cleanAuthor, ""))
		validated = append(validated, cmp.Or(aggressive, cleanAuthor))
	}
	return append(validated, "")
}

func (h *Handler) validateAuthor(ctx context.Context, name string) string {
	known, err := h.svc.audnexus.authorCandidates(ctx, name, "")
	if err != nil {
		return ""
	}
	for i, author := range known {
		if i > 10 {
			break
		}
		clean := cleanAuthorForCompares(author.Name)
		switch {
		case clean == "":
		case strings.Contains(clean, name):
			return name
		case strings.Contains(name, clean):
			return clean
		case text.LevenshteinFold(clean, name) <= validateAuthorMaxDist:
			return clean
		}
	}
	return ""
}

func stripRedundantSpaces(s string) string {
	return strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))
}

func replaceAccentedChars(s string) string {
	return accentMarks.ReplaceAllString(norm.NFD.String(s), "")
}

func stripSubtitle(title string) string {
	for _, separator := range []string{": ", " - "} {
		if strings.Contains(title, separator) {
			return strings.TrimSpace(before(title, separator))
		}
	}
	return title
}

func cleanTitleForCompares(title string) string {
	if title == "" {
		return ""
	}
	cleaned := stripSubtitle(stripRedundantSpaces(title))
	cleaned = stripRedundantSpaces(parenText.ReplaceAllString(cleaned, ""))
	cleaned = strings.ReplaceAll(cleaned, "'", "")
	return strings.ToLower(replaceAccentedChars(cleaned))
}

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

func containsLong(targetClean, clean, targetRaw, raw string) bool {
	return len(clean) > 4 && strings.Contains(targetClean, clean) || len(raw) > 4 && strings.Contains(targetRaw, raw)
}

func filterByDistance(books []BookResult, title, author string) []BookResult {
	searchTitle, searchAuthor := cleanTitleForCompares(title), cleanAuthorForCompares(author)
	type scored struct {
		book  BookResult
		total int
	}
	var kept []scored
	for _, b := range books {
		cleanedTitle := cleanTitleForCompares(b.Title)
		titleDistance := text.LevenshteinFold(cleanedTitle, title)
		totalPossible := len([]rune(b.Title))
		authorDistance, includesAuthor := 0, false
		if author != "" {
			if b.Author == "" {
				authorDistance = len([]rune(author))
			} else {
				totalPossible += len([]rune(b.Author))
				cleanedAuthor := cleanAuthorForCompares(b.Author)
				authorDistance = min(text.LevenshteinFold(cleanedAuthor, searchAuthor), text.LevenshteinFold(b.Author, author))
				includesAuthor = containsLong(cleanedAuthor, searchAuthor, b.Author, author)
			}
		}
		includesTitle := containsLong(cleanedTitle, searchTitle, b.Title, title)
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
		return cmp.Or(cmp.Compare(priority[a], priority[b]), cmp.Compare(position[a], position[b]))
	})
	return order
}

func removeAuthorFromTitle(cleanAuthor, title string) string {
	re := regexp.MustCompile(`(^| | by |)` + regexp.QuoteMeta(cleanAuthor) + `( |$)`)
	cleaned := cleanAuthorForCompares(title)
	if without := re.ReplaceAllString(cleaned, "$2"); without != cleaned {
		return strings.TrimSpace(without)
	}
	return title
}
