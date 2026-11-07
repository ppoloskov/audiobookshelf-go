package catalog

import (
	"os"
	"path/filepath"
	"strings"
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
