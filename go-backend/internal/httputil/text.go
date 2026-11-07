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

// WriteError is Express res.status(n).send(message), or res.sendStatus(n)'s
// text when the message is empty. Handlers that answer 5xx log the cause with
// logging.RequestError first; the body never carries it.
func WriteError(w http.ResponseWriter, status int, message string, opts ...Option) {
	if message == "" {
		message = http.StatusText(status)
	}
	SendText(w, status, message, opts...)
}

// ErrorJSON writes {"error": message}.
func ErrorJSON(w http.ResponseWriter, status int, message string, opts ...Option) {
	WriteJSON(w, status, map[string]string{"error": message}, opts...)
}

func writeText(w http.ResponseWriter, status int, contentType, body string, o options) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", ExpressETag([]byte(body)))
	w.WriteHeader(status)
	if !o.head() {
		_, _ = w.Write([]byte(body))
	}
}
