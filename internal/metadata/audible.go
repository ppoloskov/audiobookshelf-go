package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"monstrobook/internal/text"
)

// audible searches the Audible catalog and reads each hit from Audnexus.
type audible struct {
	getter
	catalog  string
	audnexus audnexus
}

// audibleDomains maps a region to its catalog API domain suffix.
var audibleDomains = map[string]string{
	"us": ".com", "ca": ".ca", "uk": ".co.uk", "au": ".com.au", "fr": ".fr",
	"de": ".de", "jp": ".co.jp", "it": ".it", "in": ".in", "es": ".es",
}

func (a audible) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	region := q.Region
	if region == "" {
		_, region, _ = strings.Cut(q.Provider, ".") // "audible.uk" -> "uk"
	}
	domain, known := audibleDomains[region]
	if !known {
		region = ""
	}
	var asins []string
	if asin := firstASIN(q.ASIN, q.Title); asin != "" {
		asins = []string{asin}
	} else {
		catalog := a.catalog
		if known && strings.Contains(catalog, "api.audible.com") {
			catalog = strings.Replace(catalog, "api.audible.com", "api.audible"+domain, 1)
		}
		resp, err := fetchJSON[struct {
			Products []struct {
				ASIN string `json:"asin"`
			} `json:"products"`
		}](ctx, a.getter, queryURL(catalog, "num_results", "10", "products_sort_by", "Relevance", "title", q.Title, "author", q.Author))
		if err != nil {
			return nil, nil // a failing provider is an empty answer
		}
		for _, product := range resp.Products {
			if asin := strings.ToUpper(product.ASIN); validASIN(asin) {
				asins = append(asins, asin)
			}
		}
	}
	var out []BookResult
	for _, asin := range asins {
		if book, ok := a.audnexus.book(ctx, asin, region); ok {
			out = append(out, book.result())
		}
	}
	return out, nil
}

// firstASIN is the first candidate that is an ASIN, upper-cased.
func firstASIN(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate = strings.ToUpper(candidate); validASIN(candidate) {
			return candidate
		}
	}
	return ""
}

// audnexus reads books, authors and chapters from the Audnexus API.
type audnexus struct {
	getter
	base string
}

var (
	asinPattern    = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	sequenceNumber = regexp.MustCompile(`\.\d+|\d+(?:\.\d+)?`)
)

// validASIN reports whether s, upper-cased, is an ASIN.
func validASIN(s string) bool { return asinPattern.MatchString(s) }

type audnexusSeries struct {
	Name     string `json:"name"`
	Position string `json:"position"`
}

type audnexusBook struct {
	ASIN          string      `json:"asin"`
	Title         looseText   `json:"title"`
	Subtitle      looseText   `json:"subtitle"`
	Authors       []namedItem `json:"authors"`
	Narrators     []namedItem `json:"narrators"`
	PublisherName looseText   `json:"publisherName"`
	ReleaseDate   string      `json:"releaseDate"`
	RuntimeMin    looseNumber `json:"runtimeLengthMin"`
	Summary       looseText   `json:"summary"`
	Image         looseText   `json:"image"`
	ISBN          looseText   `json:"isbn"`
	FormatType    string      `json:"formatType"`
	IsAdult       bool        `json:"isAdult"`
	Language      string      `json:"language"`
	Region        looseText   `json:"region"`
	Rating        looseText   `json:"rating"`
	Genres        []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"genres"`
	SeriesPrimary   *audnexusSeries `json:"seriesPrimary"`
	SeriesSecondary *audnexusSeries `json:"seriesSecondary"`
}

func (b audnexusBook) result() BookResult {
	book := BookResult{
		Title: string(b.Title), Subtitle: string(b.Subtitle), Author: joinNames(b.Authors), Narrator: joinNames(b.Narrators),
		Publisher: string(b.PublisherName), PublishedYear: before(b.ReleaseDate, "-"), Description: string(b.Summary),
		Cover: string(b.Image), ASIN: b.ASIN, ISBN: string(b.ISBN), Duration: float64(b.RuntimeMin),
		Region: string(b.Region), Rating: string(b.Rating), Abridged: ptr(b.FormatType == "abridged"), Explicit: ptr(b.IsAdult),
	}
	if first, size := utf8.DecodeRuneInString(b.Language); size > 0 {
		book.Language = strings.ToUpper(string(first)) + b.Language[size:]
	}
	for _, g := range b.Genres {
		switch {
		case g.Name == "":
		case g.Type == "genre":
			book.Genres = append(book.Genres, g.Name)
		case g.Type == "tag":
			book.Tags = append(book.Tags, g.Name)
		}
	}
	book.Genres, book.Tags = uniqueTrimmed(book.Genres), uniqueTrimmed(book.Tags)
	for _, s := range []*audnexusSeries{b.SeriesPrimary, b.SeriesSecondary} {
		if s != nil {
			sequence := s.Position
			if found := sequenceNumber.FindString(sequence); found != "" {
				sequence = found
			}
			book.Series = append(book.Series, Series{Series: s.Name, Sequence: sequence})
		}
	}
	return book
}

func (a audnexus) endpoint(parts ...string) string {
	out := strings.TrimRight(a.base, "/")
	for _, part := range parts {
		out += "/" + url.PathEscape(part)
	}
	return out
}

// book fetches one book; ok is false when Audnexus does not know the ASIN.
func (a audnexus) book(ctx context.Context, asin, region string) (audnexusBook, bool) {
	book, err := fetchJSON[audnexusBook](ctx, a.getter, queryURL(a.endpoint("books", asin), "region", region))
	return book, err == nil && book.ASIN != ""
}

// chapters is the chapters document of a book, as Audnexus sends it.
func (a audnexus) chapters(ctx context.Context, asin, region string) (json.RawMessage, error) {
	chapters, err := fetchJSON[json.RawMessage](ctx, a.getter, queryURL(a.endpoint("books", asin, "chapters"), "region", region))
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 || string(chapters) == "null" {
		return nil, errors.New("no chapters")
	}
	return chapters, nil
}

// authorByASIN is the author with this ASIN, or nil for an invalid ASIN or a
// failed request.
func (a audnexus) authorByASIN(ctx context.Context, asin, region string) *AuthorResult {
	asin = strings.ToUpper(asin)
	if !validASIN(asin) {
		slog.Warn("audnexus: invalid ASIN", "asin", asin)
		return nil
	}
	author, err := fetchJSON[AuthorResult](ctx, a.getter, queryURL(a.endpoint("authors", asin), "region", region))
	if err != nil {
		slog.Warn("audnexus: author request failed", "asin", asin, "error", err)
		return nil
	}
	return &author
}

// authorCandidates searches authors by name.
func (a audnexus) authorCandidates(ctx context.Context, name, region string) ([]AuthorResult, error) {
	candidates, err := fetchJSON[[]AuthorResult](ctx, a.getter, queryURL(a.endpoint("authors"), "name", name, "region", region))
	if err != nil {
		return nil, fmt.Errorf("author search %q: %w", name, err)
	}
	return candidates, nil
}

// maxAuthorDistance is how far (Levenshtein) a candidate's name may be from
// the searched one.
const maxAuthorDistance = 3

// authorByName is the closest candidate within maxAuthorDistance, fetched by
// its ASIN; a result without a name is no result.
func (a audnexus) authorByName(ctx context.Context, name, region string) *AuthorResult {
	candidates, err := a.authorCandidates(ctx, name, region)
	if err != nil {
		slog.Warn("audnexus: author lookup failed", "error", err)
		return nil
	}
	best, bestDistance := -1, 0
	for i, candidate := range candidates {
		if distance := text.LevenshteinFold(candidate.Name, name); best < 0 || distance < bestDistance {
			best, bestDistance = i, distance
		}
	}
	if best < 0 || bestDistance > maxAuthorDistance {
		return nil
	}
	author := a.authorByASIN(ctx, candidates[best].ASIN, region)
	if author == nil || author.Name == "" {
		return nil
	}
	return author
}
