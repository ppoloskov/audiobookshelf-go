package podcasts

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// getFeed sets Accept-Encoding itself, which disables net/http's transparent
// gzip; production feeds (megaphone, simplecast, audioboom) reached the XML
// parser compressed ("illegal character code U+001F"). Fetch through a real
// server for each Content-Encoding.
func TestFetchedFeedIsDecodedByContentEncoding(t *testing.T) {
	useLocalFeeds(t)
	const doc = `<rss version="2.0"><channel><title>Encoded Cast</title><item><title>E</title><enclosure url="https://e.example/1.mp3"/></item></channel></rss>`
	encoders := map[string]struct {
		header string
		wrap   func(io.Writer) io.WriteCloser
	}{
		"identity":    {"", nil},
		"gzip":        {"gzip", func(w io.Writer) io.WriteCloser { return gzip.NewWriter(w) }},
		"deflate":     {"deflate", func(w io.Writer) io.WriteCloser { return zlib.NewWriter(w) }},
		"deflate-raw": {"deflate", func(w io.Writer) io.WriteCloser { z, _ := flate.NewWriter(w, flate.DefaultCompression); return z }},
	}
	h := New(nil, config.Config{}, nil, nil)
	for name, enc := range encoders {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if enc.wrap == nil {
				buf.WriteString(doc)
			} else {
				zw := enc.wrap(&buf)
				_, _ = zw.Write([]byte(doc))
				_ = zw.Close()
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if enc.header != "" {
					w.Header().Set("Content-Encoding", enc.header)
				}
				_, _ = w.Write(buf.Bytes())
			}))
			defer srv.Close()
			feed, err := h.getFeed(context.Background(), srv.URL)
			if err != nil || feed.Metadata.Title != "Encoded Cast" || len(feed.Episodes) != 1 {
				t.Fatalf("feed = %+v, err = %v", feed, err)
			}
		})
	}
}
