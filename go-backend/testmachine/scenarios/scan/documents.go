package scan

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// bookMetadata is metadata.json as abmetadataGenerator writes it for a book.
type bookMetadata struct {
	Tags          []string  `json:"tags"`
	Chapters      []chapter `json:"chapters"`
	Title         string    `json:"title"`
	Subtitle      *string   `json:"subtitle"`
	Authors       []string  `json:"authors"`
	Narrators     []string  `json:"narrators"`
	Series        []string  `json:"series"`
	Genres        []string  `json:"genres"`
	PublishedYear *string   `json:"publishedYear"`
	PublishedDate *string   `json:"publishedDate"`
	Publisher     *string   `json:"publisher"`
	Description   *string   `json:"description"`
	ISBN          *string   `json:"isbn"`
	ASIN          *string   `json:"asin"`
	Language      *string   `json:"language"`
	Explicit      bool      `json:"explicit"`
	Abridged      bool      `json:"abridged"`
}

// podcastMetadata is metadata.json as abmetadataGenerator writes it for a podcast.
type podcastMetadata struct {
	Tags           []string `json:"tags"`
	Title          string   `json:"title"`
	Author         *string  `json:"author"`
	Description    *string  `json:"description"`
	ReleaseDate    *string  `json:"releaseDate"`
	Genres         []string `json:"genres"`
	FeedURL        *string  `json:"feedURL"`
	ImageURL       *string  `json:"imageURL"`
	ItunesPageURL  *string  `json:"itunesPageURL"`
	ItunesID       *string  `json:"itunesId"`
	ItunesArtistID *string  `json:"itunesArtistId"`
	ASIN           *string  `json:"asin"`
	Language       *string  `json:"language"`
	Explicit       bool     `json:"explicit"`
	PodcastType    *string  `json:"podcastType"`
}

// podcastSidecarKeys is the key set PodcastScanner.saveMetadataFile writes,
// sorted (server/scanner/PodcastScanner.js:441-457).
var podcastSidecarKeys = []string{
	"asin", "author", "description", "explicit", "feedURL", "genres", "imageURL",
	"itunesArtistId", "itunesId", "itunesPageURL", "language", "podcastType",
	"releaseDate", "tags", "title",
}

// chapter is one chapter object of a book and of metadata.json.
type chapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// audioFile is one audioFiles entry of a book as the API returns it.
type audioFile struct {
	Index    int     `json:"index"`
	Ino      string  `json:"ino"`
	Duration float64 `json:"duration"`
	Format   string  `json:"format"`
	Codec    string  `json:"codec"`
	Metadata struct {
		Filename string `json:"filename"`
		Ext      string `json:"ext"`
		Path     string `json:"path"`
		RelPath  string `json:"relPath"`
		Size     int64  `json:"size"`
	} `json:"metadata"`
	TrackNumFromMeta     *int `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int `json:"discNumFromMeta"`
	TrackNumFromFilename *int `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int `json:"discNumFromFilename"`
}

// libraryFile is one libraryFiles entry as the API returns it.
type libraryFile struct {
	Ino             string `json:"ino"`
	IsSupplementary bool   `json:"isSupplementary"`
	Metadata        struct {
		Filename string `json:"filename"`
		Ext      string `json:"ext"`
		Path     string `json:"path"`
		RelPath  string `json:"relPath"`
		Size     int64  `json:"size"`
	} `json:"metadata"`
}

// authorRef and seriesRef are the linked entities of a book.
type authorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type seriesRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// item is the library item document of GET /api/items/:id (LibraryItem.toOldJSON).
type item struct {
	ID          string  `json:"id"`
	Ino         string  `json:"ino"`
	LibraryID   string  `json:"libraryId"`
	FolderID    string  `json:"folderId"`
	Path        string  `json:"path"`
	RelPath     string  `json:"relPath"`
	IsFile      bool    `json:"isFile"`
	IsMissing   bool    `json:"isMissing"`
	MediaType   string  `json:"mediaType"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	LastScan    int64   `json:"lastScan"`
	ScanVersion *string `json:"scanVersion"`
	Media       struct {
		ID            string   `json:"id"`
		LibraryItemID string   `json:"libraryItemId"`
		CoverPath     *string  `json:"coverPath"`
		Tags          []string `json:"tags"`
		Metadata      struct {
			Title             string      `json:"title"`
			TitleIgnorePrefix string      `json:"titleIgnorePrefix"`
			Subtitle          *string     `json:"subtitle"`
			Authors           []authorRef `json:"authors"`
			Narrators         []string    `json:"narrators"`
			Series            []seriesRef `json:"series"`
			Genres            []string    `json:"genres"`
			PublishedYear     *string     `json:"publishedYear"`
			PublishedDate     *string     `json:"publishedDate"`
			Publisher         *string     `json:"publisher"`
			Description       *string     `json:"description"`
			ISBN              *string     `json:"isbn"`
			ASIN              *string     `json:"asin"`
			Language          *string     `json:"language"`
			Explicit          bool        `json:"explicit"`
			Abridged          bool        `json:"abridged"`
			// Podcast metadata (Podcast.oldMetadataToJSON uses the legacy lowercase names).
			Author         *string `json:"author"`
			FeedURL        *string `json:"feedUrl"`
			ImageURL       *string `json:"imageUrl"`
			ItunesPageURL  *string `json:"itunesPageUrl"`
			ItunesID       *string `json:"itunesId"`
			ItunesArtistID *string `json:"itunesArtistId"`
			ReleaseDate    *string `json:"releaseDate"`
			PodcastType    *string `json:"type"`
		} `json:"metadata"`
		EbookFile *struct {
			EbookFormat string `json:"ebookFormat"`
		} `json:"ebookFile"`
		AudioFiles []audioFile `json:"audioFiles"`
		Chapters   []chapter   `json:"chapters"`
		Episodes   []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			PublishedAt *int64 `json:"publishedAt"`
		} `json:"episodes"`
	} `json:"media"`
	LibraryFiles []libraryFile `json:"libraryFiles"`
}

// itemDocument reads one item through the API and fails unless it exists.
func itemDocument(t *testing.T, e *harness.Env, id string) item {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/items/"+id)
	wantStatus(t, "get item "+id, resp, http.StatusOK)
	return decode[item](t, resp.Body)
}

// authorNames returns the names of the linked authors in API order.
func authorNames(doc item) []string {
	names := make([]string, 0, len(doc.Media.Metadata.Authors))
	for _, author := range doc.Media.Metadata.Authors {
		names = append(names, author.Name)
	}
	return names
}

// seriesNames returns "name #sequence" per linked series in API order.
func seriesNames(doc item) []string {
	names := make([]string, 0, len(doc.Media.Metadata.Series))
	for _, series := range doc.Media.Metadata.Series {
		if series.Sequence == nil {
			names = append(names, series.Name)
			continue
		}
		names = append(names, series.Name+" #"+*series.Sequence)
	}
	return names
}

// sidecarPath is where upstream keeps metadata.json for an item when
// storeMetadataWithItem is off (the fixture setting).
func sidecarPath(e *harness.Env, itemID string) string {
	return filepath.Join(e.Metadata, "items", itemID, "metadata.json")
}

// readSidecar decodes the stored metadata.json of an item.
func readSidecar[T any](t *testing.T, e *harness.Env, itemID string) T {
	t.Helper()
	return decode[T](t, readFile(t, sidecarPath(e, itemID)))
}

// sidecarKeys returns the top-level keys of the stored metadata.json.
func sidecarKeys(t *testing.T, e *harness.Env, itemID string) []string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(readFile(t, sidecarPath(e, itemID)), &object); err != nil {
		t.Fatalf("metadata.json of %s is not a JSON object: %v", itemID, err)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// writeSidecar replaces the stored metadata.json of an item with marshalled JSON.
func writeSidecar(t *testing.T, e *harness.Env, itemID string, value any) {
	t.Helper()
	writeFile(t, sidecarPath(e, itemID), marshalJSON(t, value))
}

// marshalJSON encodes a value as indented JSON like upstream's JSON.stringify(v, null, 2).
func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
