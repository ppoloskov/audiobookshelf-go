// Package textclean cleans HTML that arrives in provider and feed metadata:
// Sanitize keeps the small allowlist of formatting markup that Audiobookshelf
// shows in descriptions, and Strip reduces HTML to plain text. Both walk the
// golang.org/x/net/html tokenizer and build no tree.
//
// The allowlist and the discarded elements follow upstream
// server/utils/htmlSanitizer.js (sanitize-html with allowedTags and
// nonTextTags).
package textclean

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var allowedTags = map[string]bool{
	"p": true, "ol": true, "ul": true, "li": true, "a": true,
	"strong": true, "em": true, "del": true, "br": true, "b": true, "i": true,
}

// discardedTags lose their content as well as their tags (upstream nonTextTags).
var discardedTags = map[string]bool{"script": true, "style": true, "textarea": true, "option": true}

var hrefScheme = regexp.MustCompile(`(?i)^([a-z][a-z0-9.+-]*):`)

// rawTextTag matches iframe and noscript, which the tokenizer reads as raw
// text. Upstream parses their content as markup, so they are renamed to
// x-iframe and x-noscript before tokenizing; the wrappers are then dropped
// while their markup is kept.
var rawTextTag = regexp.MustCompile(`(?i)<(/?)\s*(iframe|noscript)(\s[^>]*)?>`)

// escaper leaves an apostrophe as a plain apostrophe.
var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")

type tokenKind int

const (
	textToken tokenKind = iota
	startToken
	endToken
)

type token struct {
	kind  tokenKind
	name  string
	text  string
	attrs []html.Attribute
}

// Sanitize returns input with only the allowed tags and attributes kept. Text
// is escaped; tags that are not allowed are dropped and their text kept;
// script, style, textarea and option content is dropped; unsafe a[href] values
// are dropped. Unclosed allowed tags are closed at the end.
func Sanitize(input string) string {
	if input == "" {
		return ""
	}
	var out strings.Builder
	var open []string
	skip := 0
	each(input, func(t token) {
		if discardedTags[t.name] && t.kind != textToken {
			switch {
			case t.kind == startToken:
				skip++
			case skip > 0:
				skip--
			}
			return
		}
		if skip > 0 {
			return
		}
		switch t.kind {
		case textToken:
			out.WriteString(escaper.Replace(t.text))
		case startToken:
			// A new paragraph or list item closes the open one, as the HTML parser does.
			if (t.name == "p" || t.name == "li") && len(open) > 0 && open[len(open)-1] == t.name {
				out.WriteString("</" + t.name + ">")
				open = open[:len(open)-1]
			}
			if !allowedTags[t.name] {
				return
			}
			out.WriteString("<" + t.name)
			if t.name == "a" {
				writeLinkAttrs(&out, t.attrs)
			}
			if t.name == "br" {
				out.WriteString(" />")
				return
			}
			out.WriteByte('>')
			open = append(open, t.name)
		case endToken:
			if t.name == "br" {
				return
			}
			i := lastIndex(open, t.name)
			if i < 0 {
				return
			}
			for j := len(open) - 1; j >= i; j-- {
				out.WriteString("</" + open[j] + ">")
			}
			open = open[:i]
		}
	})
	for j := len(open) - 1; j >= 0; j-- {
		out.WriteString("</" + open[j] + ">")
	}
	return out.String()
}

// Strip returns the text of input with every tag removed, entities decoded,
// script, style, textarea and option content dropped, and surrounding space
// trimmed.
func Strip(input string) string {
	var out strings.Builder
	skip := 0
	each(input, func(t token) {
		if discardedTags[t.name] && t.kind != textToken {
			switch {
			case t.kind == startToken:
				skip++
			case skip > 0:
				skip--
			}
			return
		}
		if skip == 0 && t.kind == textToken {
			out.WriteString(t.text)
		}
	})
	return strings.TrimSpace(out.String())
}

// each calls visit for every text, start-tag and end-tag token of input.
// Comments and doctypes are not reported.
func each(input string, visit func(token)) {
	input = rawTextTag.ReplaceAllString(input, "<${1}x-${2}${3}>")
	z := html.NewTokenizer(strings.NewReader(input))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return
		case html.TextToken:
			visit(token{kind: textToken, text: string(z.Text())})
		case html.StartTagToken, html.SelfClosingTagToken:
			name, more := z.TagName()
			t := token{kind: startToken, name: string(name)}
			for more {
				var key, val []byte
				key, val, more = z.TagAttr()
				t.attrs = append(t.attrs, html.Attribute{Key: strings.ToLower(string(key)), Val: string(val)})
			}
			visit(t)
		case html.EndTagToken:
			name, _ := z.TagName()
			visit(token{kind: endToken, name: string(name)})
		}
	}
}

// writeLinkAttrs writes the href, name and target attributes of an a element.
// The first occurrence of an attribute wins, as in the HTML parser.
func writeLinkAttrs(out *strings.Builder, attrs []html.Attribute) {
	seen := map[string]bool{}
	for _, a := range attrs {
		if seen[a.Key] {
			continue
		}
		seen[a.Key] = true
		if a.Key != "href" && a.Key != "name" && a.Key != "target" {
			continue
		}
		if a.Key == "href" && unsafeHref(a.Val) {
			continue
		}
		out.WriteByte(' ')
		out.WriteString(a.Key)
		if a.Val != "" {
			out.WriteString(`="` + escaper.Replace(a.Val) + `"`)
		}
	}
}

func lastIndex(names []string, name string) int {
	for i := len(names) - 1; i >= 0; i-- {
		if names[i] == name {
			return i
		}
	}
	return -1
}

// unsafeHref reports whether an href must be dropped: a protocol-relative
// link, or a scheme other than http, https or mailto. Relative links have no
// scheme and are kept. The value is checked with whitespace and HTML comments
// removed, so obfuscated schemes are caught.
func unsafeHref(value string) bool {
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
