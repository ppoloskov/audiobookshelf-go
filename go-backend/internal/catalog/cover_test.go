package catalog

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
)

// The sniffed types are the file-type magic numbers upstream uses for
// downloaded covers. A URL download is SSRF-filtered, so no route test can
// serve these bytes from a loopback server; the pure function is the only place
// to check them (RETAIN).
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

// Uploading a cover replaces the item's earlier cover.* images but touches
// nothing else in the folder (CoverManager.removeOldCovers removes only an
// explicit allowlist of image extensions).
func TestUploadCoverRemovesOnlyOlderCoverImages(t *testing.T) {
	rig := newAuthorRig(t, nil)
	dir := filepath.Join(rig.h.cfg.Dirs().Items, "i1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cover.svg", "cover.foo", "cover.png", "cover.JIFF", "other.jpg", "COVER.webp", "cover.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("cover", "new.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("\xff\xd8\xffnew-cover"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/items/i1/cover", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(store.ContextWithUser(req.Context(), rootUser()))
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body)
	}
	for _, name := range []string{"cover.svg", "cover.foo", "other.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the upload removed unrelated file %q: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "cover.jpg"))
	if err != nil || string(got) != "\xff\xd8\xffnew-cover" {
		t.Errorf("cover.jpg = %q, %v; want the uploaded bytes", got, err)
	}
	for _, name := range []string{"cover.png", "cover.JIFF", "COVER.webp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("the old image %q survived the upload, stat error = %v", name, err)
		}
	}
	// A file type outside the supported list is refused and removes nothing.
	body.Reset()
	mw = multipart.NewWriter(&body)
	part, _ = mw.CreateFormFile("cover", "new.svg")
	_, _ = part.Write([]byte("<svg/>"))
	_ = mw.Close()
	req = httptest.NewRequest("POST", "/api/items/i1/cover", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(store.ContextWithUser(req.Context(), rootUser()))
	w = httptest.NewRecorder()
	rig.mux.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Invalid image type .svg") {
		t.Fatalf("svg upload: %d %q", w.Code, w.Body)
	}
}

// Entity tags are opaque validators; their hash algorithm is not a client contract.
func requireOpaqueETag(t *testing.T, tag string) {
	t.Helper()
	value := strings.TrimPrefix(tag, "W/")
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		t.Fatalf("invalid entity tag %q", tag)
	}
	for _, b := range []byte(value[1 : len(value)-1]) {
		if b < 0x21 || b == '"' || b == 0x7f {
			t.Fatalf("invalid entity tag %q", tag)
		}
	}
}
