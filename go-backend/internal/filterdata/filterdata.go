// Package filterdata is the in-memory library filter data cache that
// upstream keeps in Database.libraryFilterData. The filter menus read it for up
// to 30 minutes and scans, item edits and deletions only patch it in place, so
// its counts go stale and appended entries stay unsorted until it reloads. The
// cache is shared by the catalog (readers) and the scanner (patches).
package filterdata

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// TTL is how long a loaded entry is served before it is rebuilt
// (libraryFilters.js:455-461).
const TTL = 30 * time.Minute

// Named is an author or series entry.
type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Data is one library's filter data (libraryFilters.getFilterData).
type Data struct {
	Authors          []Named  `json:"authors"`
	Genres           []string `json:"genres"`
	Tags             []string `json:"tags"`
	Series           []Named  `json:"series"`
	Narrators        []string `json:"narrators"`
	Languages        []string `json:"languages"`
	Publishers       []string `json:"publishers"`
	PublishedDecades []string `json:"publishedDecades"`
	BookCount        int      `json:"bookCount"`
	AuthorCount      int      `json:"authorCount"`
	SeriesCount      int      `json:"seriesCount"`
	PodcastCount     int      `json:"podcastCount"`
	NumIssues        int      `json:"numIssues"`
	LoadedAt         int64    `json:"loadedAt"`
}

func (d *Data) normalize() {
	if d.Authors == nil {
		d.Authors = []Named{}
	}
	if d.Series == nil {
		d.Series = []Named{}
	}
	for _, list := range []*[]string{&d.Genres, &d.Tags, &d.Narrators, &d.Languages, &d.Publishers, &d.PublishedDecades} {
		if *list == nil {
			*list = []string{}
		}
	}
}

func (d Data) clone() Data {
	d.Authors = slices.Clone(d.Authors)
	d.Series = slices.Clone(d.Series)
	d.Genres = slices.Clone(d.Genres)
	d.Tags = slices.Clone(d.Tags)
	d.Narrators = slices.Clone(d.Narrators)
	d.Languages = slices.Clone(d.Languages)
	d.Publishers = slices.Clone(d.Publishers)
	d.PublishedDecades = slices.Clone(d.PublishedDecades)
	d.normalize()
	return d
}

// FromMap converts a freshly built filter data map into Data.
func FromMap(m map[string]any) (Data, error) {
	var d Data
	raw, err := json.Marshal(m)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	d.normalize()
	return d, nil
}

// Map converts Data back to the response map.
func (d Data) Map() (map[string]any, error) {
	raw, err := json.Marshal(d.clone())
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

// Provider is what the scanner needs from the catalog: load a library's filter
// data when no cached copy exists (upstream does this at the start of every
// scan) and reach the cache to patch it.
type Provider interface {
	EnsureFilterData(ctx context.Context, libraryID, mediaType string) error
	FilterCache() *Cache
}

// Cache holds filter data per library.
type Cache struct {
	mu   sync.Mutex
	libs map[string]*Data
}

func NewCache() *Cache { return &Cache{libs: map[string]*Data{}} }

// Fresh returns a copy of the entry when it is younger than TTL.
func (c *Cache) Fresh(libraryID string) (Data, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.libs[libraryID]
	if d == nil || time.Since(time.UnixMilli(d.LoadedAt)) >= TTL {
		return Data{}, false
	}
	return d.clone(), true
}

// Has reports whether any entry (fresh or expired) exists.
func (c *Cache) Has(libraryID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.libs[libraryID] != nil
}

func (c *Cache) Store(libraryID string, d Data) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d = d.clone()
	c.libs[libraryID] = &d
}

// Invalidate drops one library's entry, or all of them when libraryID is empty.
func (c *Cache) Invalidate(libraryID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if libraryID == "" {
		c.libs = map[string]*Data{}
		return
	}
	delete(c.libs, libraryID)
}

func (c *Cache) with(libraryID string, fn func(*Data)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.libs[libraryID]; d != nil {
		fn(d)
	}
}

func appendUnique(list []string, values ...string) []string {
	for _, v := range values {
		if v != "" && !slices.Contains(list, v) {
			list = append(list, v)
		}
	}
	return list
}

// AddAuthor is Database.addAuthorToFilterData (Database.js:581-588).
func (c *Cache) AddAuthor(libraryID, name, id string) {
	c.with(libraryID, func(d *Data) {
		for _, a := range d.Authors {
			if a.ID == id {
				return
			}
		}
		d.Authors = append(d.Authors, Named{ID: id, Name: name})
	})
}

func (c *Cache) RemoveAuthor(libraryID, id string) {
	c.with(libraryID, func(d *Data) {
		d.Authors = slices.DeleteFunc(d.Authors, func(a Named) bool { return a.ID == id })
	})
}

// AddSeries is Database.addSeriesToFilterData (Database.js:551-560).
func (c *Cache) AddSeries(libraryID, name, id string) {
	c.with(libraryID, func(d *Data) {
		for _, s := range d.Series {
			if s.ID == id {
				return
			}
		}
		d.Series = append(d.Series, Named{ID: id, Name: name})
	})
}

func (c *Cache) RemoveSeries(libraryID, id string) {
	c.with(libraryID, func(d *Data) {
		d.Series = slices.DeleteFunc(d.Series, func(s Named) bool { return s.ID == id })
	})
}

func (c *Cache) AddGenres(libraryID string, genres []string) {
	c.with(libraryID, func(d *Data) { d.Genres = appendUnique(d.Genres, genres...) })
}

func (c *Cache) AddTags(libraryID string, tags []string) {
	c.with(libraryID, func(d *Data) { d.Tags = appendUnique(d.Tags, tags...) })
}

func (c *Cache) AddNarrators(libraryID string, narrators []string) {
	c.with(libraryID, func(d *Data) { d.Narrators = appendUnique(d.Narrators, narrators...) })
}

func (c *Cache) AddPublisher(libraryID, publisher string) {
	c.with(libraryID, func(d *Data) { d.Publishers = appendUnique(d.Publishers, publisher) })
}

func (c *Cache) AddPublishedDecade(libraryID, decade string) {
	c.with(libraryID, func(d *Data) { d.PublishedDecades = appendUnique(d.PublishedDecades, decade) })
}

func (c *Cache) AddLanguage(libraryID, language string) {
	c.with(libraryID, func(d *Data) { d.Languages = appendUnique(d.Languages, language) })
}

// StoreNumIssues is Database.resetLibraryIssuesFilterData's outcome.
func (c *Cache) StoreNumIssues(libraryID string, n int) {
	c.with(libraryID, func(d *Data) { d.NumIssues = n })
}
