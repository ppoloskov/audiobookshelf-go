package podcasts

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"testing"
)

// getFeed sets Accept-Encoding itself, which disables net/http's transparent
// gzip; production feeds (megaphone, simplecast, audioboom) reached the XML
// parser compressed ("illegal character code U+001F").
func TestDecodedBodyUnwrapsContentEncoding(t *testing.T) {
	const doc = `<rss version="2.0"><channel><title>T</title></channel></rss>`
	compress := map[string]func(io.Writer) io.WriteCloser{
		"gzip":        func(w io.Writer) io.WriteCloser { return gzip.NewWriter(w) },
		"deflate":     func(w io.Writer) io.WriteCloser { return zlib.NewWriter(w) },
		"deflate-raw": func(w io.Writer) io.WriteCloser { z, _ := flate.NewWriter(w, flate.DefaultCompression); return z },
		"":            nil,
	}
	for name, wrap := range compress {
		var buf bytes.Buffer
		header := http.Header{}
		if wrap == nil {
			buf.WriteString(doc)
		} else {
			zw := wrap(&buf)
			_, _ = zw.Write([]byte(doc))
			_ = zw.Close()
			enc := name
			if enc == "deflate-raw" {
				enc = "deflate"
			}
			header.Set("Content-Encoding", enc)
		}
		resp := &http.Response{Header: header, Body: io.NopCloser(&buf)}
		body, err := decodedBody(resp)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(body)
		if err != nil || string(got) != doc {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
}
