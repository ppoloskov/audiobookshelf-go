package podcasts

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/url"
	"strings"
	"time"

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
	meta["description"], meta["descriptionPlain"] = sanitizeHTML(firstChildText(channel, "description", ""))
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
	desc, plain := sanitizeHTML(strings.TrimSpace(rawDesc))
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
	return map[string]any{"title": firstChildText(n, "title", ""), "subtitle": firstTextWithFallback(n, "subtitle", "itunes"), "description": desc, "descriptionPlain": plain, "pubDate": pubDate, "episodeType": firstTextWithFallback(n, "episodeType", "itunes"), "season": season, "episode": episodeNum, "author": firstTextWithFallback(n, "author", "itunes"), "duration": duration, "durationSeconds": dur, "explicit": firstTextWithFallback(n, "explicit", "itunes"), "publishedAt": nullableMillis(published), "enclosure": enclosure, "guid": firstChildText(n, "guid", ""), "chaptersUrl": nullableString(chaptersURL), "chaptersType": nullableString(chaptersType), "chapters": chapters}
}
func nullableMillis(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
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

func sanitizeHTML(input string) (string, string) {
	if input == "" {
		return "", ""
	}
	root, err := parseHTML(input)
	if err != nil {
		return html.EscapeString(input), html.EscapeString(input)
	}
	var safe, plain strings.Builder
	allowed := map[string]bool{"p": true, "ol": true, "ul": true, "li": true, "a": true, "strong": true, "em": true, "del": true, "br": true, "b": true, "i": true}
	var walk func(*htmlNode, bool)
	walk = func(n *htmlNode, skip bool) {
		if n.tag == "script" || n.tag == "style" {
			skip = true
		}
		if n.text != "" && !skip {
			escaped := html.EscapeString(n.text)
			safe.WriteString(escaped)
			plain.WriteString(n.text)
		}
		if allowed[n.tag] && !skip {
			safe.WriteString("<" + n.tag)
			if n.tag == "a" {
				for k, v := range n.attrs {
					if k == "href" || k == "name" || k == "target" {
						u, e := url.Parse(v)
						if k != "href" || (e == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto")) {
							safe.WriteString(" " + k + `="` + html.EscapeString(v) + `"`)
						}
					}
				}
			}
			safe.WriteString(">")
		}
		for _, c := range n.children {
			walk(c, skip)
		}
		if allowed[n.tag] && n.tag != "br" && !skip {
			safe.WriteString("</" + n.tag + ">")
		}
	}
	for _, n := range root {
		walk(n, false)
	}
	return safe.String(), strings.TrimSpace(plain.String())
}

type htmlNode struct {
	tag, text string
	attrs     map[string]string
	children  []*htmlNode
}

func parseHTML(s string) ([]*htmlNode, error) {
	// A small token parser keeps RSS text inert and preserves only the upstream
	// sanitizer's safe inline/list tags.
	type token struct {
		tag, text string
		attrs     map[string]string
		end       bool
	}
	tokens := []token{}
	for i := 0; i < len(s); {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				j = len(s) - i
			}
			tokens = append(tokens, token{text: html.UnescapeString(s[i : i+j])})
			i += j
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			tokens = append(tokens, token{text: "<"})
			i++
			continue
		}
		raw := strings.TrimSpace(s[i+1 : i+j])
		i += j + 1
		if strings.HasPrefix(raw, "!") || strings.HasPrefix(raw, "?") {
			continue
		}
		end := strings.HasPrefix(raw, "/")
		raw = strings.TrimPrefix(raw, "/")
		parts := strings.Fields(raw)
		if len(parts) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(parts[0], "/"))
		attrs := map[string]string{}
		for _, part := range parts[1:] {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				attrs[strings.ToLower(kv[0])] = strings.Trim(strings.Trim(kv[1], "\""), "'")
			}
		}
		tokens = append(tokens, token{tag: name, attrs: attrs, end: end})
	}
	root := []*htmlNode{}
	stack := []*htmlNode{}
	for _, t := range tokens {
		if t.tag == "" {
			n := &htmlNode{text: t.text}
			if len(stack) > 0 {
				stack[len(stack)-1].children = append(stack[len(stack)-1].children, n)
			} else {
				root = append(root, n)
			}
			continue
		}
		if t.end {
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == t.tag {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		n := &htmlNode{tag: t.tag, attrs: t.attrs}
		if len(stack) > 0 {
			stack[len(stack)-1].children = append(stack[len(stack)-1].children, n)
		} else {
			root = append(root, n)
		}
		if t.tag != "br" && t.tag != "img" && t.tag != "hr" {
			stack = append(stack, n)
		}
	}
	return root, nil
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
