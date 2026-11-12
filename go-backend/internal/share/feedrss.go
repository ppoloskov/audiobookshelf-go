package share

import (
	"encoding/xml"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/media"
)

// gmtLayout is an RSS date (Date.toGMTString).
const gmtLayout = "Mon, 02 Jan 2006 15:04:05 GMT"

// el is one element of the RSS document. Namespaced names are written as
// "prefix:name"; the prefixes are declared on <rss>.
type el struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	CData   string     `xml:",cdata"` // titles and descriptions carry markup
	Kids    []el       `xml:",any"`
}

func text(name, s string) el { return el{XMLName: xml.Name{Local: name}, Text: s} }

func cdata(name, s string) el { return el{XMLName: xml.Name{Local: name}, CData: s} }

// elem is an element with attributes (name, value pairs) and children.
func elem(name string, attrs []string, kids ...el) el {
	e := el{XMLName: xml.Name{Local: name}, Kids: kids}
	for i := 0; i < len(attrs); i += 2 {
		e.Attrs = append(e.Attrs, xml.Attr{Name: xml.Name{Local: attrs[i]}, Value: attrs[i+1]})
	}
	return e
}

// optional is text(name, s) when s is not empty.
func optional(mk func(string, string) el, name, s string) []el {
	if s == "" {
		return nil
	}
	return []el{mk(name, s)}
}

// marshalRSS renders the feed as an RSS 2.0 document (Feed.buildXml). Every URL
// in it is absolute with host, the origin the feed was requested on.
func marshalRSS(f db.Feed, episodes []db.FeedEpisode, host string, now time.Time) ([]byte, error) {
	title := orDefault(db.Val(f.Title), "Untitled RSS Feed")
	description, author := db.Val(f.Description), db.Val(f.Author)
	siteURL, imageURL := host+db.Val(f.SiteURL), host+db.Val(f.ImageURL)
	feedAuthor := orDefault(author, "advplyr")
	kids := []el{
		cdata("title", title), cdata("description", orDefault(description, title)), text("link", siteURL),
		elem("image", nil, text("url", imageURL), text("title", title), text("link", siteURL)),
		text("generator", "Audiobookshelf"), text("lastBuildDate", now.UTC().Format(gmtLayout)),
		elem("atom:link", []string{"href", host + db.Val(f.FeedURL), "rel", "self", "type", "application/rss+xml"}),
		text("language", orDefault(db.Val(f.Language), "en")), text("author", feedAuthor), text("itunes:author", feedAuthor),
		text("itunes:type", orDefault(db.Val(f.PodcastType), "serial")), elem("itunes:image", []string{"href", imageURL}),
		text("itunes:explicit", strconv.FormatBool(bool(f.Explicit))),
	}
	kids = append(kids, optional(cdata, "itunes:summary", description)...)
	if name, email := orDefault(db.Val(f.OwnerName), author), db.Val(f.OwnerEmail); name != "" || email != "" {
		kids = append(kids, elem("itunes:owner", nil, append(optional(text, "itunes:name", name), optional(text, "itunes:email", email)...)...))
	}
	if f.PreventIndexing {
		kids = append(kids, text("itunes:block", "yes"), text("googleplay:block", "yes"))
	}
	for _, e := range episodes {
		kids = append(kids, rssItem(e, host))
	}
	rss := elem("rss", []string{
		"xmlns:dc", "http://purl.org/dc/elements/1.1/", "xmlns:content", "http://purl.org/rss/1.0/modules/content/",
		"xmlns:atom", "http://www.w3.org/2005/Atom", "version", "2.0", "xmlns:itunes", "http://www.itunes.com/dtds/podcast-1.0.dtd",
		"xmlns:podcast", "https://podcastindex.org/namespace/1.0", "xmlns:googleplay", "http://www.google.com/schemas/play-podcasts/1.0",
	}, elem("channel", nil, kids...))
	out, err := xml.Marshal(rss)
	return append([]byte(xml.Header), out...), err
}

func rssItem(e db.FeedEpisode, host string) el {
	url := host + db.Val(e.EnclosureURL)
	mime := orDefault(db.Val(e.EnclosureType), media.KnownAudioMime(filepath.Ext(url)))
	description, author := db.Val(e.Description), db.Val(e.Author)
	guid := text("guid", url)
	guid.Attrs = []xml.Attr{{Name: xml.Name{Local: "isPermaLink"}, Value: "false"}}
	kids := []el{cdata("title", orDefault(db.Val(e.Title), "No title"))}
	kids = append(kids, optional(cdata, "description", description)...)
	kids = append(kids, text("link", host+db.Val(e.SiteURL)), guid)
	kids = append(kids, optional(cdata, "dc:creator", author)...)
	if published, ok := parsePubDate(db.Val(e.PubDate)); ok {
		kids = append(kids, text("pubDate", published.UTC().Format(gmtLayout)))
	}
	kids = append(kids, elem("enclosure", []string{"url", url, "length", strconv.FormatInt(db.Val(e.EnclosureSize), 10), "type", orDefault(mime, "application/octet-stream")}))
	kids = append(kids, optional(text, "itunes:author", author)...)
	kids = append(kids, text("itunes:duration", strconv.FormatInt(int64(math.Round(db.Val(e.Duration))), 10)), text("itunes:explicit", strconv.FormatBool(bool(e.Explicit))))
	kids = append(kids, optional(text, "itunes:episodeType", db.Val(e.EpisodeType))...)
	kids = append(kids, optional(text, "itunes:season", db.Val(e.Season))...)
	kids = append(kids, optional(text, "itunes:episode", db.Val(e.Episode))...)
	return elem("item", nil, append(kids, optional(cdata, "itunes:summary", description)...)...)
}

// parsePubDate reads an episode's pubDate: RFC 822/1123 as podcast feeds write
// it, or ISO 8601. Text without a zone is UTC; ok is false when unreadable.
func parsePubDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700",
		time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", "Jan 2, 2006"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
