package httpx

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// JSON answers 200 with value as JSON. See JSONStatus.
func JSON(w http.ResponseWriter, r *http.Request, value any) error {
	return JSONStatus(w, r, http.StatusOK, value)
}

// JSONStatus answers status with value as JSON (HTML is not escaped). A 200
// answer to GET or HEAD carries a weak ETag of the body and a matching
// If-None-Match gets 304 with no body, so a client can revalidate its cached
// GETs. HEAD sends the headers of the GET answer and no body.
func JSONStatus(w http.ResponseWriter, r *http.Request, status int, value any) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return Internal("", fmt.Errorf("encode response: %w", err))
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	header := w.Header()
	if status == http.StatusOK && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		etag := weakETag(data)
		header.Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data) // a client that went away is not a server error
	}
	return nil
}

// weakETag is W/"<length in hex>-<base64 sha1 prefix>" of body.
func weakETag(body []byte) string {
	sum := sha1.Sum(body)
	return fmt.Sprintf(`W/"%x-%s"`, len(body), base64.StdEncoding.EncodeToString(sum[:])[:27])
}

// etagMatches is the weak comparison of RFC 9110 13.1.2 over a comma-separated
// If-None-Match value; "*" matches anything.
func etagMatches(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || (tag != "" && strings.TrimPrefix(tag, "W/") == want) {
			return true
		}
	}
	return false
}

// Text answers status with body as text/plain; charset=utf-8. A 204 or 304
// answer has no body.
func Text(w http.ResponseWriter, status int, body string) error {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	if status == http.StatusNoContent || status == http.StatusNotModified {
		w.WriteHeader(status)
		return nil
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body)) // a client that went away is not a server error
	return nil
}

// Status answers status with its English status text as the body.
func Status(w http.ResponseWriter, status int) error {
	return Text(w, status, http.StatusText(status))
}
