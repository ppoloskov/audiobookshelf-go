package item

import (
	"encoding/json"
	"slices"

	"monstrobook/internal/db"
	"monstrobook/internal/text"
)

// BookPatch is the media payload of PATCH /api/items/{id}/media for a book.
// A nil field is absent and leaves the stored value alone; JSON null reads as
// absent. The same type carries quick-match results.
type BookPatch struct {
	Metadata *BookMetadataPatch `json:"metadata"`
	Tags     *[]string          `json:"tags"`

	coverPath *string // set by quick match only, never decoded
}

// BookMetadataPatch is the metadata object of a BookPatch. An empty string
// clears a text column (stored as NULL).
type BookMetadataPatch struct {
	Title         Text           `json:"title"`
	Subtitle      Text           `json:"subtitle"`
	PublishedYear Text           `json:"publishedYear"`
	PublishedDate Text           `json:"publishedDate"`
	Publisher     Text           `json:"publisher"`
	Description   Text           `json:"description"`
	ISBN          Text           `json:"isbn"`
	ASIN          Text           `json:"asin"`
	Language      Text           `json:"language"`
	Explicit      *bool          `json:"explicit"`
	Abridged      *bool          `json:"abridged"`
	Narrators     *[]string      `json:"narrators"`
	Genres        *[]string      `json:"genres"`
	Authors       *[]AuthorPatch `json:"authors"`
	Series        *[]SeriesPatch `json:"series"`
}

// AuthorPatch names one author of a book.
type AuthorPatch struct {
	Name string `json:"name"`
}

// SeriesPatch names one series of a book. A request without a sequence clears
// it; a quick match without one keeps the stored sequence.
type SeriesPatch struct {
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`

	keepSequence bool
}

// PodcastPatch is the media payload of PATCH /api/items/{id}/media for a podcast.
type PodcastPatch struct {
	Metadata                 *PodcastMetadataPatch `json:"metadata"`
	Tags                     *[]string             `json:"tags"`
	AutoDownloadEpisodes     *bool                 `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string               `json:"autoDownloadSchedule"`
	MaxEpisodesToKeep        *int64                `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload *int64                `json:"maxNewEpisodesToDownload"`

	coverPath *string // set by quick match only, never decoded
}

// PodcastMetadataPatch is the metadata object of a PodcastPatch.
type PodcastMetadataPatch struct {
	Title          Text      `json:"title"`
	Author         Text      `json:"author"`
	ReleaseDate    Text      `json:"releaseDate"`
	FeedURL        Text      `json:"feedUrl"`
	ImageURL       Text      `json:"imageUrl"`
	Description    Text      `json:"description"`
	ItunesPageURL  Text      `json:"itunesPageUrl"`
	ItunesID       Text      `json:"itunesId"`
	ItunesArtistID Text      `json:"itunesArtistId"`
	Language       Text      `json:"language"`
	Type           Text      `json:"type"`
	Explicit       *bool     `json:"explicit"`
	Genres         *[]string `json:"genres"`
}

// Text is a patch string. Set is false when the key was absent. JSON null and
// "" both clear the column (stored as NULL), and a JSON number is read as its
// decimal text, as upstream coerces it (a client may send publishedYear as 1999).
type Text struct {
	Set   bool
	Value string
}

// UnmarshalJSON reads a JSON string, number or null; anything else is an error.
func (t *Text) UnmarshalJSON(data []byte) error {
	*t = Text{Set: true}
	switch {
	case string(data) == "null":
		return nil
	case len(data) > 0 && data[0] == '"':
		return json.Unmarshal(data, &t.Value)
	}
	var n json.Number
	err := json.Unmarshal(data, &n)
	t.Value = string(n)
	return err
}

// ptr is the value to store, nil when the key was absent.
func (t Text) ptr() *string {
	if !t.Set {
		return nil
	}
	return db.Ptr(t.Value)
}

// textField is one nullable text column and the patch value for it.
type textField struct {
	column string
	dst    **string
	next   *string
}

// column collects the names of the columns a patch changed.
type columns []string

// text stores next in dst when it differs ("" is NULL).
func (c *columns) text(f textField) {
	if f.next == nil || db.Val(*f.dst) == *f.next {
		return
	}
	if *f.next == "" {
		*f.dst = nil
	} else {
		*f.dst = db.Ptr(*f.next)
	}
	*c = append(*c, f.column)
}

func (c *columns) flag(column string, dst *db.Bool, next *bool) {
	if next != nil && bool(*dst) != *next {
		*dst = db.Bool(*next)
		*c = append(*c, column)
	}
}

func (c *columns) list(column string, dst *db.JSON[[]string], next *[]string) {
	if next != nil && !slices.Equal(dst.V, *next) {
		dst.V = slices.Clone(*next)
		*c = append(*c, column)
	}
}

// sanitized is the description as stored (Book.js:382).
func sanitized(s *string) *string {
	if s == nil {
		return nil
	}
	return db.Ptr(text.Sanitize(*s))
}

// applyTitleAndCover updates titleIgnorePrefix (for PATCH / batch-update, as
// Book.updateFromRequest/Podcast.updateFromRequest do, while Scanner.quickMatchLibraryItem
// leaves titleIgnorePrefix alone) and coverPath when set.
func (c *columns) applyTitleAndCover(title *string, dstPrefix, dstCover **string, cover *string, prefixes []string, src source) {
	if src == fromRequest && slices.Contains(*c, "title") {
		*dstPrefix = db.Ptr(text.TitleIgnorePrefix(db.Val(title), prefixes))
		*c = append(*c, "titleIgnorePrefix")
	}
	if cover != nil {
		*dstCover = cover
		*c = append(*c, "coverPath")
	}
}

// apply writes the patch onto row and returns the changed columns. A new title
// from a request carries its sort form (Book.js:394-396); the libraryItems copy
// follows by trigger. Quick match leaves titleIgnorePrefix unchanged (Scanner.js:109-122).
func (p *BookPatch) apply(row *db.Book, prefixes []string, src source) columns {
	var c columns
	if m := p.Metadata; m != nil {
		for _, f := range []textField{
			{"title", &row.Title, m.Title.ptr()}, {"subtitle", &row.Subtitle, m.Subtitle.ptr()},
			{"publishedYear", &row.PublishedYear, m.PublishedYear.ptr()}, {"publishedDate", &row.PublishedDate, m.PublishedDate.ptr()},
			{"publisher", &row.Publisher, m.Publisher.ptr()}, {"description", &row.Description, sanitized(m.Description.ptr())},
			{"isbn", &row.ISBN, m.ISBN.ptr()}, {"asin", &row.ASIN, m.ASIN.ptr()}, {"language", &row.Language, m.Language.ptr()},
		} {
			c.text(f)
		}
		c.flag("explicit", &row.Explicit, m.Explicit)
		c.flag("abridged", &row.Abridged, m.Abridged)
		c.list("narrators", &row.Narrators, m.Narrators)
		c.list("genres", &row.Genres, m.Genres)
	}
	c.list("tags", &row.Tags, p.Tags)
	c.applyTitleAndCover(row.Title, &row.TitleIgnorePrefix, &row.CoverPath, p.coverPath, prefixes, src)
	return c
}

// apply writes the patch onto row (Podcast.js:210-292) and returns the changed columns.
func (p *PodcastPatch) apply(row *db.Podcast, prefixes []string, src source) columns {
	var c columns
	if m := p.Metadata; m != nil {
		for _, f := range []textField{
			{"title", &row.Title, m.Title.ptr()}, {"author", &row.Author, m.Author.ptr()}, {"releaseDate", &row.ReleaseDate, m.ReleaseDate.ptr()},
			{"feedURL", &row.FeedURL, m.FeedURL.ptr()}, {"imageURL", &row.ImageURL, m.ImageURL.ptr()}, {"description", &row.Description, sanitized(m.Description.ptr())},
			{"itunesPageURL", &row.ItunesPageURL, m.ItunesPageURL.ptr()}, {"itunesId", &row.ItunesID, m.ItunesID.ptr()},
			{"itunesArtistId", &row.ItunesArtistID, m.ItunesArtistID.ptr()}, {"language", &row.Language, m.Language.ptr()}, {"podcastType", &row.PodcastType, m.Type.ptr()},
		} {
			c.text(f)
		}
		c.flag("explicit", &row.Explicit, m.Explicit)
		c.list("genres", &row.Genres, m.Genres)
	}
	c.list("tags", &row.Tags, p.Tags)
	c.flag("autoDownloadEpisodes", &row.AutoDownloadEpisodes, p.AutoDownloadEpisodes)
	// Unlike metadata strings, Podcast.updateFromRequest (Podcast.js:276-280)
	// stores autoDownloadSchedule verbatim without converting "" to NULL.
	if p.AutoDownloadSchedule != nil && (row.AutoDownloadSchedule == nil || *row.AutoDownloadSchedule != *p.AutoDownloadSchedule) {
		row.AutoDownloadSchedule = db.Ptr(*p.AutoDownloadSchedule)
		c = append(c, "autoDownloadSchedule")
	}
	for _, n := range []struct {
		column string
		dst    **int64
		next   *int64
	}{{"maxEpisodesToKeep", &row.MaxEpisodesToKeep, p.MaxEpisodesToKeep}, {"maxNewEpisodesToDownload", &row.MaxNewEpisodesToDownload, p.MaxNewEpisodesToDownload}} {
		if n.next != nil && (*n.dst == nil || **n.dst != *n.next) {
			*n.dst = db.Ptr(*n.next)
			c = append(c, n.column)
		}
	}
	c.applyTitleAndCover(row.Title, &row.TitleIgnorePrefix, &row.CoverPath, p.coverPath, prefixes, src)
	return c
}
