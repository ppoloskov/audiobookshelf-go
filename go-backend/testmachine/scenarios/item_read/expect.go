package item_read

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// object is a JSON object under construction.
type object = map[string]any

// dbTime converts a SQLite DATETIME column ("2026-11-03 12:46:19.714 +00:00")
// to epoch milliseconds, the way upstream's Date.valueOf() serialises it.
// A NULL column gives nil.
func dbTime(t *testing.T, value any) any {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		return nil
	}
	parsed, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("parse database time %q: %v", text, err)
	}
	return parsed.UnixMilli()
}

// dbJSON decodes a JSON column; a NULL or empty column gives nil.
func dbJSON(t *testing.T, value any) any {
	t.Helper()
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode JSON column %q: %v", text, err)
	}
	return out
}

// dbList decodes a JSON array column, mapping NULL to an empty array
// (upstream copies `[...(this.tags || [])]`).
func dbList(t *testing.T, value any) []any {
	t.Helper()
	list, _ := dbJSON(t, value).([]any)
	if list == nil {
		return []any{}
	}
	return list
}

// dbBool converts a TINYINT column to a bool.
func dbBool(value any) bool {
	number, _ := value.(int64)
	return number != 0
}

// firstRow runs a query that must return exactly one row.
func firstRow(t *testing.T, e *harness.Env, query string, args ...any) map[string]any {
	t.Helper()
	rows := e.Rows(t, query, args...)
	if len(rows) != 1 {
		t.Fatalf("query %q %v returned %d rows, want 1", query, args, len(rows))
	}
	return rows[0]
}

// sortingPrefixes are the server's default sorting prefixes (ServerSettings.sortingPrefixes).
var sortingPrefixes = []string{"the", "a", "an"}

// titleWithPrefixAtEnd mirrors utils/index.js getTitlePrefixAtEnd ("The Book" gives "Book, The").
func titleWithPrefixAtEnd(title string) string {
	for _, prefix := range sortingPrefixes {
		if strings.HasPrefix(strings.ToLower(title), prefix+" ") {
			return title[len(prefix)+1:] + ", " + strings.ToUpper(prefix[:1]) + prefix[1:]
		}
	}
	return title
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// fileTypes maps lower-case extensions to LibraryFile.fileType (utils/globals.js).
var fileTypes = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".webp": "image",
	".m4b": "audio", ".mp3": "audio", ".m4a": "audio", ".flac": "audio", ".opus": "audio", ".ogg": "audio", ".oga": "audio",
	".mp4": "audio", ".aac": "audio", ".wma": "audio", ".aiff": "audio", ".aif": "audio", ".wav": "audio", ".webm": "audio",
	".webma": "audio", ".mka": "audio", ".awb": "audio", ".caf": "audio", ".mpg": "audio", ".mpeg": "audio",
	".epub": "ebook", ".pdf": "ebook", ".mobi": "ebook", ".azw3": "ebook", ".cbr": "ebook", ".cbz": "ebook",
	".txt": "text", ".nfo": "text",
	".opf": "metadata", ".abs": "metadata", ".xml": "metadata", ".json": "metadata",
}

// libraryFiles builds LibraryFile.toJSON() for every file of the item.
func libraryFiles(t *testing.T, row map[string]any) []any {
	t.Helper()
	files := dbList(t, row["libraryFiles"])
	out := make([]any, 0, len(files))
	for _, raw := range files {
		file, _ := raw.(map[string]any)
		metadata, _ := file["metadata"].(map[string]any)
		ext, _ := metadata["ext"].(string)
		fileType, ok := fileTypes[strings.ToLower(ext)]
		if !ok {
			fileType = "unknown"
		}
		supplementary := file["isSupplementary"] // absent means null
		out = append(out, object{
			"ino":             file["ino"],
			"metadata":        metadata,
			"isSupplementary": supplementary,
			"addedAt":         file["addedAt"],
			"updatedAt":       file["updatedAt"],
			"fileType":        fileType,
		})
	}
	return out
}

// itemCommon holds the keys shared by toOldJSON and toOldJSONMinified.
func itemCommon(t *testing.T, row map[string]any) object {
	t.Helper()
	extra, _ := dbJSON(t, row["extraData"]).(map[string]any)
	oldID := extra["oldLibraryItemId"]
	if oldID == nil || oldID == "" {
		oldID = nil
	}
	doc := object{
		"id": row["id"], "ino": row["ino"], "oldLibraryItemId": oldID,
		"libraryId": row["libraryId"], "folderId": row["libraryFolderId"],
		"path": row["path"], "relPath": row["relPath"], "isFile": dbBool(row["isFile"]),
		"addedAt": dbTime(t, row["createdAt"]), "updatedAt": dbTime(t, row["updatedAt"]),
		"isMissing": dbBool(row["isMissing"]), "isInvalid": dbBool(row["isInvalid"]),
		"mediaType": row["mediaType"],
	}
	// undefined values are omitted from the JSON
	for key, column := range map[string]string{"mtimeMs": "mtime", "ctimeMs": "ctime", "birthtimeMs": "birthtime"} {
		if value := dbTime(t, row[column]); value != nil {
			doc[key] = value
		}
	}
	return doc
}

// scanFields adds lastScan and scanVersion (omitted while NULL).
func scanFields(t *testing.T, row map[string]any, doc object) {
	t.Helper()
	if value := dbTime(t, row["lastScan"]); value != nil {
		doc["lastScan"] = value
	}
	if value, ok := row["lastScanVersion"].(string); ok {
		doc["scanVersion"] = value
	} else {
		doc["scanVersion"] = nil
	}
}

// itemRow loads the libraryItems row.
func itemRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	return firstRow(t, e, "SELECT * FROM libraryItems WHERE id = ?", id)
}

// expandedItem is the document of GET /api/items/:id?expanded=1 derived from the database.
func expandedItem(t *testing.T, e *harness.Env, id string) object {
	t.Helper()
	row := itemRow(t, e, id)
	doc := itemCommon(t, row)
	doc["numFiles"] = len(dbList(t, row["libraryFiles"]))
	doc["size"] = row["size"]
	scanFields(t, row, doc)
	doc["libraryFiles"] = libraryFiles(t, row)
	if row["mediaType"] == "podcast" {
		doc["media"] = podcastExpanded(t, e, row)
	} else {
		doc["media"] = bookExpanded(t, e, row)
	}
	return doc
}

// plainItem is the document of GET /api/items/:id without expanded derived from the database.
func plainItem(t *testing.T, e *harness.Env, id string) object {
	t.Helper()
	row := itemRow(t, e, id)
	doc := itemCommon(t, row)
	scanFields(t, row, doc)
	doc["libraryFiles"] = libraryFiles(t, row)
	if row["mediaType"] == "podcast" {
		doc["media"] = podcastPlain(t, e, row)
	} else {
		doc["media"] = bookPlain(t, e, row)
	}
	return doc
}

// bookData is the database state behind one book.
type bookData struct {
	row     map[string]any
	itemID  any
	authors []map[string]any
	series  []map[string]any
	audio   []any
}

func loadBook(t *testing.T, e *harness.Env, item map[string]any) bookData {
	t.Helper()
	book := firstRow(t, e, "SELECT * FROM books WHERE id = ?", item["mediaId"])
	return bookData{
		row:     book,
		itemID:  item["id"],
		authors: e.Rows(t, "SELECT a.id, a.name, a.lastFirst FROM authors a JOIN bookAuthors ba ON ba.authorId = a.id WHERE ba.bookId = ? ORDER BY ba.createdAt ASC", book["id"]),
		series:  e.Rows(t, "SELECT s.id, s.name, bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId = s.id WHERE bs.bookId = ? ORDER BY bs.createdAt ASC", book["id"]),
		audio:   dbList(t, book["audioFiles"]),
	}
}

func (b bookData) included() []map[string]any {
	var out []map[string]any
	for _, raw := range b.audio {
		file, _ := raw.(map[string]any)
		if exclude, _ := file["exclude"].(bool); !exclude {
			out = append(out, file)
		}
	}
	return out
}

// metadataPlain is Book.oldMetadataToJSON.
func (b bookData) metadataPlain(t *testing.T) object {
	t.Helper()
	authors := []any{}
	for _, author := range b.authors {
		authors = append(authors, object{"id": author["id"], "name": author["name"]})
	}
	series := []any{}
	for _, one := range b.series {
		series = append(series, object{"id": one["id"], "name": one["name"], "sequence": one["sequence"]})
	}
	return object{
		"title": b.row["title"], "subtitle": b.row["subtitle"], "authors": authors,
		"narrators": dbList(t, b.row["narrators"]), "series": series, "genres": dbList(t, b.row["genres"]),
		"publishedYear": b.row["publishedYear"], "publishedDate": b.row["publishedDate"], "publisher": b.row["publisher"],
		"description": b.row["description"], "isbn": b.row["isbn"], "asin": b.row["asin"], "language": b.row["language"],
		"explicit": dbBool(b.row["explicit"]), "abridged": dbBool(b.row["abridged"]),
	}
}

func (b bookData) authorName() string {
	names := make([]string, len(b.authors))
	for i, author := range b.authors {
		names[i], _ = author["name"].(string)
	}
	return strings.Join(names, ", ")
}

// authorNameLF joins the stored "Last, First" names (equal to nameToLastFirst for two-word names).
func (b bookData) authorNameLF() string {
	names := make([]string, len(b.authors))
	for i, author := range b.authors {
		names[i], _ = author["lastFirst"].(string)
	}
	return strings.Join(names, ", ")
}

func (b bookData) seriesName() string {
	names := make([]string, len(b.series))
	for i, one := range b.series {
		name, _ := one["name"].(string)
		if sequence, _ := one["sequence"].(string); sequence != "" {
			name += " #" + sequence
		}
		names[i] = name
	}
	return strings.Join(names, ", ")
}

func (b bookData) narratorName(t *testing.T) string {
	t.Helper()
	var names []string
	for _, narrator := range dbList(t, b.row["narrators"]) {
		name, _ := narrator.(string)
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// metadataExpanded is Book.oldMetadataToJSONExpanded.
func (b bookData) metadataExpanded(t *testing.T) object {
	t.Helper()
	doc := b.metadataPlain(t)
	title, _ := b.row["title"].(string)
	doc["titleIgnorePrefix"] = titleWithPrefixAtEnd(title)
	doc["authorName"] = b.authorName()
	doc["authorNameLF"] = b.authorNameLF()
	doc["narratorName"] = b.narratorName(t)
	doc["seriesName"] = b.seriesName()
	if description, _ := b.row["description"].(string); description != "" {
		doc["descriptionPlain"] = htmlTag.ReplaceAllString(description, "")
	} else {
		doc["descriptionPlain"] = nil
	}
	return doc
}

func (b bookData) size() int64 {
	var total float64
	for _, raw := range b.audio {
		file, _ := raw.(map[string]any)
		metadata, _ := file["metadata"].(map[string]any)
		size, _ := metadata["size"].(float64)
		total += size
	}
	return int64(total)
}

func (b bookData) ebook(t *testing.T) map[string]any {
	t.Helper()
	ebook, _ := dbJSON(t, b.row["ebookFile"]).(map[string]any)
	return ebook
}

// tracks is Book.getTracklist.
func (b bookData) tracks() []any {
	out := []any{}
	offset := 0.0
	for _, file := range b.included() {
		track := object{}
		for key, value := range file {
			track[key] = value
		}
		metadata, _ := file["metadata"].(map[string]any)
		track["title"] = metadata["filename"]
		track["startOffset"] = offset
		track["contentUrl"] = "/api/items/" + b.itemID.(string) + "/file/" + file["ino"].(string)
		duration, _ := file["duration"].(float64)
		offset += duration
		out = append(out, track)
	}
	return out
}

func (b bookData) chapters(t *testing.T) any {
	t.Helper()
	return dbJSON(t, b.row["chapters"])
}

func bookPlain(t *testing.T, e *harness.Env, item map[string]any) object {
	t.Helper()
	b := loadBook(t, e, item)
	ebook := b.ebook(t)
	var ebookValue any
	if ebook != nil {
		ebookValue = ebook
	}
	return object{
		"id": b.row["id"], "libraryItemId": item["id"], "metadata": b.metadataPlain(t), "coverPath": b.row["coverPath"],
		"tags": dbList(t, b.row["tags"]), "audioFiles": b.audio, "chapters": b.chapters(t), "ebookFile": ebookValue,
	}
}

func bookExpanded(t *testing.T, e *harness.Env, item map[string]any) object {
	t.Helper()
	b := loadBook(t, e, item)
	size := b.size()
	ebook := b.ebook(t)
	var ebookValue any
	if ebook != nil {
		ebookValue = ebook
		metadata, _ := ebook["metadata"].(map[string]any)
		ebookSize, _ := metadata["size"].(float64)
		size += int64(ebookSize)
	}
	chapters, _ := b.chapters(t).([]any)
	doc := object{
		"id": b.row["id"], "libraryItemId": item["id"], "metadata": b.metadataExpanded(t), "coverPath": b.row["coverPath"],
		"tags": dbList(t, b.row["tags"]), "numTracks": len(b.included()), "numAudioFiles": len(b.audio), "numChapters": len(chapters),
		"duration": b.row["duration"], "size": size,
		"audioFiles": b.audio, "chapters": b.chapters(t), "ebookFile": ebookValue, "tracks": b.tracks(),
	}
	if format, ok := ebook["ebookFormat"]; ok {
		doc["ebookFormat"] = format
	}
	return doc
}

// podcastData is the database state behind one podcast.
type podcastData struct {
	row      map[string]any
	itemID   any
	episodes []map[string]any
}

func loadPodcast(t *testing.T, e *harness.Env, item map[string]any) podcastData {
	t.Helper()
	return podcastData{
		row:      firstRow(t, e, "SELECT * FROM podcasts WHERE id = ?", item["mediaId"]),
		itemID:   item["id"],
		episodes: e.Rows(t, "SELECT * FROM podcastEpisodes WHERE podcastId = ? ORDER BY id", item["mediaId"]),
	}
}

func (p podcastData) metadataPlain(t *testing.T) object {
	t.Helper()
	return object{
		"title": p.row["title"], "author": p.row["author"], "description": p.row["description"], "releaseDate": p.row["releaseDate"],
		"genres": dbList(t, p.row["genres"]), "feedUrl": p.row["feedURL"], "imageUrl": p.row["imageURL"],
		"itunesPageUrl": p.row["itunesPageURL"], "itunesId": p.row["itunesId"], "itunesArtistId": p.row["itunesArtistId"],
		"explicit": dbBool(p.row["explicit"]), "language": p.row["language"], "type": p.row["podcastType"],
	}
}

// settings returns the podcast keys shared by the plain and minified shapes.
func (p podcastData) settings(t *testing.T) object {
	t.Helper()
	var lastCheck any // `lastEpisodeCheck?.valueOf() || null`: epoch 0 also gives null
	if ms, _ := dbTime(t, p.row["lastEpisodeCheck"]).(int64); ms != 0 {
		lastCheck = ms
	}
	return object{
		"id": p.row["id"], "coverPath": p.row["coverPath"], "tags": dbList(t, p.row["tags"]),
		"autoDownloadEpisodes": dbBool(p.row["autoDownloadEpisodes"]), "autoDownloadSchedule": p.row["autoDownloadSchedule"],
		"lastEpisodeCheck": lastCheck, "maxEpisodesToKeep": p.row["maxEpisodesToKeep"], "maxNewEpisodesToDownload": p.row["maxNewEpisodesToDownload"],
	}
}

func (p podcastData) episodePlain(t *testing.T) []any {
	t.Helper()
	out := []any{}
	for _, row := range p.episodes {
		out = append(out, episodePlain(t, p.itemID, row))
	}
	return out
}

func podcastPlain(t *testing.T, e *harness.Env, item map[string]any) object {
	t.Helper()
	p := loadPodcast(t, e, item)
	doc := p.settings(t)
	doc["libraryItemId"] = item["id"]
	doc["metadata"] = p.metadataPlain(t)
	doc["episodes"] = p.episodePlain(t)
	return doc
}

func podcastExpanded(t *testing.T, e *harness.Env, item map[string]any) object {
	t.Helper()
	p := loadPodcast(t, e, item)
	doc := p.settings(t)
	metadata := p.metadataPlain(t)
	title, _ := p.row["title"].(string)
	metadata["titleIgnorePrefix"] = titleWithPrefixAtEnd(title)
	var size float64
	episodes := []any{}
	for _, row := range p.episodes {
		episode := episodePlain(t, p.itemID, row)
		audioFile, _ := episode["audioFile"].(map[string]any)
		fileMetadata, _ := audioFile["metadata"].(map[string]any)
		episodeSize, _ := fileMetadata["size"].(float64)
		duration, _ := audioFile["duration"].(float64)
		track := object{}
		for key, value := range audioFile {
			track[key] = value
		}
		track["startOffset"] = 0
		track["title"] = fileMetadata["filename"]
		track["index"] = 1
		track["contentUrl"] = "/api/items/" + item["id"].(string) + "/file/" + audioFile["ino"].(string)
		episode["audioTrack"] = track
		episode["size"] = episodeSize
		episode["duration"] = duration
		size += episodeSize
		episodes = append(episodes, episode)
	}
	doc["libraryItemId"] = item["id"]
	doc["metadata"] = metadata
	doc["numEpisodes"] = len(p.episodes)
	doc["size"] = size
	doc["episodes"] = episodes
	return doc
}

// episodePlain is PodcastEpisode.toOldJSON.
func episodePlain(t *testing.T, itemID any, row map[string]any) object {
	t.Helper()
	extra, _ := dbJSON(t, row["extraData"]).(map[string]any)
	var enclosure any
	if url, _ := row["enclosureURL"].(string); url != "" {
		var length any
		if size, ok := row["enclosureSize"].(int64); ok {
			length = strconv.FormatInt(size, 10) // enclosure.length is a string
		}
		enclosure = object{"url": url, "type": row["enclosureType"], "length": length}
	}
	oldEpisodeID := extra["oldEpisodeId"]
	if oldEpisodeID == "" {
		oldEpisodeID = nil
	}
	guid := extra["guid"]
	if guid == "" {
		guid = nil
	}
	var publishedAt any // `publishedAt?.valueOf() || null`
	if ms, _ := dbTime(t, row["publishedAt"]).(int64); ms != 0 {
		publishedAt = ms
	}
	return object{
		"libraryItemId": itemID, "podcastId": row["podcastId"], "id": row["id"], "oldEpisodeId": oldEpisodeID,
		"index": row["index"], "season": row["season"], "episode": row["episode"], "episodeType": row["episodeType"],
		"title": row["title"], "subtitle": row["subtitle"], "description": row["description"], "enclosure": enclosure,
		"guid": guid, "pubDate": row["pubDate"], "chapters": dbJSON(t, row["chapters"]), "audioFile": dbJSON(t, row["audioFile"]),
		"publishedAt": publishedAt, "addedAt": dbTime(t, row["createdAt"]), "updatedAt": dbTime(t, row["updatedAt"]),
	}
}

// sortByID orders documents by their id, for order-insensitive list comparisons.
func sortByID(list []any) {
	slices.SortFunc(list, func(a, b any) int {
		left, _ := a.(map[string]any)["id"].(string)
		right, _ := b.(map[string]any)["id"].(string)
		return strings.Compare(left, right)
	})
}
