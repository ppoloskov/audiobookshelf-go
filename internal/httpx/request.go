package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// DefaultBodyLimit is the largest request body Decode reads.
const DefaultBodyLimit = 1 << 20

// Decode reads the JSON request body into a T, limited to 1 MiB. Unknown
// fields are ignored. An empty, malformed, mistyped or trailing-garbage body is
// a 400; an oversized one is a 413.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	return DecodeLimit[T](w, r, DefaultBodyLimit)
}

// DecodeLimit is Decode with an explicit limit in bytes, which must be positive.
func DecodeLimit[T any](w http.ResponseWriter, r *http.Request, limit int64) (T, error) {
	var value T
	if limit <= 0 {
		return value, Internal("", errors.New("httpx: decode limit must be positive"))
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := decoder.Decode(&value)
	if err == nil {
		// Only whitespace may follow the value.
		if _, err = decoder.Token(); err == io.EOF {
			return value, nil
		}
		if err == nil {
			err = errors.New("unexpected data after JSON value")
		}
	}
	var tooLarge *http.MaxBytesError
	var mistyped *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		return value, Error(http.StatusRequestEntityTooLarge, "Request body too large")
	case errors.Is(err, io.EOF):
		return value, BadRequest("Request body is empty")
	case errors.As(err, &mistyped):
		return value, BadRequest(fmt.Sprintf("Invalid value for %q", mistyped.Field))
	}
	return value, BadRequest("Invalid JSON body")
}

// QueryInt returns the integer query parameter key, or fallback when it is
// missing or malformed.
func QueryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}

// QueryBool reports whether the query parameter key is "1" or "true".
func QueryBool(r *http.Request, key string) bool {
	value := r.URL.Query().Get(key)
	return value == "1" || value == "true"
}

// ClientIP is the first X-Forwarded-For address when the direct socket peer is
// a reverse proxy (loopback, RFC1918/ULA private, or link-local), else the
// socket peer. The header is never trusted from public peers so callers cannot
// spoof it to bypass rate limits.
func ClientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	if addr, err := netip.ParseAddr(peer); err == nil {
		addr = addr.Unmap()
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
			if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
				return strings.TrimSpace(first)
			}
		}
	}
	return peer
}
