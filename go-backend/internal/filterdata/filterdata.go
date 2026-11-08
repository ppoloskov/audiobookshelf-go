// Package filterdata is the in-memory library filter data cache that
// upstream keeps in Database.libraryFilterData. The filter menus read it for up
// to 30 minutes and scans, item edits and deletions only patch it in place, so
// its counts go stale and appended entries stay unsorted until it reloads. The
// cache is shared by the catalog (readers) and the scanner (patches).
package filterdata

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/logging"
)

// TTL is how long a loaded entry is served before it is rebuilt
// (libraryFilters.js:455-461).
const TTL = 30 * time.Minute

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

// Load builds a library's filter data from the database
// (libraryFilters.getFilterData, libraryFilters.js:455-461): the distinct,
// case-insensitively sorted values of its items, and its authors and series by
// name.
func Load(ctx context.Context, db *sql.DB, libraryID, mediaType string) (Data, error) {
	d := Data{LoadedAt: time.Now().UnixMilli()}
	values := map[string]map[string]struct{}{}
	add := func(field string, vs ...string) {
		if values[field] == nil {
			values[field] = map[string]struct{}{}
		}
		for _, v := range vs {
			values[field][v] = struct{}{}
		}
	}
	list := func(raw []byte) (out []string) {
		_ = json.Unmarshal(raw, &out)
		return out
	}
	switch mediaType {
	case "book":
		rows, err := db.QueryContext(ctx, `SELECT b.tags,b.genres,b.narrators,b.publisher,b.publishedYear,b.language,li.isMissing OR li.isInvalid FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=?`, libraryID)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var tags, genres, narrators []byte
			var publisher, year, language sql.NullString
			var issue bool
			if err := rows.Scan(&tags, &genres, &narrators, &publisher, &year, &language, &issue); err != nil {
				rows.Close()
				return d, err
			}
			d.BookCount++
			if issue {
				d.NumIssues++
			}
			add("tags", list(tags)...)
			add("genres", list(genres)...)
			add("narrators", list(narrators)...)
			if publisher.Valid {
				add("publishers", publisher.String)
			}
			if language.Valid {
				add("languages", language.String)
			}
			if n, e := strconv.Atoi(year.String); year.Valid && e == nil && n > 0 && n < 3000 {
				add("decades", strconv.Itoa(n/10*10))
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return d, err
		}
		if err := rows.Close(); err != nil {
			return d, err
		}
		if d.Authors, err = named(ctx, db, `SELECT id,name FROM authors WHERE libraryId=?`, libraryID, ""); err != nil {
			return d, err
		}
		if d.Series, err = named(ctx, db, `SELECT id,name FROM series WHERE libraryId=?`, libraryID, "No Title"); err != nil {
			return d, err
		}
		d.AuthorCount, d.SeriesCount = len(d.Authors), len(d.Series)
	case "podcast":
		rows, err := db.QueryContext(ctx, `SELECT p.tags,p.genres,p.language FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=?`, libraryID)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var tags, genres []byte
			var language sql.NullString
			if err := rows.Scan(&tags, &genres, &language); err != nil {
				rows.Close()
				return d, err
			}
			d.PodcastCount++
			add("tags", list(tags)...)
			add("genres", list(genres)...)
			if language.Valid {
				add("languages", language.String)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return d, err
		}
		rows.Close()
	default:
		return d, fmt.Errorf("unsupported media type %q", mediaType)
	}
	d.Genres, d.Tags, d.Narrators = sorted(values["genres"]), sorted(values["tags"]), sorted(values["narrators"])
	d.Languages, d.Publishers, d.PublishedDecades = sorted(values["languages"]), sorted(values["publishers"]), sorted(values["decades"])
	d.normalize()
	return d, nil
}

// named reads id/name pairs sorted by lowercase name; a missing name is
// replaced by fallback when one is given.
func named(ctx context.Context, db *sql.DB, query, libraryID, fallback string) ([]Named, error) {
	rows, err := db.QueryContext(ctx, query, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Named
	for rows.Next() {
		var n Named
		var name sql.NullString
		if err := rows.Scan(&n.ID, &name); err != nil {
			return nil, err
		}
		if n.Name = name.String; n.Name == "" {
			n.Name = fallback
		}
		out = append(out, n)
	}
	slices.SortStableFunc(out, func(a, b Named) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out, rows.Err()
}

func sorted(set map[string]struct{}) []string {
	out := slices.Sorted(maps.Keys(set))
	slices.SortStableFunc(out, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

type Cache struct {
	mu   sync.Mutex
	libs map[string]*Data
}

func NewCache() *Cache { return &Cache{libs: map[string]*Data{}} }

func (c *Cache) Fresh(libraryID string) (Data, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.libs[libraryID]
	if d == nil || time.Since(time.UnixMilli(d.LoadedAt)) >= TTL {
		return Data{}, false
	}
	return d.clone(), true
}

// Get serves the entry while it is fresh and otherwise reloads and stores it
// (libraryFilters.js:449-466).
func (c *Cache) Get(ctx context.Context, db *sql.DB, libraryID, mediaType string) (Data, error) {
	if d, ok := c.Fresh(libraryID); ok {
		return d, nil
	}
	d, err := Load(ctx, db, libraryID, mediaType)
	if err != nil {
		return d, err
	}
	c.Store(libraryID, d)
	return d.clone(), nil
}

// ResetIssues is Database.resetLibraryIssuesFilterData: it recounts missing or
// invalid items into an existing entry and does nothing otherwise.
func (c *Cache) ResetIssues(ctx context.Context, db *sql.DB, libraryID string) {
	c.with(libraryID, func(d *Data) {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID).Scan(&n); err != nil {
			logging.Errorf("[filterdata] count issues of library %s: %v", libraryID, err)
			return
		}
		d.NumIssues = n
	})
}

func (c *Cache) Store(libraryID string, d Data) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d = d.clone()
	c.libs[libraryID] = &d
}

// Invalidate drops all entries when libraryID is empty.
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

// BookValues are the filter values a newly added book contributes.
type BookValues struct {
	Genres, Tags, Narrators, Languages, Publishers, PublishedDecades []string
}

// AddBook appends a new book's values to an existing entry, as the scanner's
// Database.addGenres/addTags/addNarrators/... calls do; a missing entry is
// left alone.
func (c *Cache) AddBook(libraryID string, v BookValues) {
	c.with(libraryID, func(d *Data) {
		d.Genres = appendUnique(d.Genres, v.Genres...)
		d.Tags = appendUnique(d.Tags, v.Tags...)
		d.Narrators = appendUnique(d.Narrators, v.Narrators...)
		d.Publishers = appendUnique(d.Publishers, v.Publishers...)
		d.PublishedDecades = appendUnique(d.PublishedDecades, v.PublishedDecades...)
		d.Languages = appendUnique(d.Languages, v.Languages...)
	})
}

// StoreNumIssues is Database.resetLibraryIssuesFilterData's outcome.
func (c *Cache) StoreNumIssues(libraryID string, n int) {
	c.with(libraryID, func(d *Data) { d.NumIssues = n })
}
