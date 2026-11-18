package protocol

import (
	"bytes"
	"os"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Fixture facts of go-backend/internal/contracttest/testdata/install. The feed
// episode ids are the seeded feedEpisodes rows; the media paths are relative to
// the package install so every scenario reads the file it just served.
const (
	novelItemID = "91bb63f7-97c9-4230-8fd0-1dfd994977ea"
	ebookItemID = "bf4f5bac-080b-465e-adf0-1a3ea5018ac6"
	ebookIno    = "1114912"
	novelIno    = "1089873"

	novelFeedSlug  = "trial-feed-novel"
	novelEpisodeID = "d24dce70-d0bc-47b6-af08-599d62455b77"

	novelShareSlug = "trial-share-novel"

	novelMediaPath = "audiobooks/Ada Example/Single File Novel/Single File Novel.m4b"
	ebookMediaPath = "audiobooks/Ada Example/Ebook Only/Ebook Only.epub"
	novelCoverPath = "metadata/items/" + novelItemID + "/cover.jpg"
)

// feedEnclosurePath is the unauthenticated enclosure URL of the novel feed.
func feedEnclosurePath() string {
	return "/feed/" + novelFeedSlug + "/item/" + novelEpisodeID + "/media.m4b"
}

// novelCoverURL is the item cover route, before query parameters.
func novelCoverURL() string {
	return "/api/items/" + novelItemID + "/cover"
}

// wantStatus fails unless the response has the expected status.
func wantStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status = %d, want %d; body %.200s", what, resp.Status, want, resp.Body)
	}
}

// wantHeader fails unless a response header has the exact expected value.
func wantHeader(t *testing.T, resp harness.Response, name, want, what string) {
	t.Helper()
	if got := resp.Header.Get(name); got != want {
		t.Fatalf("%s: %s = %q, want %q", what, name, got, want)
	}
}

// doFile performs a GET/HEAD with optional request headers.
func doFile(t *testing.T, e *harness.Env, method, path, token string, header map[string]string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: method, Path: path, Token: token, Header: header})
}

// readFile reads one file of the package install with a size bound.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	const maxFileBytes = 8 << 20
	if info.Size() > maxFileBytes {
		t.Fatalf("%s is %d bytes, above the %d byte test limit", path, info.Size(), maxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// wantBody asserts the response body is exactly the file bytes.
func wantBody(t *testing.T, resp harness.Response, file []byte, what string) {
	t.Helper()
	if !bytes.Equal(resp.Body, file) {
		t.Fatalf("%s: body differs from the file (%d vs %d bytes)", what, len(resp.Body), len(file))
	}
}

// noBytes asserts a denial body is short and cannot be the served file.
func noBytes(t *testing.T, resp harness.Response, file []byte, what string) {
	t.Helper()
	if len(file) > 0 && bytes.Contains(resp.Body, file[:min(len(file), 64)]) {
		t.Fatalf("%s: response body carried media bytes", what)
	}
	if len(resp.Body) > 1000 {
		t.Fatalf("%s: denial body is %d bytes", what, len(resp.Body))
	}
}
