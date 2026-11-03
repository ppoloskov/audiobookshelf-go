package wire

import "encoding/json"

// BookSearch is a BookFinder.search call (server/finders/BookFinder.js:243-300).
// Duration is the library item's duration in seconds (0 when unknown); it
// orders Audible results.
type BookSearch struct {
	Provider, Title, Author, ISBN, ASIN string
	Duration                            float64
	MaxFuzzySearches                    int
}

// BookMatch is one provider result. Providers differ in which keys they
// return and in their types (author is a string or a list, series a name or a
// list of {series, sequence}, years strings or numbers), so every field keeps
// the provider's JSON for the caller to interpret like the JavaScript does.
type BookMatch struct {
	Title            json.RawMessage `json:"title"`
	Subtitle         json.RawMessage `json:"subtitle"`
	Author           json.RawMessage `json:"author"`
	Narrator         json.RawMessage `json:"narrator"`
	Publisher        json.RawMessage `json:"publisher"`
	PublishedYear    json.RawMessage `json:"publishedYear"`
	Description      json.RawMessage `json:"description"`
	DescriptionPlain json.RawMessage `json:"descriptionPlain"`
	Cover            json.RawMessage `json:"cover"`
	ISBN             json.RawMessage `json:"isbn"`
	ASIN             json.RawMessage `json:"asin"`
	Genres           json.RawMessage `json:"genres"`
	Tags             json.RawMessage `json:"tags"`
	Series           json.RawMessage `json:"series"`
	Sequence         json.RawMessage `json:"sequence"`
	Language         json.RawMessage `json:"language"`
	Explicit         json.RawMessage `json:"explicit"`
	Abridged         json.RawMessage `json:"abridged"`
	Duration         json.RawMessage `json:"duration"`
}

// PodcastMatch is an iTunes podcast search result as the quick match reads it
// (iTunes.cleanPodcast, server/providers/iTunes.js:140-157).
type PodcastMatch struct {
	ID               json.RawMessage `json:"id"`
	ArtistID         json.RawMessage `json:"artistId"`
	Title            json.RawMessage `json:"title"`
	ArtistName       json.RawMessage `json:"artistName"`
	DescriptionPlain json.RawMessage `json:"descriptionPlain"`
	ReleaseDate      json.RawMessage `json:"releaseDate"`
	Genres           json.RawMessage `json:"genres"`
	Cover            json.RawMessage `json:"cover"`
	FeedURL          json.RawMessage `json:"feedUrl"`
	PageURL          json.RawMessage `json:"pageUrl"`
}

// QuickMatchResult is the answer of Scanner.quickMatchLibraryItem
// (server/scanner/Scanner.js:37-137): {warning} when nothing matched, else
// {updated, libraryItem} with the expanded item.
type QuickMatchResult struct {
	Warning     *string         `json:"warning,omitempty"`
	Updated     *bool           `json:"updated,omitempty"`
	LibraryItem json.RawMessage `json:"libraryItem,omitempty"`
}

// BatchQuickMatchResult is the batch_quickmatch_complete event payload
// (LibraryItemController.js:803-810).
type BatchQuickMatchResult struct {
	Success   bool `json:"success"`
	Updates   int  `json:"updates"`
	Unmatched int  `json:"unmatched"`
}

// SeriesCreated is Series.toOldJSON of a freshly created series, which has no
// description attribute (server/models/Series.js:168-178): the series_added
// payload of a quick match.
type SeriesCreated struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	NameIgnorePrefix string `json:"nameIgnorePrefix"`
	AddedAt          int64  `json:"addedAt"`
	UpdatedAt        int64  `json:"updatedAt"`
	LibraryID        string `json:"libraryId"`
}

// Task is Task.toJSON (server/objects/Task.js:54-74).
type Task struct {
	ID              string   `json:"id"`
	Action          string   `json:"action"`
	Data            TaskData `json:"data"`
	Title           string   `json:"title"`
	TitleKey        string   `json:"titleKey"`
	TitleSubs       []string `json:"titleSubs"`
	Description     *string  `json:"description"`
	DescriptionKey  *string  `json:"descriptionKey"`
	DescriptionSubs []string `json:"descriptionSubs"`
	Error           *string  `json:"error"`
	ErrorKey        *string  `json:"errorKey"`
	ErrorSubs       []string `json:"errorSubs"`
	ShowSuccess     bool     `json:"showSuccess"`
	IsFailed        bool     `json:"isFailed"`
	IsFinished      bool     `json:"isFinished"`
	StartedAt       int64    `json:"startedAt"`
	FinishedAt      *int64   `json:"finishedAt"`
}

// TaskData is the data of a library task.
type TaskData struct {
	LibraryID   string           `json:"libraryId"`
	ScanResults *TaskScanResults `json:"scanResults,omitempty"`
}

// TaskScanResults is LibraryScan.scanResults (LibraryScan.js:71-79).
type TaskScanResults struct {
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Missing int    `json:"missing"`
	Elapsed int64  `json:"elapsed"`
	Text    string `json:"text"`
}
