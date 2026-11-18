package item_read

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	findOneSource    = "server/controllers/LibraryItemController.js:70-100"
	middlewareSource = "server/controllers/LibraryItemController.js:1220-1250"
	itemJSONSource   = "server/models/LibraryItem.js:965-1040"
)

// itemDocScenarios checks the whole decoded item document against values
// derived from the fixture database.
func itemDocScenarios(s *setup) []harness.Scenario {
	expanded := func(assertion, itemKey, claim string, sources ...string) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r050", Invariant: "INV-R050-EXPANDED-001", Case: "normal", State: "stateless", Assertion: assertion,
			Sources: append([]string{findOneSource, itemJSONSource}, sources...), Claim: claim,
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs[itemKey]
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id+"?expanded=1")
				wantStatus(t, "GET expanded item", resp, http.StatusOK)
				requireItem(t, assertion+" document", expandedItem(t, e, id), resp.Object(t))
			},
		}
	}
	plain := func(assertion, itemKey, claim string, sources ...string) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r050", Invariant: "INV-R050-SAMPLED", Case: "normal", State: "stateless", Assertion: assertion,
			Sources: append([]string{findOneSource, itemJSONSource}, sources...), Claim: claim,
			Run: func(t *testing.T, e *harness.Env) {
				id := e.IDs[itemKey]
				resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id)
				wantStatus(t, "GET item", resp, http.StatusOK)
				requireItem(t, assertion+" document", plainItem(t, e, id), resp.Object(t))
			},
		}
	}
	return []harness.Scenario{
		plain("plain-multifile-book-document", "saga1",
			"GET /api/items/:id of the multi-file book returns the old-format item with media metadata, series, audioFiles, chapters and libraryFiles, all equal to the stored values.",
			"server/models/Book.js:550-570,612-632"),
		plain("plain-podcast-document", "podcast",
			"GET /api/items/:id of the podcast returns the item with media.episodes (each with audioFile, no audioTrack) equal to the stored values.",
			"server/models/Podcast.js:402-452", "server/models/PodcastEpisode.js:193-228"),
		expanded("expanded-multifile-book-document", "saga1",
			"GET /api/items/:id?expanded=1 of the three-file book returns every field of the expanded document: ids, paths, sizes, durations, tracks with offsets and content URLs, chapters, metadata, series, timestamps as numbers.",
			"server/models/Book.js:293-302,550-687"),
		expanded("expanded-single-file-book-document", "novel",
			"GET /api/items/:id?expanded=1 of the single-file book with cover and three chapters returns the stored chapters, cover path, genres, publishedYear and one track.",
			"server/models/Book.js:293-302,550-687"),
		expanded("expanded-flac-book-document", "saga2",
			"GET /api/items/:id?expanded=1 of the flac book with sidecar text files returns narrators, description, series sequence and text libraryFiles with fileType text.",
			"server/models/Book.js:550-687", "server/objects/files/LibraryFile.js:26-47"),
		expanded("expanded-ebook-only-document", "ebook",
			"GET /api/items/:id?expanded=1 of the ebook-only book returns ebookFile and ebookFormat, no tracks and a size equal to the ebook size.",
			"server/models/Book.js:260-269,640-687"),
		expanded("expanded-podcast-document", "podcast",
			"GET /api/items/:id?expanded=1 of the podcast returns numEpisodes, size, and every episode with audioFile, audioTrack, size and duration equal to the stored values.",
			"server/models/Podcast.js:459-494", "server/models/PodcastEpisode.js:193-240"),
	}
}
