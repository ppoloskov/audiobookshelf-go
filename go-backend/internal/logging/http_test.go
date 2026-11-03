package logging

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestErrorLogsRouteWithoutQueryString(t *testing.T) {
	var logged []string
	l, _, errBuf, _ := newTestLogger(t)
	old := defaultLogger
	defaultLogger = l
	t.Cleanup(func() { defaultLogger = old })

	r := httptest.NewRequest(http.MethodPatch, "/api/items/li_1/media?token=secret", nil)
	RequestError(r, "database write failed", errors.New("disk I/O error"))
	RequestError(r, "internal error", nil)
	RequestError(nil, "no request", nil)
	for _, e := range l.Recent() {
		logged = append(logged, e.LevelName+" "+e.Message)
	}
	want := []string{
		"ERROR [HTTP] PATCH /api/items/li_1/media: database write failed: disk I/O error",
		"ERROR [HTTP] PATCH /api/items/li_1/media: internal error",
		"ERROR [HTTP] ? ?: no request",
	}
	if strings.Join(logged, "\n") != strings.Join(want, "\n") {
		t.Fatalf("logged:\n%s", strings.Join(logged, "\n"))
	}
	if strings.Contains(errBuf.String(), "secret") {
		t.Fatalf("query string leaked: %q", errBuf.String())
	}
	if got := l.Recent()[0].Source; !strings.HasPrefix(got, "http_test.go:") {
		t.Fatalf("source = %q, want the calling file", got)
	}
}
