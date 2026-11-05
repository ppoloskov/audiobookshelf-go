package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMobileConditionalGET protects Lissen's Cacheable typed-JSON calls from
// becoming stale or unusable. A client only sends If-None-Match after it has
// cached a body with an ETag, so an ETag-less 200 is a valid fresh response.
func TestMobileConditionalGET(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	listenerToken := in.Login(t, listener.Username, listener.Password)
	root := in.Users["root"]
	rootToken := in.Login(t, root.Username, root.Password)

	var authorID string
	if err := in.DB.QueryRow(`SELECT id FROM authors WHERE libraryId=? ORDER BY name LIMIT 1`, in.IDs["library"]).Scan(&authorID); err != nil {
		t.Fatal(err)
	}

	bookItems := fmt.Sprintf("/api/libraries/%s/items?limit=10&page=0&sort=media.metadata.title&desc=0&minified=1&collapseseries=0", in.IDs["library"])
	podcastItems := fmt.Sprintf("/api/libraries/%s/items?limit=10&page=0&sort=media.metadata.title&desc=0&minified=1", in.IDs["podcastLibrary"])
	bookAuthors := fmt.Sprintf("/api/libraries/%s/authors?limit=10&page=0&sort=name&desc=0", in.IDs["library"])
	cases := []struct {
		name, path, currentID, arrayField string
		wantTotalItems                    bool
	}{
		{name: "libraries", path: "/api/libraries", currentID: in.IDs["library"], arrayField: "libraries"},
		{name: "library_filterdata", path: fmt.Sprintf("/api/libraries/%s?include=filterdata", in.IDs["library"]), currentID: in.IDs["library"]},
		{name: "me", path: "/api/me", currentID: in.IDs["listener"]},
		{name: "book_items", path: bookItems, currentID: in.IDs["novel"], arrayField: "results"},
		{name: "podcast_items", path: podcastItems, currentID: in.IDs["podcast"], arrayField: "results"},
		{name: "library_authors", path: bookAuthors, currentID: authorID, arrayField: "results"},
		{name: "book_stats", path: fmt.Sprintf("/api/libraries/%s/stats", in.IDs["library"]), wantTotalItems: true},
		{name: "narrators", path: fmt.Sprintf("/api/libraries/%s/narrators", in.IDs["library"]), arrayField: "narrators"},
		{name: "playlists", path: fmt.Sprintf("/api/libraries/%s/playlists", in.IDs["library"]), arrayField: "results"},
		{name: "book_item", path: "/api/items/" + in.IDs["novel"], currentID: in.IDs["novel"]},
		{name: "podcast_item", path: "/api/items/" + in.IDs["podcast"], currentID: in.IDs["podcast"]},
		{name: "author_items", path: "/api/authors/" + authorID + "?include=items", currentID: authorID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, firstBody := in.Do(t, http.MethodGet, tc.path, listenerToken, nil)
			requireMobileJSON200(t, first, firstBody, tc)
			oldValidator := first.Header.Get("ETag")
			if oldValidator == "" {
				// A 200 without ETag is not cacheable by Lissen; it must still be
				// a valid fresh response when a client has no stored validator.
				oldValidator = `"stale-client-validator"`
			}

			second, secondBody := in.request(t, http.MethodGet, tc.path, listenerToken, map[string]string{"If-None-Match": oldValidator}, nil)
			if first.Header.Get("ETag") != "" {
				if second.StatusCode != http.StatusNotModified || len(secondBody) != 0 || second.Header.Get("ETag") != oldValidator {
					t.Fatalf("matching validator: status=%d ETag=%q body=%d bytes; want 304, same ETag, empty body", second.StatusCode, second.Header.Get("ETag"), len(secondBody))
				}
			} else {
				if second.StatusCode != http.StatusOK {
					t.Fatalf("ETag-less first response with client validator: status=%d body=%s, want fresh 200", second.StatusCode, secondBody)
				}
				requireMobileJSON200(t, second, secondBody, tc)
			}
		})
	}

	// The client must not receive a cached 304 for a request that failed auth.
	cachePath := "/api/libraries"
	validator := `"stale-client-validator"`
	if resp, _ := in.Do(t, http.MethodGet, cachePath, listenerToken, nil); resp.Header.Get("ETag") != "" {
		validator = resp.Header.Get("ETag")
	}
	unauthorized, body := in.request(t, http.MethodGet, cachePath, "", map[string]string{"If-None-Match": validator}, nil)
	if unauthorized.StatusCode == http.StatusNotModified || unauthorized.StatusCode < 400 {
		t.Fatalf("unauthenticated conditional GET status=%d body=%s, want auth error and never 304", unauthorized.StatusCode, body)
	}

	// A successful public update must invalidate any previously observed value.
	libraryPath := fmt.Sprintf("/api/libraries/%s", in.IDs["library"])
	before, _ := in.Do(t, http.MethodGet, libraryPath, listenerToken, nil)
	oldValidator := before.Header.Get("ETag")
	if oldValidator == "" {
		oldValidator = `"stale-client-validator"`
	}
	newName := "MC05 changed library title"
	patch, patchBody := in.Do(t, http.MethodPatch, "/api/libraries/"+in.IDs["library"], rootToken, map[string]any{"name": newName})
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("PATCH library name: status=%d body=%s", patch.StatusCode, patchBody)
	}
	after, afterBody := in.request(t, http.MethodGet, libraryPath, listenerToken, map[string]string{"If-None-Match": oldValidator}, nil)
	if after.StatusCode != http.StatusOK {
		t.Fatalf("GET after changed library title: status=%d body=%s, want fresh 200", after.StatusCode, afterBody)
	}
	var updated map[string]any
	if err := json.Unmarshal(afterBody, &updated); err != nil {
		t.Fatalf("decode updated library response: %v", err)
	}
	updatedLibrary, ok := updated["library"].(map[string]any)
	if !ok {
		updatedLibrary = updated
	}
	if updatedLibrary["name"] != newName {
		t.Fatalf("updated library name=%v, want %q", updatedLibrary["name"], newName)
	}

	// ServeContent advertises a validator for media. A matching validator must
	// produce 304; changing the copied fixture file must make the old validator
	// return new bytes and a new validator.
	var rawFiles []byte
	if err := in.DB.QueryRow(`SELECT libraryFiles FROM libraryItems WHERE id=?`, in.IDs["novel"]).Scan(&rawFiles); err != nil {
		t.Fatal(err)
	}
	var files []map[string]any
	if err := json.Unmarshal(rawFiles, &files); err != nil {
		t.Fatalf("decode seeded libraryFiles: %v", err)
	}
	var ino, mediaPath string
	for _, file := range files {
		ino, _ = file["ino"].(string)
		metadata, _ := file["metadata"].(map[string]any)
		mediaPath, _ = metadata["path"].(string)
		if ino != "" && mediaPath != "" {
			break
		}
	}
	if ino == "" || mediaPath == "" {
		t.Fatal("seeded book has no readable media file")
	}
	originalMedia, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(mediaPath, originalMedia, originalInfo.Mode().Perm())
		_ = os.Chtimes(mediaPath, originalInfo.ModTime(), originalInfo.ModTime())
	})
	mediaURL := fmt.Sprintf("/api/items/%s/file/%s", in.IDs["novel"], ino)
	mediaFirst, mediaFirstBody := in.Do(t, http.MethodGet, mediaURL, listenerToken, nil)
	mediaTag := mediaFirst.Header.Get("ETag")
	if mediaFirst.StatusCode != http.StatusOK || mediaTag == "" || len(mediaFirstBody) == 0 {
		t.Fatalf("media first GET: status=%d ETag=%q body=%d bytes, want 200/body/nonempty validator", mediaFirst.StatusCode, mediaTag, len(mediaFirstBody))
	}
	media304, media304Body := in.request(t, http.MethodGet, mediaURL, listenerToken, map[string]string{"If-None-Match": mediaTag}, nil)
	if media304.StatusCode != http.StatusNotModified || len(media304Body) != 0 {
		t.Fatalf("matching media validator: status=%d body=%d bytes, want 304/empty", media304.StatusCode, len(media304Body))
	}
	changedMedia := append(append([]byte(nil), originalMedia...), 0)
	if err := os.WriteFile(mediaPath, changedMedia, originalInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	changedAt := originalInfo.ModTime().Add(3 * time.Second)
	if err := os.Chtimes(mediaPath, changedAt, changedAt); err != nil {
		t.Fatal(err)
	}
	mediaUpdated, mediaUpdatedBody := in.request(t, http.MethodGet, mediaURL, listenerToken, map[string]string{"If-None-Match": mediaTag}, nil)
	if mediaUpdated.StatusCode != http.StatusOK || mediaUpdated.Header.Get("ETag") == "" || mediaUpdated.Header.Get("ETag") == mediaTag || bytes.Equal(mediaUpdatedBody, mediaFirstBody) {
		t.Fatalf("changed media with old validator: status=%d oldETag=%q newETag=%q bodyChanged=%t", mediaUpdated.StatusCode, mediaTag, mediaUpdated.Header.Get("ETag"), !bytes.Equal(mediaUpdatedBody, mediaFirstBody))
	}
}

func requireMobileJSON200(t *testing.T, resp *http.Response, body []byte, tc struct {
	name, path, currentID, arrayField string
	wantTotalItems                    bool
}) map[string]any {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s, want 200", tc.path, resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("GET %s Content-Type=%q, want JSON", tc.path, resp.Header.Get("Content-Type"))
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("GET %s invalid typed JSON body: %v: %s", tc.path, err, body)
	}
	if tc.arrayField != "" {
		items, ok := value[tc.arrayField].([]any)
		if !ok {
			t.Fatalf("GET %s field %q has type %T, want array", tc.path, tc.arrayField, value[tc.arrayField])
		}
		if len(items) == 0 {
			t.Fatalf("GET %s field %q is empty; seeded current values expected", tc.path, tc.arrayField)
		}
	}
	if tc.currentID != "" && !bytes.Contains(body, []byte(tc.currentID)) {
		t.Fatalf("GET %s does not contain current seeded ID %q: %s", tc.path, tc.currentID, body)
	}
	if tc.wantTotalItems {
		count, ok := value["totalItems"].(float64)
		if !ok || count <= 0 {
			t.Fatalf("GET %s totalItems=%v, want positive number", tc.path, value["totalItems"])
		}
	}
	return value
}

// TestMobileCoverParameters follows Lissen's binary cover calls: raw=1 for the
// original cover and width-only requests for resized book and author images.
func TestMobileCoverParameters(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)

	var coverPath string
	if err := in.DB.QueryRow(`SELECT b.coverPath FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.id=?`, in.IDs["novel"]).Scan(&coverPath); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatal(err)
	}

	raw, rawBody := in.Do(t, http.MethodGet, "/api/items/"+in.IDs["novel"]+"/cover?raw=1", token, nil)
	if raw.StatusCode != http.StatusOK || !bytes.Equal(rawBody, original) {
		t.Fatalf("raw cover status=%d bytesMatchSource=%t", raw.StatusCode, bytes.Equal(rawBody, original))
	}
	checkMobileImage(t, raw, rawBody, 0)

	resized, resizedBody := in.Do(t, http.MethodGet, "/api/items/"+in.IDs["novel"]+"/cover?width=200", token, nil)
	checkMobileImage(t, resized, resizedBody, 200)

	var authorID string
	if err := in.DB.QueryRow(`SELECT id FROM authors WHERE libraryId=? ORDER BY name LIMIT 1`, in.IDs["library"]).Scan(&authorID); err != nil {
		t.Fatal(err)
	}
	// The scanned fixture has no author image, so point the temporary copied
	// install at its existing source image to exercise Lissen's width-only call.
	if _, err := in.DB.Exec(`UPDATE authors SET imagePath=? WHERE id=?`, coverPath, authorID); err != nil {
		t.Fatal(err)
	}
	authorImage, authorBody := in.Do(t, http.MethodGet, "/api/authors/"+authorID+"/image?width=200", token, nil)
	checkMobileImage(t, authorImage, authorBody, 200)

	remaining, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(remaining, original) {
		t.Fatal("cover source asset changed while serving raw/resized cover requests")
	}
}

func checkMobileImage(t *testing.T, resp *http.Response, body []byte, wantWidth int) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("image GET status=%d body=%s, want 200", resp.StatusCode, body)
	}
	contentType := strings.Split(resp.Header.Get("Content-Type"), ";")[0]
	if !strings.HasPrefix(contentType, "image/") {
		t.Fatalf("image Content-Type=%q, want image MIME", resp.Header.Get("Content-Type"))
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode %s image: %v", contentType, err)
	}
	if "image/"+format != contentType {
		t.Fatalf("decoded format %q does not match Content-Type %q", format, contentType)
	}
	if wantWidth > 0 && img.Bounds().Dx() != wantWidth {
		t.Fatalf("decoded image width=%d, want %d", img.Bounds().Dx(), wantWidth)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("decoded image has invalid bounds %v", img.Bounds())
	}
}
