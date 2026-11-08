package httputil

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"audiobookshelf-compat/internal/logging"
)

// Handler is the one handler shape (ARCHITECTURE.md "HTTP and JSON"): it
// writes its success response itself and returns an error otherwise. ServeHTTP
// maps the error once, so handlers do not repeat log-write-return blocks.
type Handler func(http.ResponseWriter, *http.Request) error

// ServeHTTP runs h and answers a returned error: an *Error with its status and
// message, anything else with a logged 500.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		Fail(w, r, err)
	}
}

// Error is a client-visible failure: Status and a plain-text Message. Use it
// for 4xx answers and for 5xx answers whose text a client reads.
type Error struct {
	Status  int
	Message string
	Cause   error // logged, never sent
	Empty   bool  // no body at all: Express res.status(n).end()
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// Status returns an *Error answering status with message (the English status
// text when message is empty).
func Status(status int, message string) error {
	return &Error{Status: status, Message: message}
}

// Empty returns an *Error answering status with no body or Content-Type.
func Empty(status int) error { return &Error{Status: status, Empty: true} }

// Internal returns a 500 *Error with message as the body and cause logged.
func Internal(message string, cause error) error {
	return &Error{Status: http.StatusInternalServerError, Message: message, Cause: cause}
}

// Fail answers err. A 5xx is logged with its cause and the route (without the
// query string, which can carry a token); the body is only the message.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Status: http.StatusInternalServerError, Cause: err}
	}
	if e.Status >= 500 {
		message, cause := e.Message, e.Cause
		if message == "" {
			message = "internal error"
		}
		if cause == nil {
			cause = errors.New(message)
		}
		logging.RequestError(r, message, cause)
	}
	if e.Empty {
		w.WriteHeader(e.Status)
		return
	}
	// Express: res.status(n).send(message) is text/html, res.sendStatus(n)
	// (no message) is the status text as text/plain; both carry a weak ETag.
	if e.Message == "" {
		SendStatus(w, e.Status, ForRequest(r))
		return
	}
	SendText(w, e.Status, e.Message, ForRequest(r))
}

// JSON answers 200 with value as JSON. GET and HEAD responses carry a weak
// ETag of the body and answer a matching If-None-Match with 304, the
// conditional caching clients rely on (Lissen revalidates its cached GETs).
func JSON(w http.ResponseWriter, r *http.Request, value any) error {
	return JSONStatus(w, r, http.StatusOK, value)
}

// JSONStatus is JSON with an explicit status; only 200 responses get an ETag.
func JSONStatus(w http.ResponseWriter, r *http.Request, status int, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return Internal("Internal Server Error", err)
	}
	h := w.Header()
	h.Set("Content-Type", jsonContentType)
	if status == http.StatusOK && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		etag := ExpressETag(data)
		h.Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
			h.Del("Content-Type")
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
	return nil
}

// etagMatches is the weak comparison of RFC 9110 13.1.2 over a list header.
func etagMatches(header, etag string) bool {
	want := []byte(stripWeak(etag))
	for _, part := range bytes.Split([]byte(header), []byte(",")) {
		tag := bytes.TrimSpace(part)
		if string(tag) == "*" || bytes.Equal([]byte(stripWeak(string(tag))), want) {
			return true
		}
	}
	return false
}

func stripWeak(tag string) string {
	if len(tag) > 2 && tag[:2] == "W/" {
		return tag[2:]
	}
	return tag
}

// OK answers 200 "OK", Express res.sendStatus(200).
func OK(w http.ResponseWriter, r *http.Request) error {
	SendStatus(w, http.StatusOK, ForRequest(r))
	return nil
}
