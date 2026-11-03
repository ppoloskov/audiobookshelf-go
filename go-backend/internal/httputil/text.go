package httputil

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ExpressETag computes the weak ETag Express attaches to a buffered response
// body (server/etag.js): W/"<length in hex>-<sha1 base64 without padding>".
func ExpressETag(body []byte) string {
	sum := sha1.Sum(body)
	hash := strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	return fmt.Sprintf("W/\"%x-%s\"", len(body), hash)
}

// SendStatus is Express res.sendStatus: the English status text as a
// text/plain body, with Content-Length and a weak ETag.
func SendStatus(w http.ResponseWriter, status int, opts ...Option) {
	writeText(w, status, "text/plain; charset=utf-8", http.StatusText(status), apply(opts))
}

// SendText is Express res.status(n).send(string): a text/html body without a
// trailing newline, with Content-Length and a weak ETag.
func SendText(w http.ResponseWriter, status int, body string, opts ...Option) {
	o := apply(opts)
	contentType := "text/html; charset=utf-8"
	if o.contentType != "" {
		contentType = o.contentType
	}
	writeText(w, status, contentType, body, o)
}

func writeText(w http.ResponseWriter, status int, contentType, body string, o options) {
	w.Header().Set("Content-Type", contentType)
	if !o.noContentLength {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	if !o.noETag {
		w.Header().Set("ETag", ExpressETag([]byte(body)))
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
