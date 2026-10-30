package catalog

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
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

func TestWriteExpressJSONSetsExpressHeaders(t *testing.T) {
	response := httptest.NewRecorder()
	writeExpressJSON(response, 200, struct {
		Success bool   `json:"success"`
		Cover   string `json:"cover"`
	}{true, "/metadata/items/<id&>/cover.png"})
	body := []byte(`{"success":true,"cover":"/metadata/items/<id&>/cover.png"}`)
	if response.Code != 200 || response.Body.String() != string(body) {
		t.Fatalf("response = %d %q, want exact JSON response", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
	if got := response.Header().Get("ETag"); got != expressETag(body) {
		t.Errorf("ETag = %q, want %q", got, expressETag(body))
	}
}
