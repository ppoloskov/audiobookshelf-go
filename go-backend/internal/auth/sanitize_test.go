package auth

import "testing"

func TestSanitizeHTMLMatchesUpstreamCases(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"hello <script>alert(1)</script>world", "hello world"},
		{`<p onclick="x">Hi <strong>there</strong></p>`, `<p>Hi <strong>there</strong></p>`},
		{`<a href="javascript:alert(1)" target="_blank" rel="x">bad</a>`, `<a target="_blank">bad</a>`},
		{`<a href="//evil.example">bad</a><a href="https://ok.example" name="hi">ok</a>`, `<a>bad</a><a href="https://ok.example" name="hi">ok</a>`},
		{`<iframe>hello <b>world</b></iframe>`, `hello <b>world</b>`},
		{"one<br>two", "one<br />two"},
		{`<a href="mailto:reader@example.org">email</a>`, `<a href="mailto:reader@example.org">email</a>`},
	} {
		if got := SanitizeHTML(tc.input); got != tc.want {
			t.Errorf("SanitizeHTML(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
