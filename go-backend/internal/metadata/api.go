package metadata

import (
	"context"
	"log/slog"
	"strings"
)

// SearchPodcasts returns the iTunes podcasts matching term; the quick match
// takes the first. A failed search is no results.
func (h *Handler) SearchPodcasts(ctx context.Context, term string) []PodcastResult {
	if term == "" {
		return nil
	}
	results, err := h.svc.itunes.searchPodcasts(ctx, term, "")
	if err != nil {
		return nil
	}
	return results
}

// FindAuthorByASIN is the Audnexus author with this ASIN, or nil.
func (h *Handler) FindAuthorByASIN(ctx context.Context, asin, region string) *AuthorResult {
	if asin == "" {
		return nil
	}
	return h.svc.audnexus.authorByASIN(ctx, asin, region)
}

// FindAuthorByName is the Audnexus author whose name is within a Levenshtein
// distance of 3 of name, or nil.
func (h *Handler) FindAuthorByName(ctx context.Context, name, region string) *AuthorResult {
	if name == "" {
		return nil
	}
	return h.svc.audnexus.authorByName(ctx, name, region)
}

// SearchBooks is the quick-match book search: one provider search followed,
// when it finds nothing, by up to q.MaxFuzzy searches with cleaned title and
// author candidates; Audible results are ordered by how close their duration
// is to q.Duration. A custom provider is asked once and trusted.
func (h *Handler) SearchBooks(ctx context.Context, q BookQuery) []BookResult {
	if isCustom(q.Provider) {
		results, err := h.searchCustom(ctx, q.Provider, "book", q)
		if err != nil {
			slog.Warn("metadata: custom provider search failed", "provider", q.Provider, "error", err)
			return nil
		}
		return results
	}
	if q.Title == "" {
		return nil
	}
	books := h.fuzzySearch(ctx, q)
	if books == nil {
		books = []BookResult{}
	}
	if len(books) > 0 && strings.HasPrefix(q.Provider, "audible") && q.Duration > 0 {
		sortByDuration(books, q.Duration/60)
	}
	return books
}
