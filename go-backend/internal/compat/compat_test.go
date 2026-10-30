package compat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func serverURL(t *testing.T, s *httptest.Server) *url.URL {
	t.Helper()
	u, e := url.Parse(s.URL)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func runPair(t *testing.T, left, right *httptest.Server, c Case, limit int64) (Result, error) {
	t.Helper()
	return Run(context.Background(), c, Options{LeftURL: serverURL(t, left), RightURL: serverURL(t, right), BodyLimit: limit})
}

func TestJSONPreservesNumberAndStringTypes(t *testing.T) {
	left := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":1}`))
	}))
	defer left.Close()
	right := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":"1"}`))
	}))
	defer right.Close()
	r, err := runPair(t, left, right, Case{Name: "typed", Method: "GET", Path: "/"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal {
		t.Fatal("numeric vs string drift was missed")
	}
}

func TestJSONNumbersCompareByValue(t *testing.T) {
	makeServer := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}
	left, right := makeServer(`{"count":1}`), makeServer(`{"count":1.0}`)
	defer left.Close()
	defer right.Close()
	r, err := runPair(t, left, right, Case{Method: "GET", Path: "/"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Equal {
		t.Fatalf("equivalent JSON numbers differed: %+v", r)
	}
}

func TestBodyLimitFailsExplicitly(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("12345"))
	}
	left := httptest.NewServer(http.HandlerFunc(handler))
	defer left.Close()
	right := httptest.NewServer(http.HandlerFunc(handler))
	defer right.Close()
	_, err := runPair(t, left, right, Case{Method: "GET", Path: "/"}, 4)
	if err == nil || !strings.Contains(err.Error(), "exceeded 4 byte limit") {
		t.Fatalf("expected explicit body limit error, got %v", err)
	}
}

func TestCrossOriginRedirectIsRefused(t *testing.T) {
	cross := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cross-origin server received request") }))
	defer cross.Close()
	left := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cross.URL+"/secret", http.StatusFound)
	}))
	defer left.Close()
	right := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer right.Close()
	_, err := runPair(t, left, right, Case{Method: "GET", Path: "/"}, 1024)
	if err == nil || !strings.Contains(err.Error(), "refused cross-origin redirect") {
		t.Fatalf("expected redirect refusal, got %v", err)
	}
}

func TestRedirectResponsesRemainObservable(t *testing.T) {
	makeServer := func(status int, location string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", location)
			w.WriteHeader(status)
		}))
	}
	left, right := makeServer(http.StatusFound, "/left"), makeServer(http.StatusTemporaryRedirect, "/right")
	defer left.Close()
	defer right.Close()
	r, err := runPair(t, left, right, Case{Method: "GET", Path: "/"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal || !strings.Contains(r.Detail, "status differs") {
		t.Fatalf("redirect status drift was not reported: %+v", r)
	}
	left.Close()
	right.Close()
	left, right = makeServer(http.StatusFound, "/left"), makeServer(http.StatusFound, "/right")
	defer left.Close()
	defer right.Close()
	r, err = runPair(t, left, right, Case{Method: "GET", Path: "/"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal || !strings.Contains(r.Detail, `header "Location" differs`) {
		t.Fatalf("redirect Location drift was not reported: %+v", r)
	}
}

func TestRelevantHeaderDriftIsReported(t *testing.T) {
	makeServer := func(tag string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", tag)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	}
	left, right := makeServer("left"), makeServer("right")
	defer left.Close()
	defer right.Close()
	r, err := runPair(t, left, right, Case{Method: "GET", Path: "/"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal || !strings.Contains(r.Detail, `header "Etag" differs`) {
		t.Fatalf("expected ETag drift, got %+v", r)
	}
}

func TestIgnoreExplicitJSONPointerOnly(t *testing.T) {
	makeServer := func(value string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"time":"` + value + `","stable":1}`))
		}))
	}
	left, right := makeServer("a"), makeServer("b")
	defer left.Close()
	defer right.Close()
	c := Case{Method: "GET", Path: "/", IgnoreJSONPointers: []string{"/time"}}
	r, err := runPair(t, left, right, c, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Equal {
		t.Fatalf("explicitly ignored pointer still differed: %+v", r)
	}
	c.IgnoreJSONPointers = nil
	r, err = runPair(t, left, right, c, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal {
		t.Fatal("unignored JSON drift was missed")
	}
}

func TestHeaderArrayOrderCompared(t *testing.T) {
	makeServer := func(value string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Item", value)
			w.Header().Add("X-Item", "second")
			_, _ = w.Write([]byte("ok"))
		}))
	}
	left, right := makeServer("first"), makeServer("other")
	defer left.Close()
	defer right.Close()
	r, err := runPair(t, left, right, Case{Method: "GET", Path: "/", CompareHeaders: []string{"X-Item"}}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if r.Equal {
		t.Fatal("relevant header drift was missed")
	}
}

func TestRejectsMutatingMethods(t *testing.T) {
	left := httptest.NewServer(http.NotFoundHandler())
	defer left.Close()
	right := httptest.NewServer(http.NotFoundHandler())
	defer right.Close()
	_, err := runPair(t, left, right, Case{Method: "DELETE", Path: "/"}, 1024)
	if err == nil || !strings.Contains(err.Error(), "only GET and HEAD") {
		t.Fatalf("expected method refusal, got %v", err)
	}
}
