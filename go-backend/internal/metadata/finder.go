package metadata

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/text"
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
	bracketedText = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\{[^}]*\}|_`)
	titleSplit    = regexp.MustCompile(` - | -|- `)
	etAl          = regexp.MustCompile(` et al\.?( |$)`)
	initialDots   = regexp.MustCompile(`([a-z])\.([a-z])`)
	// middleInitials matches the initials between a first and a last name
	// ("john q. smith"). Go has no lookaround, so the two-word-character
	// context on each side is captured and kept by the replacement.
	middleInitials = regexp.MustCompile(`(\w\w)(?:\s+[a-z]\.?)+(\s+\w\w)`)
	aggressiveTail = regexp.MustCompile(`[,/-].*$`)
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
	books := h.searchBuiltin(ctx, q)
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
		return cmpFloat(distance(a), distance(b))
	})
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// authorCandidates are the author names to retry with: the cleaned author and
// the cleaned title parts, each confirmed against Audnexus; else an
// aggressively cleaned author; the empty author is always last.
func (h *Handler) authorCandidates(ctx context.Context, cleanAuthor string, titleParts []string) []string {
	var candidates []string
	add := func(name string) {
		if name != "" && !slices.Contains(candidates, name) {
			candidates = append(candidates, name)
		}
	}
	add(cleanAuthor)
	for _, part := range titleParts {
		add(strings.TrimSpace(cleanAuthorForCompares(part)))
	}
	var validated []string
	for _, candidate := range candidates {
		if valid := h.validateAuthor(ctx, candidate); valid != "" && !slices.Contains(validated, valid) {
			validated = append(validated, valid)
		}
	}
	if len(validated) == 0 && cleanAuthor != "" {
		aggressive := strings.TrimSpace(aggressiveTail.ReplaceAllString(cleanAuthor, ""))
		validated = append(validated, cmpOr(aggressive, cleanAuthor))
	}
	return append(validated, "")
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// validateAuthor is the name Audnexus knows closest to name (one that contains
// it, is contained by it, or is within validateAuthorMaxDist), or "".
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
