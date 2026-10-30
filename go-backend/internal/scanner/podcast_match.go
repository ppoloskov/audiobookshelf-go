package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// QuickMatchPodcastEpisodes updates unmatched episodes from the item's RSS
// feed. The feed fetcher is injected by runtime from the podcasts package.
func (s *Scanner) QuickMatchPodcastEpisodes(ctx context.Context, itemID string, overrideDetails bool) (int, error) {
	var mediaID, feedURL string
	if err := s.db.QueryRowContext(ctx, `SELECT li.mediaId,p.feedURL FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.id=? AND li.mediaType='podcast'`, itemID).Scan(&mediaID, &feedURL); err != nil {
		return 0, err
	}
	s.mu.Lock()
	fetcher := s.podcastFeedFetcher
	s.mu.Unlock()
	if fetcher == nil {
		return 0, errors.New("scanner: podcast feed fetcher is not configured")
	}
	feed, err := fetcher(ctx, feedURL)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,subtitle,description,enclosureURL,enclosureSize,enclosureType,episode,episodeType,season,pubDate,publishedAt FROM podcastEpisodes WHERE podcastId=? AND (enclosureURL IS NULL OR enclosureURL='') ORDER BY createdAt ASC`, mediaID)
	if err != nil {
		return 0, err
	}
	type episode struct {
		id     string
		values [11]sql.NullString
	}
	var unmatched []episode
	for rows.Next() {
		var ep episode
		if err := rows.Scan(&ep.id, &ep.values[0], &ep.values[1], &ep.values[2], &ep.values[3], &ep.values[4], &ep.values[5], &ep.values[6], &ep.values[7], &ep.values[8], &ep.values[9], &ep.values[10]); err != nil {
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
		match := bestFeedEpisode(local.values[0].String, feed)
		if match == nil {
			continue
		}
		fields := map[string]any{}
		values := []struct {
			column string
			index  int
			value  any
		}{
			{"title", 0, match["title"]}, {"subtitle", 1, match["subtitle"]}, {"description", 2, match["description"]},
			{"enclosureURL", 3, nestedString(match, "enclosure", "url")}, {"enclosureSize", 4, nestedValue(match, "enclosure", "length")}, {"enclosureType", 5, nestedString(match, "enclosure", "type")},
			{"episode", 6, match["episode"]}, {"episodeType", 7, match["episodeType"]}, {"season", 8, match["season"]}, {"pubDate", 9, match["pubDate"]}, {"publishedAt", 10, match["publishedAt"]},
		}
		for _, field := range values {
			value := field.value
			if field.column == "episodeType" && (value == nil || value == "") {
				value = "full"
			}
			if value == nil || value == "" || value == float64(0) {
				continue
			}
			if local.values[field.index].Valid && !overrideDetails {
				continue
			}
			fields[field.column] = value
		}
		if len(fields) == 0 {
			continue
		}
		if published, ok := fields["publishedAt"]; ok {
			fields["publishedAt"] = dbDateTime(published)
		}
		set := []string{}
		args := []any{}
		for _, field := range values {
			if value, ok := fields[field.column]; ok {
				set = append(set, field.column+"=?")
				args = append(args, value)
			}
		}
		set = append(set, "updatedAt=?")
		args = append(args, nowDB(), local.id)
		if _, err := s.db.ExecContext(ctx, `UPDATE podcastEpisodes SET `+strings.Join(set, ",")+` WHERE id=?`, args...); err != nil {
			return updated, fmt.Errorf("scanner: update podcast episode %q: %w", local.id, err)
		}
		updated++
	}
	return updated, nil
}

func bestFeedEpisode(title string, episodes []map[string]any) map[string]any {
	needle := strings.ToLower(strings.TrimSpace(title))
	if needle == "" {
		return nil
	}
	var best map[string]any
	bestScore := 0.0
	for _, episode := range episodes {
		candidate := strings.ToLower(strings.TrimSpace(asString(episode["title"])))
		subtitle := strings.ToLower(strings.TrimSpace(asString(episode["subtitle"])))
		titleScore, subtitleScore := editSimilarity(needle, candidate), editSimilarity(needle, subtitle)
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

func editSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		cur := make([]int, len(br)+1)
		cur[0] = i + 1
		for j, rb := range br {
			cost := 0
			if ra != rb {
				cost = 1
			}
			cur[j+1] = min(cur[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev = cur
	}
	n := len(ar)
	if len(br) > n {
		n = len(br)
	}
	return 1 - float64(prev[len(br)])/float64(n)
}
func asString(value any) string { s, _ := value.(string); return s }
func nestedString(values map[string]any, key, sub string) any {
	nested, _ := values[key].(map[string]any)
	if nested == nil {
		return nil
	}
	return nested[sub]
}
func nestedValue(values map[string]any, key, sub string) any {
	nested, _ := values[key].(map[string]any)
	if nested == nil {
		return nil
	}
	value := nested[sub]
	if str, ok := value.(string); ok {
		if n, err := strconv.ParseInt(str, 10, 64); err == nil {
			return n
		}
	}
	return value
}
func dbDateTime(value any) any {
	switch v := value.(type) {
	case float64:
		if v == 0 {
			return nil
		}
		return dbtime.Format(time.UnixMilli(int64(v)))
	case int64:
		if v == 0 {
			return nil
		}
		return dbtime.Format(time.UnixMilli(v))
	}
	return value
}
