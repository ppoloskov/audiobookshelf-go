package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/textclean"
)

// itunes searches the iTunes catalog for audiobooks and podcasts.
type itunes struct {
	getter
	base string
}

// itunesItem is one iTunes search result.
type itunesItem struct {
	CollectionID   json.RawMessage `json:"collectionId"`
	ArtistID       json.RawMessage `json:"artistId"`
	TrackCount     json.RawMessage `json:"trackCount"`
	CollectionName string          `json:"collectionName"`
	ArtistName     string          `json:"artistName"`
	Description    string          `json:"description"`
	ReleaseDate    string          `json:"releaseDate"`
	PrimaryGenre   string          `json:"primaryGenreName"`
	Genres         []string        `json:"genres"`
	FeedURL        string          `json:"feedUrl"`
	ViewURL        string          `json:"collectionViewUrl"`
	Explicitness   string          `json:"trackExplicitness"`
	artwork        []artwork
}

// artwork is one artworkUrl<size> key of an item.
type artwork struct {
	size int
	url  string
}

// UnmarshalJSON reads the named fields, then collects every artworkUrl<size>
// key. A mistyped field stays zero.
func (i *itunesItem) UnmarshalJSON(data []byte) error {
	type plain itunesItem
	_ = json.Unmarshal(data, (*plain)(i)) // a mistyped field stays zero
	var all map[string]json.RawMessage
	_ = json.Unmarshal(data, &all) // the same document decoded above
	for key, raw := range all {
		size, ok := strings.CutPrefix(key, "artworkUrl")
		if !ok {
			continue
		}
		var link string
		if json.Unmarshal(raw, &link) == nil {
			n, _ := strconv.Atoi(size) // a non-numeric suffix sorts first
			i.artwork = append(i.artwork, artwork{n, link})
		}
	}
	slices.SortFunc(i.artwork, func(a, b artwork) int { return a.size - b.size })
	return nil
}

// cover is the 600px artwork, else the first larger one, else a smaller one
// resized to 600, else the largest there is.
func (i itunesItem) cover() string {
	for _, a := range i.artwork {
		if a.size == 600 && a.url != "" {
			return a.url
		}
	}
	for _, a := range i.artwork {
		if a.size > 600 {
			return a.url
		}
	}
	for _, a := range i.artwork {
		if dims := fmt.Sprintf("%dx%d", a.size, a.size); strings.Contains(a.url, dims) {
			return strings.Replace(a.url, dims, "600x600", 1)
		}
	}
	if len(i.artwork) > 0 {
		return i.artwork[len(i.artwork)-1].url
	}
	return ""
}

func (p itunes) search(ctx context.Context, term, entity, country string) ([]itunesItem, error) {
	query := url.Values{"term": {term}, "entity": {entity}, "media": {entity}}
	if country != "" {
		query.Set("country", country)
	}
	var response struct {
		Results []itunesItem `json:"results"`
	}
	if err := p.json(ctx, p.base+"?"+query.Encode(), &response); err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (p itunes) searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error) {
	if q.Title == "" {
		return nil, nil
	}
	items, err := p.search(ctx, q.Title, "audiobook", "")
	if err != nil {
		return nil, nil // a failing provider is an empty answer
	}
	var out []BookResult
	for _, item := range items {
		book := BookResult{
			ID: item.CollectionID, ArtistID: item.ArtistID, Title: item.CollectionName,
			Author: strings.ReplaceAll(item.ArtistName, " & ", ", "), Description: item.Description,
			PublishedYear: before(item.ReleaseDate, "-"), Cover: item.cover(),
		}
		if item.PrimaryGenre != "" {
			book.Genres = []string{item.PrimaryGenre}
		}
		out = append(out, book)
	}
	return out, nil
}

// searchPodcasts returns the cleaned podcast results; country is left out of
// the query when empty.
func (p itunes) searchPodcasts(ctx context.Context, term, country string) ([]PodcastResult, error) {
	items, err := p.search(ctx, term, "podcast", country)
	if err != nil {
		return nil, err
	}
	out := make([]PodcastResult, len(items))
	for i, item := range items {
		genres := item.Genres
		if genres == nil {
			genres = []string{}
		}
		out[i] = PodcastResult{
			ID: item.CollectionID, ArtistID: item.ArtistID, Title: nonEmpty(item.CollectionName), ArtistName: nonEmpty(item.ArtistName),
			Description: textclean.Sanitize(item.Description), DescriptionPlain: textclean.Strip(item.Description),
			ReleaseDate: nonEmpty(item.ReleaseDate), Genres: genres, Cover: nonEmpty(item.cover()), TrackCount: item.TrackCount,
			FeedURL: nonEmpty(item.FeedURL), PageURL: nonEmpty(item.ViewURL), Explicit: item.Explicitness == "explicit",
		}
	}
	return out, nil
}

// podcastCovers are the distinct covers of the podcasts matching term.
func (p itunes) podcastCovers(ctx context.Context, term string) ([]string, error) {
	items, err := p.search(ctx, term, "podcast", "")
	if err != nil {
		return nil, err
	}
	var covers []string
	for _, item := range items {
		covers = append(covers, item.cover())
	}
	return uniqueTrimmed(covers), nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
