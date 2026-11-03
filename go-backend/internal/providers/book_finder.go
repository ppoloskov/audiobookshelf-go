package providers

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/wire"
)

// SearchBooks is BookFinder.search (server/finders/BookFinder.js:243-312): a
// provider search followed, when it finds nothing, by up to MaxFuzzySearches
// searches with cleaned title and author candidates; Audible results are
// ordered by how close their duration is to the library item's.
func (h *Handler) SearchBooks(ctx context.Context, q wire.BookSearch) []wire.BookMatch {
	const maxTitleDistance, maxAuthorDistance = 4, 4
	maxFuzzy := q.MaxFuzzySearches
	if strings.HasPrefix(q.Provider, "custom-") {
		// Custom providers are assumed to be correct (BookFinder.js:254-256).
		results, err := h.searchCustom(ctx, q.Provider, "book", q.Title, q.Author, q.ISBN, 10*time.Second)
		if err != nil {
			logging.Errorf("Error searching Custom provider '%s': %v", q.Provider, err)
			return nil
		}
		return convertJSON[wire.BookMatch](results)
	}
	if q.Title == "" {
		return nil
	}
	title, author := truncateRunes(q.Title, 500), truncateRunes(q.Author, 500)
	actualTitle, actualAuthor := title, author
	books := h.runBookSearch(ctx, actualTitle, actualAuthor, q.Provider, q.ASIN, maxTitleDistance, maxAuthorDistance)
	if len(books) == 0 && maxFuzzy > 0 {
		title = strings.ToLower(strings.TrimSpace(title))
		author = strings.ToLower(strings.TrimSpace(author))
		cleanAuthor := cleanAuthorForCompares(author)
		cleanTitle := bracketedText.ReplaceAllString(title, " - ")
		titleParts := splitTitleParts(cleanTitle)
		authorCandidates := h.authorCandidates(ctx, cleanAuthor, titleParts)
		tries := 0
	candidates:
		for _, authorCandidate := range authorCandidates {
			for _, titleCandidate := range titleCandidates(authorCandidate, titleParts) {
				if titleCandidate == actualTitle && authorCandidate == actualAuthor {
					continue // already tried
				}
				if tries++; tries > maxFuzzy {
					break candidates
				}
				actualTitle, actualAuthor = titleCandidate, authorCandidate
				books = h.runBookSearch(ctx, actualTitle, actualAuthor, q.Provider, q.ASIN, maxTitleDistance, maxAuthorDistance)
				if len(books) > 0 {
					break candidates
				}
			}
		}
	}
	if len(books) > 0 && strings.HasPrefix(q.Provider, "audible") && q.Duration > 0 {
		minutes := q.Duration / 60
		distance := func(m wire.BookMatch) float64 {
			var duration float64
			if json.Unmarshal(m.Duration, &duration) != nil || duration == 0 {
				return math.Inf(1)
			}
			return math.Abs(duration - minutes)
		}
		sort.SliceStable(books, func(i, j int) bool { return distance(books[i]) < distance(books[j]) })
	}
	return books
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// convertJSON converts provider results into typed matches through their JSON.
func convertJSON[T any, S any](results []S) []T {
	out := make([]T, 0, len(results))
	for _, result := range results {
		raw, err := json.Marshal(result)
		if err != nil {
			continue
		}
		var match T
		if json.Unmarshal(raw, &match) == nil {
			out = append(out, match)
		}
	}
	return out
}

// runBookSearch is BookFinder.runSearch (BookFinder.js:586-615): the provider's
// search, the OpenLibrary distance filter, and sanitized descriptions.
func (h *Handler) runBookSearch(ctx context.Context, title, author, provider, asin string, maxTitleDistance, maxAuthorDistance int) []wire.BookMatch {
	logging.Debugf(`Book Search: title: "%s", author: "%s", provider: %s`, title, author, provider)
	region := ""
	if strings.Contains(provider, ".") {
		region = provider[strings.LastIndex(provider, ".")+1:]
	}
	var books []wire.BookMatch
	switch {
	case provider == "openlibrary":
		results, err := h.searchOpenLibrary(ctx, title, author)
		if err != nil {
			return nil
		}
		books = filterSearchResults(convertJSON[wire.BookMatch](results), title, author, maxTitleDistance, maxAuthorDistance)
	case strings.HasPrefix(provider, "audible"):
		results, err := h.searchAudible(ctx, provider, title, author, asin, region)
		if err != nil {
			return nil
		}
		books = convertJSON[wire.BookMatch](results)
	default:
		results, err := h.searchBuiltinBooks(ctx, provider, title, author, asin, "")
		if err != nil {
			return nil
		}
		books = convertJSON[wire.BookMatch](results)
	}
	for i := range books {
		var description string
		if json.Unmarshal(books[i].Description, &description) == nil && description != "" {
			sanitized := auth.SanitizeHTML(description)
			books[i].Description, _ = json.Marshal(sanitized)
			books[i].DescriptionPlain, _ = json.Marshal(stripHTML(sanitized))
		}
	}
	return books
}

var (
	bracketedText = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\{[^}]*\}|_`)
	titleSplit    = regexp.MustCompile(` - | -|- `)
	accentMarks   = regexp.MustCompile(`[\x{0300}-\x{036f}]`)
	spaceRuns     = regexp.MustCompile(`\s+`)
	parenText     = regexp.MustCompile(`\([^)]*\)`)
	initialDots   = regexp.MustCompile(`([a-z])\.([a-z])`)
	etAl          = regexp.MustCompile(` et al\.?( |$)`)
)

func splitTitleParts(title string) []string { return titleSplit.Split(title, -1) }

func stripRedundantSpaces(s string) string {
	return strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))
}

func replaceAccentedChars(s string) string {
	return accentMarks.ReplaceAllString(norm.NFD.String(s), "")
}

func stripSubtitle(title string) string {
	if strings.Contains(title, ": ") {
		return strings.TrimSpace(strings.Split(title, ": ")[0])
	}
	if strings.Contains(title, " - ") {
		return strings.TrimSpace(strings.Split(title, " - ")[0])
	}
	return title
}

// cleanTitleForCompares (BookFinder.js:660-676).
func cleanTitleForCompares(title string) string {
	if title == "" {
		return ""
	}
	cleaned := stripSubtitle(stripRedundantSpaces(title))
	cleaned = strings.TrimSpace(spaceRuns.ReplaceAllString(parenText.ReplaceAllString(cleaned, ""), " "))
	cleaned = strings.ReplaceAll(cleaned, "'", "")
	return strings.ToLower(replaceAccentedChars(cleaned))
}

func isWordChar(r rune) bool {
	return r == '_' || unicode.IsLetter(r) && r < 128 || unicode.IsDigit(r) && r < 128
}

// cleanAuthorForCompares (BookFinder.js:678-690).
func cleanAuthorForCompares(author string) string {
	if author == "" {
		return ""
	}
	clean := strings.ToLower(replaceAccentedChars(stripRedundantSpaces(author)))
	clean = initialDots.ReplaceAllString(clean, "$1. $2")
	clean = removeMiddleInitials(clean)
	return etAl.ReplaceAllString(clean, "$1")
}

// removeMiddleInitials is replace(/(?<=\w\w)(\s+[a-z]\.?)+(?=\s+\w\w)/g, ”),
// with the backtracking of the regular expression done by hand.
func removeMiddleInitials(s string) string {
	runes := []rune(s)
	var out []rune
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) && i >= 2 && isWordChar(runes[i-1]) && isWordChar(runes[i-2]) {
			// Possible ends of 1..n repetitions of \s+[a-z]\.?
			var ends []int
			pos := i
			for {
				j := pos
				for j < len(runes) && unicode.IsSpace(runes[j]) {
					j++
				}
				if j == pos || j >= len(runes) || runes[j] < 'a' || runes[j] > 'z' {
					break
				}
				j++
				if j < len(runes) && runes[j] == '.' {
					j++
				}
				ends = append(ends, j)
				pos = j
			}
			matched := -1
			for k := len(ends) - 1; k >= 0; k-- { // greedy, then backtrack
				j := ends[k]
				spaces := j
				for spaces < len(runes) && unicode.IsSpace(runes[spaces]) {
					spaces++
				}
				if spaces > j && spaces+1 < len(runes) && isWordChar(runes[spaces]) && isWordChar(runes[spaces+1]) {
					matched = ends[k]
					break
				}
			}
			if matched >= 0 {
				i = matched
				continue
			}
		}
		out = append(out, runes[i])
		i++
	}
	return string(out)
}

func levenshteinDistance(a, b string) int { return levenshtein(a, b) }

func levenshteinSimilarity(a, b string) float64 {
	max := math.Max(float64(len([]rune(a))), float64(len([]rune(b))))
	if max == 0 {
		return 1
	}
	return 1 - float64(levenshtein(a, b))/max
}

// filterSearchResults is BookFinder.filterSearchResults (BookFinder.js:41-100)
// followed by the ascending total distance sort of getOpenLibResults.
func filterSearchResults(books []wire.BookMatch, title, author string, maxTitleDistance, maxAuthorDistance int) []wire.BookMatch {
	searchTitle, searchAuthor := cleanTitleForCompares(title), cleanAuthorForCompares(author)
	type scored struct {
		book  wire.BookMatch
		total int
	}
	var kept []scored
	for _, b := range books {
		var bookTitle, bookAuthor string
		_ = json.Unmarshal(b.Title, &bookTitle)
		_ = json.Unmarshal(b.Author, &bookAuthor)
		cleanedTitle := cleanTitleForCompares(bookTitle)
		titleDistance := levenshteinDistance(cleanedTitle, title)
		totalPossible := len([]rune(bookTitle))
		authorDistance, includesAuthor := 0, false
		if author != "" {
			if bookAuthor == "" {
				authorDistance = len([]rune(author))
			} else {
				totalPossible += len([]rune(bookAuthor))
				cleanedAuthor := cleanAuthorForCompares(bookAuthor)
				authorDistance = int(math.Min(float64(levenshteinDistance(cleanedAuthor, searchAuthor)), float64(levenshteinDistance(bookAuthor, author))))
				includesAuthor = len(searchAuthor) > 4 && strings.Contains(cleanedAuthor, searchAuthor) || len(author) > 4 && strings.Contains(bookAuthor, author)
			}
		}
		total := titleDistance + authorDistance
		includesTitle := len(searchTitle) > 4 && strings.Contains(cleanedTitle, searchTitle) || len(title) > 4 && strings.Contains(bookTitle, title)
		if !includesTitle && titleDistance > maxTitleDistance {
			continue
		}
		if author != "" && !includesAuthor && authorDistance > maxAuthorDistance {
			continue
		}
		if totalPossible < 5 && total > 0 {
			continue
		}
		kept = append(kept, scored{b, total})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].total < kept[j].total })
	out := make([]wire.BookMatch, len(kept))
	for i, k := range kept {
		out[i] = k.book
	}
	return out
}

var (
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
	onlyDigits = regexp.MustCompile(`^\d+$`)
)

// titleCandidates is BookFinder.TitleCandidates (BookFinder.js:151-244): the
// cleaned title parts and their simplified variants, best first.
func titleCandidates(cleanAuthor string, titleParts []string) []string {
	var order []string
	priority, position := map[string]int{}, map[string]int{}
	current := 0
	add := func(title string) {
		if cleanAuthor != "" {
			title = removeAuthorFromTitle(cleanAuthor, title)
		}
		clean := strings.TrimSpace(cleanTitleForCompares(title))
		if clean == "" {
			return
		}
		if _, seen := priority[clean]; !seen {
			order = append(order, clean)
		}
		priority[clean], position[clean] = 0, current
		candidate := clean
		for _, t := range titleTransformers {
			candidate = strings.TrimSpace(t.re.ReplaceAllString(candidate, t.with))
		}
		if candidate != clean {
			if candidate != "" {
				if _, seen := priority[candidate]; !seen {
					order = append(order, candidate)
				}
				priority[candidate], position[candidate] = 0, current
			}
			priority[clean] = 1
		}
		current++
	}
	for _, part := range titleParts {
		add(part)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if da, db := onlyDigits.MatchString(a), onlyDigits.MatchString(b); da != db {
			return !da
		}
		if priority[a] != priority[b] {
			return priority[a] < priority[b]
		}
		return position[a] < position[b]
	})
	return order
}

// removeAuthorFromTitle is TitleCandidates.#removeAuthorFromTitle: a title
// that contains the author loses it.
func removeAuthorFromTitle(cleanAuthor, title string) string {
	re, err := regexp.Compile(`(^| | by |)` + regexp.QuoteMeta(cleanAuthor) + `( |$)`)
	if err != nil {
		return title
	}
	cleaned := cleanAuthorForCompares(title)
	without := re.ReplaceAllString(cleaned, "$2")
	if without != cleaned {
		return strings.TrimSpace(without)
	}
	return title
}

// authorCandidates is BookFinder.AuthorCandidates.getCandidates
// (BookFinder.js:313-352): each candidate confirmed against Audnexus, an
// aggressively cleaned author if none was, and always the empty author last.
func (h *Handler) authorCandidates(ctx context.Context, cleanAuthor string, titleParts []string) []string {
	var candidates []string
	seen := map[string]bool{}
	addCandidate := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			candidates = append(candidates, a)
		}
	}
	addCandidate(cleanAuthor)
	for _, part := range titleParts {
		addCandidate(strings.TrimSpace(cleanAuthorForCompares(part)))
	}
	var validated []string
	seenValid := map[string]bool{}
	for _, candidate := range candidates {
		valid := h.validateAuthor(ctx, candidate, "", 2)
		if valid != "" && !seenValid[valid] {
			seenValid[valid] = true
			validated = append(validated, valid)
		}
	}
	if len(validated) == 0 && cleanAuthor != "" {
		aggressive := strings.TrimSpace(regexp.MustCompile(`[,/-].*$`).ReplaceAllString(cleanAuthor, ""))
		if aggressive == "" {
			aggressive = cleanAuthor
		}
		validated = append(validated, aggressive)
	}
	return append(validated, "")
}

// validateAuthor is AuthorCandidates.validateAuthor (BookFinder.js:289-302).
func (h *Handler) validateAuthor(ctx context.Context, name, region string, maxLevenshtein int) string {
	query := url.Values{"name": {name}}
	if region != "" {
		query.Set("region", region)
	}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/authors?" + query.Encode()
	var asins []audnexusAuthor
	if err := h.getJSON(ctx, endpoint, &asins); err != nil {
		return ""
	}
	for i, asin := range asins {
		if i > 10 {
			break
		}
		cleanName := cleanAuthorForCompares(asin.Name)
		if cleanName == "" {
			continue
		}
		if strings.Contains(cleanName, name) {
			return name
		}
		if strings.Contains(name, cleanName) {
			return cleanName
		}
		if levenshteinDistance(cleanName, name) <= maxLevenshtein {
			return cleanName
		}
	}
	return ""
}
