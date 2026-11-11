package httpx

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog routes slog output into a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestHandlerMapsErrors(t *testing.T) {
	cause := errors.New("secret database detail")
	tests := []struct {
		name    string
		err     error
		status  int
		body    string
		logged  bool
		logPart string
	}{
		{"nil", nil, 200, "", false, ""},
		{"error", Error(http.StatusBadRequest, "Bad input"), 400, "Bad input", false, ""},
		{"empty message is status text", Error(http.StatusNotFound, ""), 404, "Not Found", false, ""},
		{"errorf", Errorf(http.StatusConflict, "name %q taken", "x"), 409, `name "x" taken`, false, ""},
		{"helpers", Forbidden(), 403, "Forbidden", false, ""},
		{"bad request helper", BadRequest("nope"), 400, "nope", false, ""},
		{"wrapped", fmt.Errorf("outer: %w", Error(http.StatusUnauthorized, "Unauthorized")), 401, "Unauthorized", false, ""},
		{"internal hides cause", Internal("Could not save", cause), 500, "Could not save", true, "secret database detail"},
		{"internal default message", Internal("", cause), 500, "Internal Server Error", true, "secret database detail"},
		{"plain error", cause, 500, "Internal Server Error", true, "secret database detail"},
		{"wrapped plain error", fmt.Errorf("x: %w", cause), 500, "Internal Server Error", true, "secret database detail"},
		{"5xx without cause", Error(http.StatusBadGateway, "Upstream down"), 502, "Upstream down", true, "Upstream down"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			handler := Handler(func(w http.ResponseWriter, r *http.Request) error { return tc.err })
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x?token=hunter2", nil))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if rec.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.body)
			}
			if tc.err != nil && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
			}
			if tc.logged {
				out := logs.String()
				if strings.Count(out, "request failed") != 1 || !strings.Contains(out, "path=/api/x") || !strings.Contains(out, "method=GET") || !strings.Contains(out, tc.logPart) {
					t.Fatalf("log = %q", out)
				}
				if strings.Contains(out, "hunter2") {
					t.Fatalf("log leaks the query string: %q", out)
				}
			} else if logs.Len() != 0 {
				t.Fatalf("unexpected log: %q", logs.String())
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatal("cause was sent to the client")
			}
		})
	}
}

func TestHandlerErrorsIs(t *testing.T) {
	cause := errors.New("db")
	if err := Internal("x", cause); !errors.Is(err, cause) {
		t.Fatal("Internal does not unwrap to its cause")
	}
}

func TestTextAndStatus(t *testing.T) {
	tests := []struct {
		name   string
		write  func(w http.ResponseWriter) error
		status int
		body   string
		length string
	}{
		{"text", func(w http.ResponseWriter) error { return Text(w, 201, "ok <b>") }, 201, "ok <b>", "6"},
		{"status", func(w http.ResponseWriter) error { return Status(w, 200) }, 200, "OK", "2"},
		{"status 204 has no body", func(w http.ResponseWriter) error { return Status(w, 204) }, 204, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := tc.write(rec); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || rec.Body.String() != tc.body || rec.Header().Get("Content-Length") != tc.length {
				t.Fatalf("got %d %q len=%q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Length"))
			}
			if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct{ name, forwarded, remote, want string }{
		{"peer", "", "192.0.2.1:1234", "192.0.2.1"},
		{"peer without port", "", "192.0.2.1", "192.0.2.1"},
		{"ipv6 peer", "", "[2001:db8::1]:80", "2001:db8::1"},
		{"first forwarded", "203.0.113.5, 10.0.0.1", "192.0.2.1:1", "203.0.113.5"},
		{"blank forwarded falls back", "  ", "192.0.2.1:1", "192.0.2.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := ClientIP(r); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}
