package providers

import (
	"context"
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/wire"
)

// audnexusAuthor is the part of an Audnexus author record the author match
// uses (server/providers/Audnexus.js:97-110).
type audnexusAuthor struct {
	ASIN        string  `json:"asin"`
	Description string  `json:"description"`
	Image       *string `json:"image"`
	Name        string  `json:"name"`
}

// authorRequest is Audnexus.authorRequest (Audnexus.js:67-89): an invalid ASIN
// or a failed request yields nil.
func (h *Handler) authorRequest(ctx context.Context, asin, region string) *audnexusAuthor {
	upper := strings.ToUpper(asin)
	if !validASIN(upper) {
		logging.Errorf("[Audnexus] Invalid ASIN %s", asin) // Audnexus.js:69
		return nil
	}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/authors/" + url.PathEscape(upper)
	if region != "" {
		endpoint += "?" + url.Values{"region": {region}}.Encode()
	}
	logging.Infof(`[Audnexus] Searching for author "%s"`, endpoint) // Audnexus.js:78
	var author audnexusAuthor
	if err := h.getJSON(ctx, endpoint, &author); err != nil {
		logging.Errorf("[Audnexus] Author request failed for %s %s", upper, err.Error()) // Audnexus.js:83
		return nil
	}
	return &author
}

func authorMatch(author *audnexusAuthor) *wire.AuthorMatch {
	if author == nil {
		return nil
	}
	return &wire.AuthorMatch{ASIN: author.ASIN, Description: author.Description, Image: author.Image, Name: author.Name}
}

// FindAuthorByASIN is AuthorFinder.findAuthorByASIN (server/finders/AuthorFinder.js:13-16).
func (h *Handler) FindAuthorByASIN(ctx context.Context, asin, region string) *wire.AuthorMatch {
	if asin == "" {
		return nil
	}
	return authorMatch(h.authorRequest(ctx, asin, region))
}

// FindAuthorByName is AuthorFinder.findAuthorByName over Audnexus.findAuthorByName
// (AuthorFinder.js:25-34, Audnexus.js:121-147): the closest candidate within
// the Levenshtein limit of 3, fetched by its ASIN; a result without a name is
// no result.
func (h *Handler) FindAuthorByName(ctx context.Context, name, region string) *wire.AuthorMatch {
	if name == "" {
		return nil
	}
	query := url.Values{"name": {name}}
	if region != "" {
		query.Set("region", region)
	}
	endpoint := strings.TrimRight(h.endpoint("audnexus"), "/") + "/authors?" + query.Encode()
	logging.Infof(`[Audnexus] Searching for author "%s"`, endpoint) // Audnexus.js:55
	var candidates []audnexusAuthor
	if err := h.getJSON(ctx, endpoint, &candidates); err != nil {
		logging.Errorf("[Audnexus] Author ASIN request failed for %s %s", name, err.Error()) // Audnexus.js:62
		return nil
	}
	best, bestDistance := -1, 0
	for i, candidate := range candidates {
		distance := levenshtein(candidate.Name, name)
		if best < 0 || bestDistance > distance {
			best, bestDistance = i, distance
		}
	}
	if best < 0 || bestDistance > 3 {
		return nil
	}
	match := authorMatch(h.authorRequest(ctx, candidates[best].ASIN, region))
	if match == nil || match.Name == "" {
		return nil
	}
	return match
}
