package feeds

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// coverScenarios covers route-r008: GET /feed/:slug/cover*.
func coverScenarios() []harness.Scenario {
	const req = "route-r008"
	const inv = "INV-R008-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "cover-serves-item-cover-bytes",
			Sources: []string{"server/managers/RssFeedManager.js:234-265", "server/Server.js:366-368"},
			Claim:   "GET /feed/:slug/cover.jpg on each fixture feed answers 200 image/jpg with the exact bytes and length of the feed's persisted coverPath file, without authentication.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, slug := range []string{novelFeedSlug, saga1FeedSlug, podcastFeedSlug} {
					row := feedRowBySlug(t, e, slug)
					cover := readMediaFile(t, feedCoverPath(t, row))
					resp := e.Do(t, harness.Request{Path: "/feed/" + slug + "/cover.jpg"})
					wantStatus(t, resp, http.StatusOK, "cover "+slug)
					if got := resp.Header.Get("Content-Type"); got != "image/jpg" {
						t.Fatalf("cover %s Content-Type = %q, want image/jpg", slug, got)
					}
					if len(resp.Body) != len(cover) {
						t.Fatalf("cover %s body length = %d, want %d", slug, len(resp.Body), len(cover))
					}
					if !bytes.Equal(resp.Body, cover) {
						t.Fatalf("cover %s body differs from %s", slug, feedCoverPath(t, row))
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "cover-extensionless-and-cache-busting-url",
			Sources: []string{"server/managers/RssFeedManager.js:252-264", "client/pages/config/rss-feeds.vue:121-124", "server/models/Feed.js:90-100"},
			Claim:   "The extensionless URL the web client builds (/feed/:slug/cover) and the XML imageURL with its ?ts= cache buster both serve the same cover bytes as /cover.jpg, and a repeated read returns the identical body.",
			Run: func(t *testing.T, e *harness.Env) {
				row := feedRowBySlug(t, e, saga1FeedSlug)
				cover := readMediaFile(t, feedCoverPath(t, row))
				for _, path := range []string{"/feed/trial-feed-saga1/cover", "/feed/trial-feed-saga1/cover.jpg?ts=1793709986209"} {
					resp := e.Do(t, harness.Request{Path: path})
					wantStatus(t, resp, http.StatusOK, "cover "+path)
					if !bytes.Equal(resp.Body, cover) {
						t.Fatalf("%s body differs from the cover", path)
					}
				}
				first := e.Do(t, harness.Request{Path: "/feed/trial-feed-saga1/cover.jpg"})
				second := e.Do(t, harness.Request{Path: "/feed/trial-feed-saga1/cover.jpg"})
				if !bytes.Equal(first.Body, second.Body) || len(first.Body) != len(cover) {
					t.Fatal("repeated cover read differs")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R008-CONTRACT-002", Case: "error", State: "failure", Assertion: "cover-rejected-read-changes-nothing",
			Sources: []string{"server/managers/RssFeedManager.js:234-250"},
			Claim:   "A rejected cover read for an unknown slug answers 404, writes no feed row and leaves the served fixture cover file byte-identical.",
			Run: func(t *testing.T, e *harness.Env) {
				row := feedRowBySlug(t, e, saga1FeedSlug)
				cover := readMediaFile(t, feedCoverPath(t, row))
				before := snapshotCounts(t, e)
				wantStatus(t, e.Do(t, harness.Request{Path: "/feed/no-such-cover-feed/cover.jpg"}), http.StatusNotFound, "rejected cover read")
				wantSameCounts(t, e, before, "rejected cover read")
				if got := readMediaFile(t, feedCoverPath(t, feedRowBySlug(t, e, saga1FeedSlug))); !bytes.Equal(got, cover) {
					t.Fatal("rejected cover read changed the cover file")
				}
			},
		},
	}
}

// episodeFileScenarios covers route-r009: GET /feed/:slug/item/:episodeId/*.
func episodeFileScenarios() []harness.Scenario {
	const req = "route-r009"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: "INV-R009-CONTRACT-001", Case: "normal", State: "initial", Assertion: "enclosure-serves-audio-file-with-range-support",
			Sources: []string{
				"server/managers/RssFeedManager.js:197-226",
				"server/utils/fileUtils.js:424-429",
				"server/Server.js:369-372",
			},
			Claim: "The unauthenticated enclosure URL from the XML serves the exact episode file with audio/mp4 for the m4b and audio/mpeg for the mp3, Content-Length equal to the file size, Accept-Ranges bytes and no download attachment.",
			Run: func(t *testing.T, e *harness.Env) {
				cases := []struct {
					slug, episodeID, file, mime string
				}{
					{novelFeedSlug, novelEpisodeID, "media.m4b", "audio/mp4"},
					{saga1FeedSlug, saga1EpisodeOne, "media.mp3", "audio/mpeg"},
				}
				for _, test := range cases {
					feedID := novelFeedID
					if test.slug == saga1FeedSlug {
						feedID = saga1FeedID
					}
					file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, feedID)[0]))
					resp := e.Do(t, harness.Request{Path: "/feed/" + test.slug + "/item/" + test.episodeID + "/" + test.file})
					wantStatus(t, resp, http.StatusOK, "episode "+test.episodeID)
					if got := resp.Header.Get("Content-Type"); got != test.mime {
						t.Fatalf("%s Content-Type = %q, want %q", test.file, got, test.mime)
					}
					if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(file)) {
						t.Fatalf("%s Content-Length = %q, want %d", test.file, got, len(file))
					}
					if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
						t.Fatalf("%s Accept-Ranges = %q", test.file, got)
					}
					if got := resp.Header.Get("Content-Disposition"); got != "" {
						t.Fatalf("%s is an attachment: %q", test.file, got)
					}
					if !bytes.Equal(resp.Body, file) {
						t.Fatalf("%s body differs from the episode file", test.file)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R009-CONTRACT-001", Case: "edge", State: "initial", Assertion: "enclosure-honours-byte-ranges",
			Sources: []string{"server/managers/RssFeedManager.js:220-225"},
			Claim:   "The enclosure route answers a closed byte range with 206 and the exact slice plus Content-Range, a suffix range with the file tail, and a range at the file size with 416 and no media bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0]))
				path := "/feed/trial-feed-novel/item/" + novelEpisodeID + "/media.m4b"
				partial := e.Do(t, harness.Request{Path: path, Header: map[string]string{"Range": "bytes=10-19"}})
				wantStatus(t, partial, http.StatusPartialContent, "closed range")
				if got, want := partial.Header.Get("Content-Range"), "bytes 10-19/"+strconv.Itoa(len(file)); got != want {
					t.Fatalf("Content-Range = %q, want %q", got, want)
				}
				if !bytes.Equal(partial.Body, file[10:20]) {
					t.Fatal("closed range body differs")
				}
				suffix := e.Do(t, harness.Request{Path: path, Header: map[string]string{"Range": "bytes=-5"}})
				wantStatus(t, suffix, http.StatusPartialContent, "suffix range")
				if !bytes.Equal(suffix.Body, file[len(file)-5:]) {
					t.Fatal("suffix range body differs")
				}
				outside := e.Do(t, harness.Request{Path: path, Header: map[string]string{"Range": "bytes=" + strconv.Itoa(len(file)) + "-"}})
				wantStatus(t, outside, http.StatusRequestedRangeNotSatisfiable, "range at the file size")
				if got, want := outside.Header.Get("Content-Range"), "bytes */"+strconv.Itoa(len(file)); got != want {
					t.Fatalf("416 Content-Range = %q, want %q", got, want)
				}
				if bytes.Equal(outside.Body, file) || bytes.Contains(outside.Body, file[:64]) {
					t.Fatal("416 response carried media bytes")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R009-CONTRACT-001", Case: "edge", State: "repeat", Assertion: "enclosure-repeat-is-identical",
			Sources: []string{"server/managers/RssFeedManager.js:197-226"},
			Claim:   "Two identical enclosure reads return the same bytes, MIME type and byte length.",
			Run: func(t *testing.T, e *harness.Env) {
				path := "/feed/trial-feed-saga1/item/" + saga1EpisodeOne + "/media.mp3"
				first := e.Do(t, harness.Request{Path: path})
				second := e.Do(t, harness.Request{Path: path})
				wantStatus(t, first, http.StatusOK, "first enclosure read")
				wantStatus(t, second, http.StatusOK, "second enclosure read")
				if !bytes.Equal(first.Body, second.Body) || first.Header.Get("Content-Type") != second.Header.Get("Content-Type") || first.Header.Get("Content-Length") != second.Header.Get("Content-Length") {
					t.Fatal("repeated enclosure reads differ")
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R009-CONTRACT-002", Case: "error", State: "initial", Assertion: "enclosure-denies-unknown-feed-and-episode",
			Sources: []string{"server/managers/RssFeedManager.js:197-219"},
			Claim:   "The enclosure route answers 404 for an unknown feed, an unknown episode id, and an episode id of a different feed, and never returns episode bytes.",
			Run: func(t *testing.T, e *harness.Env) {
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0]))
				cases := []struct{ name, path string }{
					{"unknown feed", "/feed/no-such-feed/item/" + novelEpisodeID + "/media.m4b"},
					{"unknown episode", "/feed/trial-feed-novel/item/00000000-0000-4000-8000-000000000000/media.m4b"},
					{"foreign episode", "/feed/trial-feed-novel/item/" + saga1EpisodeOne + "/media.mp3"},
				}
				for _, test := range cases {
					resp := e.Do(t, harness.Request{Path: test.path})
					wantStatus(t, resp, http.StatusNotFound, test.name)
					if bytes.Contains(resp.Body, file[:64]) {
						t.Fatalf("%s returned media bytes", test.name)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: "INV-R009-CONTRACT-002", Case: "error", State: "failure", Assertion: "enclosure-never-reads-another-file",
			Sources: []string{"server/managers/RssFeedManager.js:214-225", "server/models/Feed.js:639-643"},
			Claim:   "A traversing tail on the enclosure path still serves only the stored episode file (never /etc/passwd or any other file), and a rejected episode request changes no feed row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := snapshotCounts(t, e)
				file := readMediaFile(t, episodeFile(t, feedEpisodeRows(t, e, novelFeedID)[0]))
				resp := e.Do(t, harness.Request{Path: "/feed/trial-feed-novel/item/" + novelEpisodeID + "/media.m4b/../../../../etc/passwd"})
				wantStatus(t, resp, http.StatusOK, "traversing tail")
				if !bytes.Equal(resp.Body, file) {
					t.Fatalf("traversing tail served %d bytes, not the episode", len(resp.Body))
				}
				wantStatus(t, e.Do(t, harness.Request{Path: "/feed/trial-feed-novel/item/not-an-id/../../../../etc/passwd"}), http.StatusNotFound, "traversing unknown episode")
				wantSameCounts(t, e, before, "rejected episode request")
			},
		},
	}
}

// coverErrorScenario is declared here but runs after a feed without a cover
// has been opened (it needs that state).
func coverErrorScenario() harness.Scenario {
	const req = "route-r008"
	return harness.Scenario{
		Requirement: req, Invariant: "INV-R008-CONTRACT-001", Case: "error", State: "initial", Assertion: "cover-denies-unknown-feed-and-coverless-feed",
		Sources: []string{"server/managers/RssFeedManager.js:234-250"},
		Claim:   "GET /feed/:slug/cover.jpg answers 404 for an unknown slug and for an existing feed whose persisted coverPath is NULL, without streaming a file.",
		Run: func(t *testing.T, e *harness.Env) {
			wantStatus(t, e.Do(t, harness.Request{Path: "/feed/no-such-feed/cover.jpg"}), http.StatusNotFound, "unknown feed cover")
			row := feedRowBySlug(t, e, coverlessSlug)
			if row["coverPath"] != nil {
				t.Fatalf("%s must have no coverPath: %v", coverlessSlug, row)
			}
			resp := e.Do(t, harness.Request{Path: "/feed/" + coverlessSlug + "/cover.jpg"})
			wantStatus(t, resp, http.StatusNotFound, "coverless feed cover")
			if len(resp.Body) > 0 && bytes.Contains(resp.Body, []byte("\x89PNG")) {
				t.Fatal("coverless feed streamed an image")
			}
		},
	}
}
