package harness

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func newHTTPEnv(t *testing.T, handler http.Handler) *Env {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Env{URL: server.URL, Base: server.URL + "/audiobookshelf", client: newClient(), tokens: map[string]string{}}
}

func TestDoBuildsRequestsAndDoesNotFollowRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/audiobookshelf/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen", r.Method+"|"+r.Header.Get("Authorization")+"|"+r.Header.Get("Content-Type")+"|"+r.URL.RawQuery+"|"+r.Header.Get("X-Extra"))
		http.SetCookie(w, &http.Cookie{Name: "c", Value: "v"})
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/audiobookshelf/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/audiobookshelf/echo", http.StatusFound)
	})
	mux.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("raw")) })
	e := newHTTPEnv(t, mux)

	resp := e.Do(t, Request{Method: "PATCH", Path: "/echo", Token: "tok", Query: url.Values{"a": {"1"}}, JSON: map[string]int{"n": 1}, Header: map[string]string{"X-Extra": "x"}})
	if got := resp.Header.Get("X-Seen"); got != "PATCH|Bearer tok|application/json|a=1|x" {
		t.Fatalf("request seen by server: %q", got)
	}
	if string(resp.Body) != `{"n":1}` || len(resp.Cookies) != 1 || resp.Cookies[0].Name != "c" {
		t.Fatalf("response: %s %v", resp.Body, resp.Cookies)
	}
	if resp := e.Do(t, Request{Method: "POST", Path: "/echo", JSON: `{"verbatim":true}`}); string(resp.Body) != `{"verbatim":true}` {
		t.Fatalf("string body not sent verbatim: %s", resp.Body)
	}
	if resp := e.Get(t, "", "/redirect"); resp.Status != http.StatusFound {
		t.Fatalf("redirect followed: %d", resp.Status)
	}
	if resp := e.Do(t, Request{Path: "/raw", Raw: true}); string(resp.Body) != "raw" {
		t.Fatalf("raw path: %s", resp.Body)
	}
}

func TestResponseDecoding(t *testing.T) {
	e := newHTTPEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audiobookshelf/object":
			_, _ = w.Write([]byte(`{"a":{"b":[1,{"c":"x"}]}}`))
		case "/audiobookshelf/array":
			_, _ = w.Write([]byte(`[1,2]`))
		case "/audiobookshelf/login":
			_, _ = w.Write([]byte(`{"user":{"id":"u","accessToken":"tok"}}`))
		}
	}))
	object := e.Get(t, "", "/object").Object(t)
	if Dig(object, "a", "b", 1, "c") != "x" || Dig(object, "a", "b", 5) != nil || Dig(object, "a", 0) != nil || Dig(object, "z", "y") != nil {
		t.Fatalf("Dig: %v", object)
	}
	if got := e.Get(t, "", "/array").Array(t); len(got) != 2 {
		t.Fatalf("Array: %v", got)
	}
	token, user := e.Login(t, "u", "p")
	if token != "tok" || user["id"] != "u" {
		t.Fatalf("Login: %q %v", token, user)
	}
}

func TestMultipartRoundTrip(t *testing.T) {
	body, contentType := Multipart(map[string]string{"b": "2", "a": "1"}, File{Field: "file", Name: "x.txt", Content: []byte("data")})
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type %q: %v", contentType, err)
	}
	reader := multipart.NewReader(body, params["boundary"])
	got := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(part)
		got[part.FormName()+"/"+part.FileName()] = string(content)
	}
	want := map[string]string{"a/": "1", "b/": "2", "file/x.txt": "data"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parts %v, want %v", got, want)
	}
}

func TestEventually(t *testing.T) {
	calls := 0
	Eventually(t, time.Second, "third call", func() bool { calls++; return calls == 3 })
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
}
