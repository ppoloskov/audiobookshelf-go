package browse

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// The fixture keys of ids.json used by the packet.
const (
	bookLibrary    = "library"
	podcastLibrary = "podcastLibrary"
)

// seriesRef is the series object of a minified item (media.metadata.series).
type seriesRef struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// itemMetadata is the metadata block of a minified book or podcast item.
type itemMetadata struct {
	Title         string          `json:"title"`
	Subtitle      *string         `json:"subtitle"`
	Author        string          `json:"author"`
	AuthorName    string          `json:"authorName"`
	AuthorNameLF  string          `json:"authorNameLF"`
	NarratorName  string          `json:"narratorName"`
	SeriesName    string          `json:"seriesName"`
	Genres        []string        `json:"genres"`
	PublishedYear *string         `json:"publishedYear"`
	Publisher     *string         `json:"publisher"`
	Language      *string         `json:"language"`
	Explicit      bool            `json:"explicit"`
	Abridged      bool            `json:"abridged"`
	ISBN          *string         `json:"isbn"`
	ASIN          *string         `json:"asin"`
	TitleIgnore   string          `json:"titleIgnorePrefix"`
	Series        json.RawMessage `json:"series"` // an object in lists, an array in expanded items
}

// itemMedia is the media block of a minified item.
type itemMedia struct {
	ID          string       `json:"id"`
	Metadata    itemMetadata `json:"metadata"`
	CoverPath   *string      `json:"coverPath"`
	Tags        []string     `json:"tags"`
	NumTracks   *int         `json:"numTracks"`
	NumAudio    *int         `json:"numAudioFiles"`
	NumChapters *int         `json:"numChapters"`
	Duration    *float64     `json:"duration"`
	Size        int64        `json:"size"`
	NumEpisodes *int         `json:"numEpisodes"`
}

// collapsedSeries is the collapsedSeries object of a collapsed item.
type collapsedSeries struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	NameIgnore     string   `json:"nameIgnorePrefix"`
	Sequence       *string  `json:"sequence"`
	NumBooks       int      `json:"numBooks"`
	LibraryItemIDs []string `json:"libraryItemIds"`
}

// feedRef is the minified feed of an item or series.
type feedRef struct {
	ID         string `json:"id"`
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	FeedURL    string `json:"feedUrl"`
}

// listItem is one entry of the items list, shelf entities and series books.
type listItem struct {
	ID                    string           `json:"id"`
	LibraryID             string           `json:"libraryId"`
	MediaType             string           `json:"mediaType"`
	Path                  string           `json:"path"`
	IsMissing             bool             `json:"isMissing"`
	IsInvalid             bool             `json:"isInvalid"`
	NumFiles              int              `json:"numFiles"`
	Size                  int64            `json:"size"`
	Media                 itemMedia        `json:"media"`
	CollapsedSeries       *collapsedSeries `json:"collapsedSeries"`
	RssFeed               *feedRef         `json:"rssFeed"`
	NumEpisodesIncomplete *int             `json:"numEpisodesIncomplete"`
	MediaItemShare        *struct {
		Slug string `json:"slug"`
	} `json:"mediaItemShare"`
	RecentEpisode *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"recentEpisode"`
}

// itemPage is the payload of GET /api/libraries/:id/items.
type itemPage struct {
	Results   []listItem `json:"results"`
	Total     *int       `json:"total"`
	Limit     *int       `json:"limit"`
	Page      *int       `json:"page"`
	Offset    *int       `json:"offset"`
	SortBy    *string    `json:"sortBy"`
	SortDesc  bool       `json:"sortDesc"`
	FilterBy  *string    `json:"filterBy"`
	MediaType string     `json:"mediaType"`
	Minified  bool       `json:"minified"`
	Collapse  bool       `json:"collapseseries"`
	Include   string     `json:"include"`
}

// ids returns the ids of the results in response order.
func (p itemPage) ids() []string {
	out := make([]string, 0, len(p.Results))
	for _, item := range p.Results {
		out = append(out, item.ID)
	}
	return out
}

// titles returns the media titles of the results in response order.
func (p itemPage) titles() []string {
	out := make([]string, 0, len(p.Results))
	for _, item := range p.Results {
		out = append(out, item.Media.Metadata.Title)
	}
	return out
}

// query builds url.Values from key, value pairs.
func query(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		values.Add(pairs[i], pairs[i+1])
	}
	return values
}

// filterParam builds a filter value the way the web client does: the group, a
// dot, and encodeURIComponent(base64(value)). An empty value builds a bare group.
func filterParam(group, value string) string {
	if value == "" {
		return group
	}
	return group + "." + url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(value)))
}

// fixtureIDs resolves fixture keys ("saga1", "novel", ...) to ids.
func fixtureIDs(e *harness.Env, keys ...string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, e.IDs[key])
	}
	return out
}

// libraryPath is the path of a library resource.
func libraryPath(e *harness.Env, libraryKey, suffix string) string {
	return "/api/libraries/" + e.IDs[libraryKey] + suffix
}

// getItems requests the items list and decodes it; it fails unless status is 200.
func getItems(t *testing.T, e *harness.Env, token, libraryKey string, values url.Values) itemPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: libraryPath(e, libraryKey, "/items"), Token: token, Query: values})
	if resp.Status != http.StatusOK {
		t.Fatalf("GET items %v: status %d: %s", values, resp.Status, resp.Body)
	}
	var page itemPage
	resp.JSON(t, &page)
	return page
}

// sameSet reports whether a and b hold the same elements, ignoring order.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// expectOrder fails unless got equals want in order.
func expectOrder(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// expectSet fails unless got and want hold the same elements.
func expectSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !sameSet(got, want) {
		t.Fatalf("%s: got %v, want the set %v", what, got, want)
	}
}

// expectStatus fails unless the response has the status.
func expectStatus(t *testing.T, what string, resp harness.Response, status int) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: status %d, want %d: %s", what, resp.Status, status, resp.Body)
	}
}

// expectInt fails unless the pointer holds want.
func expectInt(t *testing.T, what string, got *int, want int) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s: got %v, want %d", what, got, want)
	}
}

// mustOK fails unless the response is 200.
func mustOK(t *testing.T, what string, resp harness.Response) {
	t.Helper()
	expectStatus(t, what, resp, http.StatusOK)
}

// patchJSON sends a PATCH with a JSON string body.
func patchJSON(t *testing.T, e *harness.Env, token, path, body string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPatch, Path: path, Token: token, JSON: json.RawMessage(body)})
}

// postJSON sends a POST with a JSON string body.
func postJSON(t *testing.T, e *harness.Env, token, path, body string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Method: http.MethodPost, Path: path, Token: token, JSON: json.RawMessage(body)})
}
