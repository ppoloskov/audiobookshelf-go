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

	"audiobookshelf-compat/internal/text"
)

// audnexus reads books, authors and chapters from the Audnexus API.
type audnexus struct {
	getter
	base string
}

var asinPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// validASIN reports whether s, upper-cased, is an ASIN.
func validASIN(s string) bool { return asinPattern.MatchString(s) }

type audnexusName struct {
	Name string `json:"name"`
}

type audnexusSeries struct {
	Name     string `json:"name"`
	Position string `json:"position"`
}

type audnexusBook struct {
	ASIN          string         `json:"asin"`
	Title         looseText      `json:"title"`
	Subtitle      looseText      `json:"subtitle"`
	Authors       []audnexusName `json:"authors"`
	Narrators     []audnexusName `json:"narrators"`
	PublisherName looseText      `json:"publisherName"`
	ReleaseDate   string         `json:"releaseDate"`
	RuntimeMin    looseNumber    `json:"runtimeLengthMin"`
	Summary       looseText      `json:"summary"`
	Image         looseText      `json:"image"`
	ISBN          looseText      `json:"isbn"`
	FormatType    string         `json:"formatType"`
	IsAdult       bool           `json:"isAdult"`
	Language      string         `json:"language"`
	Region        looseText      `json:"region"`
	Rating        looseText      `json:"rating"`
	Genres        []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"genres"`
	SeriesPrimary   *audnexusSeries `json:"seriesPrimary"`
	SeriesSecondary *audnexusSeries `json:"seriesSecondary"`
}

var sequenceNumber = regexp.MustCompile(`\.\d+|\d+(?:\.\d+)?`)

func joinNames(list []audnexusName) string {
	var names []string
	for _, n := range list {
		if n.Name != "" {
			names = append(names, n.Name)
		}
	}
	return strings.Join(names, ", ")
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

func ptr[T any](v T) *T { return &v }

// bookURL is the Audnexus URL of a book; region is left out when empty.
func (a audnexus) bookURL(asin, region string) string {
	endpoint := strings.TrimRight(a.base, "/") + "/books/" + url.PathEscape(asin)
	if region != "" {
		endpoint += "?" + url.Values{"region": {region}}.Encode()
	}
	return endpoint
}

// book fetches one book; ok is false when Audnexus does not know the ASIN.
func (a audnexus) book(ctx context.Context, asin, region string) (audnexusBook, bool) {
	var book audnexusBook
	return book, a.json(ctx, a.bookURL(asin, region), &book) == nil && book.ASIN != ""
}

// chapters is the chapters document of a book, as Audnexus sends it.
func (a audnexus) chapters(ctx context.Context, asin, region string) (json.RawMessage, error) {
	endpoint := strings.TrimRight(a.base, "/") + "/books/" + url.PathEscape(asin) + "/chapters?" + url.Values{"region": {region}}.Encode()
	var chapters json.RawMessage // open data: passed through to the client
	if err := a.json(ctx, endpoint, &chapters); err != nil {
		return nil, err
	}
	if len(chapters) == 0 || string(chapters) == "null" {
		return nil, errors.New("no chapters")
	}
	return chapters, nil
}

type audnexusAuthor struct {
	ASIN        string  `json:"asin"`
	Description string  `json:"description"`
	Image       *string `json:"image"`
	Name        string  `json:"name"`
}

func (a audnexus) authorURL(query url.Values, asin string) string {
	endpoint := strings.TrimRight(a.base, "/") + "/authors"
	if asin != "" {
		endpoint += "/" + url.PathEscape(asin)
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

// authorByASIN is the author with this ASIN, or nil for an invalid ASIN or a
// failed request.
func (a audnexus) authorByASIN(ctx context.Context, asin, region string) *AuthorResult {
	asin = strings.ToUpper(asin)
	if !validASIN(asin) {
		slog.Warn("audnexus: invalid ASIN", "asin", asin)
		return nil
	}
	query := url.Values{}
	if region != "" {
		query.Set("region", region)
	}
	var author audnexusAuthor
	if err := a.json(ctx, a.authorURL(query, asin), &author); err != nil {
		slog.Warn("audnexus: author request failed", "asin", asin, "error", err)
		return nil
	}
	return &AuthorResult{ASIN: author.ASIN, Description: author.Description, Image: author.Image, Name: author.Name}
}

// authorCandidates searches authors by name.
func (a audnexus) authorCandidates(ctx context.Context, name, region string) ([]audnexusAuthor, error) {
	query := url.Values{"name": {name}}
	if region != "" {
		query.Set("region", region)
	}
	var candidates []audnexusAuthor
	if err := a.json(ctx, a.authorURL(query, ""), &candidates); err != nil {
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
