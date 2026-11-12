package text

// The golden tables were captured from the implementations this package
// replaced (auth.SanitizeHTML, catalog/providers stripHTML) on the contract
// fixture descriptions, the RSS test fixtures and tricky inputs, so they are a
// regression lock, not independent evidence. The independent evidence is the
// *UpstreamCases tables (upstream sanitize-html / stripAllTags behaviour) and
// TestSanitizeGoldenMeaning (rendered DOM meaning, not byte spelling).

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var sanitizeGolden = []struct{ in, want string }{
	{"The second book of the saga, narrated slowly.", "The second book of the saga, narrated slowly."},
	{"The first episode of the fixture cast.", "The first episode of the fixture cast."},
	{"A <b>second</b> episode.", "A <b>second</b> episode."},
	{"Episode three closes the season.", "Episode three closes the season."},
	{"Useful <script>alert(1)</script><strong>news</strong>", "Useful <strong>news</strong>"},
	{"<p>Useful <script>alert(1)</script><strong>news</strong></p>", "<p>Useful <strong>news</strong></p>"},
	{"Plain description", "Plain description"},
	{"<p>Episode <a href=\"javascript:alert(1)\">link</a></p>", "<p>Episode <a>link</a></p>"},
	{"Trial feed", "Trial feed"},
	{"Feed fixture description with <em>emphasis</em> and &amp; entity", "Feed fixture description with <em>emphasis</em> and &amp; entity"},
	{"hello <script>alert(1)</script>world", "hello world"},
	{"<p onclick=\"x\">Hi <strong>there</strong></p>", "<p>Hi <strong>there</strong></p>"},
	{"<a href=\"javascript:alert(1)\" target=\"_blank\" rel=\"x\">bad</a>", "<a target=\"_blank\">bad</a>"},
	{"<a href=\"//evil.example\">bad</a><a href=\"https://ok.example\" name=\"hi\">ok</a>", "<a>bad</a><a href=\"https://ok.example\" name=\"hi\">ok</a>"},
	{"<iframe>hello <b>world</b></iframe>", "hello <b>world</b>"},
	{"one<br>two", "one<br />two"},
	{"<a href=\"mailto:reader@example.org\">email</a>", "<a href=\"mailto:reader@example.org\">email</a>"},
	{"<p><strong><em>deep</em> text</strong></p>", "<p><strong><em>deep</em> text</strong></p>"},
	{"Tom &amp; Jerry &lt;3 &quot;quoted&quot; &#39;apos&#39; &nbsp;x &copy; &#x2603;", "Tom &amp; Jerry &lt;3 &#34;quoted&#34; 'apos'  x © ☃"},
	{"It's the reader's \"pick\" - don't miss it", "It's the reader's &#34;pick&#34; - don't miss it"},
	{"<p>open <b>bold", "<p>open <b>bold</b></p>"},
	{"<p>a<p>b", "<p>a</p><p>b</p>"},
	{"a</b>b", "ab"},
	{"<style>p{color:red}</style>x", "x"},
	{"<script>var a = \"<b>x</b>\";</script>after", "after"},
	{"<option>o</option>kept <textarea>t</textarea>end", "kept end"},
	{"<a href=\" javascript:alert(1)\">sp</a>", "<a>sp</a>"},
	{"<a href=\"java&#x09;script:alert(1)\">tab</a>", "<a>tab</a>"},
	{"<a href=\"<!-- x -->javascript:alert(1)\">c</a>", "<a>c</a>"},
	{"<a href='https://single.example'>q</a>", "<a href=\"https://single.example\">q</a>"},
	{"<a HREF=\"https://upper.example\" TARGET=\"_blank\">u</a>", "<a href=\"https://upper.example\" target=\"_blank\">u</a>"},
	{"<a name>empty</a>", "<a name>empty</a>"},
	{"<a href=\"https://x.example\" href=\"javascript:bad\">dup</a>", "<a href=\"https://x.example\">dup</a>"},
	{"<img src=x onerror=alert(1)>after", "after"},
	{"<!-- comment -->visible", "visible"},
	{"<!DOCTYPE html>text", "text"},
	{"a < b and c > d", "a &lt; b and c &gt; d"},
	{"1<2 and 3>2", "1&lt;2 and 3&gt;2"},
	{"<br/>and<BR>and<br />", "<br />and<br />and<br />"},
	{"line1\nline2\r\nline3", "line1\nline2\nline3"},
	{"<ul><li>one</li><li>two</li></ul>", "<ul><li>one</li><li>two</li></ul>"},
	{"<p>  padded  </p>  ", "<p>  padded  </p>  "},
	{"<a href=\"https://a.example\"><b>x</b></a>", "<a href=\"https://a.example\"><b>x</b></a>"},
	{"<a href=\"/relative\">rel</a>", "<a href=\"/relative\">rel</a>"},
	{"<a href=\"https://a.example?x=1&amp;y=2\">amp</a>", "<a href=\"https://a.example?x=1&amp;y=2\">amp</a>"},
	{"<a href=\"https://a.example/?q=it's\">apos href</a>", "<a href=\"https://a.example/?q=it's\">apos href</a>"},
	{"<del>gone</del><ins>new</ins>", "<del>gone</del>new"},
	{"<P>UPPER</P>", "<p>UPPER</p>"},
	{"<h1>heading</h1>text", "headingtext"},
	{"<div><p>in div</p></div>", "<p>in div</p>"},
	{"<p>unicode é ☃</p>", "<p>unicode é ☃</p>"},
	{"<p>&lt;script&gt;x&lt;/script&gt;</p>", "<p>&lt;script&gt;x&lt;/script&gt;</p>"},
	{"<title>T</title>x", "Tx"},
	{"<p>x</p>\n\n<p>y</p>", "<p>x</p>\n\n<p>y</p>"},
	{"<a href=\"https://a.example\">one</a> and <a href=\"https://b.example\">two</a>", "<a href=\"https://a.example\">one</a> and <a href=\"https://b.example\">two</a>"},
	{"<strong>a</strong>&nbsp;<em>b</em>", "<strong>a</strong> <em>b</em>"},
	{"   ", "   "},
	{"<em></em>", "<em></em>"},
	{"<p>it's ok</p><p>don&#39;t</p>", "<p>it's ok</p><p>don't</p>"},
}

var stripGolden = []struct{ in, want string }{
	{"The second book of the saga, narrated slowly.", "The second book of the saga, narrated slowly."},
	{"The first episode of the fixture cast.", "The first episode of the fixture cast."},
	{"A <b>second</b> episode.", "A second episode."},
	{"Episode three closes the season.", "Episode three closes the season."},
	{"Plain description", "Plain description"},
	{"<p>Episode <a href=\"javascript:alert(1)\">link</a></p>", "Episode link"},
	{"Trial feed", "Trial feed"},
	{"Feed fixture description with <em>emphasis</em> and &amp; entity", "Feed fixture description with emphasis and & entity"},
	{"<p onclick=\"x\">Hi <strong>there</strong></p>", "Hi there"},
	{"<a href=\"javascript:alert(1)\" target=\"_blank\" rel=\"x\">bad</a>", "bad"},
	{"<a href=\"//evil.example\">bad</a><a href=\"https://ok.example\" name=\"hi\">ok</a>", "badok"},
	{"<iframe>hello <b>world</b></iframe>", "hello world"},
	{"one<br>two", "onetwo"},
	{"<a href=\"mailto:reader@example.org\">email</a>", "email"},
	{"<p><strong><em>deep</em> text</strong></p>", "deep text"},
	{"Tom &amp; Jerry &lt;3 &quot;quoted&quot; &#39;apos&#39; &nbsp;x &copy; &#x2603;", "Tom & Jerry <3 \"quoted\" 'apos'  x © ☃"},
	{"It's the reader's \"pick\" - don't miss it", "It's the reader's \"pick\" - don't miss it"},
	{"<p>open <b>bold", "open bold"},
	{"<p>a<p>b", "ab"},
	{"<b>x<i>y</b>z</i>", "xyz"},
	{"a</b>b", "ab"},
	{"<a href=\" javascript:alert(1)\">sp</a>", "sp"},
	{"<a href=\"java&#x09;script:alert(1)\">tab</a>", "tab"},
	{"<a href='https://single.example'>q</a>", "q"},
	{"<a HREF=\"https://upper.example\" TARGET=\"_blank\">u</a>", "u"},
	{"<a name>empty</a>", "empty"},
	{"<a href=\"https://x.example\" href=\"javascript:bad\">dup</a>", "dup"},
	{"<img src=x onerror=alert(1)>after", "after"},
	{"<!-- comment -->visible", "visible"},
	{"<!DOCTYPE html>text", "text"},
	{"<br/>and<BR>and<br />", "andand"},
	{"<ul><li>one</li><li>two</li></ul>", "onetwo"},
	{"<p>  padded  </p>  ", "padded"},
	{"<a href=\"https://a.example\"><b>x</b></a>", "x"},
	{"<a href=\"/relative\">rel</a>", "rel"},
	{"<a href=\"https://a.example?x=1&amp;y=2\">amp</a>", "amp"},
	{"<a href=\"https://a.example/?q=it's\">apos href</a>", "apos href"},
	{"<del>gone</del><ins>new</ins>", "gonenew"},
	{"<P>UPPER</P>", "UPPER"},
	{"<h1>heading</h1>text", "headingtext"},
	{"<div><p>in div</p></div>", "in div"},
	{"<p>unicode é ☃</p>", "unicode é ☃"},
	{"<p>&lt;script&gt;x&lt;/script&gt;</p>", "<script>x</script>"},
	{"<title>T</title>x", "Tx"},
	{"<noscript><b>n</b></noscript>", "n"},
	{"</p>stray", "stray"},
	{"<p>x</p>\n\n<p>y</p>", "x\n\ny"},
	{"<a href=\"https://a.example\">one</a> and <a href=\"https://b.example\">two</a>", "one and two"},
	{"<strong>a</strong>&nbsp;<em>b</em>", "a b"},
	{"   ", ""},
	{"<em></em>", ""},
	{"<p>it's ok</p><p>don&#39;t</p>", "it's okdon't"},
}

func TestSanitizeGolden(t *testing.T) {
	for _, tc := range sanitizeGolden {
		if isRepresentationOnlySanitizeCase(tc.in) {
			continue
		}
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func isRepresentationOnlySanitizeCase(input string) bool {
	switch input {
	case "one<br>two",
		"Tom &amp; Jerry &lt;3 &quot;quoted&quot; &#39;apos&#39; &nbsp;x &copy; &#x2603;",
		`It's the reader's "pick" - don't miss it`,
		"<br/>and<BR>and<br />":
		return true
	default:
		return false
	}
}

func TestSanitizeGoldenMeaning(t *testing.T) {
	cases := []struct {
		name, input string
		tokens      []string
	}{
		{
			name:   "single line break",
			input:  "one<br>two",
			tokens: []string{"text:one", "void:br", "text:two"},
		},
		{
			name:  "decoded entities",
			input: "Tom &amp; Jerry &lt;3 &quot;quoted&quot; &#39;apos&#39; &nbsp;x &copy; &#x2603;",
			tokens: []string{
				"text:Tom & Jerry <3 \"quoted\" 'apos' \u00a0x © ☃",
			},
		},
		{
			name:   "visible quote characters",
			input:  `It's the reader's "pick" - don't miss it`,
			tokens: []string{`text:It's the reader's "pick" - don't miss it`},
		},
		{
			name:   "mixed line break spellings",
			input:  "<br/>and<BR>and<br />",
			tokens: []string{"void:br", "text:and", "void:br", "text:and", "void:br"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeHTMLMeaning(Sanitize(tc.input))
			if err != nil {
				t.Fatalf("Sanitize output is not safe HTML: %v", err)
			}
			if !equalStrings(got, tc.tokens) {
				t.Fatalf("meaning tokens = %q, want %q", got, tc.tokens)
			}
		})
	}
}

func TestSafeHTMLMeaningAcceptsEquivalentEntityAndBreakSyntax(t *testing.T) {
	for _, pair := range [][2]string{
		{"one<br>two", "one<BR/>two"},
		{"Tom &amp; Jerry", "Tom &#38; Jerry"},
	} {
		got, err := safeHTMLMeaning(pair[0])
		if err != nil {
			t.Fatalf("safeHTMLMeaning(%q): %v", pair[0], err)
		}
		want, err := safeHTMLMeaning(pair[1])
		if err != nil {
			t.Fatalf("safeHTMLMeaning(%q): %v", pair[1], err)
		}
		if !equalStrings(got, want) {
			t.Errorf("equivalent forms differ: %q = %#v, %q = %#v", pair[0], got, pair[1], want)
		}
	}
}

func TestSafeHTMLMeaningRejectsChangedTextAndUnsafeMarkup(t *testing.T) {
	expected := htmlMeaning{"text:one", "void:br", "text:two"}
	changed, err := safeHTMLMeaning("one<br>three")
	if err != nil {
		t.Fatal(err)
	}
	if equalStrings(changed, expected) {
		t.Fatal("changed visible text matched the independent expected meaning")
	}

	for _, output := range []string{
		"<script>alert(1)</script>",
		`<br onclick="alert(1)">`,
		`<a href="javascript:alert(1)">link</a>`,
		`one<em>markup</em>`,
	} {
		if _, err := safeHTMLMeaning(output); err == nil {
			t.Errorf("unsafe output %q was accepted", output)
		}
	}

	// A literal script string is rendered as text; turning it into an element
	// changes the DOM and must fail the same semantic/safety comparison.
	literal, err := safeHTMLMeaning("&lt;script&gt;x&lt;/script&gt;")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(literal, []string{"text:<script>x</script>"}) {
		t.Fatalf("literal markup meaning = %#v", literal)
	}
	if _, err := safeHTMLMeaning("<script>x</script>"); err == nil {
		t.Fatal("executable script was accepted as literal text")
	}
}

type htmlMeaning []string

func safeHTMLMeaning(rendered string) (htmlMeaning, error) {
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(rendered), context)
	if err != nil {
		return nil, fmt.Errorf("parse HTML fragment: %w", err)
	}
	var meaning htmlMeaning
	var visit func(*html.Node) error
	visit = func(node *html.Node) error {
		if node.Type == html.TextNode {
			if len(meaning) > 0 && strings.HasPrefix(meaning[len(meaning)-1], "text:") {
				meaning[len(meaning)-1] += node.Data
			} else {
				meaning = append(meaning, "text:"+node.Data)
			}
			return nil
		}
		if node.Type != html.ElementNode || node.Data != "br" {
			return fmt.Errorf("unexpected HTML node type %v, element <%s>", node.Type, node.Data)
		}
		if len(node.Attr) != 0 {
			return fmt.Errorf("unexpected attributes on <br>: %v", node.Attr)
		}
		meaning = append(meaning, "void:br")
		return nil
	}
	for _, node := range nodes {
		if err := visit(node); err != nil {
			return nil, err
		}
	}
	return meaning, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStripGolden(t *testing.T) {
	for _, tc := range stripGolden {
		if got := Strip(tc.in); got != tc.want {
			t.Errorf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// sanitizeUpstreamCases are inputs where upstream sanitize-html (htmlparser2
// tokenizer, no tree repair) differs from what an HTML5 tree builder would
// give; the expected values are upstream's behaviour, which clients depend on.
var sanitizeUpstreamCases = []struct{ in, want string }{
	{"<b>x<i>y</b>z</i>", "<b>x<i>y</i></b>z"},
	{"<noscript><b>n</b></noscript>", "<b>n</b>"},
	{"</p>stray", "stray"},
}

// stripUpstreamCases follow upstream stripAllTags (nonTextTags such as script,
// style, textarea and option contribute no text; "<2" is text, not a tag) and
// the HTML newline rules (CRLF becomes LF).
var stripUpstreamCases = []struct{ in, want string }{
	{"Useful <script>alert(1)</script><strong>news</strong>", "Useful news"},
	{"<p>Useful <script>alert(1)</script><strong>news</strong></p>", "Useful news"},
	{"hello <script>alert(1)</script>world", "hello world"},
	{"<style>p{color:red}</style>x", "x"},
	{"<script>var a = \"<b>x</b>\";</script>after", "after"},
	{"<option>o</option>kept <textarea>t</textarea>end", "kept end"},
	{"<a href=\"<!-- x -->javascript:alert(1)\">c</a>", "c"},
	{"a < b and c > d", "a < b and c > d"},
	{"1<2 and 3>2", "1<2 and 3>2"},
	{"line1\nline2\r\nline3", "line1\nline2\nline3"},
}

func TestSanitizeMatchesUpstreamSanitizeHTML(t *testing.T) {
	for _, tc := range sanitizeUpstreamCases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripMatchesUpstreamStripAllTags(t *testing.T) {
	for _, tc := range stripUpstreamCases {
		if got := Strip(tc.in); got != tc.want {
			t.Errorf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
