// Package filterdata computes and caches the library filter menus that
// GET /api/libraries/{id}/filterdata and ?include=filterdata return.
//
// A document is rebuilt from the database on the first Get after an
// Invalidate, so writers only say that a library changed and never patch the
// menus. Menus are sorted case-insensitively.
package filterdata

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/db"
)

// TTL is how long a document is served without an Invalidate. It is a backstop
// for writers that do not invalidate (series rename, podcast creation).
const TTL = 30 * time.Minute

// Named is an author or series menu entry.
type Named struct {
	ID   string `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

// Data is one library's filter data. Every list is non-nil so it encodes as [].
// The counts and NumIssues are those of the moment LoadedAt (Unix ms) names.
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

// Cache holds the document of each library. The zero value is ready to use; a
// nil *Cache ignores Invalidate.
//
// Invariant: a document computed from a read that began before an Invalidate is
// never stored, because Invalidate bumps gen and Get stores only if gen is
// unchanged since its read began. The mutex is not held during the queries.
type Cache struct {
	mu   sync.Mutex
	gen  uint64
	libs map[string]Data
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{} }

// Invalidate drops the document of libraryID, or of every library when it is
// empty. The next Get recomputes it.
func (c *Cache) Invalidate(libraryID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if libraryID == "" {
		clear(c.libs)
		return
	}
	delete(c.libs, libraryID)
}

// Get returns the library's document, computing it when it is not cached. The
// result is a deep copy the caller may modify. mediaType is "book" or "podcast".
func (c *Cache) Get(ctx context.Context, d *db.DB, libraryID, mediaType string) (Data, error) {
	c.mu.Lock()
	if data, ok := c.libs[libraryID]; ok && time.Since(time.UnixMilli(data.LoadedAt)) < TTL {
		c.mu.Unlock()
		return data.clone(), nil
	}
	gen := c.gen
	c.mu.Unlock()

	data, err := compute(ctx, d, libraryID, mediaType)
	if err != nil {
		return Data{}, err
	}
	c.mu.Lock()
	if c.gen == gen {
		if c.libs == nil {
			c.libs = map[string]Data{}
		}
		c.libs[libraryID] = data.clone()
	}
	c.mu.Unlock()
	return data, nil
}

func (d Data) clone() Data {
	d.Authors, d.Series = slices.Clone(d.Authors), slices.Clone(d.Series)
	for _, list := range []*[]string{&d.Genres, &d.Tags, &d.Narrators, &d.Languages, &d.Publishers, &d.PublishedDecades} {
		*list = slices.Clone(*list)
	}
	return d
}

// pair is one menu value: field names the list it belongs to.
type pair struct {
	Field string `db:"f"`
	Value string `db:"v"`
}

type counts struct {
	Items  int `db:"items"`
	Issues int `db:"issues"`
}

// compute builds the document with set-based queries. A malformed JSON list
// column counts as empty; a missing series name shows as "No Title".
func compute(ctx context.Context, d *db.DB, libraryID, mediaType string) (Data, error) {
	data := Data{LoadedAt: time.Now().UnixMilli(), Authors: []Named{}, Series: []Named{}}
	var table string
	var selects []string
	switch mediaType {
	case "book":
		table = "books"
		selects = []string{
			list("genres", "books", "genres"), list("tags", "books", "tags"), list("narrators", "books", "narrators"),
			scalar("publishers", "books", "m.publisher", "m.publisher <> ''"),
			scalar("languages", "books", "m.language", "m.language <> ''"),
			// Years that are digits only, 1..2999, become their decade.
			scalar("decades", "books", "CAST(CAST(m.publishedYear AS INTEGER) / 10 * 10 AS TEXT)",
				"m.publishedYear <> '' AND m.publishedYear NOT GLOB '*[^0-9]*' AND CAST(m.publishedYear AS INTEGER) BETWEEN 1 AND 2999"),
		}
	case "podcast":
		table = "podcasts"
		selects = []string{list("genres", "podcasts", "genres"), list("tags", "podcasts", "tags"), scalar("languages", "podcasts", "m.language", "m.language <> ''")}
	default:
		return Data{}, fmt.Errorf("filter data: unsupported media type %q", mediaType)
	}
	var n counts
	if err := d.Get(ctx, &n, `SELECT COUNT(*) AS items, COALESCE(SUM(li.isMissing = 1 OR li.isInvalid = 1), 0) AS issues
		FROM `+table+` m JOIN libraryItems li ON li.mediaId = m.id WHERE li.libraryId = ?`, libraryID); err != nil {
		return Data{}, fmt.Errorf("filter data: count items: %w", err)
	}
	data.NumIssues = n.Issues
	var pairs []pair
	if err := d.Select(ctx, &pairs, strings.Join(selects, " UNION "), slices.Repeat([]any{libraryID}, len(selects))...); err != nil {
		return Data{}, fmt.Errorf("filter data: values: %w", err)
	}
	menus := map[string][]string{}
	for _, p := range pairs {
		menus[p.Field] = append(menus[p.Field], p.Value)
	}
	menu := func(field string) []string {
		out := menus[field]
		slices.SortFunc(out, foldCompare)
		if out == nil {
			out = []string{}
		}
		return out
	}
	data.Genres, data.Tags, data.Languages = menu("genres"), menu("tags"), menu("languages")
	data.Narrators, data.Publishers, data.PublishedDecades = menu("narrators"), menu("publishers"), menu("decades")
	if mediaType == "podcast" {
		data.PodcastCount = n.Items
		return data, nil
	}
	data.BookCount = n.Items
	var err error
	if data.Authors, err = named(ctx, d, `SELECT id, COALESCE(name, '') AS name FROM authors WHERE libraryId = ?`, libraryID); err != nil {
		return Data{}, err
	}
	if data.Series, err = named(ctx, d, `SELECT id, COALESCE(NULLIF(name, ''), 'No Title') AS name FROM series WHERE libraryId = ?`, libraryID); err != nil {
		return Data{}, err
	}
	data.AuthorCount, data.SeriesCount = len(data.Authors), len(data.Series)
	return data, nil
}

// list selects the distinct text elements of a JSON array column.
func list(field, table, column string) string {
	return `SELECT '` + field + `' AS f, j.value AS v FROM ` + table + ` m JOIN libraryItems li ON li.mediaId = m.id,
		json_each(CASE WHEN json_valid(m.` + column + `) THEN m.` + column + ` ELSE '[]' END) j WHERE li.libraryId = ? AND j.type = 'text'`
}

// scalar selects a per-item value of the rows that satisfy where.
func scalar(field, table, expr, where string) string {
	return `SELECT '` + field + `' AS f, ` + expr + ` AS v FROM ` + table + ` m JOIN libraryItems li ON li.mediaId = m.id WHERE li.libraryId = ? AND ` + where
}

// named reads id/name entries of the library's authors or series, sorted.
func named(ctx context.Context, d *db.DB, query, libraryID string) ([]Named, error) {
	out := []Named{}
	if err := d.Select(ctx, &out, query, libraryID); err != nil {
		return nil, fmt.Errorf("filter data: %w", err)
	}
	slices.SortFunc(out, func(a, b Named) int { return cmp.Or(foldCompare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// foldCompare orders case-insensitively, then by bytes so distinct values stay in a fixed order.
func foldCompare(a, b string) int {
	return cmp.Or(cmp.Compare(strings.ToLower(a), strings.ToLower(b)), cmp.Compare(a, b))
}
