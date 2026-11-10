package podcasts

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

// QuickMatchEpisodes updates unmatched episodes from the item's RSS feed
// (Scanner.quickMatchPodcastEpisodes) and returns how many it updated.
func (h *Handler) QuickMatchEpisodes(ctx context.Context, itemID string, overrideDetails bool) (int, error) {
	var mediaID, feedURL string
	if err := h.db.QueryRowContext(ctx, `SELECT li.mediaId,p.feedURL FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.id=? AND li.mediaType='podcast'`, itemID).Scan(&mediaID, &feedURL); err != nil {
		return 0, err
	}
	feed, err := h.getFeed(ctx, feedURL)
	if err != nil {
		return 0, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id,title,subtitle,description,enclosureURL,enclosureSize,enclosureType,episode,episodeType,season,pubDate,publishedAt FROM podcastEpisodes WHERE podcastId=? AND (enclosureURL IS NULL OR enclosureURL='') ORDER BY createdAt ASC`, mediaID)
	if err != nil {
		return 0, err
	}
	type episode struct {
		id                                                                       string
		title, subtitle, description, enclosureURL, enclosureSize, enclosureType sql.NullString
		episode, episodeType, season, pubDate, publishedAt                       sql.NullString
	}
	var unmatched []episode
	for rows.Next() {
		var ep episode
		if err := rows.Scan(&ep.id, &ep.title, &ep.subtitle, &ep.description, &ep.enclosureURL, &ep.enclosureSize, &ep.enclosureType, &ep.episode, &ep.episodeType, &ep.season, &ep.pubDate, &ep.publishedAt); err != nil {
			rows.Close()
			return 0, err
		}
		unmatched = append(unmatched, ep)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	updated := 0
	for _, local := range unmatched {
		if err := ctx.Err(); err != nil {
			return updated, err
		}
		match := bestFeedEpisode(local.title.String, feed.Episodes)
		if match == nil {
			continue
		}
		var size any = string(match.Enclosure.Length)
		if n, err := strconv.ParseInt(string(match.Enclosure.Length), 10, 64); err == nil {
			size = n
		}
		kind := match.EpisodeType
		if kind == "" {
			kind = "full"
		}
		var published any
		if match.PublishedAt != nil && *match.PublishedAt != 0 {
			published = dbtime.Format(time.UnixMilli(*match.PublishedAt))
		}
		values := []struct {
			column string
			old    sql.NullString
			value  any
		}{
			{"title", local.title, match.Title}, {"subtitle", local.subtitle, match.Subtitle}, {"description", local.description, match.Description},
			{"enclosureURL", local.enclosureURL, match.Enclosure.URL}, {"enclosureSize", local.enclosureSize, size}, {"enclosureType", local.enclosureType, match.Enclosure.Type},
			{"episode", local.episode, match.Episode}, {"episodeType", local.episodeType, kind}, {"season", local.season, match.Season}, {"pubDate", local.pubDate, match.PubDate}, {"publishedAt", local.publishedAt, published},
		}
		var set []string
		var args []any
		for _, field := range values {
			if field.value == nil || field.value == "" || (field.old.Valid && !overrideDetails) {
				continue
			}
			set = append(set, field.column+"=?")
			args = append(args, field.value)
		}
		if len(set) == 0 {
			continue
		}
		set = append(set, "updatedAt=?")
		args = append(args, dbtime.Now(), local.id)
		if _, err := h.db.ExecContext(ctx, `UPDATE podcastEpisodes SET `+strings.Join(set, ",")+` WHERE id=?`, args...); err != nil {
			return updated, fmt.Errorf("podcasts: update podcast episode %q: %w", local.id, err)
		}
		updated++
	}
	return updated, nil
}

func bestFeedEpisode(title string, episodes []Episode) *Episode {
	needle := strings.ToLower(strings.TrimSpace(title))
	if needle == "" {
		return nil
	}
	var best *Episode
	bestScore := 0.0
	for i := range episodes {
		episode := &episodes[i]
		candidate := strings.ToLower(strings.TrimSpace(episode.Title))
		subtitle := strings.ToLower(strings.TrimSpace(episode.Subtitle))
		titleScore, subtitleScore := similarity(needle, candidate), similarity(needle, subtitle)
		score := titleScore*.7 + subtitleScore*.3
		if titleScore > score {
			score = titleScore
		}
		if subtitleScore > score {
			score = subtitleScore
		}
		if score > bestScore {
			bestScore, best = score, episode
		}
	}
	if bestScore < .9 {
		return nil
	}
	return best
}
