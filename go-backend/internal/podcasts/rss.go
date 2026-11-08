package podcasts

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"audiobookshelf-compat/internal/textclean"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"golang.org/x/net/html/charset"
)

// Feed is a parsed RSS feed in the API shape of POST /api/podcasts/feed.
type Feed struct {
	Metadata FeedMetadata `json:"metadata"`
	Episodes []Episode    `json:"episodes"`
}

type FeedMetadata struct {
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

// Episode is a feed episode in the API shape. Clients post these back to
// download-episodes.
type Episode struct {
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

type enclosure struct {
	URL    string  `json:"url"`
	Type   string  `json:"type,omitempty"`
	Length numText `json:"length,omitempty"`
}

// numText is a size a client may send as a JSON number or a string.
type numText string

func (n *numText) UnmarshalJSON(b []byte) error {
	var num json.Number
	if json.Unmarshal(b, &num) != nil {
		_ = json.Unmarshal(b, (*string)(n))
		return nil
	}
	*n = numText(num)
	return nil
}

type chapter struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type opmlFeed struct {
	Title   string `json:"title"`
	FeedURL string `json:"feedUrl"`
}

// parseFeed maps a gofeed parse of an RSS document to the podcast metadata and
// episodes Go stores. The iTunes fields are read from gofeed's iTunes
// extension, the chapter and media fields from its generic extensions.
// gofeed's translated fields are used only where they equal the raw element.
func parseFeed(raw []byte, feedURL string) (*Feed, error) {
	parser := gofeed.NewParser()
	// The channel image comes from <image>, then itunes:image. gofeed's
	// last-resort scan of the description HTML is off.
	parser.RSSTranslator = &gofeed.DefaultRSSTranslator{DisableContentImageScan: true}
	feed, err := parser.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if feed.FeedType != "rss" {
		return nil, fmt.Errorf("invalid RSS document")
	}
	if len(feed.Items) == 0 {
		return nil, fmt.Errorf("RSS feed has no episodes")
	}
	itunes := feed.ITunesExt
	if itunes == nil {
		itunes = &ext.ITunesFeedExtension{}
	}
	image := itunes.Image
	if feed.Image != nil && feed.Image.URL != "" {
		image = feed.Image.URL
	}
	out := &Feed{Metadata: FeedMetadata{Title: feed.Title, Language: feed.Language, Explicit: itunes.Explicit, Author: itunes.Author, PubDate: feed.Published, Link: feed.Link, Image: image,
		Categories: feedCategories(itunes), FeedURL: feedURL, Type: itunes.Type}}
	if feedURL == "" {
		out.Metadata.FeedURL = feed.FeedLink
	}
	if itunes.NewFeedURL != "" {
		out.Metadata.FeedURL = itunes.NewFeedURL
	}
	description := strings.TrimSpace(feed.Description)
	out.Metadata.Description, out.Metadata.DescriptionPlain = textclean.Sanitize(description), textclean.Strip(description)
	for _, item := range feed.Items {
		if episode, ok := parseEpisode(item); ok {
			out.Episodes = append(out.Episodes, episode)
		}
	}
	if len(out.Episodes) == 0 {
		return nil, fmt.Errorf("RSS feed has no playable episodes")
	}
	return out, nil
}

// feedCategories joins each itunes:category with its one subcategory as
// "Parent:Child", and keeps a category without one as its own name. A category
// without a name is skipped. A category with no names gives a nil slice.
func feedCategories(itunes *ext.ITunesFeedExtension) []string {
	var genres []string
	for _, c := range itunes.Categories {
		if c == nil || c.Text == "" {
			continue
		}
		if c.Subcategory == nil {
			genres = append(genres, c.Text)
		} else if c.Subcategory.Text != "" {
			genres = append(genres, c.Text+":"+c.Subcategory.Text)
		}
	}
	return genres
}

func parseEpisode(item *gofeed.Item) (Episode, bool) {
	itunes := item.ITunesExt
	if itunes == nil {
		itunes = &ext.ITunesItemExtension{}
	}
	urlValue, typeValue, length := episodeEnclosure(item)
	if urlValue == "" {
		return Episode{}, false
	}
	rawDesc := strings.TrimSpace(item.Content)
	if rawDesc == "" {
		rawDesc = strings.TrimSpace(item.Description)
	}
	ep := Episode{Title: item.Title, Subtitle: itunes.Subtitle, Description: textclean.Sanitize(rawDesc), DescriptionPlain: textclean.Strip(rawDesc), PubDate: item.Published,
		EpisodeType: itunes.EpisodeType, Season: itunes.Season, Episode: itunes.Episode, Author: itunes.Author, Duration: itunes.Duration, Explicit: itunes.Explicit,
		Chapters: []chapter{}, Enclosure: enclosure{URL: urlValue, Type: typeValue, Length: numText(length)}}
	if item.GUID != "" {
		ep.GUID = &item.GUID
	}
	if item.PublishedParsed != nil && item.PublishedParsed.UnixMilli() != 0 {
		ms := item.PublishedParsed.UnixMilli()
		ep.PublishedAt = &ms
	}
	durSec := durationSeconds(itunes.Duration)
	if durSec > 0 {
		ep.DurationSeconds = &durSec
	}
	if psc := firstExtension(item.Extensions, "psc", "chapters"); psc != nil {
		ep.Chapters = append(ep.Chapters, pscChapters(psc.Children["chapter"], durSec)...)
	}
	if ch := firstExtension(item.Extensions, "podcast", "chapters"); ch != nil {
		if url := strings.TrimSpace(ch.Attrs["url"]); url != "" {
			chType := cmp.Or(strings.TrimSpace(ch.Attrs["type"]), "application/json")
			ep.ChaptersURL, ep.ChaptersType = &url, &chType
		} else if chType := strings.TrimSpace(ch.Attrs["type"]); chType != "" {
			ep.ChaptersType = &chType
		}
	}
	return ep, true
}

// episodeEnclosure returns the first enclosure. When it has no URL, it returns
// the first audio media:content instead.
func episodeEnclosure(item *gofeed.Item) (urlValue, typeValue, length string) {
	if len(item.Enclosures) > 0 {
		enc := item.Enclosures[0]
		urlValue, typeValue, length = strings.TrimSpace(enc.URL), strings.TrimSpace(enc.Type), strings.TrimSpace(enc.Length)
	}
	if urlValue != "" {
		return urlValue, typeValue, length
	}
	for _, m := range item.Extensions["media"]["content"] {
		if strings.HasPrefix(m.Attrs["type"], "audio") {
			return strings.TrimSpace(m.Attrs["url"]), strings.TrimSpace(m.Attrs["type"]), strings.TrimSpace(m.Attrs["fileSize"])
		}
	}
	return "", "", ""
}

// firstExtension returns the first element named name in the namespace prefix.
func firstExtension(exts ext.Extensions, prefix, name string) *ext.Extension {
	matches := exts[prefix][name]
	if len(matches) == 0 {
		return nil
	}
	return &matches[0]
}

func durationSeconds(s string) int {
	parts := strings.Split(strings.TrimSpace(s), ":")
	total := 0
	for _, part := range parts {
		var v int
		_, _ = fmt.Sscanf(part, "%d", &v)
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

// parseOPML lists the RSS feeds of an OPML document in document order,
// folders flattened. Element and attribute names match case-insensitively, as
// in TS (htmlparser2). A document that does not parse has none.
func parseOPML(raw string) []opmlFeed {
	decoder := xml.NewDecoder(strings.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	var out []opmlFeed
	sawElement := false
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return []opmlFeed{}
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		sawElement = true
		attrs := map[string]string{}
		for _, a := range start.Attr {
			attrs[strings.ToLower(a.Name.Local)] = strings.TrimSpace(a.Value)
		}
		if feed := attrs["xmlurl"]; strings.EqualFold(start.Name.Local, "outline") && strings.EqualFold(attrs["type"], "rss") && feed != "" {
			out = append(out, opmlFeed{cmp.Or(attrs["title"], attrs["text"]), feed})
		}
	}
	if !sawElement {
		return []opmlFeed{}
	}
	return out
}
