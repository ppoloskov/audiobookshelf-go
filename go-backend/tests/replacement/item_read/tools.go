package item_read

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	metadataObjectSource = "server/controllers/LibraryItemController.js:884-896"
	ffmetadataSource     = "server/utils/ffmpegHelpers.js:407-431"
	ffprobeSource        = "server/controllers/LibraryItemController.js:967-980"
)

// ffMetadata derives the metadata object (ffmpegHelpers.getFFMetadataObject)
// of a book from its expanded document: falsy values are dropped.
func ffMetadata(t *testing.T, e *harness.Env, itemID string) map[string]any {
	t.Helper()
	doc := expandedItem(t, e, itemID)
	media := doc["media"].(object)
	meta := media["metadata"].(object)
	str := func(key string) string { value, _ := meta[key].(string); return value }
	joined := func(key, sep string) string {
		var parts []string
		for _, item := range meta[key].([]any) {
			parts = append(parts, item.(string))
		}
		return strings.Join(parts, sep)
	}
	var series []string
	for _, one := range meta["series"].([]any) {
		entry := one.(object)
		name := entry["name"].(string)
		if sequence, _ := entry["sequence"].(string); sequence != "" {
			name += " #" + sequence
		}
		series = append(series, name)
	}
	album := str("title")
	if str("subtitle") != "" {
		album += ": " + str("subtitle")
	}
	all := map[string]string{
		"title": str("title"), "artist": str("authorName"), "album_artist": str("authorName"), "album": album,
		"TIT3": str("subtitle"), "genre": joined("genres", "; "), "date": str("publishedYear"),
		"comment": str("description"), "description": str("description"), "composer": joined("narrators", ", "),
		"copyright": str("publisher"), "publisher": str("publisher"),
		"TRACKTOTAL": strconv.Itoa(media["numTracks"].(int)), "grouping": strings.Join(series, "; "),
	}
	out := map[string]any{}
	for key, value := range all {
		if value != "" {
			out[key] = value
		}
	}
	return out
}

func metadataObjectScenarios(s *setup) []harness.Scenario {
	route := func(t *testing.T, e *harness.Env, itemID string) string {
		return "/api/items/" + itemID + "/metadata-object"
	}
	build := func(assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r063", Invariant: "INV-ROUTE-R063-CONTRACT", Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{metadataObjectSource, ffmetadataSource, "server/managers/AudioMetadataManager.js:42-44"}, Claim: claim, Run: run,
		}
	}
	scenarios := []harness.Scenario{
		build("metadata-object-books", "normal",
			"An admin gets the ffmpeg metadata object of each book with audio: title, artist, album, composer, grouping, TRACKTOTAL and the other tags, with empty values omitted.",
			func(t *testing.T, e *harness.Env) {
				for _, key := range []string{"saga1", "saga2", "novel"} {
					resp := e.Get(t, e.Token(t, "root"), route(t, e, e.IDs[key]))
					wantStatus(t, key, resp, http.StatusOK)
					requireSame(t, key+" metadata object", ffMetadata(t, e, e.IDs[key]), resp.Object(t))
				}
				saga1 := e.Get(t, e.Token(t, "root"), route(t, e, e.IDs["saga1"])).Object(t)
				if saga1["TRACKTOTAL"] != "3" || saga1["grouping"] != "Long Saga #1" || saga1["artist"] != "Terry Fixture" {
					t.Fatalf("saga1 object %v", saga1)
				}
			}),
		build("metadata-object-non-root-admin", "edge",
			"An admin-type user (not root) may read the metadata object too.",
			func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, s.token(t, e, "admin"), route(t, e, e.IDs["novel"]))
				wantStatus(t, "admin", resp, http.StatusOK)
				requireSame(t, "novel metadata object", ffMetadata(t, e, e.IDs["novel"]), resp.Object(t))
			}),
		build("metadata-object-non-admin-forbidden", "error",
			"A non-admin user gets 403 before the item-state check (also for the ebook-only book and the podcast), after the item lookup: an unknown item is 404 for them.",
			func(t *testing.T, e *harness.Env) {
				for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "plain")} {
					wantStatus(t, "non-admin", e.Get(t, token, route(t, e, e.IDs["saga1"])), http.StatusForbidden)
					wantStatus(t, "non-admin unknown item", e.Get(t, token, route(t, e, missingID)), http.StatusNotFound)
					for _, key := range []string{"ebook", "podcast"} {
						wantStatus(t, "non-admin on "+key+" (admin check before item state)", e.Get(t, token, route(t, e, e.IDs[key])), http.StatusForbidden)
					}
				}
			}),
		build("metadata-object-invalid-items", "error",
			"An item that is not a book with included audio files (the ebook-only book, a podcast) answers 400.",
			func(t *testing.T, e *harness.Env) {
				for _, key := range []string{"ebook", "podcast"} {
					wantStatus(t, key, e.Get(t, e.Token(t, "root"), route(t, e, e.IDs[key])), http.StatusBadRequest)
				}
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r063", invariant: "INV-ROUTE-R063-ACCESS-001", label: "metadata-object", path: route, allowedAdmin: true, adminOKStatus: http.StatusBadRequest,
		sources: []string{metadataObjectSource},
	})...)
}

type probeTarget struct {
	name string
	item string
	file fileInfo
}

// probeTargets are the audio files whose ffprobe output is checked.
func probeTargets(t *testing.T, e *harness.Env) []probeTarget {
	t.Helper()
	return []probeTarget{
		{"mp3 track", e.IDs["saga1"], fileNamed(t, e, e.IDs["saga1"], "02 - Part 2.mp3")},
		{"m4b with chapters", e.IDs["novel"], fileNamed(t, e, e.IDs["novel"], "Single File Novel.m4b")},
		{"flac", e.IDs["saga2"], fileNamed(t, e, e.IDs["saga2"], "The Middle.flac")},
		{"podcast episode", e.IDs["podcast"], fileNamed(t, e, e.IDs["podcast"], "episode-2.mp3")},
	}
}

// storedAudio finds the stored audio file (book audioFiles or episode audioFile) by inode.
func storedAudio(t *testing.T, e *harness.Env, itemID, ino string) map[string]any {
	t.Helper()
	media := expandedItem(t, e, itemID)["media"].(object)
	if files, ok := media["audioFiles"].([]any); ok {
		for _, raw := range files {
			if file := raw.(map[string]any); file["ino"] == ino {
				return file
			}
		}
	}
	if episodes, ok := media["episodes"].([]any); ok {
		for _, raw := range episodes {
			if file := raw.(object)["audioFile"].(map[string]any); file["ino"] == ino {
				return file
			}
		}
	}
	t.Fatalf("no stored audio file %s", ino)
	return nil
}

func ffprobeScenarios(s *setup) []harness.Scenario {
	route := func(t *testing.T, e *harness.Env, itemID string) string {
		return "/api/items/" + itemID + "/ffprobe/" + anyFile(t, e, itemID, true).Ino
	}
	build := func(assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r065", Invariant: "INV-ROUTE-R065-CONTRACT", Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{ffprobeSource, "server/scanner/AudioFileScanner.js:208-211", "server/utils/prober.js:327-340"}, Claim: claim, Run: run,
		}
	}
	scenarios := []harness.Scenario{
		build("ffprobe-audio-files", "normal",
			"An admin gets the raw ffprobe JSON of an audio file (book track, m4b, flac, podcast episode): filename, size, duration, stream codec and channels match the stored audio file and the file on disk.",
			func(t *testing.T, e *harness.Env) {
				for _, target := range probeTargets(t, e) {
					resp := e.Get(t, e.Token(t, "root"), "/api/items/"+target.item+"/ffprobe/"+target.file.Ino)
					wantStatus(t, target.name, resp, http.StatusOK)
					doc := resp.Object(t)
					stored := storedAudio(t, e, target.item, target.file.Ino)
					if harness.Dig(doc, "format", "filename") != target.file.Path {
						t.Fatalf("%s: format.filename = %v, want %s", target.name, harness.Dig(doc, "format", "filename"), target.file.Path)
					}
					if harness.Dig(doc, "format", "size") != strconv.FormatInt(target.file.Size, 10) {
						t.Fatalf("%s: format.size = %v, want %d", target.name, harness.Dig(doc, "format", "size"), target.file.Size)
					}
					duration, _ := strconv.ParseFloat(fmt.Sprint(harness.Dig(doc, "format", "duration")), 64)
					if math.Abs(duration-stored["duration"].(float64)) > 0.1 {
						t.Fatalf("%s: format.duration %v, stored duration %v", target.name, duration, stored["duration"])
					}
					stream, _ := harness.Dig(doc, "streams", 0).(map[string]any)
					if stream["codec_type"] != "audio" || stream["codec_name"] != stored["codec"] || stream["channels"] != stored["channels"] {
						t.Fatalf("%s: first stream %v does not match stored codec %v channels %v", target.name, stream, stored["codec"], stored["channels"])
					}
					for _, key := range []string{"format", "streams", "chapters", "programs"} {
						if harness.Dig(doc, key) == nil {
							t.Fatalf("%s: ffprobe output has no %s", target.name, key)
						}
					}
				}
			}),
		build("ffprobe-chapters-of-m4b", "edge",
			"The ffprobe output of the single-file m4b lists the same number of chapters as the item stores for its file.",
			func(t *testing.T, e *harness.Env) {
				file := fileNamed(t, e, e.IDs["novel"], "Single File Novel.m4b")
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+e.IDs["novel"]+"/ffprobe/"+file.Ino)
				wantStatus(t, "novel", resp, http.StatusOK)
				stored, _ := storedAudio(t, e, e.IDs["novel"], file.Ino)["chapters"].([]any)
				chapters, _ := resp.Object(t)["chapters"].([]any)
				if len(chapters) != len(stored) {
					t.Fatalf("ffprobe chapters %d, stored file chapters %d", len(chapters), len(stored))
				}
			}),
		build("ffprobe-non-admin-forbidden", "error",
			"A non-admin user gets 403 for a real audio file and for a real non-audio file (admin check before the audio lookup), while an unknown file id or item is 404 for them (the lookup comes first).",
			func(t *testing.T, e *harness.Env) {
				for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "plain")} {
					wantStatus(t, "non-admin", e.Get(t, token, route(t, e, e.IDs["saga1"])), http.StatusForbidden)
					wantStatus(t, "unknown file", e.Get(t, token, "/api/items/"+e.IDs["saga1"]+"/ffprobe/0"), http.StatusNotFound)
					wantStatus(t, "unknown item", e.Get(t, token, "/api/items/"+missingID+"/ffprobe/1"), http.StatusNotFound)
					cover := fileNamed(t, e, e.IDs["saga1"], "cover.jpg")
					wantStatus(t, "non-audio file (admin check before audio lookup)", e.Get(t, token, "/api/items/"+e.IDs["saga1"]+"/ffprobe/"+cover.Ino), http.StatusForbidden)
				}
			}),
		build("ffprobe-unknown-and-non-audio-file", "error",
			"An admin asking for a file id that is not a library file, or a library file that is not an audio file (cover, text, ebook), gets 404.",
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				wantStatus(t, "unknown ino", e.Get(t, root, "/api/items/"+e.IDs["saga1"]+"/ffprobe/0"), http.StatusNotFound)
				for _, c := range []struct{ item, name string }{{"saga1", "cover.jpg"}, {"saga2", "desc.txt"}, {"ebook", "Ebook Only.epub"}} {
					file := fileNamed(t, e, e.IDs[c.item], c.name)
					wantStatus(t, c.name, e.Get(t, root, "/api/items/"+e.IDs[c.item]+"/ffprobe/"+file.Ino), http.StatusNotFound)
				}
				other := fileNamed(t, e, e.IDs["saga2"], "The Middle.flac")
				wantStatus(t, "audio file of another item", e.Get(t, root, "/api/items/"+e.IDs["saga1"]+"/ffprobe/"+other.Ino), http.StatusNotFound)
			}),
	}
	return append(scenarios, accessScenarios(s, routeSpec{
		requirement: "route-r065", invariant: "INV-ROUTE-R065-ACCESS-001", label: "ffprobe", path: route, allowedAdmin: true,
		sources: []string{ffprobeSource},
	})...)
}
