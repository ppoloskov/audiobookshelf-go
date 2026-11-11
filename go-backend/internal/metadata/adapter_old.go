// TEMPORARY: feeds old catalog/people; delete with them.

package metadata

import (
	"context"
	"encoding/json"

	"audiobookshelf-compat/internal/wire"
)

// Legacy adapts a Handler to the interfaces of the old catalog (BookFinder)
// and people (Authors) packages, which speak wire types.
type Legacy struct{ h *Handler }

// Legacy returns the adapter for the old packages.
func (h *Handler) Legacy() Legacy { return Legacy{h} }

// SearchBooks is the old catalog.BookFinder.SearchBooks.
func (l Legacy) SearchBooks(ctx context.Context, q wire.BookSearch) []wire.BookMatch {
	results := l.h.SearchBooks(ctx, BookQuery{
		Provider: q.Provider, Title: q.Title, Author: q.Author, ISBN: q.ISBN, ASIN: q.ASIN,
		Duration: q.Duration, MaxFuzzy: q.MaxFuzzySearches,
	})
	return convert[wire.BookMatch](results)
}

// SearchPodcasts is the old catalog.BookFinder.SearchPodcasts.
func (l Legacy) SearchPodcasts(ctx context.Context, term string) []wire.PodcastMatch {
	return convert[wire.PodcastMatch](l.h.SearchPodcasts(ctx, term))
}

// FindAuthorByASIN is the old people.Authors.FindAuthorByASIN.
func (l Legacy) FindAuthorByASIN(ctx context.Context, asin, region string) *wire.AuthorMatch {
	return wireAuthor(l.h.FindAuthorByASIN(ctx, asin, region))
}

// FindAuthorByName is the old people.Authors.FindAuthorByName.
func (l Legacy) FindAuthorByName(ctx context.Context, name, region string) *wire.AuthorMatch {
	return wireAuthor(l.h.FindAuthorByName(ctx, name, region))
}

func wireAuthor(a *AuthorResult) *wire.AuthorMatch {
	if a == nil {
		return nil
	}
	return &wire.AuthorMatch{ASIN: a.ASIN, Description: a.Description, Image: a.Image, Name: a.Name}
}

// convert re-reads typed results as their wire form through JSON; the wire
// types keep each field as the raw JSON the old callers interpret.
func convert[W, T any](results []T) []W {
	if results == nil {
		return nil
	}
	out := make([]W, 0, len(results))
	for _, result := range results {
		raw, err := json.Marshal(result)
		if err != nil {
			continue
		}
		var match W
		if json.Unmarshal(raw, &match) == nil {
			out = append(out, match)
		}
	}
	return out
}
