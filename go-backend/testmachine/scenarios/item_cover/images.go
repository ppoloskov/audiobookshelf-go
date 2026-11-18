package item_cover

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// jpegImage returns a valid JPEG of the given pixel size whose colour pattern
// lets a scenario prove that the received bytes are the ones it created.
func jpegImage(t *testing.T, width, height int) []byte {
	t.Helper()
	return encodeImage(t, width, height, "jpeg")
}

// pngImage returns a valid PNG of the given pixel size.
func pngImage(t *testing.T, width, height int) []byte {
	t.Helper()
	return encodeImage(t, width, height, "png")
}

// encodeImage draws a deterministic gradient and encodes it.
func encodeImage(t *testing.T, width, height int, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 11), B: uint8((x + y) * 3), A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "png":
		err = png.Encode(&buf, img)
	default:
		t.Fatalf("encodeImage: unknown format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", format, err)
	}
	return buf.Bytes()
}

// imageSize decodes an image header (JPEG or PNG) and returns its pixel size.
func imageSize(t *testing.T, raw []byte) (int, int) {
	t.Helper()
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode image config: %v", err)
	}
	if format != "jpeg" && format != "png" {
		t.Fatalf("decoded unexpected image format %q", format)
	}
	return config.Width, config.Height
}

// wantImageSize fails unless raw is a JPEG/PNG of the exact pixel size.
func wantImageSize(t *testing.T, what string, raw []byte, width, height int) {
	t.Helper()
	gotWidth, gotHeight := imageSize(t, raw)
	if gotWidth != width || gotHeight != height {
		t.Fatalf("%s: image is %dx%d, want %dx%d", what, gotWidth, gotHeight, width, height)
	}
}

// wantWebP fails unless raw is a WebP container.
func wantWebP(t *testing.T, what string, raw []byte) {
	t.Helper()
	if len(raw) < 16 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		t.Fatalf("%s: not a WebP file (%.16q)", what, raw)
	}
}

// coverURLPath is the relative API path of an item cover.
func coverURLPath(itemID string) string {
	return "/api/items/" + itemID + "/cover"
}
