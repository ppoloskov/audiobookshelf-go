package httputil

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSendStatusIsExpressSendStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	body := "Forbidden"
	SendStatus(rec, http.StatusForbidden)
	if rec.Code != http.StatusForbidden || rec.Body.String() != body {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	assertOpaqueETag(t, rec.Header().Get("ETag"))
}

func assertOpaqueETag(t *testing.T, value string) {
	t.Helper()
	if !validOpaqueETag(value) {
		t.Fatalf("ETag = %q, want syntactically valid opaque entity-tag", value)
	}
}

func validOpaqueETag(value string) bool {
	value = strings.TrimPrefix(value, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, b := range []byte(value[1 : len(value)-1]) {
		if b < 0x21 || b == '"' || b == 0x7f {
			return false
		}
	}
	return true
}

func TestOpaqueETagSyntax(t *testing.T) {
	for _, tag := range []string{`"opaque"`, `W/""`, "W/\"\x80\xff\""} {
		if !validOpaqueETag(tag) {
			t.Errorf("valid opaque tag rejected: %q", tag)
		}
	}
	for _, tag := range []string{"", "opaque", `"has space"`, "\"control\x7f\"", `"embedded"quote"`} {
		if validOpaqueETag(tag) {
			t.Errorf("invalid opaque tag accepted: %q", tag)
		}
	}
}
