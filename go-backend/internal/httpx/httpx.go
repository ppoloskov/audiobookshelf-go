// Package httpx is the HTTP foundation: one handler shape that returns an
// error, plain-text error responses, JSON in and out, file serving and the
// SSRF-guarded outbound client. It imports only the standard library.
package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Handler is the one handler shape: write the success response yourself and
// return an error otherwise. ServeHTTP maps the error once.
type Handler func(w http.ResponseWriter, r *http.Request) error

// ServeHTTP runs h and answers a returned error: an httpx error with its
// status and message, anything else as a 500 "Internal Server Error". Every
// 5xx is logged once with the method, the path (never the query string, which
// can carry a token) and the cause. 4xx answers are not logged.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := h(w, r)
	if err == nil {
		return
	}
	var failure *httpError
	if !errors.As(err, &failure) {
		failure = &httpError{status: http.StatusInternalServerError, message: http.StatusText(http.StatusInternalServerError), cause: err}
	}
	if failure.status >= 500 {
		cause := failure.cause
		if cause == nil {
			cause = errors.New(failure.message)
		}
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "status", failure.status, "error", cause)
	}
	_ = Text(w, failure.status, failure.message) // Text never fails; a dead client is not our error
}

// httpError is a client-visible failure: the status and plain-text message
// that are sent, and an optional cause that is only logged.
type httpError struct {
	status  int
	message string
	cause   error
}

func (e *httpError) Error() string { return e.message }
func (e *httpError) Unwrap() error { return e.cause }

// Error returns a client-visible failure answered with status and message as a
// plain-text body. An empty message means the status text.
func Error(status int, message string) error {
	if message == "" {
		message = http.StatusText(status)
	}
	return &httpError{status: status, message: message}
}

// Errorf is Error with a formatted message.
func Errorf(status int, format string, args ...any) error {
	return Error(status, fmt.Sprintf(format, args...))
}

// Internal returns a 500 whose fixed message is sent to the client (the status
// text when empty). cause is logged and never sent.
func Internal(message string, cause error) error {
	if message == "" {
		message = http.StatusText(http.StatusInternalServerError)
	}
	return &httpError{status: http.StatusInternalServerError, message: message, cause: cause}
}

// NotFound is Error(404, "").
func NotFound() error { return Error(http.StatusNotFound, "") }

// Forbidden is Error(403, "").
func Forbidden() error { return Error(http.StatusForbidden, "") }

// BadRequest is Error(400, message).
func BadRequest(message string) error { return Error(http.StatusBadRequest, message) }
