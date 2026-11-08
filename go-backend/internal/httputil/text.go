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
func SendStatus(w http.ResponseWriter, r *http.Request, status int) {
	writeText(w, r, status, "text/plain; charset=utf-8", http.StatusText(status))
}

// SendText is Express res.status(n).send(string): a text/html body without a
// trailing newline, with Content-Length and a weak ETag.
func SendText(w http.ResponseWriter, r *http.Request, status int, body string) {
	writeText(w, r, status, "text/html; charset=utf-8", body)
}

// writeText answers a HEAD request with the headers of the GET response and
// no body.
func writeText(w http.ResponseWriter, r *http.Request, status int, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", ExpressETag([]byte(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}
