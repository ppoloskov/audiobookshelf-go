package providers

import (
	"context"

	"audiobookshelf-compat/internal/wire"
)

// SearchPodcasts is PodcastFinder.search(term) without options
// (server/finders/PodcastFinder.js:12-18): the iTunes podcast results the quick
// match takes its first entry from.
func (h *Handler) SearchPodcasts(ctx context.Context, term string) []wire.PodcastMatch {
	if term == "" {
		return nil
	}
	results, ok := h.itunesPodcasts(ctx, term, "")
	if !ok {
		return nil
	}
	return convertJSON[wire.PodcastMatch](results)
}
