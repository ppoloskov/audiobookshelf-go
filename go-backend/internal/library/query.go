package library

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Library item list queries are data. filterDefs and sortDefs hold the SQL of
// every filter group and sort key of libraryItemsBookFilters.js,
// libraryItemsPodcastFilters.js and libraryFilters.js; filterSQL and orderSQL
// compile a request against them. In a clause "$v" binds the decoded filter
// value, "$u" the user id, "$y"/"$y9" a decade's first/last year and
// "$recent" the cutoff of the recent filter.

type filterDef struct {
	podcast bool              // podcast libraries filter on it too
	valued  bool              // "group.value"; podcast filters split only these at the dot
	sql     string            // clause for any value
	values  map[string]string // clause per value, when sql is empty
	other   string            // clause for a value that is not in values
	strict  bool              // an unlisted value is an error, not "no filter"
}

const (
	issuesSQL   = ` AND (li.isMissing=1 OR li.isInvalid=1)`
	noSeriesSQL = ` AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id)`
)

func jsonHas(column string) string {
	return ` AND EXISTS (SELECT 1 FROM json_each(b.` + column + `) WHERE json_valid(b.` + column + `) AND json_each.value=$v)`
}

// progressSQL is one of getMediaGroupQuery's progress conditions on the user's
// single progress row; a missing row reads as NULL, so "not finished" and "not
// started" include items without one.
func progressSQL(not, cond string) string {
	return ` AND ` + not + `EXISTS (SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=$u AND (` + cond + `))`
}

func missingSQL() map[string]string {
	values := map[string]string{"authors": ` AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id)`, "series": noSeriesSQL, "issues": issuesSQL, "cover": ` AND (b.coverPath IS NULL OR b.coverPath='')`}
	for _, c := range []string{"asin", "isbn", "subtitle", "publishedYear", "description", "publisher", "language"} {
		values[c] = ` AND (b.` + c + ` IS NULL OR b.` + c + `='')`
	}
	for _, c := range []string{"genres", "tags", "narrators", "chapters"} {
		values[c] = ` AND (b.` + c + ` IS NULL OR json_array_length(b.` + c + `)=0)`
	}
	return values
}

var filterDefs = map[string]filterDef{
	"issues":    {podcast: true, sql: issuesSQL},
	"recent":    {podcast: true, sql: ` AND li.createdAt>=$recent`},
	"feed-open": {podcast: true, sql: ` AND EXISTS (SELECT 1 FROM feeds f WHERE f.entityType='libraryItem' AND f.entityId=li.id)`},
	"explicit":  {podcast: true, sql: ` AND b.explicit=1`},
	"genres":    {podcast: true, valued: true, sql: jsonHas("genres")},
	"tags":      {podcast: true, valued: true, sql: jsonHas("tags")},
	"languages": {podcast: true, valued: true, sql: ` AND b.language=$v`},

	"share-open": {sql: ` AND EXISTS (SELECT 1 FROM mediaItemShares ms WHERE ms.mediaItemType='book' AND ms.mediaItemId=b.id)`},
	"abridged":   {sql: ` AND b.abridged=1`},
	"narrators":  {valued: true, sql: ` AND EXISTS (SELECT 1 FROM json_each(b.narrators) WHERE json_each.value=$v)`},
	"publishers": {valued: true, sql: ` AND b.publisher=$v`},
	"authors":    {valued: true, sql: ` AND EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId=$v)`},
	"series": {valued: true, values: map[string]string{"no-series": noSeriesSQL},
		other: ` AND EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=$v)`},
	"progress": {valued: true, values: map[string]string{
		"not-finished":      progressSQL("NOT ", `mp.isFinished=1`),
		"not-started":       progressSQL("NOT ", `NOT ((mp.currentTime IS NULL OR mp.currentTime=0) AND (mp.isFinished IS NULL OR mp.isFinished=0))`),
		"finished":          progressSQL("", `mp.isFinished=1`),
		"in-progress":       progressSQL("", `(mp.currentTime>0 OR mp.ebookProgress>0) AND mp.isFinished=0`),
		"audio-in-progress": progressSQL("", `mp.currentTime>0 AND mp.isFinished=0`),
		"ebook-in-progress": ` AND json_array_length(b.audioFiles)=0` + progressSQL("", `mp.ebookProgress>0 AND mp.isFinished=0`),
		"ebook-finished":    ` AND json_array_length(b.audioFiles)=0 AND b.ebookFile IS NOT NULL` + progressSQL("", `mp.isFinished=1`),
	}},
	"tracks": {valued: true, values: map[string]string{"none": ` AND json_array_length(b.audioFiles)=0`, "multi": ` AND json_array_length(b.audioFiles)>1`},
		other: ` AND json_array_length(b.audioFiles)=1`},
	"ebooks": {valued: true, strict: true, values: map[string]string{
		"ebook":            ` AND b.ebookFile IS NOT NULL AND b.ebookFile!='null'`,
		"no-ebook":         ` AND (b.ebookFile IS NULL OR b.ebookFile='null')`,
		"supplementary":    ` AND li.libraryFiles LIKE '%"isSupplementary":true%'`,
		"no-supplementary": ` AND li.libraryFiles NOT LIKE '%"isSupplementary":true%'`,
	}},
	"publishedDecades": {valued: true, sql: ` AND CAST(b.publishedYear AS INTEGER) BETWEEN $y AND $y9`},
	"missing":          {valued: true, strict: true, values: missingSQL()},
}

type sortDef struct {
	expr     string // ORDER BY expression
	prefixed string // expression when the server ignores title prefixes
	bookOnly bool   // podcast libraries add no ORDER BY
	nulls    string // "flip": NULLS LAST ascending, FIRST descending; "last": always last
}

func progressSort(field string) sortDef {
	return sortDef{expr: `(SELECT mp.` + field + ` FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=$u LIMIT 1)`, bookOnly: true, nulls: "last"}
}

var sortDefs = map[string]sortDef{
	"addedAt":                      {expr: "li.createdAt"},
	"size":                         {expr: "li.size"},
	"birthtimeMs":                  {expr: "li.birthtime"},
	"mtimeMs":                      {expr: "li.mtime"},
	"media.numTracks":              {expr: "b.numEpisodes"},
	"random":                       {expr: "RANDOM()"},
	"media.metadata.author":        {expr: "b.author COLLATE NOCASE", nulls: "flip"},
	"media.metadata.title":         {expr: "li.title COLLATE NOCASE", prefixed: "li.titleIgnorePrefix COLLATE NOCASE"},
	"media.duration":               {expr: "b.duration", bookOnly: true},
	"media.metadata.publishedYear": {expr: "CAST(b.publishedYear AS INTEGER)", bookOnly: true},
	"media.metadata.authorName":    {expr: "li.authorNamesFirstLast COLLATE NOCASE", bookOnly: true},
	"media.metadata.authorNameLF":  {expr: "li.authorNamesLastFirst COLLATE NOCASE", bookOnly: true},
	"sequence":                     {expr: `CAST((SELECT bs.sequence FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=$v LIMIT 1) AS FLOAT)`, bookOnly: true, nulls: "flip"},
	"progress":                     progressSort("updatedAt"),
	"progress.updatedAt":           progressSort("updatedAt"),
	"progress.createdAt":           progressSort("createdAt"),
	"progress.finishedAt":          progressSort("finishedAt"),
}

var markers = regexp.MustCompile(`\$(?:v|u|y9|y|recent)`)

func bind(clause, value string, user store.User) (string, []any, error) {
	var args []any
	var err error
	out := markers.ReplaceAllStringFunc(clause, func(marker string) string {
		switch marker {
		case "$v":
			args = append(args, value)
		case "$u":
			args = append(args, user.ID)
		case "$recent":
			args = append(args, dbtime.Format(time.Now().Add(-60*24*time.Hour)))
		default:
			year, e := strconv.Atoi(value)
			err = errors.Join(err, e)
			if marker == "$y9" {
				year += 9
			}
			args = append(args, year)
		}
		return "?"
	})
	return out, args, err
}

// filterSQL is the AND clause of a "group.value" filter on the media row b and
// item row li. Podcast libraries ignore the groups they do not support; book
// libraries reject an unknown group.
func FilterSQL(raw string, podcast bool, user store.User) (string, []any, error) {
	group, value, hasValue := strings.Cut(raw, ".")
	// LibraryFilters splits only the valued groups at the dot for podcasts
	// (libraryFilters.js:35); anything else is taken whole.
	if podcast && !(hasValue && filterDefs[group].valued) {
		group, value, hasValue = raw, "", false
	}
	if hasValue {
		value = decodeFilterValue(value)
	}
	def, known := filterDefs[group]
	if group == "" || (podcast && !(known && def.podcast)) {
		return "", nil, nil
	}
	if !known {
		return "", nil, fmt.Errorf("unsupported filter group %q", group)
	}
	clause := def.sql
	if clause == "" {
		var ok bool
		if clause, ok = def.values[value]; !ok {
			clause = def.other
		}
		if clause == "" && def.strict {
			return "", nil, fmt.Errorf("unsupported %s filter", group)
		}
	}
	if clause == "" {
		return "", nil, nil
	}
	return bind(clause, value, user)
}

// orderSQL is the ORDER BY clause of a sort key. A book library rejects an
// unknown key; a podcast library adds no ORDER BY for one.
func OrderSQL(sortBy, filter string, desc, ignorePrefix, podcast bool, user store.User) (string, []any, error) {
	if sortBy == "" {
		return "", nil, nil
	}
	value := ""
	if strings.HasPrefix(filter, "series.") {
		_, value, _ = strings.Cut(filter, ".")
		value = decodeFilterValue(value)
	} else if sortBy == "sequence" && !podcast {
		sortBy = "media.metadata.title"
	}
	def, ok := sortDefs[sortBy]
	if !ok && !podcast {
		return "", nil, errors.New("unsupported sort field")
	}
	if !ok || (podcast && def.bookOnly) {
		return "", nil, nil
	}
	expr := def.expr
	if ignorePrefix && def.prefixed != "" {
		expr = def.prefixed
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	switch def.nulls {
	case "flip":
		dir += map[bool]string{false: " NULLS LAST", true: " NULLS FIRST"}[desc]
	case "last":
		dir += " NULLS LAST"
	}
	clause, args, err := bind(expr, value, user)
	return " ORDER BY " + clause + " " + dir, args, err
}

// decodeFilterValue is libraryFilters.decode (libraryFilters.js:13-20):
// Buffer.from(decodeURIComponent(text), 'base64').toString(). Node's decoder
// accepts both alphabets and skips characters outside them. Every filter
// value goes through it, as upstream does.
func decodeFilterValue(text string) string {
	var clean strings.Builder
	for _, c := range text {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
			clean.WriteRune(c)
		case c == '-':
			clean.WriteByte('+')
		case c == '_':
			clean.WriteByte('/')
		case c == '=':
			// Decoding stops at the first padding character.
			decoded, _ := base64.RawStdEncoding.DecodeString(trimPartial(clean.String()))
			return string(decoded)
		}
	}
	decoded, _ := base64.RawStdEncoding.DecodeString(trimPartial(clean.String()))
	return string(decoded)
}

func trimPartial(s string) string {
	if len(s)%4 == 1 {
		return s[:len(s)-1]
	}
	return s
}

// DecodeFilterValue is decodeFilterValue for callers that parse a filter.
func DecodeFilterValue(text string) string { return decodeFilterValue(text) }
