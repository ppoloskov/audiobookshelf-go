//go:build tsdiagnostic

package textclean

import "testing"

// TestTSRepresentationR07 retains the previous serializer spellings for the
// four cases whose rendered HTML meaning is checked in the default suite.
func TestTSRepresentationR07(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one<br>two", "one<br />two"},
		{"Tom &amp; Jerry &lt;3 &quot;quoted&quot; &#39;apos&#39; &nbsp;x &copy; &#x2603;", "Tom &amp; Jerry &lt;3 &#34;quoted&#34; 'apos'  x © ☃"},
		{`It's the reader's "pick" - don't miss it`, `It's the reader's &#34;pick&#34; - don't miss it`},
		{"<br/>and<BR>and<br />", "<br />and<br />and<br />"},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
