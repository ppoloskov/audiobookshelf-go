package podcasts

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"

	"golang.org/x/net/html/charset"
)

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
func children(n *xmlNode, local string, namespace string) []*xmlNode {
	var out []*xmlNode
	if n == nil {
		return out
	}
	for _, c := range n.Children {
		if strings.EqualFold(c.Name.Local, local) && (namespace == "" || strings.Contains(strings.ToLower(c.Name.Space), strings.ToLower(namespace))) {
			out = append(out, c)
		}
	}
	return out
}
func child(n *xmlNode, local string, namespace string) *xmlNode {
	xs := children(n, local, namespace)
	if len(xs) > 0 {
		return xs[0]
	}
	return nil
}
func value(n *xmlNode) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Text.String())
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
func firstChildText(n *xmlNode, local, namespace string) string {
	return value(child(n, local, namespace))
}
func firstTextWithFallback(n *xmlNode, local, namespace string) string {
	v := firstChildText(n, local, namespace)
	if v != "" {
		return v
	}
	return firstChildText(n, local, "")
}

func parseFeed(raw []byte, feedURL string) (map[string]any, error) {
	root, err := parseXML(raw)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(root.Name.Local, "rss") {
		return nil, fmt.Errorf("invalid RSS document")
	}
	channel := child(root, "channel", "")
	if channel == nil {
		return nil, fmt.Errorf("RSS channel is missing")
	}
	items := children(channel, "item", "")
	if len(items) == 0 {
		return nil, fmt.Errorf("RSS feed has no episodes")
	}
	meta := map[string]any{"title": firstChildText(channel, "title", ""), "language": firstChildText(channel, "language", ""), "explicit": firstTextWithFallback(channel, "explicit", "itunes"), "author": firstTextWithFallback(channel, "author", "itunes"), "pubDate": firstChildText(channel, "pubDate", ""), "link": firstChildText(channel, "link", ""), "image": firstChildText(child(channel, "image", ""), "url", ""), "categories": []string{}, "feedUrl": feedURL, "description": "", "descriptionPlain": "", "type": firstTextWithFallback(channel, "type", "itunes")}
	if meta["image"] == "" {
		meta["image"] = attr(child(channel, "image", "itunes"), "href")
	}
	channelDesc := firstChildText(channel, "description", "")
	meta["description"], meta["descriptionPlain"] = textclean.Sanitize(channelDesc), textclean.Strip(channelDesc)
	if atom := child(channel, "link", "http://www.w3.org/2005/Atom"); atom != nil && meta["feedUrl"] == "" {
		meta["feedUrl"] = attr(atom, "href")
	}
	if link := child(channel, "new-feed-url", "itunes"); link != nil {
		meta["feedUrl"] = value(link)
	}
	var genres []string
	var category func(*xmlNode, string)
	category = func(parent *xmlNode, prefix string) {
		for _, c := range children(parent, "category", "itunes") {
			label := attr(c, "text")
			if label == "" {
				continue
			}
			nested := children(c, "category", "itunes")
			if len(nested) > 0 {
				category(c, label)
			} else if prefix != "" {
				genres = append(genres, prefix+":"+label)
			} else {
				genres = append(genres, label)
			}
		}
	}
	category(channel, "")
	meta["categories"] = genres
	episodes := make([]map[string]any, 0, len(items))
	for _, item := range items {
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

func parseEpisode(n *xmlNode) map[string]any {
	enc := child(n, "enclosure", "")
	urlValue := attr(enc, "url")
	typeValue := attr(enc, "type")
	length := attr(enc, "length")
	if urlValue == "" {
		for _, m := range children(n, "content", "media") {
			if strings.HasPrefix(attr(m, "type"), "audio") {
				urlValue = attr(m, "url")
				typeValue = attr(m, "type")
				length = attr(m, "fileSize")
				break
			}
		}
	}
	if urlValue == "" {
		return nil
	}
	urlValue = strings.TrimSpace(urlValue)
	rawDesc := firstTextWithFallback(n, "encoded", "http://purl.org/rss/1.0/modules/content/")
	if rawDesc == "" {
		rawDesc = firstChildText(n, "description", "")
	}
	rawDesc = strings.TrimSpace(rawDesc)
	desc, plain := textclean.Sanitize(rawDesc), textclean.Strip(rawDesc)
	pubDate := firstChildText(n, "pubDate", "")
	published := int64(0)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 MST"} {
		if parsed, e := time.Parse(layout, pubDate); e == nil {
			published = parsed.UnixMilli()
			break
		}
	}
	season := firstTextWithFallback(n, "season", "itunes")
	episodeNum := firstTextWithFallback(n, "episode", "itunes")
	duration := firstTextWithFallback(n, "duration", "itunes")
	durSec := durationSeconds(duration)
	var dur any
	if durSec > 0 {
		dur = durSec
	}
	var chapters []map[string]any
	if psc := child(n, "chapters", "psc"); psc != nil {
		chapters = pscChapters(psc, durSec)
	}
	chaptersURL := ""
	chaptersType := ""
	if ch := child(n, "chapters", "podcast"); ch != nil {
		chaptersURL = attr(ch, "url")
		chaptersType = attr(ch, "type")
	}
	if chaptersType == "" && chaptersURL != "" {
		chaptersType = "application/json"
	}
	var enclosure any = map[string]any{"url": urlValue}
	enclosureMap := enclosure.(map[string]any)
	if typeValue != "" {
		enclosureMap["type"] = typeValue
	}
	if length != "" {
		enclosureMap["length"] = length
	}
	return map[string]any{"title": firstChildText(n, "title", ""), "subtitle": firstTextWithFallback(n, "subtitle", "itunes"), "description": desc, "descriptionPlain": plain, "pubDate": pubDate, "episodeType": firstTextWithFallback(n, "episodeType", "itunes"), "season": season, "episode": episodeNum, "author": firstTextWithFallback(n, "author", "itunes"), "duration": duration, "durationSeconds": dur, "explicit": firstTextWithFallback(n, "explicit", "itunes"), "publishedAt": store.NullIfZero(published), "enclosure": enclosure, "guid": firstChildText(n, "guid", ""), "chaptersUrl": store.NullIfEmpty(chaptersURL), "chaptersType": store.NullIfEmpty(chaptersType), "chapters": chapters}
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
func pscChapters(n *xmlNode, duration int) []map[string]any {
	var out []map[string]any
	nodes := children(n, "chapter", "")
	for i, ch := range nodes {
		start := durationSeconds(attr(ch, "start"))
		title := attr(ch, "title")
		if title == "" {
			continue
		}
		end := duration
		if i+1 < len(nodes) {
			end = durationSeconds(attr(nodes[i+1], "start"))
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
