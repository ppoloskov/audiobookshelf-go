package podcasts

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"golang.org/x/net/html/charset"
)

// xmlNode is a small element tree for OPML, which gofeed does not parse.
type xmlNode struct {
	Name     xml.Name
	Attr     []xml.Attr
	Text     strings.Builder
	Children []*xmlNode
}

func parseXML(raw []byte) (*xmlNode, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.CharsetReader = charset.NewReaderLabel
	root := &xmlNode{Name: xml.Name{Local: "root"}}
	stack := []*xmlNode{root}
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{Name: t.Name, Attr: t.Attr}
			stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			stack[len(stack)-1].Text.Write([]byte(t))
		}
	}
	if len(root.Children) == 0 {
		return nil, fmt.Errorf("empty XML")
	}
	return root.Children[0], nil
}

func attr(n *xmlNode, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if strings.EqualFold(a.Name.Local, key) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

// parseFeed maps a gofeed parse of an RSS document to the podcast metadata and
// episodes Go stores. The iTunes fields are read from gofeed's iTunes
// extension, the chapter and media fields from its generic extensions.
// gofeed's translated fields are used only where they equal the raw element.
func parseFeed(raw []byte, feedURL string) (map[string]any, error) {
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
	meta := map[string]any{"title": feed.Title, "language": feed.Language, "explicit": itunes.Explicit, "author": itunes.Author, "pubDate": feed.Published, "link": feed.Link, "image": image, "categories": feedCategories(itunes), "feedUrl": feedURL, "description": "", "descriptionPlain": "", "type": itunes.Type}
	if feedURL == "" {
		meta["feedUrl"] = feed.FeedLink
	}
	if itunes.NewFeedURL != "" {
		meta["feedUrl"] = itunes.NewFeedURL
	}
	description := strings.TrimSpace(feed.Description)
	meta["description"], meta["descriptionPlain"] = textclean.Sanitize(description), textclean.Strip(description)
	episodes := make([]map[string]any, 0, len(feed.Items))
	for _, item := range feed.Items {
		episode := parseEpisode(item)
		if episode != nil {
			episodes = append(episodes, episode)
		}
	}
	if len(episodes) == 0 {
		return nil, fmt.Errorf("RSS feed has no playable episodes")
	}
	return map[string]any{"metadata": meta, "episodes": episodes}, nil
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

func parseEpisode(item *gofeed.Item) map[string]any {
	itunes := item.ITunesExt
	if itunes == nil {
		itunes = &ext.ITunesItemExtension{}
	}
	urlValue, typeValue, length := episodeEnclosure(item)
	if urlValue == "" {
		return nil
	}
	rawDesc := strings.TrimSpace(item.Content)
	if rawDesc == "" {
		rawDesc = strings.TrimSpace(item.Description)
	}
	published := int64(0)
	if item.PublishedParsed != nil {
		published = item.PublishedParsed.UnixMilli()
	}
	durSec := durationSeconds(itunes.Duration)
	var dur any
	if durSec > 0 {
		dur = durSec
	}
	var chapters []map[string]any
	if psc := firstExtension(item.Extensions, "psc", "chapters"); psc != nil {
		chapters = pscChapters(psc.Children["chapter"], durSec)
	}
	chaptersURL, chaptersType := "", ""
	if ch := firstExtension(item.Extensions, "podcast", "chapters"); ch != nil {
		chaptersURL = strings.TrimSpace(ch.Attrs["url"])
		chaptersType = strings.TrimSpace(ch.Attrs["type"])
	}
	if chaptersType == "" && chaptersURL != "" {
		chaptersType = "application/json"
	}
	enclosure := map[string]any{"url": urlValue}
	if typeValue != "" {
		enclosure["type"] = typeValue
	}
	if length != "" {
		enclosure["length"] = length
	}
	return map[string]any{"title": item.Title, "subtitle": itunes.Subtitle, "description": textclean.Sanitize(rawDesc), "descriptionPlain": textclean.Strip(rawDesc), "pubDate": item.Published, "episodeType": itunes.EpisodeType, "season": itunes.Season, "episode": itunes.Episode, "author": itunes.Author, "duration": itunes.Duration, "durationSeconds": dur, "explicit": itunes.Explicit, "publishedAt": store.NullIfZero(published), "enclosure": enclosure, "guid": item.GUID, "chaptersUrl": store.NullIfEmpty(chaptersURL), "chaptersType": store.NullIfEmpty(chaptersType), "chapters": chapters}
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

func pscChapters(nodes []ext.Extension, duration int) []map[string]any {
	var out []map[string]any
	for i, ch := range nodes {
		start := durationSeconds(ch.Attrs["start"])
		title := strings.TrimSpace(ch.Attrs["title"])
		if title == "" {
			continue
		}
		end := duration
		if i+1 < len(nodes) {
			end = durationSeconds(nodes[i+1].Attrs["start"])
		}
		out = append(out, map[string]any{"id": len(out), "title": title, "start": start, "end": end})
	}
	return out
}

func parseOPML(raw string) []map[string]string {
	root, err := parseXML([]byte(raw))
	if err != nil {
		return []map[string]string{}
	}
	var out []map[string]string
	var visit func(*xmlNode)
	visit = func(n *xmlNode) {
		if strings.EqualFold(n.Name.Local, "outline") && strings.EqualFold(attr(n, "type"), "rss") {
			feed := attr(n, "xmlurl")
			if feed != "" {
				title := attr(n, "title")
				if title == "" {
					title = attr(n, "text")
				}
				out = append(out, map[string]string{"title": title, "feedUrl": feed})
			}
		}
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(root)
	return out
}
