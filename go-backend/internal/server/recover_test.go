package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/logging"
)

func TestGuardPanicsAnswersLikeExpressFinalhandlerAndLogsStack(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/items/abc?token=secret", nil)
	guardPanics(w, r, func(http.ResponseWriter, *http.Request) { panic("boom") })
	if w.Code != 500 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if text := errorHTMLText(t, w.Body.String()); !strings.Contains(text, http.StatusText(http.StatusInternalServerError)) {
		t.Fatalf("public error information = %q", text)
	}
	for _, private := range []string{"boom", "secret", "goroutine", "/api/items/abc"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private diagnostic %q leaked into public error", private)
		}
	}
	if w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
		t.Fatalf("panic error length = %q, body %d bytes", w.Header().Get("Content-Length"), w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	if w.Header().Get("Content-Security-Policy") != "default-src 'none'" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("finalhandler headers missing: %v", w.Header())
	}
	found := false
	for _, e := range logging.Default().Entries() {
		if e.LevelName == "ERROR" && strings.Contains(e.Message, "[HTTP] GET /api/items/abc: panic: boom") && strings.Contains(e.Message, "goroutine") {
			found = true
			if strings.Contains(e.Message, "secret") {
				t.Fatal("query string leaked into the log")
			}
		}
	}
	if !found {
		t.Fatal("panic with stack not logged at ERROR")
	}
}

func TestGuardPanicsAfterResponseStartedOnlyLogs(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	guardPanics(w, r, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	})
	if w.Code != 200 || w.Body.String() != "partial" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestGuardPanicsKeepsAbortHandlerPanic(t *testing.T) {
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("http.ErrAbortHandler must propagate")
		}
	}()
	guardPanics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
}

func TestGuardPanicsPassesThroughNormalResponses(t *testing.T) {
	w := httptest.NewRecorder()
	guardPanics(w, httptest.NewRequest(http.MethodGet, "/", nil), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "1")
		_, _ = w.Write([]byte("ok"))
	})
	if w.Code != 200 || w.Body.String() != "ok" || w.Header().Get("X-Test") != "1" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAccessLineIsDebugOnly(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.SetLevel(before) })
	count := func() int {
		n := 0
		for _, e := range l.Entries() {
			if strings.Contains(e.Message, "/access-line-probe") {
				if e.Level != int(logging.LevelDebug) {
					t.Fatalf("access line logged at level %d (%s), want DEBUG", e.Level, e.LevelName)
				}
				n++
			}
		}
		return n
	}
	serve := func() {
		guardPanics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/access-line-probe?token=x", nil), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	}
	// The default logger's ring buffer outlives the test (go test -count=N runs
	// it again in the same process), so count lines added by this run only.
	seen := count()
	l.SetLevel(logging.LevelInfo)
	serve()
	if count()-seen != 0 {
		t.Fatal("access line logged at INFO")
	}
	l.SetLevel(logging.LevelDebug)
	serve()
	if got := count() - seen; got != 1 {
		t.Fatalf("access lines at DEBUG = %d, want 1", got)
	}
}

func TestLivenessProbesAreTraceOnly(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.SetLevel(before) })
	count := func(path string) int {
		n := 0
		for _, e := range l.Entries() {
			if strings.Contains(e.Message, " "+path+" ") {
				if want := int(logging.LevelDebug); path == "/other/request" && e.Level != want {
					t.Fatalf("%s logged at level %s, want DEBUG", path, e.LevelName)
				} else if path != "/other/request" && e.Level != int(logging.LevelTrace) {
					t.Fatalf("%s logged at level %s, want TRACE", path, e.LevelName)
				}
				n++
			}
		}
		return n
	}
	serve := func(path string) {
		guardPanics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	}
	// The default logger's ring buffer outlives the test (go test -count=N runs
	// it again in the same process), so count lines added by this run only.
	seen := map[string]int{}
	for _, path := range []string{"/healthcheck", "/ping", "/audiobookshelf/ping", "/other/request"} {
		seen[path] = count(path)
	}
	l.SetLevel(logging.LevelDebug)
	for _, path := range []string{"/healthcheck", "/ping", "/audiobookshelf/ping"} {
		serve(path)
		if count(path)-seen[path] != 0 {
			t.Fatalf("%s logged at DEBUG", path)
		}
	}
	serve("/other/request")
	if got := count("/other/request") - seen["/other/request"]; got != 1 {
		t.Fatalf("other requests stay at DEBUG: %d", got)
	}
	l.SetLevel(logging.LevelTrace)
	for _, path := range []string{"/healthcheck", "/ping", "/audiobookshelf/ping"} {
		serve(path)
		if count(path)-seen[path] != 1 {
			t.Fatalf("%s not logged at TRACE", path)
		}
	}
}
