package podcast

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/textdist"
)

// matchThreshold is the title similarity a feed episode needs to be taken for a
// local episode.
const matchThreshold = 0.9

// QuickMatchEpisodes fills the episodes without an enclosure from the best
// matching episode of the item's RSS feed (Scanner.quickMatchPodcastEpisodes).
// A field is written when the feed has a value and the episode has none, or
// when overrideDetails is set. It returns how many episodes it updated.
func (h *Handler) QuickMatchEpisodes(ctx context.Context, itemID string, overrideDetails bool) (int, error) {
	it, err := h.load(ctx, itemID)
	if err != nil {
		return 0, err
	}
	if it.Podcast == nil {
		return 0, fmt.Errorf("item %s is not a podcast", itemID)
	}
	parsed, err := h.fetchFeed(ctx, db.Val(it.Podcast.FeedURL))
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, local := range it.Podcast.Episodes {
		if db.Val(local.EnclosureURL) != "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return updated, err
		}
		match := bestMatch(db.Val(local.Title), parsed.Episodes)
		if match == nil {
			continue
		}
		row, columns := local.PodcastEpisode, []string{}
		text := func(column string, dst **string, value string) {
			if value != "" && (*dst == nil || overrideDetails) {
				*dst = &value
				columns = append(columns, column)
			}
		}
		text("title", &row.Title, match.Title)
		text("subtitle", &row.Subtitle, match.Subtitle)
		text("description", &row.Description, match.Description)
		text("enclosureURL", &row.EnclosureURL, match.Enclosure.URL)
		text("enclosureType", &row.EnclosureType, match.Enclosure.Type)
		text("episode", &row.Episode, match.Episode)
		text("episodeType", &row.EpisodeType, cmp.Or(match.EpisodeType, "full"))
		text("season", &row.Season, match.Season)
		text("pubDate", &row.PubDate, match.PubDate)
		if size, err := strconv.ParseInt(match.Enclosure.Length, 10, 64); err == nil && (row.EnclosureSize == nil || overrideDetails) {
			row.EnclosureSize = &size
			columns = append(columns, "enclosureSize")
		}
		if ms := match.PublishedAt; ms != nil && *ms != 0 && (row.PublishedAt.IsZero() || overrideDetails) {
			row.PublishedAt = db.FromMillis(*ms)
			columns = append(columns, "publishedAt")
		}
		if len(columns) == 0 {
			continue
		}
		row.UpdatedAt = db.Now()
		if err := h.db.Update(ctx, "podcastEpisodes", &row, append(columns, "updatedAt")...); err != nil {
			return updated, fmt.Errorf("update podcast episode %q: %w", row.ID, err)
		}
		updated++
	}
	return updated, nil
}

// bestMatch is the feed episode most similar to title, nil below matchThreshold.
// An episode's score is the best of its title, its subtitle and their 70/30 mix.
func bestMatch(title string, episodes []feedEpisode) *feedEpisode {
	needle := strings.ToLower(strings.TrimSpace(title))
	if needle == "" {
		return nil
	}
	var best *feedEpisode
	bestScore := 0.0
	for i := range episodes {
		byTitle := similarity(needle, strings.ToLower(strings.TrimSpace(episodes[i].Title)))
		bySubtitle := similarity(needle, strings.ToLower(strings.TrimSpace(episodes[i].Subtitle)))
		if score := max(byTitle*.7+bySubtitle*.3, byTitle, bySubtitle); score > bestScore {
			bestScore, best = score, &episodes[i]
		}
	}
	if bestScore < matchThreshold {
		return nil
	}
	return best
}

// similarity is 1 minus the edit distance over the longer length, 0 for an
// empty side.
func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	return 1 - float64(textdist.Levenshtein(a, b))/float64(max(len([]rune(a)), len([]rune(b))))
}
