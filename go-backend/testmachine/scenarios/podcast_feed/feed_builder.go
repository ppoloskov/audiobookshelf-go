package podcast_feed

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// feedChapter is one podlove simple chapter.
type feedChapter struct {
	Start string
	Title string
}

// feedItem is one RSS item of a stub feed.
type feedItem struct {
	Title           string
	Subtitle        string // CDATA
	SubtitleType    string // writes a type attribute on itunes:subtitle
	DescriptionType string // writes a type attribute on description
	PubDateType     string // writes a type attribute on pubDate
	ContentEncoded  string // CDATA, full HTML description
	Description     string // CDATA
	PubDate         string
	GUID            string
	GUIDPermaLink   bool
	GUIDBare        bool // writes guid without attributes
	EnclosureURL    string
	EnclosureType   string
	EnclosureLen    string
	MediaURL        string // media:content fallback
	MediaType       string
	MediaLen        string
	Duration        string
	Episode         string
	Season          string
	EpisodeType     string
	Author          string
	Explicit        string
	ChaptersURL     string
	ChaptersType    string
	Chapters        []feedChapter
}

// feed is a stub RSS channel.
type feed struct {
	Title       string
	Link        string
	Language    string
	Description string // CDATA
	Author      string
	Explicit    string
	Type        string
	PubDate     string
	Image       string // itunes:image href
	NewFeedURL  string
	AtomLink    string
	Categories  []string // "Technology" or "Technology/Software"
	Items       []feedItem
}

// document returns the RSS XML.
func (f feed) document() string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:atom="http://www.w3.org/2005/Atom" xmlns:podcast="https://podcastindex.org/namespace/1.0" xmlns:psc="http://podlove.org/simple-chapters" xmlns:media="http://search.yahoo.com/mrss/">` + "\n")
	b.WriteString("  <channel>\n")
	element(&b, "title", f.Title)
	element(&b, "link", f.Link)
	element(&b, "language", f.Language)
	if f.Description != "" {
		fmt.Fprintf(&b, "    <description><![CDATA[%s]]></description>\n", f.Description)
	}
	element(&b, "itunes:author", f.Author)
	element(&b, "itunes:explicit", f.Explicit)
	element(&b, "itunes:type", f.Type)
	element(&b, "pubDate", f.PubDate)
	if f.Image != "" {
		fmt.Fprintf(&b, "    <itunes:image %s/>\n", attribute("href", f.Image))
	}
	for _, category := range f.Categories {
		parts := strings.SplitN(category, "/", 2)
		if len(parts) == 2 {
			fmt.Fprintf(&b, "    <itunes:category %s><itunes:category %s/></itunes:category>\n", attribute("text", parts[0]), attribute("text", parts[1]))
			continue
		}
		fmt.Fprintf(&b, "    <itunes:category %s/>\n", attribute("text", category))
	}
	element(&b, "itunes:new-feed-url", f.NewFeedURL)
	if f.AtomLink != "" {
		fmt.Fprintf(&b, "    <atom:link %s %s %s/>\n", attribute("href", f.AtomLink), attribute("rel", "self"), attribute("type", "application/rss+xml"))
	}
	for _, item := range f.Items {
		b.WriteString("    <item>\n")
		element(&b, "title", item.Title)
		attributeCDATAElement(&b, "itunes:subtitle", item.SubtitleType, item.Subtitle)
		cdataElement(&b, "content:encoded", item.ContentEncoded)
		attributeCDATAElement(&b, "description", item.DescriptionType, item.Description)
		attributedElement(&b, "pubDate", item.PubDateType, item.PubDate)
		if item.GUID != "" {
			switch {
			case item.GUIDBare:
				fmt.Fprintf(&b, "    <guid>%s</guid>\n", text(item.GUID))
			case item.GUIDPermaLink:
				fmt.Fprintf(&b, "    <guid %s>%s</guid>\n", attribute("isPermaLink", "true"), text(item.GUID))
			default:
				fmt.Fprintf(&b, "    <guid %s>%s</guid>\n", attribute("isPermaLink", "false"), text(item.GUID))
			}
		}
		if item.EnclosureURL != "" {
			var attributes []string
			attributes = append(attributes, attribute("url", item.EnclosureURL))
			if item.EnclosureType != "" {
				attributes = append(attributes, attribute("type", item.EnclosureType))
			}
			if item.EnclosureLen != "" {
				attributes = append(attributes, attribute("length", item.EnclosureLen))
			}
			fmt.Fprintf(&b, "    <enclosure %s/>\n", strings.Join(attributes, " "))
		}
		if item.MediaURL != "" {
			var attributes []string
			attributes = append(attributes, attribute("url", item.MediaURL))
			if item.MediaType != "" {
				attributes = append(attributes, attribute("type", item.MediaType))
			}
			if item.MediaLen != "" {
				attributes = append(attributes, attribute("length", item.MediaLen))
			}
			fmt.Fprintf(&b, "    <media:content %s/>\n", strings.Join(attributes, " "))
		}
		element(&b, "itunes:duration", item.Duration)
		element(&b, "itunes:episode", item.Episode)
		element(&b, "itunes:season", item.Season)
		element(&b, "itunes:episodeType", item.EpisodeType)
		element(&b, "itunes:author", item.Author)
		element(&b, "itunes:explicit", item.Explicit)
		if item.ChaptersURL != "" {
			fmt.Fprintf(&b, "    <podcast:chapters %s %s/>\n", attribute("url", item.ChaptersURL), attribute("type", item.ChaptersType))
		}
		if len(item.Chapters) > 0 {
			b.WriteString(`    <psc:chapters version="1.2">`)
			for _, chapter := range item.Chapters {
				fmt.Fprintf(&b, "<psc:chapter %s %s/>", attribute("start", chapter.Start), attribute("title", chapter.Title))
			}
			b.WriteString("</psc:chapters>\n")
		}
		b.WriteString("    </item>\n")
	}
	b.WriteString("  </channel>\n</rss>\n")
	return b.String()
}

// element writes an element when the value is non-empty.
func element(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "    <%s>%s</%s>\n", name, text(value), name)
}

// cdataElement writes an element whose text is CDATA-wrapped.
func cdataElement(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	if strings.Contains(value, "]]>") {
		panic("CDATA content must not contain ]]>")
	}
	fmt.Fprintf(b, "    <%s><![CDATA[%s]]></%s>\n", name, value, name)
}

// attributedElement writes a text element with a type attribute when given.
func attributedElement(b *strings.Builder, name, attributeName, value string) {
	if value == "" {
		return
	}
	if attributeName == "" {
		element(b, name, value)
		return
	}
	fmt.Fprintf(b, "    <%s %s>%s</%s>\n", name, attribute("type", attributeName), text(value), name)
}

// attributeCDATAElement writes a CDATA element with one attribute when given.
func attributeCDATAElement(b *strings.Builder, name, attributeName, value string) {
	if value == "" {
		return
	}
	if attributeName == "" {
		cdataElement(b, name, value)
		return
	}
	if strings.Contains(value, "]]>") {
		panic("CDATA content must not contain ]]>")
	}
	fmt.Fprintf(b, "    <%s %s><![CDATA[%s]]></%s>\n", name, attribute("type", attributeName), value, name)
}

// text escapes character data.
func text(value string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(value)); err != nil {
		panic(err) // building a test document cannot fail on a string
	}
	return b.String()
}

// attribute renders name="value" with escaped quotes.
func attribute(name, value string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;").Replace(value)
	return name + `="` + escaped + `"`
}
