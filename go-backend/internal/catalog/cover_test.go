package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/httputil"
)

func TestSniffCoverTypeMatchesPinnedImageTypeHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "jpeg", header: "\xff\xd8\xff", want: "jpg"},
		{name: "png", header: "\x89PNG\r\n\x1a\n", want: "png"},
		{name: "gif", header: "GIF", want: "gif"},
		{name: "webp", header: "RIFFxxxxWEBP", want: "webp"},
		{name: "flif", header: "FLIF", want: "flif"},
		{name: "canon raw", header: "II*\x00xxxxCR", want: "cr2"},
		{name: "tiff", header: "II*\x00", want: "tif"},
		{name: "bmp", header: "BM", want: "bmp"},
		{name: "jpeg xr", header: "II\xbc", want: "jxr"},
		{name: "photoshop", header: "8BPS", want: "psd"},
		{name: "icon", header: "\x00\x00\x01\x00", want: "ico"},
		{name: "cursor", header: "\x00\x00\x02\x00", want: "cur"},
		{name: "bpg", header: "BPG\xfb", want: "bpg"},
		{name: "heic", header: "xxxxftypheic", want: "heic"},
		{name: "heif sequence", header: "xxxxftypmsf1", want: "heic"},
		{name: "ktx", header: "\xabKTX 11\xbb\r\n\x1a\n", want: "ktx"},
		{name: "xml", header: "<?xml ", want: "xml"},
		{name: "unknown", header: "not-image", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sniffCoverType([]byte(test.header)); got != test.want {
				t.Fatalf("sniffCoverType() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRemoveOldCoversUsesPinnedExtensionAllowlist(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"cover.svg", "cover.foo", "cover.png", "cover.JIFF", "other.jpg", "COVER.webp", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeOldCovers(dir, ".jpg")
	for _, name := range []string{"cover.svg", "cover.foo", "other.jpg", "cover.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("removeOldCovers removed unrelated or selected file %q: %v", name, err)
		}
	}
	for _, name := range []string{"cover.png", "cover.JIFF", "COVER.webp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("removeOldCovers retained old image %q, stat error = %v", name, err)
		}
	}
}

func TestWriteJSONWithETagSetsExpressHeaders(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/authors/a1?include=items", nil)
	value := struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}{true, "/metadata/items/<id&>/cover.png"}
	writeJSONWithETag(response, request, http.StatusOK, value)
	var got struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if response.Code != http.StatusOK || got != value {
		t.Fatalf("response = %d %#v, want success and original cover value", response.Code, got)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(response.Body.Len()) {
		t.Errorf("Content-Length = %q, want %d", got, response.Body.Len())
	}
	if got := response.Header().Get("ETag"); got != httputil.ExpressETag(response.Body.Bytes()) {
		t.Errorf("ETag = %q, want %q", got, httputil.ExpressETag(response.Body.Bytes()))
	}
}

func TestWriteJSONWithETagConditionalRequests(t *testing.T) {
	value := struct {
		Name string `json:"name"`
	}{"Series"}
	initial := httptest.NewRecorder()
	writeJSONWithETag(initial, httptest.NewRequest(http.MethodGet, "/api/series/s1", nil), http.StatusOK, value)
	etag := initial.Header().Get("ETag")
	if etag == "" {
		t.Fatal("initial response has no ETag")
	}

	tests := []struct {
		name, method, validator string
		wantStatus              int
	}{
		{name: "weak form", method: http.MethodGet, validator: etag, wantStatus: http.StatusNotModified},
		{name: "strong form", method: http.MethodGet, validator: strings.TrimPrefix(etag, "W/"), wantStatus: http.StatusNotModified},
		{name: "comma list", method: http.MethodGet, validator: `W/"other", ` + etag, wantStatus: http.StatusNotModified},
		{name: "whole wildcard", method: http.MethodGet, validator: " * ", wantStatus: http.StatusNotModified},
		{name: "star inside quoted list member", method: http.MethodGet, validator: `"foo,*,bar"`, wantStatus: http.StatusOK},
		{name: "nonmatching", method: http.MethodGet, validator: `W/"other"`, wantStatus: http.StatusOK},
		{name: "POST ignores validator", method: http.MethodPost, validator: etag, wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/api/series/s1", nil)
			request.Header.Set("If-None-Match", test.validator)
			response := httptest.NewRecorder()
			writeJSONWithETag(response, request, http.StatusOK, value)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if response.Header().Get("ETag") != etag {
				t.Errorf("ETag = %q, want %q", response.Header().Get("ETag"), etag)
			}
			if test.wantStatus == http.StatusNotModified {
				if response.Body.Len() != 0 {
					t.Fatalf("304 body = %q, want empty", response.Body.String())
				}
			} else {
				var got map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got["name"] != value.Name {
					t.Fatalf("fresh body = %q, want valid JSON name %q (decode err %v)", response.Body.String(), value.Name, err)
				}
			}
		})
	}
}

func TestWriteJSONWithETagHeadSuppressesBody(t *testing.T) {
	value := map[string]string{"name": "Series"}
	get := httptest.NewRecorder()
	writeJSONWithETag(get, httptest.NewRequest(http.MethodGet, "/api/series/s1", nil), http.StatusOK, value)

	for _, test := range []struct {
		name, validator string
		wantStatus      int
	}{
		{name: "ordinary HEAD", validator: `W/"other"`, wantStatus: http.StatusOK},
		{name: "conditional HEAD", validator: get.Header().Get("ETag"), wantStatus: http.StatusNotModified},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodHead, "/api/series/s1", nil)
			request.Header.Set("If-None-Match", test.validator)
			response := httptest.NewRecorder()
			writeJSONWithETag(response, request, http.StatusOK, value)
			if response.Code != test.wantStatus || response.Body.Len() != 0 {
				t.Fatalf("HEAD response = %d with %d body bytes, want %d/empty", response.Code, response.Body.Len(), test.wantStatus)
			}
		})
	}
}
