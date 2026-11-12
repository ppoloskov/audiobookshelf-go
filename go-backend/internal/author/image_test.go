package author

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestImageDownloadIsBounded(t *testing.T) {
	r := newRig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		size := maxImageBytes + 1
		if req.URL.Path == "/exact.jpg" {
			size = maxImageBytes
		}
		if req.URL.Path == "/chunked.jpg" {
			// No Content-Length: the reader has to stop on its own.
			chunk := make([]byte, 1<<20)
			for range 12 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
			return
		}
		if req.URL.Path != "/exact.jpg" {
			w.Header().Set("Content-Length", strconv.Itoa(size))
		}
		_, _ = w.Write(make([]byte, size))
	}))
	t.Cleanup(server.Close)
	post := func(path string) *httptest.ResponseRecorder {
		return r.do(r.root(), "POST", "/api/authors/"+zed+"/image", `{"url":"`+server.URL+path+`"}`)
	}
	dest := filepath.Join(r.meta, "authors", zed+".jpg")
	for _, path := range []string{"/big.jpg", "/chunked.jpg"} {
		w := post(path)
		if w.Code != 400 || w.Body.String() != errImageTooLarge.Error() {
			t.Errorf("%s: %d %q", path, w.Code, w.Body)
		}
		if _, err := os.Stat(dest); err == nil {
			t.Errorf("%s: an oversized download left a file", path)
		}
	}
	if len(r.take()) != 0 {
		t.Fatal("refused downloads emitted events")
	}
	if w := post("/exact.jpg"); w.Code != 200 {
		t.Fatalf("image of exactly the cap: %d %.100s", w.Code, w.Body)
	}
	if info, err := os.Stat(dest); err != nil || info.Size() != maxImageBytes {
		t.Fatalf("saved file: %v %v", info, err)
	}
}
