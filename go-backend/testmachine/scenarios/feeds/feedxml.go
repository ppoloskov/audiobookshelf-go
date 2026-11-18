package feeds

import (
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Namespaces of the feed XML root and the elements this packet reads.
const (
	nsItunes     = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	nsAtom       = "http://www.w3.org/2005/Atom"
	nsGooglePlay = "http://www.google.com/schemas/play-podcasts/1.0"
	nsDC         = "http://purl.org/dc/elements/1.1/"
	nsPodcast    = "https://podcastindex.org/namespace/1.0"
	nsContent    = "http://purl.org/rss/1.0/modules/content/"
)

// xmlNode is a namespace-aware element tree: every element keeps its QName,
// attributes, character data (CDATA included) and children.
type xmlNode struct {
	XMLName  xml.Name
	Attr     []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []xmlNode  `xml:",any"`
}

// parsedFeed is one decoded RSS document (the channel element is the part the
// scenarios assert).
type parsedFeed struct {
	channel xmlNode
}

// parseFeed decodes the RSS document and validates the fixed root shape and
// namespaces every feed carries.
func parseFeed(t *testing.T, body []byte) parsedFeed {
	t.Helper()
	var root xmlNode
	if err := xml.Unmarshal(body, &root); err != nil {
		t.Fatalf("feed XML: %v: %.300s", err, body)
	}
	if root.XMLName.Local != "rss" || root.XMLName.Space != "" {
		t.Fatalf("feed root = %q, want rss", root.XMLName.Local)
	}
	if got := strings.TrimSpace(root.Text); got != "" {
		t.Fatalf("feed root has stray text %q", got)
	}
	wantAttr(t, root, "version", "2.0")
	for name, want := range map[string]string{
		"dc":         nsDC,
		"content":    nsContent,
		"atom":       nsAtom,
		"itunes":     nsItunes,
		"podcast":    nsPodcast,
		"googleplay": nsGooglePlay,
	} {
		wantAttr(t, root, "xmlns:"+name, want)
	}
	channels := childrenNamed(root, "channel")
	if len(channels) != 1 {
		t.Fatalf("feed has %d channel elements, want 1", len(channels))
	}
	return parsedFeed{channel: channels[0]}
}

// label renders an element name with the prefix a reader sees.
func label(node xmlNode) string {
	switch node.XMLName.Space {
	case nsItunes:
		return "itunes:" + node.XMLName.Local
	case nsAtom:
		return "atom:" + node.XMLName.Local
	case nsGooglePlay:
		return "googleplay:" + node.XMLName.Local
	case nsDC:
		return "dc:" + node.XMLName.Local
	case nsPodcast:
		return "podcast:" + node.XMLName.Local
	}
	return node.XMLName.Local
}

// childrenNamed returns the direct children with that plain local name.
func childrenNamed(node xmlNode, local string) []xmlNode {
	var found []xmlNode
	for _, child := range node.Children {
		if child.XMLName.Local == local && child.XMLName.Space == "" {
			found = append(found, child)
		}
	}
	return found
}

// childrenLabelled returns the direct children with a rendered label.
func childrenLabelled(node xmlNode, name string) []xmlNode {
	var found []xmlNode
	for _, child := range node.Children {
		if label(child) == name {
			found = append(found, child)
		}
	}
	return found
}

// child returns the only direct child with that label.
func child(t *testing.T, node xmlNode, name string) xmlNode {
	t.Helper()
	found := childrenLabelled(node, name)
	if len(found) != 1 {
		t.Fatalf("%q has %d %q children, want 1; children %v", label(node), len(found), name, childLabels(node))
	}
	return found[0]
}

// childLabels lists the direct children as sorted prefixed names.
func childLabels(node xmlNode) []string {
	names := make([]string, 0, len(node.Children))
	for _, item := range node.Children {
		names = append(names, label(item))
	}
	slices.Sort(names)
	return names
}

// wantShape asserts the exact set of direct children (element order is not a
// contract; every element a reader needs and no stray element is).
func wantShape(t *testing.T, node xmlNode, want ...string) {
	t.Helper()
	wanted := slices.Clone(want)
	slices.Sort(wanted)
	if got := childLabels(node); !slices.Equal(got, wanted) {
		t.Fatalf("%q children = %v, want %v", label(node), got, wanted)
	}
}

// wantText asserts the trimmed character data of a direct child.
func wantText(t *testing.T, node xmlNode, name, want string) {
	t.Helper()
	if got := strings.TrimSpace(child(t, node, name).Text); got != want {
		t.Fatalf("%q %s text = %q, want %q", label(node), name, got, want)
	}
}

// attrName renders an attribute QName (namespace declarations keep the xmlns
// prefix the decoder stores as the name space).
func attrName(attr xml.Attr) string {
	if attr.Name.Space == "" {
		return attr.Name.Local
	}
	return attr.Name.Space + ":" + attr.Name.Local
}

// wantAttr asserts one attribute value of a node.
func wantAttr(t *testing.T, node xmlNode, name, want string) {
	t.Helper()
	for _, attr := range node.Attr {
		if attrName(attr) == name {
			if attr.Value != want {
				t.Fatalf("%q attribute %s = %q, want %q", label(node), name, attr.Value, want)
			}
			return
		}
	}
	t.Fatalf("%q has no attribute %s (attributes %v)", label(node), name, describeAttrs(node))
}

// describeAttrs lists attribute names for failure messages.
func describeAttrs(node xmlNode) []string {
	names := make([]string, 0, len(node.Attr))
	for _, attr := range node.Attr {
		names = append(names, attrName(attr))
	}
	return names
}

// absent asserts a node has no direct child with that label.
func absent(t *testing.T, node xmlNode, name string) {
	t.Helper()
	if got := childrenLabelled(node, name); len(got) != 0 {
		t.Fatalf("%q unexpectedly has %q: %q", label(node), name, got[0].Text)
	}
}

// items returns the channel's item elements.
func (f parsedFeed) items(t *testing.T) []xmlNode {
	t.Helper()
	return childrenNamed(f.channel, "item")
}

// itemTitles returns the titles of the channel items in document order.
func (f parsedFeed) itemTitles(t *testing.T) []string {
	t.Helper()
	var titles []string
	for _, item := range f.items(t) {
		titles = append(titles, strings.TrimSpace(child(t, item, "title").Text))
	}
	return titles
}

// item returns the only item whose title is want.
func (f parsedFeed) item(t *testing.T, want string) xmlNode {
	t.Helper()
	for _, item := range f.items(t) {
		if strings.TrimSpace(child(t, item, "title").Text) == want {
			return item
		}
	}
	t.Fatalf("feed has no item titled %q; titles %v", want, f.itemTitles(t))
	return xmlNode{}
}

// wantItemOrder asserts the item titles in document order.
func (f parsedFeed) wantItemOrder(t *testing.T, want ...string) {
	t.Helper()
	if got := f.itemTitles(t); !slices.Equal(got, want) {
		t.Fatalf("feed item titles = %v, want %v", got, want)
	}
}

// wantLastBuildDate asserts the generation timestamp is a GMT RFC 1123 date.
func (f parsedFeed) wantLastBuildDate(t *testing.T) {
	t.Helper()
	value := strings.TrimSpace(child(t, f.channel, "lastBuildDate").Text)
	parsed, err := time.Parse(time.RFC1123, value)
	if err != nil {
		t.Fatalf("lastBuildDate %q: %v", value, err)
	}
	if !strings.HasSuffix(value, " GMT") {
		t.Fatalf("lastBuildDate %q does not end in GMT", value)
	}
	if parsed.Year() < 2025 {
		t.Fatalf("lastBuildDate %q is not a plausible generation time", value)
	}
}

// wantEnclosure asserts an item's enclosure element and returns its URL node.
func wantEnclosure(t *testing.T, item xmlNode, wantURL string, wantSize int64, wantType string) {
	t.Helper()
	node := child(t, item, "enclosure")
	wantAttr(t, node, "url", wantURL)
	wantAttr(t, node, "length", fmt.Sprint(wantSize))
	wantAttr(t, node, "type", wantType)
}

// wantGUID asserts the item GUID is the absolute enclosure URL and is not a
// permalink.
func wantGUID(t *testing.T, item xmlNode, wantURL string) {
	t.Helper()
	node := child(t, item, "guid")
	wantAttr(t, node, "isPermaLink", "false")
	if got := strings.TrimSpace(node.Text); got != wantURL {
		t.Fatalf("guid = %q, want %q", got, wantURL)
	}
}
