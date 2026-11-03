package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/logging"
)

func TestGuardPanicsAnswersLikeExpressFinalhandlerAndLogsStack(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/items/abc?token=secret", nil)
	guardPanics(w, r, func(http.ResponseWriter, *http.Request) { panic("boom") })
	if w.Code != 500 || !strings.Contains(w.Body.String(), "<pre>Internal Server Error</pre>") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type %q", ct)
	}
	if w.Header().Get("Content-Security-Policy") != "default-src 'none'" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("finalhandler headers missing: %v", w.Header())
	}
	found := false
	for _, e := range logging.Default().Recent() {
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
	t.Cleanup(func() { l.ChangeLevel(before) })
	count := func() int {
		n := 0
		for _, e := range l.Recent() {
			if strings.HasPrefix(e.Message, "[HTTP] GET /access-line-probe 204 ") {
				n++
			}
		}
		return n
	}
	serve := func() {
		guardPanics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/access-line-probe?token=x", nil), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	}
	l.ChangeLevel(logging.LevelInfo)
	serve()
	if count() != 0 {
		t.Fatal("access line logged at INFO")
	}
	l.ChangeLevel(logging.LevelDebug)
	serve()
	if count() != 1 {
		t.Fatalf("access lines at DEBUG = %d, want 1", count())
	}
}

func TestLivenessProbesAreTraceOnly(t *testing.T) {
	l := logging.Default()
	before := l.Level()
	t.Cleanup(func() { l.ChangeLevel(before) })
	count := func(path string) int {
		n := 0
		for _, e := range l.Recent() {
			if strings.HasPrefix(e.Message, "[HTTP] GET "+path+" 200 ") {
				n++
			}
		}
		return n
	}
	serve := func(path string) {
		guardPanics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	}
	l.ChangeLevel(logging.LevelDebug)
	for _, path := range []string{"/healthcheck", "/ping", "/audiobookshelf/ping"} {
		serve(path)
		if count(path) != 0 {
			t.Fatalf("%s logged at DEBUG", path)
		}
	}
	serve("/other/request")
	if count("/other/request") != 1 {
		t.Fatalf("other requests stay at DEBUG: %d", count("/other/request"))
	}
	l.ChangeLevel(logging.LevelTrace)
	for _, path := range []string{"/healthcheck", "/ping", "/audiobookshelf/ping"} {
		serve(path)
		if count(path) != 1 {
			t.Fatalf("%s not logged at TRACE", path)
		}
	}
}
