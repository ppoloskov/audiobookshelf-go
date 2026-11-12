package browse

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// The item list is data: filterDefs and sortDefs hold the SQL of every filter
// group and sort key (libraryItemsBookFilters.js, libraryItemsPodcastFilters.js,
// libraryFilters.js) on the media row b and the item row li. A fragment names
// its arguments: :v the decoded filter value, :u the user id, :y and :y9 a
// decade's first and last year, :recent the cutoff of the recent filter.

// Progress predicates on the user's progress row mp of a book. Filters, shelves
// and the series filter all build on these.
const (
	mpFinished   = `mp.isFinished=1`
	mpInProgress = `(mp.currentTime>0 OR mp.ebookProgress>0) AND mp.isFinished=0`
	mpAudio      = `mp.currentTime>0 AND mp.isFinished=0`
	mpStarted    = `NOT ((mp.currentTime IS NULL OR mp.currentTime=0) AND (mp.isFinished IS NULL OR mp.isFinished=0))`
)

// hasProgress is an AND clause on the user's progress row of book b. A book
// without a row has none of the conditions, so "not finished" and "not
// started" (negated) include it.
func hasProgress(negate bool, cond string) string {
	not := ""
	if negate {
		not = "NOT "
	}
	return ` AND ` + not + `EXISTS (SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=:u AND (` + cond + `))`
}

const (
	issuesSQL   = ` AND (li.isMissing=1 OR li.isInvalid=1)`
	noSeriesSQL = ` AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id)`
	noAudioSQL  = ` AND json_array_length(b.audioFiles)=0`
)

func jsonHas(column string) string {
	return ` AND EXISTS (SELECT 1 FROM json_each(b.` + column + `) WHERE json_valid(b.` + column + `) AND json_each.value=:v)`
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

type filterDef struct {
	podcast bool              // podcast libraries filter on it too
	valued  bool              // "group.value"; podcast filters split only these at the dot
	sql     string            // clause for any value
	values  map[string]string // clause per value, when sql is empty
	other   string            // clause for a value that is not in values
	strict  bool              // an unlisted value is an error, not "no filter"
}

var filterDefs = map[string]filterDef{
	"issues":    {podcast: true, sql: issuesSQL},
	"recent":    {podcast: true, sql: ` AND li.createdAt>=:recent`},
	"feed-open": {podcast: true, sql: ` AND EXISTS (SELECT 1 FROM feeds f WHERE f.entityType='libraryItem' AND f.entityId=li.id)`},
	"explicit":  {podcast: true, sql: ` AND b.explicit=1`},
	"genres":    {podcast: true, valued: true, sql: jsonHas("genres")},
	"tags":      {podcast: true, valued: true, sql: jsonHas("tags")},
	"languages": {podcast: true, valued: true, sql: ` AND b.language=:v`},

	"share-open": {sql: ` AND EXISTS (SELECT 1 FROM mediaItemShares ms WHERE ms.mediaItemType='book' AND ms.mediaItemId=b.id)`},
	"abridged":   {sql: ` AND b.abridged=1`},
	"narrators":  {valued: true, sql: ` AND EXISTS (SELECT 1 FROM json_each(b.narrators) WHERE json_each.value=:v)`},
	"publishers": {valued: true, sql: ` AND b.publisher=:v`},
	"authors":    {valued: true, sql: ` AND EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId=:v)`},
	"series": {valued: true, values: map[string]string{"no-series": noSeriesSQL},
		other: ` AND EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=:v)`},
	"progress": {valued: true, values: map[string]string{
		"not-finished":      hasProgress(true, mpFinished),
		"not-started":       hasProgress(true, mpStarted),
		"finished":          hasProgress(false, mpFinished),
		"in-progress":       hasProgress(false, mpInProgress),
		"audio-in-progress": hasProgress(false, mpAudio),
		"ebook-in-progress": noAudioSQL + hasProgress(false, `mp.ebookProgress>0 AND mp.isFinished=0`),
		"ebook-finished":    noAudioSQL + ` AND b.ebookFile IS NOT NULL` + hasProgress(false, mpFinished),
	}},
	"tracks": {valued: true, values: map[string]string{"none": noAudioSQL, "multi": ` AND json_array_length(b.audioFiles)>1`},
		other: ` AND json_array_length(b.audioFiles)=1`},
	"ebooks": {valued: true, strict: true, values: map[string]string{
		"ebook":            ` AND b.ebookFile IS NOT NULL AND b.ebookFile!='null'`,
		"no-ebook":         ` AND (b.ebookFile IS NULL OR b.ebookFile='null')`,
		"supplementary":    ` AND li.libraryFiles LIKE '%"isSupplementary":true%'`,
		"no-supplementary": ` AND li.libraryFiles NOT LIKE '%"isSupplementary":true%'`,
	}},
	"publishedDecades": {valued: true, sql: ` AND CAST(b.publishedYear AS INTEGER) BETWEEN :y AND :y9`},
	"missing":          {valued: true, strict: true, values: missingSQL()},
}

type sortDef struct {
	expr     string // ORDER BY expression
	books    string // expression for book libraries when it differs
	prefixed string // expression when the server ignores title prefixes
	bookOnly bool   // podcast libraries add no ORDER BY
	nulls    string // "flip": NULLS LAST ascending, FIRST descending; "last": always last
}

func progressSort(column string) sortDef {
	return sortDef{expr: `(SELECT mp.` + column + ` FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=:u LIMIT 1)`, bookOnly: true, nulls: "last"}
}

var sortDefs = map[string]sortDef{
	"addedAt":                      {expr: "li.createdAt"},
	"size":                         {expr: "li.size"},
	"birthtimeMs":                  {expr: "li.birthtime"},
	"mtimeMs":                      {expr: "li.mtime"},
	"media.numTracks":              {expr: "b.numEpisodes", books: "json_array_length(b.audioFiles)"},
	"random":                       {expr: "RANDOM()"},
	"media.metadata.author":        {expr: "b.author COLLATE NOCASE", nulls: "flip"},
	"media.metadata.title":         {expr: "li.title COLLATE NOCASE", prefixed: "li.titleIgnorePrefix COLLATE NOCASE"},
	"media.duration":               {expr: "b.duration", bookOnly: true},
	"media.metadata.publishedYear": {expr: "CAST(b.publishedYear AS INTEGER)", bookOnly: true},
	"media.metadata.authorName":    {expr: "li.authorNamesFirstLast COLLATE NOCASE", bookOnly: true},
	"media.metadata.authorNameLF":  {expr: "li.authorNamesLastFirst COLLATE NOCASE", bookOnly: true},
	"sequence":                     {expr: `CAST((SELECT bs.sequence FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=:v LIMIT 1) AS FLOAT)`, bookOnly: true, nulls: "flip"},
	"progress":                     progressSort("updatedAt"),
	"progress.updatedAt":           progressSort("updatedAt"),
	"progress.createdAt":           progressSort("createdAt"),
	"progress.finishedAt":          progressSort("finishedAt"),
}

// bind replaces the named placeholders of a fragment by ? and returns the
// arguments in order. A colon that does not start a known name is left alone
// (the isSupplementary pattern contains one).
func bind(fragment, value string, user *auth.Principal) (string, []any, error) {
	var out strings.Builder
	var args []any
	for i := 0; i < len(fragment); i++ {
		if fragment[i] != ':' {
			out.WriteByte(fragment[i])
			continue
		}
		name := fragment[i+1:]
		name = name[:len(name)-len(strings.TrimLeft(name, "abcdefghijklmnopqrstuvwxyz9"))]
		var arg any
		switch name {
		case "v":
			arg = value
		case "u":
			arg = user.ID
		case "recent":
			arg = db.FromMillis(time.Now().Add(-recentWindow).UnixMilli())
		case "y", "y9":
			year, err := strconv.Atoi(value)
			if err != nil {
				return "", nil, httpx.Error(http.StatusNotImplemented, "unsupported publishedDecades filter")
			}
			if name == "y9" {
				year += 9
			}
			arg = year
		default:
			out.WriteByte(':')
			continue
		}
		out.WriteByte('?')
		args = append(args, arg)
		i += len(name)
	}
	return out.String(), args, nil
}

// recentWindow is how far back "recent" and the recently-added shelves reach.
const recentWindow = 60 * 24 * time.Hour

// filterSQL is the AND clause of a "group.value" filter on the media row b and
// item row li. Podcast libraries ignore the groups they do not support; book
// libraries answer 501 for an unknown group or value of a strict group.
func filterSQL(raw string, podcast bool, user *auth.Principal) (string, []any, error) {
	group, value, hasValue := strings.Cut(raw, ".")
	// Only the valued groups split at the dot for podcasts (libraryFilters.js:35).
	if podcast && !(hasValue && filterDefs[group].valued) {
		group, value, hasValue = raw, "", false
	}
	if hasValue {
		value = decodeValue(value)
	}
	def, known := filterDefs[group]
	if group == "" || (podcast && !(known && def.podcast)) {
		return "", nil, nil
	}
	if !known {
		return "", nil, httpx.Error(http.StatusNotImplemented, "unsupported filter group")
	}
	clause := def.sql
	if clause == "" {
		var ok bool
		if clause, ok = def.values[value]; !ok {
			clause = def.other
		}
		if clause == "" && def.strict {
			return "", nil, httpx.Error(http.StatusNotImplemented, "unsupported "+group+" filter")
		}
	}
	if clause == "" {
		return "", nil, nil
	}
	return bind(clause, value, user)
}

// orderSQL is the ORDER BY clause of a sort key. A book library answers 400
// for an unknown key; a podcast library adds no ORDER BY for one.
func orderSQL(sortBy, filter string, desc, ignorePrefix, podcast bool, user *auth.Principal) (string, []any, error) {
	if sortBy == "" {
		return "", nil, nil
	}
	value := ""
	if rest, ok := strings.CutPrefix(filter, "series."); ok {
		value = decodeValue(rest)
	} else if sortBy == "sequence" && !podcast {
		sortBy = "media.metadata.title"
	}
	def, ok := sortDefs[sortBy]
	if !ok && !podcast {
		return "", nil, httpx.BadRequest("unsupported sort field")
	}
	if !ok || (podcast && def.bookOnly) {
		return "", nil, nil
	}
	expr := def.expr
	switch {
	case ignorePrefix && def.prefixed != "":
		expr = def.prefixed
	case !podcast && def.books != "":
		expr = def.books
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

// decodeValue decodes a filter value: base64, standard or URL-safe alphabet,
// with or without padding (web client and apps). Anything else is "".
func decodeValue(text string) string {
	text = strings.TrimRight(text, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(text); err == nil {
			return string(decoded)
		}
	}
	return ""
}

// encodeValue is the inverse of decodeValue for the standard alphabet.
func encodeValue(text string) string { return base64.StdEncoding.EncodeToString([]byte(text)) }
