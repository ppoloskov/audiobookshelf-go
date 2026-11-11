package podcast

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"golang.org/x/net/html/charset"

	"audiobookshelf-compat/internal/textclean"
)

// maxFeedBytes bounds a feed document.
const maxFeedBytes = 32 << 20

// feed is a parsed RSS feed in the API shape of POST /api/podcasts/feed.
type feed struct {
	Metadata feedMetadata  `json:"metadata"`
	Episodes []feedEpisode `json:"episodes"`
}

type feedMetadata struct {
	Title            string   `json:"title"`
	Language         string   `json:"language"`
	Explicit         string   `json:"explicit"`
	Author           string   `json:"author"`
	PubDate          string   `json:"pubDate"`
	Link             string   `json:"link"`
	Image            string   `json:"image"`
	Categories       []string `json:"categories"`
	FeedURL          string   `json:"feedUrl"`
	Description      string   `json:"description"`
	DescriptionPlain string   `json:"descriptionPlain"`
	Type             string   `json:"type"`
}

// feedEpisode is a feed episode in the API shape. The web client posts these
// back to download-episodes.
type feedEpisode struct {
	Title            string    `json:"title"`
	Subtitle         string    `json:"subtitle"`
	Description      string    `json:"description"`
	DescriptionPlain string    `json:"descriptionPlain"`
	PubDate          string    `json:"pubDate"`
	EpisodeType      string    `json:"episodeType"`
	Season           string    `json:"season"`
	Episode          string    `json:"episode"`
	Author           string    `json:"author"`
	Duration         string    `json:"duration"`
	DurationSeconds  *int      `json:"durationSeconds"`
	Explicit         string    `json:"explicit"`
	PublishedAt      *int64    `json:"publishedAt"`
	Enclosure        enclosure `json:"enclosure"`
	GUID             *string   `json:"guid"`
	ChaptersURL      *string   `json:"chaptersUrl"`
	ChaptersType     *string   `json:"chaptersType"`
	Chapters         []chapter `json:"chapters"`
}

// enclosure is the audio file of a feed episode; Length is the decimal size
// text the feed gives (the client posts it back as a string).
type enclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Length string `json:"length,omitempty"`
}

type chapter struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// fetchFeed downloads and parses the feed at rawURL. net/http decodes gzip.
func (h *Handler) fetchFeed(ctx context.Context, rawURL string) (*feed, error) {
	feedURL := httpURL(rawURL)
	if feedURL == "" {
		return nil, errors.New("invalid feed URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/xhtml+xml, application/xml, */*;q=0.8")
	agent := "audiobookshelf (+https://audiobookshelf.org; like iTMS)"
	if strings.HasPrefix(feedURL, "https://www.cbc.ca") {
		agent += " - CBC" // podcastUtils.js:362: the CBC feed rejects the plain agent
	}
	req.Header.Set("User-Agent", agent)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("feed returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFeedBytes {
		return nil, errors.New("podcast feed is too large")
	}
	return parseFeed(raw, feedURL)
}

// httpURL returns the normalised http(s) URL, or "" when raw is not one.
func httpURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}

// parseFeed maps an RSS document to the feed Go stores: iTunes fields from
// gofeed's iTunes extension, chapters and media from its generic extensions.
func parseFeed(raw []byte, feedURL string) (*feed, error) {
	parser := gofeed.NewParser()
	// The channel image is <image>, then itunes:image; gofeed's last-resort
	// scan of the description HTML is off.
	parser.RSSTranslator = &gofeed.DefaultRSSTranslator{DisableContentImageScan: true}
	parsed, err := parser.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if parsed.FeedType != "rss" {
		return nil, errors.New("invalid RSS document")
	}
	if len(parsed.Items) == 0 {
		return nil, errors.New("RSS feed has no episodes")
	}
	itunes := parsed.ITunesExt
	if itunes == nil {
		itunes = &ext.ITunesFeedExtension{}
	}
	image := itunes.Image
	if parsed.Image != nil && parsed.Image.URL != "" {
		image = parsed.Image.URL
	}
	description := strings.TrimSpace(parsed.Description)
	out := &feed{Metadata: feedMetadata{Title: parsed.Title, Language: parsed.Language, Explicit: itunes.Explicit, Author: itunes.Author,
		PubDate: parsed.Published, Link: parsed.Link, Image: image, Categories: categories(itunes), FeedURL: cmp.Or(itunes.NewFeedURL, feedURL, parsed.FeedLink),
		Description: textclean.Sanitize(description), DescriptionPlain: textclean.Strip(description), Type: itunes.Type}}
	for _, it := range parsed.Items {
		if episode, ok := parseEpisode(it); ok {
			out.Episodes = append(out.Episodes, episode)
		}
	}
	if len(out.Episodes) == 0 {
		return nil, errors.New("RSS feed has no playable episodes")
	}
	return out, nil
}

// categories joins each itunes:category with its subcategory as "Parent:Child";
// a category without a name is skipped.
func categories(itunes *ext.ITunesFeedExtension) []string {
	var genres []string
	for _, c := range itunes.Categories {
		switch {
		case c == nil || c.Text == "":
		case c.Subcategory == nil:
			genres = append(genres, c.Text)
		case c.Subcategory.Text != "":
			genres = append(genres, c.Text+":"+c.Subcategory.Text)
		}
	}
	return genres
}

func parseEpisode(it *gofeed.Item) (feedEpisode, bool) {
	itunes := it.ITunesExt
	if itunes == nil {
		itunes = &ext.ITunesItemExtension{}
	}
	enc := firstEnclosure(it)
	if enc.URL == "" {
		return feedEpisode{}, false
	}
	desc := cmp.Or(strings.TrimSpace(it.Content), strings.TrimSpace(it.Description))
	ep := feedEpisode{Title: it.Title, Subtitle: itunes.Subtitle, Description: textclean.Sanitize(desc), DescriptionPlain: textclean.Strip(desc), PubDate: it.Published,
		EpisodeType: itunes.EpisodeType, Season: itunes.Season, Episode: itunes.Episode, Author: itunes.Author, Duration: itunes.Duration, Explicit: itunes.Explicit,
		Chapters: []chapter{}, Enclosure: enc}
	if it.GUID != "" {
		ep.GUID = &it.GUID
	}
	if it.PublishedParsed != nil && it.PublishedParsed.UnixMilli() != 0 {
		ms := it.PublishedParsed.UnixMilli()
		ep.PublishedAt = &ms
	}
	seconds := durationSeconds(itunes.Duration)
	if seconds > 0 {
		ep.DurationSeconds = &seconds
	}
	if psc := first(it.Extensions, "psc", "chapters"); psc != nil {
		ep.Chapters = append(ep.Chapters, pscChapters(psc.Children["chapter"], seconds)...)
	}
	if ch := first(it.Extensions, "podcast", "chapters"); ch != nil {
		kind := strings.TrimSpace(ch.Attrs["type"])
		if link := strings.TrimSpace(ch.Attrs["url"]); link != "" {
			kind = cmp.Or(kind, "application/json")
			ep.ChaptersURL, ep.ChaptersType = &link, &kind
		} else if kind != "" {
			ep.ChaptersType = &kind
		}
	}
	return ep, true
}

// firstEnclosure is the first enclosure, or the first audio media:content when
// that has no URL.
func firstEnclosure(it *gofeed.Item) enclosure {
	if len(it.Enclosures) > 0 {
		e := it.Enclosures[0]
		if u := strings.TrimSpace(e.URL); u != "" {
			return enclosure{URL: u, Type: strings.TrimSpace(e.Type), Length: strings.TrimSpace(e.Length)}
		}
	}
	for _, m := range it.Extensions["media"]["content"] {
		if strings.HasPrefix(m.Attrs["type"], "audio") {
			return enclosure{URL: strings.TrimSpace(m.Attrs["url"]), Type: strings.TrimSpace(m.Attrs["type"]), Length: strings.TrimSpace(m.Attrs["fileSize"])}
		}
	}
	return enclosure{}
}

// first returns the first element named name in the namespace prefix.
func first(exts ext.Extensions, prefix, name string) *ext.Extension {
	if matches := exts[prefix][name]; len(matches) > 0 {
		return &matches[0]
	}
	return nil
}

// durationSeconds reads "1:02:03", "62:03" or "3723"; each part keeps its
// leading digits (Sscanf), so "02.500" is 2.
func durationSeconds(s string) int {
	total := 0
	for _, part := range strings.Split(strings.TrimSpace(s), ":") {
		var v int
		_, _ = fmt.Sscanf(part, "%d", &v) // no leading digits counts as 0
		total = total*60 + v
	}
	return total
}

func pscChapters(nodes []ext.Extension, duration int) []chapter {
	var out []chapter
	for i, ch := range nodes {
		title := strings.TrimSpace(ch.Attrs["title"])
		if title == "" {
			continue
		}
		end := duration
		if i+1 < len(nodes) {
			end = durationSeconds(nodes[i+1].Attrs["start"])
		}
		out = append(out, chapter{ID: len(out), Title: title, Start: durationSeconds(ch.Attrs["start"]), End: end})
	}
	return out
}

// opmlFeed is one RSS subscription of an OPML document.
type opmlFeed struct {
	Title   string `json:"title"`
	FeedURL string `json:"feedUrl"`
}

type opmlOutline struct {
	Type     string        `xml:"type,attr"`
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr"`
	XMLURL   string        `xml:"xmlUrl,attr"`
	Outlines []opmlOutline `xml:"outline"`
}

// parseOPML lists the RSS outlines of an OPML document in document order,
// folders flattened. A document that does not parse has none.
func parseOPML(text string) []opmlFeed {
	decoder := xml.NewDecoder(strings.NewReader(text))
	decoder.CharsetReader = charset.NewReaderLabel
	var doc struct {
		Outlines []opmlOutline `xml:"body>outline"`
	}
	feeds := []opmlFeed{}
	if decoder.Decode(&doc) != nil {
		return feeds
	}
	var walk func([]opmlOutline)
	walk = func(outlines []opmlOutline) {
		for _, o := range outlines {
			if link := strings.TrimSpace(o.XMLURL); strings.EqualFold(strings.TrimSpace(o.Type), "rss") && link != "" {
				feeds = append(feeds, opmlFeed{Title: cmp.Or(strings.TrimSpace(o.Title), strings.TrimSpace(o.Text)), FeedURL: link})
			}
			walk(o.Outlines)
		}
	}
	walk(doc.Outlines)
	return feeds
}
