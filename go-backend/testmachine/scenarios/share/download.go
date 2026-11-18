package share

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// downloadRequest reads the public share download.
func downloadRequest(t *testing.T, e *harness.Env, slug, cookie string) harness.Response {
	t.Helper()
	header := map[string]string{}
	if cookie != "" {
		header = cookieHeader(cookie)
	}
	return e.Do(t, harness.Request{Path: publicSharePath(slug) + "/download", Header: header})
}

// zipEntries reads a zip body into entry name -> content.
func zipEntries(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("download body is not a zip archive: %v", err)
	}
	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatalf("zip entry %s: %v", file.Name, err)
		}
		content, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			t.Fatalf("zip entry %s: %v", file.Name, readErr)
		}
		if closeErr != nil {
			t.Fatalf("zip entry %s: %v", file.Name, closeErr)
		}
		entries[file.Name] = content
	}
	return entries
}

// publicDownloadScenarios covers route-r223: GET /public/share/:slug/download.
func publicDownloadScenarios() []harness.Scenario {
	const req = "route-r223"
	const inv = "INV-R223-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "download-zips-shared-book-folder",
			Sources: []string{"server/controllers/ShareController.js:226-283", "server/utils/zipHelpers.js:4-50"},
			Claim:   "Downloading the downloadable novel share with its session cookie streams a zip attachment named after the item that holds the novel's media file byte-for-byte.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				resp := downloadRequest(t, e, novelSlug, cookie)
				wantStatus(t, resp, http.StatusOK, "novel download")
				if got := resp.Header.Get("Content-Type"); got != "application/zip" {
					t.Fatalf("Content-Type = %q, want application/zip", got)
				}
				if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="Single File Novel.zip"` {
					t.Fatalf("Content-Disposition = %q", got)
				}
				entries := zipEntries(t, resp.Body)
				file := mediaFile(t, e, libraryPath("audiobooks", "Ada Example", "Single File Novel", "Single File Novel.m4b"))
				if len(entries) != 1 || !bytes.Equal(entries["Single File Novel.m4b"], file) {
					t.Fatalf("zip entries = %v, want the novel media file", mapKeys(entries))
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "download-repeats-with-equal-entries",
			Sources: []string{"server/controllers/ShareController.js:253-277", "server/utils/zipHelpers.js:27-50"},
			Claim:   "Repeated downloads return a fresh zip with the same entry names and the same file contents.",
			Run: func(t *testing.T, e *harness.Env) {
				_, cookie := openPublicSession(t, e, novelSlug, nil, "")
				first := downloadRequest(t, e, novelSlug, cookie)
				wantStatus(t, first, http.StatusOK, "first download")
				second := downloadRequest(t, e, novelSlug, cookie)
				wantStatus(t, second, http.StatusOK, "repeat download")
				one, two := zipEntries(t, first.Body), zipEntries(t, second.Body)
				if len(one) != len(two) {
					t.Fatalf("repeat download entries differ: %v vs %v", mapKeys(one), mapKeys(two))
				}
				for name, content := range one {
					if !bytes.Equal(two[name], content) {
						t.Fatalf("zip entry %s differs between downloads", name)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "download-denies-disabled-and-invalid-session",
			Sources: []string{"server/controllers/ShareController.js:227-256"},
			Claim:   "Download returns 404 without a cookie or for an unknown slug or foreign session, and 403 for a share with downloads disabled; none of the denials streams a body.",
			Run: func(t *testing.T, e *harness.Env) {
				_, novelCookie := openPublicSession(t, e, novelSlug, nil, "")
				_, saga2Cookie := openPublicSession(t, e, saga2Slug, nil, "")
				cases := []struct {
					name, slug, cookie string
					status             int
				}{
					{"no cookie", novelSlug, "", http.StatusNotFound},
					{"unknown slug", "no-such-share", novelCookie, http.StatusNotFound},
					{"downloads disabled", saga2Slug, saga2Cookie, http.StatusForbidden},
					{"foreign session", novelSlug, saga2Cookie, http.StatusNotFound},
					{"unknown session", novelSlug, "00000000-0000-4000-8000-000000000000", http.StatusNotFound},
				}
				for _, test := range cases {
					resp := downloadRequest(t, e, test.slug, test.cookie)
					wantStatus(t, resp, test.status, test.name)
					if len(resp.Body) > 100 {
						t.Fatalf("%s: denial must be a short message, got %d bytes", test.name, len(resp.Body))
					}
					if strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
						t.Fatalf("%s: denial must not look like a download", test.name)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "download-closes-with-share-deletion",
			Sources: []string{"server/controllers/ShareController.js:240-272", "server/managers/ShareManager.js:149-165"},
			Claim:   "A downloadable saga share zips its three audio files and cover, and after the share is deleted the same cookie gets 404 from the download route.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-download-life", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				resp := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, resp, http.StatusOK, "saga download")
				entries := zipEntries(t, resp.Body)
				want := []string{"01 - Part 1.mp3", "02 - Part 2.mp3", "03 - Part 3.mp3", "cover.jpg"}
				if len(entries) != len(want) {
					t.Fatalf("saga zip entries = %v, want %v", mapKeys(entries), want)
				}
				for _, name := range want {
					file := mediaFile(t, e, libraryPath("audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning", name))
					if !bytes.Equal(entries[name], file) {
						t.Fatalf("saga zip entry %s differs from the media file", name)
					}
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "delete download share")
				closed := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, closed, http.StatusNotFound, "download after deletion")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "download-denies-after-expiry",
			Sources: []string{"server/controllers/ShareController.js:233-236", "server/managers/ShareManager.js:136-147"},
			Claim:   "A download session for a share that reaches its expiry streams nothing afterwards: the old cookie gets 404 and the share row is gone.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-download-expiry", mediaIDForItem(t, e, fixtureOf(e).disposable), time.Now().Add(3000*time.Millisecond).UnixMilli(), true)
				_, cookie := openPublicSession(t, e, created.Slug, nil, "")
				before := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, before, http.StatusOK, "download before expiry")
				harness.Eventually(t, 15*time.Second, "download share expiry", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				after := downloadRequest(t, e, created.Slug, cookie)
				wantStatus(t, after, http.StatusNotFound, "download after expiry")
				if len(after.Body) > 100 {
					t.Fatalf("expired download denial must be a short message, got %d bytes", len(after.Body))
				}
			},
		},
	}
}

// mapKeys lists the sorted entry names of a zip for failure messages.
func mapKeys(entries map[string][]byte) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
