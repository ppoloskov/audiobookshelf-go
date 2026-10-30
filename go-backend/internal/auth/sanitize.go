package auth

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var allowedMessageTags = map[string]bool{
	"p": true, "ol": true, "ul": true, "li": true, "a": true,
	"strong": true, "em": true, "del": true, "br": true, "b": true, "i": true,
}

var hrefScheme = regexp.MustCompile(`(?i)^([a-z][a-z0-9.+-]*):`)
var iframeTag = regexp.MustCompile(`(?i)<(/?)\s*iframe(?:\s[^>]*)?>`)

// SanitizeHTML matches the allowlist used for Audiobookshelf's login custom
// message. Disallowed formatting tags are removed while their text remains;
// content in script, style, textarea, and option elements is discarded.
func SanitizeHTML(input string) string {
	if input == "" {
		return ""
	}
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	// htmlparser2 (used by upstream) treats iframe content as parsed markup;
	// the HTML5 parser treats it as raw text. Rename the discarded wrapper so
	// nested allowed formatting is kept in the same way upstream keeps it.
	input = iframeTag.ReplaceAllStringFunc(input, func(tag string) string {
		match := iframeTag.FindStringSubmatch(tag)
		if match[1] == "/" {
			return "</x-iframe>"
		}
		return "<x-iframe>"
	})
	nodes, err := html.ParseFragment(strings.NewReader(input), context)
	if err != nil {
		return html.EscapeString(input)
	}
	var output bytes.Buffer
	for _, node := range nodes {
		sanitizeNode(&output, node)
	}
	return output.String()
}

func sanitizeNode(output *bytes.Buffer, node *html.Node) {
	switch node.Type {
	case html.TextNode:
		output.WriteString(html.EscapeString(node.Data))
	case html.ElementNode:
		name := strings.ToLower(node.Data)
		if name == "script" || name == "style" || name == "textarea" || name == "option" {
			return
		}
		if allowedMessageTags[name] {
			output.WriteByte('<')
			output.WriteString(name)
			if name == "a" {
				for _, attr := range node.Attr {
					key := strings.ToLower(attr.Key)
					if key != "href" && key != "name" && key != "target" {
						continue
					}
					if key == "href" && unsafeMessageHref(attr.Val) {
						continue
					}
					output.WriteByte(' ')
					output.WriteString(key)
					if attr.Val != "" {
						output.WriteString(`="`)
						output.WriteString(html.EscapeString(attr.Val))
						output.WriteByte('"')
					}
				}
			}
			if name == "br" {
				output.WriteString(" />")
				return
			}
			output.WriteByte('>')
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				sanitizeNode(output, child)
			}
			output.WriteString("</")
			output.WriteString(name)
			output.WriteByte('>')
			return
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		sanitizeNode(output, child)
	}
}

func unsafeMessageHref(value string) bool {
	compact := strings.Map(func(r rune) rune {
		if r <= 0x20 {
			return -1
		}
		return r
	}, value)
	for {
		start := strings.Index(compact, "<!--")
		if start < 0 {
			break
		}
		end := strings.Index(compact[start+4:], "-->")
		if end < 0 {
			compact = compact[:start]
			break
		}
		compact = compact[:start] + compact[start+4+end+3:]
	}
	if len(compact) >= 2 && (compact[0] == '/' || compact[0] == '\\') && (compact[1] == '/' || compact[1] == '\\') {
		return true
	}
	matches := hrefScheme.FindStringSubmatch(compact)
	if len(matches) == 0 {
		return false
	}
	switch strings.ToLower(matches[1]) {
	case "http", "https", "mailto":
		return false
	default:
		return true
	}
}
