package item_write

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// jpegBytes returns a small valid JPEG.
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 99, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mediaCoverScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R053-COVER-001"
	return []harness.Scenario{
		scenario(r053, inv, "normal", "initial", "patch-media-url-downloads-cover",
			"PATCH media with a cover url downloads the image to metadata/items/<id>/cover.jpg, stores coverPath, answers updated true, applies the metadata of the same payload and emits item_updated.",
			srcs(updateMediaSrc, "server/managers/CoverManager.js:300-345", controllerSrc+":196-240"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				image := jpegBytes(t)
				stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "image/jpeg")
					_, _ = w.Write(image) // a failed write fails the download and the scenario
				}))
				defer stub.Close()
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				if row := mediaRow(t, e, id); row["coverPath"] != nil {
					t.Fatalf("fixture cover = %v", row["coverPath"])
				}
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, map[string]any{"url": stub.URL + "/cover.jpg", "metadata": map[string]any{"subtitle": "With cover"}})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				want := filepath.Join(e.Metadata, "items", id, "cover.jpg")
				if got := mediaRow(t, e, id)["coverPath"]; got != want {
					t.Fatalf("books.coverPath = %v, want %v", got, want)
				}
				if got := harness.Dig(result.LibraryItem, "media", "coverPath"); got != want {
					t.Fatalf("response coverPath = %v", got)
				}
				data, err := os.ReadFile(want)
				if err != nil || !bytes.Equal(data, image) {
					t.Fatalf("cover file: %v (%d bytes, want %d)", err, len(data), len(image))
				}
				if mediaRow(t, e, id)["subtitle"] != "With cover" {
					t.Fatal("metadata of the same payload was not applied")
				}
				wantMoved(t, e, id, before)
				a.receive(t, "item_updated", idOf(id), []string{"root"}, nil)
			}),
		scenario(r053, inv, "error", "failure", "patch-media-url-requires-upload-permission",
			"A cover url from a user without the upload permission answers 403 and neither the cover nor the metadata of the payload is stored.",
			srcs(updateMediaSrc, controllerSrc+":196-205"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Errorf("the server fetched %s without upload permission", r.URL)
				}))
				defer stub.Close()
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				resp := patchMedia(t, e, s.token(t, e, "noupload"), id, map[string]any{"url": stub.URL + "/c.jpg", "metadata": map[string]any{"subtitle": "No upload"}})
				wantStatus(t, "no upload permission", resp, http.StatusForbidden)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, inv, "error", "failure", "patch-media-url-not-an-image-is-400",
			"A cover url that does not serve an image answers 400 and the metadata of the payload is not stored.",
			srcs(updateMediaSrc, "server/managers/CoverManager.js:300-345"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte("<html>not an image</html>")) // a failed write fails the download
				}))
				defer stub.Close()
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				resp := patchMedia(t, e, e.Token(t, "root"), id, map[string]any{"url": stub.URL + "/page", "metadata": map[string]any{"subtitle": "Bad cover"}})
				wantStatus(t, "html cover", resp, http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
	}
}
