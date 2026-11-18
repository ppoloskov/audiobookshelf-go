package item_read

import (
	"math"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// numberAt asserts a JSON number at path equals want (within float noise).
func numberAt(t *testing.T, doc any, want float64, path ...any) {
	t.Helper()
	got, ok := harness.Dig(doc, path...).(float64)
	if !ok || math.Abs(got-want) > 1e-9 {
		t.Fatalf("%v = %v, want %v", path, harness.Dig(doc, path...), want)
	}
}

// valueAt asserts a JSON scalar at path equals want.
func valueAt(t *testing.T, doc any, want any, path ...any) {
	t.Helper()
	if got := harness.Dig(doc, path...); got != want {
		t.Fatalf("%v = %v (%T), want %v", path, got, got, want)
	}
}

// factScenarios pins literal fixture facts, independent of the derivation in expect.go.
func factScenarios(*setup) []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r050", Invariant: "INV-R050-EXPANDED-001", Case: "normal", State: "stateless", Assertion: "expanded-literal-facts-multifile-book",
			Sources: []string{findOneSource, "server/models/Book.js:293-305,640-690"},
			Claim:   "The expanded document of the three-file book carries the literal fixture facts: ids, relative path, sizes, duration, track order with offsets and content URLs, chapter titles and bounds, series and files.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+e.IDs["saga1"]+"?expanded=1")
				wantStatus(t, "saga1", resp, 200)
				doc := resp.Object(t)
				valueAt(t, doc, "b3c61dfc-dd4b-47ee-8998-0340c1228f52", "id")
				valueAt(t, doc, "828682", "ino")
				valueAt(t, doc, "51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41", "libraryId")
				valueAt(t, doc, "bc88289a-fc01-4256-a39e-90649091e152", "folderId")
				valueAt(t, doc, "Terry Fixture/Long Saga/Book 1 - The Beginning", "relPath")
				if path, _ := doc["path"].(string); !strings.HasSuffix(path, "/media/audiobooks/Terry Fixture/Long Saga/Book 1 - The Beginning") {
					t.Fatalf("path %q", path)
				}
				valueAt(t, doc, false, "isFile")
				valueAt(t, doc, "book", "mediaType")
				numberAt(t, doc, 244064, "size")
				numberAt(t, doc, 4, "numFiles")
				valueAt(t, doc, "5e33de11-3e94-4c51-87d8-d06d2928ac38", "media", "id")
				valueAt(t, doc, "The Beginning", "media", "metadata", "title")
				valueAt(t, doc, "Beginning, The", "media", "metadata", "titleIgnorePrefix")
				valueAt(t, doc, "Terry Fixture", "media", "metadata", "authorName")
				valueAt(t, doc, "Fixture, Terry", "media", "metadata", "authorNameLF")
				valueAt(t, doc, "Long Saga #1", "media", "metadata", "seriesName")
				valueAt(t, doc, "e987a322-9111-4f79-8cb9-c25ee714e0d6", "media", "metadata", "series", 0, "id")
				valueAt(t, doc, "1", "media", "metadata", "series", 0, "sequence")
				valueAt(t, doc, "75e57a0b-a39d-4073-8501-e3b745ebd79d", "media", "metadata", "authors", 0, "id")
				numberAt(t, doc, 3, "media", "numTracks")
				numberAt(t, doc, 3, "media", "numAudioFiles")
				numberAt(t, doc, 3, "media", "numChapters")
				numberAt(t, doc, 60.186123, "media", "duration")
				numberAt(t, doc, 241674, "media", "size")
				offsets := []float64{0, 20.062041, 40.124082}
				for i, c := range []struct{ ino, name string }{{"828693", "01 - Part 1.mp3"}, {"828709", "02 - Part 2.mp3"}, {"828727", "03 - Part 3.mp3"}} {
					valueAt(t, doc, c.ino, "media", "tracks", i, "ino")
					valueAt(t, doc, c.name, "media", "tracks", i, "title")
					valueAt(t, doc, c.name, "media", "tracks", i, "metadata", "filename")
					valueAt(t, doc, "/api/items/b3c61dfc-dd4b-47ee-8998-0340c1228f52/file/"+c.ino, "media", "tracks", i, "contentUrl")
					valueAt(t, doc, "audio/mpeg", "media", "tracks", i, "mimeType")
					numberAt(t, doc, offsets[i], "media", "tracks", i, "startOffset")
					numberAt(t, doc, 20.062041, "media", "tracks", i, "duration")
					numberAt(t, doc, 80558, "media", "tracks", i, "metadata", "size")
					numberAt(t, doc, float64(i+1), "media", "tracks", i, "index")
					valueAt(t, doc, c.name, "libraryFiles", i, "metadata", "filename")
					valueAt(t, doc, "audio", "libraryFiles", i, "fileType")
					valueAt(t, doc, "Part "+string(rune('1'+i)), "media", "chapters", i, "title")
					numberAt(t, doc, offsets[i], "media", "chapters", i, "start")
				}
				numberAt(t, doc, 60.186123, "media", "chapters", 2, "end")
				valueAt(t, doc, "cover.jpg", "libraryFiles", 3, "metadata", "filename")
				valueAt(t, doc, "image", "libraryFiles", 3, "fileType")
				numberAt(t, doc, 2390, "libraryFiles", 3, "metadata", "size")
			},
		},
		{
			Requirement: "route-r050", Invariant: "INV-R050-EXPANDED-001", Case: "normal", State: "stateless", Assertion: "expanded-literal-facts-single-file-book",
			Sources: []string{findOneSource, "server/models/Book.js:293-305,640-690"},
			Claim:   "The expanded document of the single-file book carries the literal fixture facts: ids, path, size, duration 45, one m4b track, three titled chapters with bounds, year, genre and cover path.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+e.IDs["novel"]+"?expanded=1")
				wantStatus(t, "novel", resp, 200)
				doc := resp.Object(t)
				valueAt(t, doc, "91bb63f7-97c9-4230-8fd0-1dfd994977ea", "id")
				valueAt(t, doc, "1089837", "ino")
				valueAt(t, doc, "Ada Example/Single File Novel", "relPath")
				numberAt(t, doc, 193740, "size")
				numberAt(t, doc, 1, "numFiles")
				valueAt(t, doc, "85ab0f32-b17f-4c9a-883e-a61e260f1e46", "media", "id")
				valueAt(t, doc, "Single File Novel", "media", "metadata", "title")
				valueAt(t, doc, "Ada Example", "media", "metadata", "authorName")
				valueAt(t, doc, "2021", "media", "metadata", "publishedYear")
				valueAt(t, doc, "Fiction", "media", "metadata", "genres", 0)
				if cover, _ := harness.Dig(doc, "media", "coverPath").(string); !strings.HasSuffix(cover, "/metadata/items/91bb63f7-97c9-4230-8fd0-1dfd994977ea/cover.jpg") {
					t.Fatalf("coverPath %q", cover)
				}
				numberAt(t, doc, 1, "media", "numTracks")
				numberAt(t, doc, 3, "media", "numChapters")
				numberAt(t, doc, 45, "media", "duration")
				numberAt(t, doc, 193740, "media", "size")
				valueAt(t, doc, "1089873", "media", "tracks", 0, "ino")
				valueAt(t, doc, "Single File Novel.m4b", "media", "tracks", 0, "title")
				valueAt(t, doc, "/api/items/91bb63f7-97c9-4230-8fd0-1dfd994977ea/file/1089873", "media", "tracks", 0, "contentUrl")
				valueAt(t, doc, "audio/mp4", "media", "tracks", 0, "mimeType")
				numberAt(t, doc, 0, "media", "tracks", 0, "startOffset")
				numberAt(t, doc, 45, "media", "tracks", 0, "duration")
				for i, c := range []struct {
					title      string
					start, end float64
				}{{"Opening", 0, 15}, {"Middle", 15, 30}, {"Ending", 30, 45}} {
					valueAt(t, doc, c.title, "media", "chapters", i, "title")
					numberAt(t, doc, c.start, "media", "chapters", i, "start")
					numberAt(t, doc, c.end, "media", "chapters", i, "end")
					numberAt(t, doc, float64(i), "media", "chapters", i, "id")
				}
			},
		},
	}
}
