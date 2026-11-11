package item

import (
	"encoding/json"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/textclean"
	"audiobookshelf-compat/internal/textsort"
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
	return db.Ptr(textclean.Sanitize(*s))
}

// apply writes the patch onto row and returns the changed columns. A new title
// carries its sort form (Book.js:394-396); the libraryItems copy follows by trigger.
func (p *BookPatch) apply(row *db.Book, prefixes []string) columns {
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
	if slices.Contains(c, "title") {
		row.TitleIgnorePrefix = db.Ptr(textsort.TitleIgnorePrefix(db.Val(row.Title), prefixes))
		c = append(c, "titleIgnorePrefix")
	}
	if p.coverPath != nil {
		row.CoverPath = p.coverPath
		c = append(c, "coverPath")
	}
	return c
}

// apply writes the patch onto row (Podcast.js:210-292) and returns the changed columns.
func (p *PodcastPatch) apply(row *db.Podcast, prefixes []string) columns {
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
	// A blank schedule is ignored, as upstream (Podcast.js:259).
	if p.AutoDownloadSchedule != nil && strings.TrimSpace(*p.AutoDownloadSchedule) != "" {
		c.text(textField{"autoDownloadSchedule", &row.AutoDownloadSchedule, p.AutoDownloadSchedule})
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
	if slices.Contains(c, "title") {
		row.TitleIgnorePrefix = db.Ptr(textsort.TitleIgnorePrefix(db.Val(row.Title), prefixes))
		c = append(c, "titleIgnorePrefix")
	}
	if p.coverPath != nil {
		row.CoverPath = p.coverPath
		c = append(c, "coverPath")
	}
	return c
}
