// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

func scanItemRow(rows *sql.Rows) (map[string]any, string, error) {
	var id, ino, libraryID, folderID, itemPath, relPath, mediaType, mediaID sql.NullString
	var isFile, missing, invalid int64
	var mtime, ctime, birthtime, created, updated, size any
	var extra, libraryFiles []byte
	var lastScan any
	var scanVersion sql.NullString
	err := rows.Scan(&id, &ino, &libraryID, &folderID, &itemPath, &relPath, &isFile, &mtime, &ctime, &birthtime, &created, &updated, &lastScan, &scanVersion, &missing, &invalid, &mediaType, &size, &extra, &libraryFiles, &mediaID)
	if err != nil {
		return nil, "", err
	}
	extraData := decodeObject(extra)
	oldID, _ := extraData["oldLibraryItemId"].(string)
	item := map[string]any{
		"id": id.String, "ino": nullString(ino), "oldLibraryItemId": valueOrNull(oldID),
		"libraryId": libraryID.String, "folderId": nullString(folderID), "path": itemPath.String, "relPath": relPath.String,
		"isFile": isFile != 0, "addedAt": nil, "updatedAt": nil, "isMissing": missing != 0, "isInvalid": invalid != 0,
		"mediaType": mediaType.String,
	}
	for key, raw := range map[string]any{"mtimeMs": mtime, "ctimeMs": ctime, "birthtimeMs": birthtime, "addedAt": created, "updatedAt": updated} {
		ms, err := dateMillis(raw)
		if err != nil {
			return nil, "", err
		}
		if ms != nil {
			item[key] = *ms
		}
	}
	last, err := dateMillis(lastScan)
	if err != nil {
		return nil, "", err
	}
	if last != nil {
		item["lastScan"] = *last
	}
	item["scanVersion"] = nullString(scanVersion)
	files := decodeArray(libraryFiles)
	for _, f := range files {
		if meta, ok := f["metadata"].(map[string]any); ok {
			f["fileType"] = fileType(meta["ext"])
		}
	}
	item["libraryFiles"] = files
	item["numFiles"] = len(files)
	if size != nil {
		item["size"] = size
	}
	return item, mediaID.String, nil
}

func (h *Handler) bookJSON(ctx context.Context, bookID, itemID string, expanded bool) (map[string]any, error) {
	var id, title, subtitle, year, publishedDate, publisher, description, isbn, asin, language, coverPath sql.NullString
	var explicit, abridged int64
	var duration any
	var narratorsRaw, audioRaw, ebookRaw, chapterRaw, tagRaw, genreRaw []byte
	err := h.db.QueryRowContext(ctx, `SELECT id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres FROM books WHERE id=?`, bookID).
		Scan(&id, &title, &subtitle, &year, &publishedDate, &publisher, &description, &isbn, &asin, &language, &explicit, &abridged, &coverPath, &duration, &narratorsRaw, &audioRaw, &ebookRaw, &chapterRaw, &tagRaw, &genreRaw)
	if err != nil {
		return nil, err
	}
	authors, series, err := h.bookRelations(ctx, bookID)
	if err != nil {
		return nil, err
	}
	narrators := decodeStrings(narratorsRaw)
	genres, tags := decodeStrings(genreRaw), decodeStrings(tagRaw)
	audio := decodeArray(audioRaw)
	chapters := decodeArray(chapterRaw)
	ebook := decodeObject(ebookRaw)
	if len(ebook) == 0 || string(ebookRaw) == "null" {
		ebook = nil
	}
	authorNames, authorNamesLF := make([]string, 0, len(authors)), make([]string, 0, len(authors))
	for _, author := range authors {
		authorNames = append(authorNames, stringValue(author["name"]))
		authorNamesLF = append(authorNamesLF, lastFirst(stringValue(author["name"])))
	}
	seriesNames := make([]string, 0, len(series))
	for _, row := range series {
		n := stringValue(row["name"])
		seq := stringValue(row["sequence"])
		if seq != "" {
			n += " #" + seq
		}
		seriesNames = append(seriesNames, n)
	}
	metadata := map[string]any{
		"title": nullString(title), "subtitle": nullString(subtitle), "authors": authors, "narrators": narrators, "series": series,
		"genres": genres, "publishedYear": nullString(year), "publishedDate": nullString(publishedDate), "publisher": nullString(publisher),
		"description": nullString(description), "isbn": nullString(isbn), "asin": nullString(asin), "language": nullString(language),
		"explicit": explicit != 0, "abridged": abridged != 0,
	}
	minMetadata := map[string]any{
		"title": nullString(title), "titleIgnorePrefix": h.titlePrefix(ctx, title.String), "subtitle": nullString(subtitle),
		"authorName": strings.Join(authorNames, ", "), "authorNameLF": strings.Join(authorNamesLF, ", "), "narratorName": strings.Join(narrators, ", "),
		"seriesName": strings.Join(seriesNames, ", "), "genres": genres, "publishedYear": nullString(year), "publishedDate": nullString(publishedDate),
		"publisher": nullString(publisher), "description": nullString(description), "isbn": nullString(isbn), "asin": nullString(asin),
		"language": nullString(language), "explicit": explicit != 0, "abridged": abridged != 0,
	}
	book := map[string]any{"id": id.String, "metadata": metadata, "coverPath": nullString(coverPath), "tags": tags, "audioFiles": audio, "chapters": chapters, "ebookFile": ebook}
	if expanded {
		if description.Valid && description.String != "" {
			minMetadata["descriptionPlain"] = stripHTML(description.String)
		} else {
			minMetadata["descriptionPlain"] = nil
		}
		book["metadata"] = minMetadata
		book["libraryItemId"] = itemID
		book["numTracks"] = includedAudioCount(audio)
		book["numAudioFiles"] = len(audio)
		book["numChapters"] = len(chapters)
		book["duration"] = duration
		book["size"] = audioSize(audio, ebook)
		if format, ok := ebook["ebookFormat"]; ok {
			book["ebookFormat"] = format
		}
		book["tracks"] = makeTracks(itemID, audio)
		delete(book, "audioFiles")
		delete(book, "chapters")
		delete(book, "ebookFile")
		return book, nil
	}
	book["libraryItemId"] = itemID
	return book, nil
}

// bookMinifiedJSON mirrors Book.toOldJSONMinified. It is intentionally kept
// separate from the expanded serializer because their fields differ.
func (h *Handler) bookMinifiedJSON(ctx context.Context, bookID, itemID string) (map[string]any, error) {
	book, err := h.bookJSON(ctx, bookID, itemID, true)
	if err != nil {
		return nil, err
	}
	delete(book, "libraryItemId")
	delete(book, "audioFiles")
	delete(book, "chapters")
	delete(book, "ebookFile")
	delete(book, "tracks")
	if metadata, ok := book["metadata"].(map[string]any); ok {
		delete(metadata, "descriptionPlain")
	}
	return book, nil
}

func (h *Handler) bookRelations(ctx context.Context, bookID string) ([]map[string]any, []map[string]any, error) {
	aRows, err := h.db.QueryContext(ctx, `SELECT a.id,a.name,ba.createdAt FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt ASC`, bookID)
	if err != nil {
		return nil, nil, err
	}
	authors := make([]map[string]any, 0)
	for aRows.Next() {
		var id, name string
		var created any
		if err := aRows.Scan(&id, &name, &created); err != nil {
			aRows.Close()
			return nil, nil, err
		}
		authors = append(authors, map[string]any{"id": id, "name": name})
	}
	if err := aRows.Err(); err != nil {
		aRows.Close()
		return nil, nil, err
	}
	if err := aRows.Close(); err != nil {
		return nil, nil, err
	}
	sRows, err := h.db.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence,bs.createdAt FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt ASC`, bookID)
	if err != nil {
		return nil, nil, err
	}
	series := make([]map[string]any, 0)
	for sRows.Next() {
		var id, name string
		var sequence sql.NullString
		var created any
		if err := sRows.Scan(&id, &name, &sequence, &created); err != nil {
			sRows.Close()
			return nil, nil, err
		}
		series = append(series, map[string]any{"id": id, "name": name, "sequence": nullString(sequence)})
	}
	if err := sRows.Err(); err != nil {
		sRows.Close()
		return nil, nil, err
	}
	if err := sRows.Close(); err != nil {
		return nil, nil, err
	}
	return authors, series, nil
}

func (h *Handler) podcastJSON(ctx context.Context, podcastID, itemID string, minified bool) (map[string]any, error) {
	var id, title, author, releaseDate, feedURL, imageURL, description, itunesPageURL, itunesID, itunesArtistID, language, podcastType, autoSchedule, coverPath sql.NullString
	var explicit, autoDownload int64
	var lastCheck any
	var maxKeep, maxNew sql.NullInt64
	var tagsRaw, genresRaw []byte
	err := h.db.QueryRowContext(ctx, `SELECT id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres FROM podcasts WHERE id=?`, podcastID).Scan(&id, &title, &author, &releaseDate, &feedURL, &imageURL, &description, &itunesPageURL, &itunesID, &itunesArtistID, &language, &podcastType, &explicit, &autoDownload, &autoSchedule, &lastCheck, &maxKeep, &maxNew, &coverPath, &tagsRaw, &genresRaw)
	if err != nil {
		return nil, err
	}
	episodes, err := h.podcastEpisodes(ctx, podcastID, itemID, minified)
	if err != nil {
		return nil, err
	}
	check, err := dateMillis(lastCheck)
	if err != nil {
		return nil, err
	}
	if check != nil && *check == 0 {
		check = nil
	}
	metadata := map[string]any{"title": nullString(title), "author": nullString(author), "description": nullString(description), "releaseDate": nullString(releaseDate), "genres": decodeStrings(genresRaw), "feedUrl": nullString(feedURL), "imageUrl": nullString(imageURL), "itunesPageUrl": nullString(itunesPageURL), "itunesId": nullString(itunesID), "itunesArtistId": nullString(itunesArtistID), "language": nullString(language), "explicit": explicit != 0, "type": nullString(podcastType)}
	if minified {
		metadata["titleIgnorePrefix"] = h.titlePrefix(ctx, title.String)
	}
	result := map[string]any{"id": id.String, "metadata": metadata, "coverPath": nullString(coverPath), "tags": decodeStrings(tagsRaw), "autoDownloadEpisodes": autoDownload != 0, "autoDownloadSchedule": nullString(autoSchedule), "lastEpisodeCheck": deref(check), "maxEpisodesToKeep": maxKeep.Int64, "maxNewEpisodesToDownload": maxNew.Int64, "size": episodesSize(episodes)}
	if minified {
		result["numEpisodes"] = len(episodes)
		return result, nil
	}
	result["libraryItemId"] = itemID
	result["episodes"] = episodes
	return result, nil
}

func (h *Handler) podcastEpisodes(ctx context.Context, podcastID, itemID string, expanded bool) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt FROM podcastEpisodes WHERE podcastId=? ORDER BY createdAt ASC`, podcastID)
	if err != nil {
		return nil, err
	}
	type episodeRow struct {
		id                                                                                               string
		index                                                                                            sql.NullInt64
		season, episode, episodeType, title, subtitle, description, pubDate, enclosureURL, enclosureType sql.NullString
		enclosureSize                                                                                    sql.NullInt64
		published, created, updated                                                                      any
		audio, chapters, extra                                                                           []byte
	}
	data := make([]episodeRow, 0)
	for rows.Next() {
		var e episodeRow
		if err := rows.Scan(&e.id, &e.index, &e.season, &e.episode, &e.episodeType, &e.title, &e.subtitle, &e.description, &e.pubDate, &e.enclosureURL, &e.enclosureSize, &e.enclosureType, &e.published, &e.audio, &e.chapters, &e.extra, &e.created, &e.updated); err != nil {
			rows.Close()
			return nil, err
		}
		data = append(data, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(data))
	for _, e := range data {
		pub, err := dateMillis(e.published)
		if err != nil {
			return nil, err
		}
		if pub != nil && *pub == 0 {
			pub = nil
		}
		created, err := dateMillis(e.created)
		if err != nil {
			return nil, err
		}
		updated, err := dateMillis(e.updated)
		if err != nil {
			return nil, err
		}
		extra := decodeObject(e.extra)
		var enclosure any
		if e.enclosureURL.Valid && e.enclosureURL.String != "" {
			var length any
			if e.enclosureSize.Valid {
				length = strconv.FormatInt(e.enclosureSize.Int64, 10)
			}
			enclosure = map[string]any{"url": e.enclosureURL.String, "type": nullString(e.enclosureType), "length": length}
		}
		audio := decodeObject(e.audio)
		chapters := decodeArray(e.chapters)
		ep := map[string]any{"libraryItemId": itemID, "podcastId": podcastID, "id": e.id, "oldEpisodeId": valueOrNull(stringValue(extra["oldEpisodeId"])), "index": nullInt(e.index), "season": nullString(e.season), "episode": nullString(e.episode), "episodeType": nullString(e.episodeType), "title": nullString(e.title), "subtitle": nullString(e.subtitle), "description": nullString(e.description), "enclosure": enclosure, "guid": valueOrNull(stringValue(extra["guid"])), "pubDate": nullString(e.pubDate), "chapters": chapters, "audioFile": audio, "publishedAt": deref(pub), "addedAt": deref(created), "updatedAt": deref(updated)}
		if expanded {
			track := map[string]any{}
			for k, v := range audio {
				track[k] = v
			}
			meta, _ := audio["metadata"].(map[string]any)
			track["startOffset"] = 0
			track["title"] = meta["filename"]
			track["index"] = 1
			track["contentUrl"] = "/api/items/" + itemID + "/file/" + url.PathEscape(stringValue(audio["ino"]))
			ep["audioTrack"] = track
			ep["size"] = number(meta["size"])
			ep["duration"] = audio["duration"]
		}
		out = append(out, ep)
	}
	return out, nil
}

func (h *Handler) expandedPodcastItem(ctx context.Context, item map[string]any, podcastID, itemID string) (map[string]any, error) {
	media, err := h.podcastJSON(ctx, podcastID, itemID, false)
	if err != nil {
		return nil, err
	}
	episodes, err := h.podcastEpisodes(ctx, podcastID, itemID, true)
	if err != nil {
		return nil, err
	}
	media["numEpisodes"] = len(episodes)
	media["size"] = episodesSize(episodes)
	metadata := media["metadata"].(map[string]any)
	metadata["titleIgnorePrefix"] = h.titlePrefix(ctx, stringValue(metadata["title"]))
	media["episodes"] = episodes
	item["media"] = media
	if _, ok := item["lastScan"]; !ok {
		item["lastScan"] = nil
	}
	return item, nil
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}
func episodesSize(episodes []map[string]any) int64 {
	var n int64
	for _, e := range episodes {
		if audio, ok := e["audioFile"].(map[string]any); ok {
			if meta, ok := audio["metadata"].(map[string]any); ok {
				n += number(meta["size"])
			}
		}
	}
	return n
}

func (h *Handler) expandedBookItem(ctx context.Context, item map[string]any, bookID, itemID string) (map[string]any, error) {
	media, err := h.bookJSON(ctx, bookID, itemID, false)
	if err != nil {
		return nil, err
	}
	metadata, _ := media["metadata"].(map[string]any)
	if metadata != nil {
		metadata["titleIgnorePrefix"] = h.titlePrefix(ctx, stringValue(metadata["title"]))
		authors, _ := metadata["authors"].([]map[string]any)
		authorNames := make([]string, 0, len(authors))
		authorNamesLF := make([]string, 0, len(authors))
		for _, author := range authors {
			name := stringValue(author["name"])
			authorNames = append(authorNames, name)
			authorNamesLF = append(authorNamesLF, lastFirst(name))
		}
		metadata["authorName"] = strings.Join(authorNames, ", ")
		metadata["authorNameLF"] = strings.Join(authorNamesLF, ", ")
		narrators, _ := metadata["narrators"].([]string)
		metadata["narratorName"] = strings.Join(narrators, ", ")
		series, _ := metadata["series"].([]map[string]any)
		seriesNames := make([]string, 0, len(series))
		for _, entry := range series {
			name := stringValue(entry["name"])
			if sequence := stringValue(entry["sequence"]); sequence != "" {
				name += " #" + sequence
			}
			seriesNames = append(seriesNames, name)
		}
		metadata["seriesName"] = strings.Join(seriesNames, ", ")
		if description := stringValue(metadata["description"]); description != "" {
			metadata["descriptionPlain"] = stripHTML(description)
		} else {
			metadata["descriptionPlain"] = nil
		}
	}
	audio, _ := media["audioFiles"].([]map[string]any)
	chapters, _ := media["chapters"].([]map[string]any)
	ebook, _ := media["ebookFile"].(map[string]any)
	media["numTracks"] = includedAudioCount(audio)
	media["numAudioFiles"] = len(audio)
	media["numChapters"] = len(chapters)
	var duration any
	if err := h.db.QueryRowContext(ctx, `SELECT duration FROM books WHERE id=?`, bookID).Scan(&duration); err != nil {
		return nil, err
	}
	media["duration"] = duration
	media["size"] = audioSize(audio, ebook)
	if format, ok := ebook["ebookFormat"]; ok {
		media["ebookFormat"] = format
	}
	media["tracks"] = makeTracks(itemID, audio)
	item["media"] = media
	if _, ok := item["lastScan"]; !ok {
		item["lastScan"] = nil
	}
	return item, nil
}

func orderClause(sortBy string, desc, ignorePrefix bool) (string, error) {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	fields := map[string]string{"addedAt": "li.createdAt", "size": "li.size", "birthtimeMs": "li.birthtime", "mtimeMs": "li.mtime", "media.duration": "b.duration", "media.metadata.publishedYear": "CAST(b.publishedYear AS INTEGER)", "media.metadata.authorName": "li.authorNamesFirstLast COLLATE NOCASE", "media.metadata.authorNameLF": "li.authorNamesLastFirst COLLATE NOCASE", "media.metadata.title": "li.title COLLATE NOCASE"}
	fields["media.metadata.author"] = "b.author COLLATE NOCASE"
	fields["media.numTracks"] = "b.numEpisodes"
	fields["random"] = "RANDOM()"
	if sortBy == "" {
		return "", nil
	}
	field, ok := fields[sortBy]
	if !ok {
		return "", fmt.Errorf("unsupported sort field")
	}
	if sortBy == "media.metadata.title" && ignorePrefix {
		field = "li.titleIgnorePrefix COLLATE NOCASE"
	}
	return " ORDER BY " + field + " " + dir, nil
}

// orderClauseForRequest adds the two user scoped orderings supported by the
// upstream book library query. Keep these as correlated subqueries so each
// item can still be paginated without multiplying rows by progress records.
func orderClauseForRequest(sortBy string, desc, ignorePrefix bool, mediaType, filter string, user store.User) (string, []any, error) {
	if sortBy == "sequence" {
		if mediaType != "book" {
			return "", nil, fmt.Errorf("sequence sort is unavailable for podcasts")
		}
		if strings.HasPrefix(filter, "series.") {
			_, value, _ := strings.Cut(filter, ".")
			value, _ = decodeFilterValue(value)
			dir := "ASC NULLS LAST"
			if desc {
				dir = "DESC NULLS FIRST"
			}
			return " ORDER BY CAST((SELECT bs.sequence FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=? LIMIT 1) AS FLOAT) " + dir, []any{value}, nil
		}
		clause, err := orderClause("media.metadata.title", desc, ignorePrefix)
		return clause, nil, err
	}
	if strings.HasPrefix(sortBy, "progress") {
		if mediaType != "book" {
			return "", nil, fmt.Errorf("progress sort is unavailable for podcasts")
		}
		dir := "ASC"
		if desc {
			dir = "DESC"
		}
		field := "updatedAt"
		switch sortBy {
		case "progress", "progress.updatedAt":
			field = "updatedAt"
		case "progress.createdAt":
			field = "createdAt"
		case "progress.finishedAt":
			field = "finishedAt"
		default:
			return "", nil, fmt.Errorf("unsupported sort field")
		}
		return " ORDER BY (SELECT mp." + field + " FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1) " + dir + " NULLS LAST", []any{user.ID}, nil
	}
	clause, err := orderClause(sortBy, desc, ignorePrefix)
	return clause, nil, err
}

func filterClause(raw, mediaType string, user store.User) (string, []any, error) {
	if raw == "" {
		return "", nil, nil
	}
	group, value, hasValue := strings.Cut(raw, ".")
	if !hasValue {
		group = raw
		value = ""
	} else if decoded, err := decodeFilterValue(value); err == nil {
		value = decoded
	}
	if group == "" {
		return "", nil, nil
	}
	var clause string
	var arg any
	switch group {
	case "issues":
		clause = ` AND (li.isMissing=1 OR li.isInvalid=1)`
	case "recent":
		clause = ` AND li.createdAt>=?`
		arg = dbtime.Format(time.Now().Add(-60 * 24 * time.Hour))
	case "feed-open":
		clause = ` AND EXISTS (SELECT 1 FROM feeds f WHERE f.entityType='libraryItem' AND f.entityId=li.id)`
	case "share-open":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("share filter is unavailable for podcasts")
		}
		clause = ` AND EXISTS (SELECT 1 FROM mediaItemShares ms WHERE ms.mediaItemType='book' AND ms.mediaItemId=b.id)`
	case "progress":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("progress filter is unavailable for podcasts")
		}
		base := `SELECT mp.isFinished,mp.currentTime,mp.ebookProgress FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1`
		switch value {
		case "not-finished":
			clause = ` AND (COALESCE((` + base + `),0)=0)`
		case "not-started":
			clause = ` AND COALESCE((` + base + `),0)=0 AND COALESCE((SELECT mp.currentTime FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1),0)=0`
		case "finished":
			clause = ` AND COALESCE((` + base + `),0)=1`
		case "in-progress":
			clause = ` AND COALESCE((SELECT (mp.currentTime>0 OR mp.ebookProgress>0) AND mp.isFinished=0 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1),0)=1`
		case "audio-in-progress":
			clause = ` AND COALESCE((SELECT mp.currentTime>0 AND mp.isFinished=0 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1),0)=1`
		case "ebook-in-progress":
			clause = ` AND json_array_length(b.audioFiles)=0 AND COALESCE((SELECT mp.ebookProgress>0 AND mp.isFinished=0 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? LIMIT 1),0)=1`
		case "ebook-finished":
			clause = ` AND json_array_length(b.audioFiles)=0 AND b.ebookFile IS NOT NULL AND b.ebookFile!='null' AND COALESCE((` + base + `),0)=1`
		default:
			return "", nil, fmt.Errorf("unsupported progress filter")
		}
		// Each predicate is scoped to the authenticated user's progress row.
		args := []any{user.ID}
		if value == "not-started" || value == "in-progress" || value == "audio-in-progress" || value == "ebook-in-progress" {
			args = []any{user.ID, user.ID}
		}
		return clause, args, nil
	case "genres", "tags":
		clause = ` AND EXISTS (SELECT 1 FROM json_each(b.` + group + `) WHERE json_each.value=?)`
		arg = value
	case "narrators":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("narrator filter is unavailable for podcasts")
		}
		clause = ` AND EXISTS (SELECT 1 FROM json_each(b.narrators) WHERE json_each.value=?)`
		arg = value
	case "languages":
		clause = ` AND b.language=?`
		arg = value
	case "publishers":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("publisher filter is unavailable for podcasts")
		}
		clause = ` AND b.publisher=?`
		arg = value
	case "authors":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("author filter is unavailable for podcasts")
		}
		clause = ` AND EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id AND ba.authorId=?)`
		arg = value
	case "series":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("series filter is unavailable for podcasts")
		}
		if value == "no-series" {
			clause = ` AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id)`
		} else {
			clause = ` AND EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id AND bs.seriesId=?)`
			arg = value
		}
	case "explicit":
		clause = ` AND b.explicit=1`
	case "abridged":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("abridged filter is unavailable for podcasts")
		}
		clause = ` AND b.abridged=1`
	case "tracks":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("track filter is unavailable for podcasts")
		}
		if value == "none" {
			clause = ` AND json_array_length(b.audioFiles)=0`
		} else if value == "multi" {
			clause = ` AND json_array_length(b.audioFiles)>1`
		} else {
			clause = ` AND json_array_length(b.audioFiles)=1`
		}
	case "ebooks":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("ebook filter is unavailable for podcasts")
		}
		if value == "ebook" {
			clause = ` AND b.ebookFile IS NOT NULL AND b.ebookFile!='null'`
		} else if value == "no-ebook" {
			clause = ` AND (b.ebookFile IS NULL OR b.ebookFile='null')`
		} else if value == "supplementary" {
			clause = ` AND li.libraryFiles LIKE '%"isSupplementary":true%'`
		} else if value == "no-supplementary" {
			clause = ` AND li.libraryFiles NOT LIKE '%"isSupplementary":true%'`
		} else {
			return "", nil, fmt.Errorf("unsupported ebook filter")
		}
	case "publishedDecades":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("published decade filter is unavailable for podcasts")
		}
		year, err := strconv.Atoi(value)
		if err != nil {
			return "", nil, err
		}
		clause = ` AND CAST(b.publishedYear AS INTEGER) BETWEEN ? AND ?`
		return clause, []any{year, year + 9}, nil
	case "missing":
		if mediaType != "book" {
			return "", nil, fmt.Errorf("missing metadata filter is unavailable for podcasts")
		}
		switch value {
		case "authors":
			clause = ` AND NOT EXISTS(SELECT 1 FROM bookAuthors ba WHERE ba.bookId=b.id)`
		case "series":
			clause = ` AND NOT EXISTS(SELECT 1 FROM bookSeries bs WHERE bs.bookId=b.id)`
		case "issues":
			clause = ` AND (li.isMissing=1 OR li.isInvalid=1)`
		case "asin", "isbn", "subtitle", "publishedYear", "description", "publisher", "language":
			clause = ` AND (b.` + value + ` IS NULL OR b.` + value + `='')`
		case "cover":
			clause = ` AND (b.coverPath IS NULL OR b.coverPath='')`
		case "genres", "tags", "narrators", "chapters":
			clause = ` AND (b.` + value + ` IS NULL OR json_array_length(b.` + value + `)=0)`
		default:
			return "", nil, fmt.Errorf("unsupported missing metadata filter")
		}
	default:
		return "", nil, fmt.Errorf("unsupported filter group %q", group)
	}
	if arg == nil {
		return clause, nil, nil
	}
	return clause, []any{arg}, nil
}

func decodeFilterValue(value string) (string, error) {
	if value == "no-series" || value == "no-ebook" || value == "ebook" || value == "issues" || value == "authors" || value == "series" || value == "genres" || value == "tags" || value == "narrators" || value == "not-finished" || value == "not-started" || value == "finished" || value == "in-progress" || value == "audio-in-progress" || value == "ebook-in-progress" || value == "ebook-finished" || value == "multi" || value == "none" || value == "supplementary" || value == "no-supplementary" || value == "share-open" || value == "feed-open" || value == "recent" {
		return value, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(value); err == nil {
			return string(b), nil
		}
	}
	return value, fmt.Errorf("filter is not base64")
}

func userMediaClause(user store.User, _ string) (string, []any) {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	parts := make([]string, 0, 2)
	args := make([]any, 0)
	if permissions["accessExplicitContent"] != true || !user.IsActive {
		parts = append(parts, " AND b.explicit=0")
	}
	if permissions["accessAllTags"] != true {
		selected, _ := permissions["itemTagsSelected"].([]any)
		if len(selected) > 0 {
			placeholders := strings.TrimRight(strings.Repeat("?,", len(selected)), ",")
			count := `(SELECT count(*) FROM json_each(b.tags) WHERE json_each.value IN (` + placeholders + `))`
			if permissions["selectedTagsNotAccessible"] == true {
				parts = append(parts, " AND "+count+"=0")
			} else {
				parts = append(parts, " AND "+count+">0")
			}
			args = append(args, selected...)
		}
	}
	return strings.Join(parts, ""), args
}

func pagination(r *http.Request) (int, int, error) {
	page, limit := 0, 0
	for name, target := range map[string]*int{"page": &page, "limit": &limit} {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, 0, fmt.Errorf("Invalid request. %s must be a positive integer", name)
		}
		*target = n
	}
	return page, limit, nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func writeError(w http.ResponseWriter, status int, message string) {
	// Upstream responses are Express res.sendStatus(status) (English status
	// text) or res.status(..).send('message'); neither appends a trailing
	// newline the way net/http's http.Error does, and express 'send' attaches
	// a weak ETag computed from the body.
	if message == "" {
		message = http.StatusText(status)
	}
	_, _ = sendExpressText(w, status, message)
}

// writeStatus mirrors Express res.sendStatus: plain-text status text with ETag.
func writeStatus(w http.ResponseWriter, status int) {
	body := http.StatusText(status)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", expressETag([]byte(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// sendExpressText writes an Express 'send' text body: no trailing newline and
// a W/"<len hex>-<sha1 base64>" ETag header.
func sendExpressText(w http.ResponseWriter, status int, message string) (bool, error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.Header().Set("ETag", expressETag([]byte(message)))
	w.WriteHeader(status)
	if _, err := w.Write([]byte(message)); err != nil {
		return false, err
	}
	return true, nil
}
func userCanAccessLibrary(user store.User, id string) bool {
	var p map[string]any
	_ = json.Unmarshal(user.Permissions, &p)
	if p["accessAllLibraries"] == true {
		return true
	}
	ids, _ := p["librariesAccessible"].([]any)
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// LibraryController.findAll filters only a non-empty librariesAccessible list;
// it does not special-case accessAllLibraries on this list route.
func userCanListLibrary(user store.User, id string) bool {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	ids, _ := permissions["librariesAccessible"].([]any)
	if len(ids) == 0 {
		return true
	}
	for _, value := range ids {
		if value == id {
			return true
		}
	}
	return false
}
func decodeObject(raw []byte) map[string]any {
	v := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &v)
	}
	if v == nil {
		return map[string]any{}
	}
	return v
}
func decodeArray(raw []byte) []map[string]any {
	v := make([]map[string]any, 0)
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &v)
	}
	if v == nil {
		return make([]map[string]any, 0)
	}
	return v
}
func decodeStrings(raw []byte) []string {
	v := make([]string, 0)
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &v)
	}
	if v == nil {
		return make([]string, 0)
	}
	return v
}
func dateMillis(v any) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	switch x := v.(type) {
	case int64:
		n := x
		return &n, nil
	case float64:
		n := int64(x)
		return &n, nil
	case string:
		return parseTime(x)
	case []byte:
		return parseTime(string(x))
	case time.Time:
		n := x.UnixMilli()
		return &n, nil
	default:
		return nil, fmt.Errorf("unsupported timestamp value %T", v)
	}
}
func parseTime(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05 -07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	for _, l := range layouts {
		if t, e := time.Parse(l, s); e == nil {
			n := t.UnixMilli()
			return &n, nil
		}
	}
	return nil, fmt.Errorf("invalid timestamp %q", s)
}
func nullString(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
func deref(n *int64) any {
	if n == nil {
		return nil
	}
	return *n
}
func stringValue(v any) string { s, _ := v.(string); return s }
func valueOrNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (h *Handler) titlePrefix(ctx context.Context, title string) string {
	prefixes := []string{"the", "a"}
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err == nil {
		if values, ok := decodeObject(raw)["sortingPrefixes"].([]any); ok {
			prefixes = make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok {
					prefixes = append(prefixes, text)
				}
			}
		}
	}
	return catalogTitlePrefixAtEnd(title, prefixes)
}
func catalogTitlePrefixAtEnd(title string, prefixes []string) string {
	for _, prefix := range prefixes {
		if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) && title[len(prefix)] == ' ' {
			capPrefix := prefix
			if len(capPrefix) > 0 {
				capPrefix = strings.ToUpper(capPrefix[:1]) + capPrefix[1:]
			}
			return title[len(prefix)+1:] + ", " + capPrefix
		}
	}
	return title
}

var htmlTags = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTags.ReplaceAllString(s, "")))
}
func lastFirst(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	return strings.Join(parts[1:], " ") + ", " + parts[0]
}
func includedAudioCount(a []map[string]any) int {
	n := 0
	for _, f := range a {
		if f["exclude"] != true {
			n++
		}
	}
	return n
}
func audioSize(audio []map[string]any, ebook map[string]any) int64 {
	var n int64
	for _, f := range audio {
		if m, ok := f["metadata"].(map[string]any); ok {
			n += number(m["size"])
		}
	}
	if m, ok := ebook["metadata"].(map[string]any); ok {
		n += number(m["size"])
	}
	return n
}
func number(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}
func makeTracks(itemID string, audio []map[string]any) []map[string]any {
	out := make([]map[string]any, 0)
	var offset float64
	for _, f := range audio {
		if f["exclude"] == true {
			continue
		}
		track := map[string]any{}
		for k, v := range f {
			track[k] = v
		}
		meta, _ := f["metadata"].(map[string]any)
		track["title"] = meta["filename"]
		track["startOffset"] = offset
		track["contentUrl"] = "/api/items/" + itemID + "/file/" + url.PathEscape(stringValue(f["ino"]))
		offset += floating(f["duration"])
		out = append(out, track)
	}
	return out
}
func floating(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}
func fileType(ext any) string {
	s := strings.ToLower(strings.TrimPrefix(stringValue(ext), "."))
	switch s {
	case "mp3", "m4a", "m4b", "aac", "flac", "ogg", "opus", "wav", "wma", "aiff":
		return "audio"
	case "epub", "pdf", "mobi", "azw", "azw3", "cbz", "cbr":
		return "ebook"
	case "jpg", "jpeg", "png", "webp", "gif":
		return "image"
	case "txt", "md", "rtf":
		return "text"
	case "opf", "nfo", "json":
		return "metadata"
	}
	return "unknown"
}

type emitFunc func(userID, event string, payload any)

// Handler reads library and media catalog data from an existing database.
type Handler struct {
	db             *sql.DB
	cfg            config.Config
	emit           emitFunc
	libraryChanged func(libraryID string)
	itemChanged    func(context.Context, string) error
}

// New constructs a catalog handler. The emit callback is used for events from
// catalog write routes.
func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any)) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit}
}

// SetLibraryChanged installs the scanner watcher refresh callback. It runs
// only after a catalog library mutation commits successfully.
func (h *Handler) SetLibraryChanged(callback func(libraryID string)) { h.libraryChanged = callback }

// SetItemChanged installs the source-compatible metadata sidecar callback for
// media mutations. It runs only after the catalog row has been saved.
func (h *Handler) SetItemChanged(callback func(context.Context, string) error) {
	h.itemChanged = callback
}

// Handle writes a response for a catalog route and returns true when matched.
// Paths are unprefixed (/api/...) as received by the main server router.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	p := path.Clean(r.URL.Path)
	switch {
	case p == "/api/libraries":
		if r.Method == http.MethodPost {
			h.createLibrary(w, r, user)
			return true
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return false
		}
		h.libraries(w, r, user)
		return true
	case p == "/api/libraries/order" && r.Method == http.MethodPost:
		h.reorderLibraries(w, r, user)
		return true
	case strings.HasPrefix(p, "/api/libraries/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/libraries/"), "/")
		if len(parts) == 1 && parts[0] != "" {
			if r.Method == http.MethodPatch {
				h.updateLibrary(w, r, user, parts[0])
				return true
			}
			if r.Method == http.MethodDelete {
				h.deleteLibrary(w, r, user, parts[0])
				return true
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			h.library(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "items" {
			h.libraryItems(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "stats" {
			h.libraryStats(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "filterdata" {
			h.libraryFilterData(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "personalized" {
			h.personalized(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "authors" {
			h.authors(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "narrators" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.narrators(w, r, user, parts[0])
			return true
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "narrators" && r.Method == http.MethodPatch {
			h.updateNarrator(w, r, user, parts[0], parts[2], false)
			return true
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "narrators" && r.Method == http.MethodDelete {
			h.updateNarrator(w, r, user, parts[0], parts[2], true)
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "podcast-titles" {
			h.podcastTitles(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "series" {
			h.series(w, r, user, parts[0])
			return true
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "series" {
			h.seriesByID(w, r, user, parts[0], parts[2])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "search" {
			h.search(w, r, user, parts[0])
			return true
		}
	case strings.HasPrefix(p, "/api/items/"):
		parts := strings.Split(strings.TrimPrefix(p, "/api/items/"), "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] == "download" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.itemDownload(w, r, user, parts[0])
			return true
		}
		if len(parts) == 4 && parts[0] != "" && parts[1] == "file" && parts[3] == "download" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.mediaFile(w, r, user, parts[0], parts[2], true)
			return true
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "ffprobe" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.ffprobe(w, r, user, parts[0], parts[2])
			return true
		}
		if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodDelete {
			h.deleteItem(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] == "batch" && parts[1] == "get" && r.Method == http.MethodPost {
			h.batchGet(w, r, user)
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "chapters" && r.Method == http.MethodPost {
			h.updateChapters(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "tracks" && r.Method == http.MethodPatch {
			h.updateTracks(w, r, user, parts[0])
			return true
		}
		if len(parts) == 4 && parts[0] != "" && parts[1] == "ebook" && parts[3] == "status" && r.Method == http.MethodPatch {
			h.updateEbookStatus(w, r, user, parts[0], parts[2])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "metadata-object" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			h.metadataObject(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "media" && r.Method == http.MethodPatch {
			h.updateMedia(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "cover" && r.Method == http.MethodDelete {
			h.removeCover(w, r, user, parts[0])
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "cover" && r.Method == http.MethodPatch {
			h.updateCover(w, r, user, parts[0])
			return true
		}
		if len(parts) == 1 && parts[0] != "" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			h.item(w, r, user, parts[0])
			return true
		}
		if len(parts) == 3 && parts[0] != "" && parts[1] == "file" {
			if r.Method == http.MethodDelete {
				h.deleteLibraryFile(w, r, user, parts[0], parts[2])
				return true
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			h.mediaFile(w, r, user, parts[0], parts[2], false)
			return true
		}
		if (len(parts) == 2 || len(parts) == 3) && parts[0] != "" && parts[1] == "ebook" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			fileID := ""
			if len(parts) == 3 {
				fileID = parts[2]
			}
			h.ebookFile(w, r, user, parts[0], fileID)
			return true
		}
		if len(parts) == 2 && parts[0] != "" && parts[1] == "cover" {
			if r.Method == http.MethodPost {
				h.uploadCover(w, r, user, parts[0])
				return true
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				return false
			}
			h.cover(w, r, parts[0])
			return true
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return false
}

func (h *Handler) libraryFilterData(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if !userCanAccessLibrary(user, id) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, id).Scan(&mediaType); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	data, err := h.filterData(r.Context(), id, mediaType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	writeJSON(w, r, http.StatusOK, data)
}

func (h *Handler) libraries(w http.ResponseWriter, r *http.Request, user store.User) {
	var rows *sql.Rows
	var err error
	includeStats := strings.Contains(","+r.URL.Query().Get("include")+",", ",stats,")
	rows, err = h.db.QueryContext(r.Context(), `SELECT id,name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type libraryRow struct {
		id, name, icon, mediaType, provider, version sql.NullString
		order                                        sql.NullInt64
		lastScan, createdAt, updatedAt               any
		settings                                     []byte
	}
	selected := make([]libraryRow, 0)
	for rows.Next() {
		var row libraryRow
		if err := rows.Scan(&row.id, &row.name, &row.order, &row.icon, &row.mediaType, &row.provider, &row.lastScan, &row.version, &row.settings, &row.createdAt, &row.updatedAt); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		if userCanListLibrary(user, row.id.String) {
			selected = append(selected, row)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type libraryJSON struct {
		ID              string           `json:"id"`
		Name            string           `json:"name"`
		Folders         []map[string]any `json:"folders"`
		DisplayOrder    int64            `json:"displayOrder"`
		Icon            any              `json:"icon"`
		MediaType       any              `json:"mediaType"`
		Provider        any              `json:"provider"`
		Settings        map[string]any   `json:"settings"`
		LastScan        any              `json:"lastScan"`
		LastScanVersion any              `json:"lastScanVersion"`
		CreatedAt       any              `json:"createdAt"`
		LastUpdate      any              `json:"lastUpdate"`
		Stats           any              `json:"stats,omitempty"`
	}
	var out []libraryJSON
	for _, row := range selected {
		folders, err := h.libraryFolders(r.Context(), row.id.String)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		last, err := dateMillis(row.lastScan)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		if last != nil && *last == 0 {
			last = nil // Library.toOldJSON emits lastScan?.valueOf() || null.
		}
		created, err := dateMillis(row.createdAt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		updated, err := dateMillis(row.updatedAt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		lib := libraryJSON{
			ID: row.id.String, Name: row.name.String, Folders: folders, DisplayOrder: row.order.Int64,
			Icon: nullString(row.icon), MediaType: nullString(row.mediaType), Provider: nullString(row.provider),
			Settings: decodeObject(row.settings), LastScan: last, LastScanVersion: nullString(row.version),
			CreatedAt: deref(created), LastUpdate: deref(updated),
		}
		if includeStats {
			stats, err := h.libraryStatsValue(r.Context(), row.id.String, row.mediaType.String)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "database read failed")
				return
			}
			lib.Stats = stats
		}
		out = append(out, lib)
	}
	if out == nil {
		out = []libraryJSON{}
	}
	body, err := json.Marshal(struct {
		Libraries []libraryJSON `json:"libraries"`
	}{Libraries: out})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *Handler) library(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if !userCanAccessLibrary(user, id) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var name, icon, mediaType, provider, version sql.NullString
	var order sql.NullInt64
	var lastScan, createdAt, updatedAt any
	var rawSettings []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries WHERE id=?`, id).
		Scan(&name, &order, &icon, &mediaType, &provider, &lastScan, &version, &rawSettings, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	folders, err := h.libraryFolders(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	last, err := dateMillis(lastScan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if last != nil && *last == 0 {
		last = nil
	}
	created, err := dateMillis(createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	updated, err := dateMillis(updatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	payload := map[string]any{
		"id": id, "name": name.String, "folders": folders, "displayOrder": order.Int64,
		"icon": nullString(icon), "mediaType": nullString(mediaType), "provider": nullString(provider),
		"settings": decodeObject(rawSettings), "lastScan": last, "lastScanVersion": nullString(version),
		"createdAt": deref(created), "lastUpdate": deref(updated),
	}
	if strings.Contains(","+r.URL.Query().Get("include")+",", ",filterdata,") {
		filterdata, err := h.filterData(r.Context(), id, mediaType.String)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		var playlists int64
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM playlists WHERE userId=? AND libraryId=?`, user.ID, id).Scan(&playlists); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{"filterdata": filterdata, "issues": filterdata["numIssues"], "numUserPlaylists": playlists, "library": payload})
		return
	}
	writeJSON(w, r, http.StatusOK, payload)
}

func (h *Handler) libraryStats(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if !userCanAccessLibrary(user, id) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var mediaType string
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, id).Scan(&mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	stats, err := h.libraryStatsValue(r.Context(), id, mediaType)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	writeJSON(w, r, http.StatusOK, stats)
}

func (h *Handler) libraryStatsValue(ctx context.Context, id, mediaType string) (map[string]any, error) {
	var size, audioCount, total int64
	var duration float64
	if mediaType == "book" {
		err := h.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(li.size),0),COALESCE(SUM(b.duration),0),COALESCE(SUM(json_array_length(b.audioFiles)),0),COUNT(*) FROM libraryItems li JOIN books b ON b.id=li.mediaId WHERE li.libraryId=?`, id).Scan(&size, &duration, &audioCount, &total)
		if err != nil {
			return nil, err
		}
		genres, err := h.genreCounts(ctx, id, "books")
		if err != nil {
			return nil, err
		}
		var authorTotal int64
		if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM authors WHERE libraryId=?`, id).Scan(&authorTotal); err != nil {
			return nil, err
		}
		authorRows, err := h.db.QueryContext(ctx, `SELECT a.id,a.name,COUNT(ba.bookId) AS n FROM authors a LEFT JOIN bookAuthors ba ON ba.authorId=a.id WHERE a.libraryId=? GROUP BY a.id ORDER BY n DESC LIMIT 10`, id)
		if err != nil {
			return nil, err
		}
		authors := make([]map[string]any, 0)
		for authorRows.Next() {
			var aid, name string
			var count int64
			if err := authorRows.Scan(&aid, &name, &count); err != nil {
				authorRows.Close()
				return nil, err
			}
			authors = append(authors, map[string]any{"id": aid, "name": name, "count": count})
		}
		if err := authorRows.Err(); err != nil {
			authorRows.Close()
			return nil, err
		}
		if err := authorRows.Close(); err != nil {
			return nil, err
		}
		longest, err := h.longestItems(ctx, id, "books", "m.duration")
		if err != nil {
			return nil, err
		}
		largest, err := h.largestItems(ctx, id, "books")
		if err != nil {
			return nil, err
		}
		return map[string]any{"totalAuthors": authorTotal, "authorsWithCount": authors, "totalGenres": len(genres), "genresWithCount": genres, "totalItems": total, "longestItems": longest, "largestItems": largest, "totalSize": size, "totalDuration": duration, "numAudioTracks": audioCount}, nil
	}
	if mediaType == "podcast" {
		err := h.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(li.size),0),COALESCE(SUM(json_extract(pe.audioFile,'$.duration')),0),COUNT(pe.id),COUNT(DISTINCT li.id) FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId LEFT JOIN podcastEpisodes pe ON pe.podcastId=p.id WHERE li.libraryId=?`, id).Scan(&size, &duration, &audioCount, &total)
		if err != nil {
			return nil, err
		}
		genres, err := h.genreCounts(ctx, id, "podcasts")
		if err != nil {
			return nil, err
		}
		longest, err := h.longestItems(ctx, id, "podcasts", "(SELECT SUM(json_extract(pe.audioFile,'$.duration')) FROM podcastEpisodes pe WHERE pe.podcastId=m.id)")
		if err != nil {
			return nil, err
		}
		largest, err := h.largestItems(ctx, id, "podcasts")
		if err != nil {
			return nil, err
		}
		return map[string]any{"totalGenres": len(genres), "genresWithCount": genres, "totalItems": total, "longestItems": longest, "largestItems": largest, "totalSize": size, "totalDuration": duration, "numAudioTracks": audioCount}, nil
	}
	return nil, fmt.Errorf("unsupported library media type %q", mediaType)
}

func (h *Handler) genreCounts(ctx context.Context, id, table string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := h.db.QueryContext(ctx, `SELECT j.value,COUNT(*) AS n FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id, json_each(m.genres) j WHERE li.libraryId=? AND json_valid(m.genres) GROUP BY j.value ORDER BY n DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var genre string
		var n int64
		if err := rows.Scan(&genre, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"genre": genre, "count": n})
	}
	return out, rows.Err()
}

func (h *Handler) longestItems(ctx context.Context, id, table, expression string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,m.title,`+expression+` AS duration FROM `+table+` m JOIN libraryItems li ON li.mediaId=m.id WHERE li.libraryId=? ORDER BY duration DESC LIMIT 10`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var itemID string
		var title sql.NullString
		var duration any
		if err := rows.Scan(&itemID, &title, &duration); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": itemID, "title": nullString(title), "duration": duration})
	}
	return out, rows.Err()
}

func (h *Handler) largestItems(ctx context.Context, id, table string) ([]map[string]any, error) {
	if table != "books" && table != "podcasts" {
		return nil, fmt.Errorf("invalid media table")
	}
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,m.title,li.size FROM libraryItems li JOIN `+table+` m ON m.id=li.mediaId WHERE li.libraryId=? ORDER BY li.size DESC LIMIT 10`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var itemID string
		var title sql.NullString
		var size any
		if err := rows.Scan(&itemID, &title, &size); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": itemID, "title": nullString(title), "size": size})
	}
	return out, rows.Err()
}

func (h *Handler) filterData(ctx context.Context, libraryID, mediaType string) (map[string]any, error) {
	sets := map[string]map[string]struct{}{"genres": {}, "tags": {}, "narrators": {}, "languages": {}, "publishers": {}, "publishedDecades": {}}
	data := map[string]any{"authors": []any{}, "genres": []string{}, "tags": []string{}, "series": []any{}, "narrators": []string{}, "languages": []string{}, "publishers": []string{}, "publishedDecades": []string{}, "bookCount": 0, "authorCount": 0, "seriesCount": 0, "podcastCount": 0, "numIssues": 0, "loadedAt": time.Now().UnixMilli()}
	if mediaType == "book" {
		rows, err := h.db.QueryContext(ctx, `SELECT b.tags,b.genres,b.narrators,b.publisher,b.publishedYear,b.language,li.isMissing,li.isInvalid FROM books b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=?`, libraryID)
		if err != nil {
			return nil, err
		}
		var bookCount, issues int
		for rows.Next() {
			var tags, genres, narrators []byte
			var publisher, year, language sql.NullString
			var missing, invalid int
			if err := rows.Scan(&tags, &genres, &narrators, &publisher, &year, &language, &missing, &invalid); err != nil {
				rows.Close()
				return nil, err
			}
			bookCount++
			if missing != 0 || invalid != 0 {
				issues++
			}
			collectSet(sets["tags"], decodeStrings(tags))
			collectSet(sets["genres"], decodeStrings(genres))
			collectSet(sets["narrators"], decodeStrings(narrators))
			if publisher.Valid {
				sets["publishers"][publisher.String] = struct{}{}
			}
			if language.Valid {
				sets["languages"][language.String] = struct{}{}
			}
			if year.Valid {
				if n, e := strconv.Atoi(year.String); e == nil && n > 0 && n < 3000 {
					sets["publishedDecades"][strconv.Itoa((n/10)*10)] = struct{}{}
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		data["bookCount"] = bookCount
		data["numIssues"] = issues
		for _, entry := range []struct{ table, key string }{{"authors", "authorCount"}, {"series", "seriesCount"}} {
			var n int
			if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+entry.table+` WHERE libraryId=?`, libraryID).Scan(&n); err != nil {
				return nil, err
			}
			data[entry.key] = n
		}
		series, err := h.db.QueryContext(ctx, `SELECT id,name FROM series WHERE libraryId=?`, libraryID)
		if err != nil {
			return nil, err
		}
		seriesValues := make([]map[string]any, 0)
		for series.Next() {
			var id string
			var name sql.NullString
			if err := series.Scan(&id, &name); err != nil {
				series.Close()
				return nil, err
			}
			seriesValues = append(seriesValues, map[string]any{"id": id, "name": orString(name, "No Title")})
		}
		if err := series.Err(); err != nil {
			series.Close()
			return nil, err
		}
		series.Close()
		sort.SliceStable(seriesValues, func(i, j int) bool {
			return strings.ToLower(seriesValues[i]["name"].(string)) < strings.ToLower(seriesValues[j]["name"].(string))
		})
		data["series"] = seriesValues
		authors, err := h.db.QueryContext(ctx, `SELECT id,name FROM authors WHERE libraryId=?`, libraryID)
		if err != nil {
			return nil, err
		}
		authorValues := make([]map[string]any, 0)
		for authors.Next() {
			var id string
			var name sql.NullString
			if err := authors.Scan(&id, &name); err != nil {
				authors.Close()
				return nil, err
			}
			authorValues = append(authorValues, map[string]any{"id": id, "name": nullString(name)})
		}
		if err := authors.Err(); err != nil {
			authors.Close()
			return nil, err
		}
		authors.Close()
		sort.SliceStable(authorValues, func(i, j int) bool {
			return strings.ToLower(stringValue(authorValues[i]["name"])) < strings.ToLower(stringValue(authorValues[j]["name"]))
		})
		data["authors"] = authorValues
	} else if mediaType == "podcast" {
		rows, err := h.db.QueryContext(ctx, `SELECT p.tags,p.genres,p.language FROM podcasts p JOIN libraryItems li ON li.mediaId=p.id WHERE li.libraryId=?`, libraryID)
		if err != nil {
			return nil, err
		}
		var n int
		for rows.Next() {
			var tags, genres []byte
			var language sql.NullString
			if err := rows.Scan(&tags, &genres, &language); err != nil {
				rows.Close()
				return nil, err
			}
			n++
			collectSet(sets["tags"], decodeStrings(tags))
			collectSet(sets["genres"], decodeStrings(genres))
			if language.Valid {
				sets["languages"][language.String] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		data["podcastCount"] = n
	} else {
		return nil, fmt.Errorf("unsupported media type %q", mediaType)
	}
	for name, set := range sets {
		values := make([]string, 0, len(set))
		for value := range set {
			values = append(values, value)
		}
		sort.SliceStable(values, func(i, j int) bool { return naturalLess(values[i], values[j]) })
		data[name] = values
	}
	return data, nil
}

func collectSet(set map[string]struct{}, values []string) {
	for _, value := range values {
		set[value] = struct{}{}
	}
}
func naturalLess(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }
func orString(s sql.NullString, fallback string) string {
	if !s.Valid || s.String == "" {
		return fallback
	}
	return s.String
}

func (h *Handler) authors(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=? AND mediaType='book'`, libraryID).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if exists == 0 {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,asin,name,description,imagePath,libraryId,lastFirst,createdAt,updatedAt FROM authors WHERE libraryId=?`, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type authorRow struct {
		id, libraryID                                 string
		asin, name, description, imagePath, lastFirst sql.NullString
		created, updated                              any
		numBooks                                      int64
	}
	data := make([]authorRow, 0)
	for rows.Next() {
		var a authorRow
		if err := rows.Scan(&a.id, &a.asin, &a.name, &a.description, &a.imagePath, &a.libraryID, &a.lastFirst, &a.created, &a.updated); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		data = append(data, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	rows.Close()
	admin := user.Type == "root" || user.Type == "admin"
	filtered := make([]authorRow, 0, len(data))
	for _, a := range data {
		var count int64
		where, args := userMediaClause(user, "book")
		q := `SELECT COUNT(DISTINCT b.id) FROM bookAuthors ba JOIN books b ON b.id=ba.bookId WHERE ba.authorId=?` + where
		params := append([]any{a.id}, args...)
		if err := h.db.QueryRowContext(r.Context(), q, params...).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		a.numBooks = count
		if admin || count > 0 {
			filtered = append(filtered, a)
		}
	}
	sortBy := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("desc") == "1"
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		var less bool
		switch sortBy {
		case "name":
			less = strings.ToLower(a.name.String) < strings.ToLower(b.name.String)
		case "lastFirst":
			less = strings.ToLower(a.lastFirst.String) < strings.ToLower(b.lastFirst.String)
		case "addedAt":
			less = stringValue(a.created) < stringValue(b.created)
		case "updatedAt":
			less = stringValue(a.updated) < stringValue(b.updated)
		case "numBooks":
			less = a.numBooks < b.numBooks
		default:
			return false
		}
		if desc {
			return !less
		}
		return less
	})
	limit, page, paginated := authorPagination(r)
	if sortBy == "numBooks" && paginated {
		start := page * limit
		end := start + limit
		if start > len(filtered) {
			filtered = []authorRow{}
		} else if end < len(filtered) {
			filtered = filtered[start:end]
		} else {
			filtered = filtered[start:]
		}
	} else if paginated {
		if limit == 0 {
			filtered = []authorRow{}
		} else {
			start := page * limit
			if start > len(filtered) {
				filtered = []authorRow{}
			} else {
				end := start + limit
				if end < len(filtered) {
					filtered = filtered[start:end]
				} else {
					filtered = filtered[start:]
				}
			}
		}
	}
	authors := make([]map[string]any, 0, len(filtered))
	for _, a := range filtered {
		created, e := dateMillis(a.created)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		updated, e := dateMillis(a.updated)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		authors = append(authors, map[string]any{"id": a.id, "asin": nullString(a.asin), "name": nullString(a.name), "description": nullString(a.description), "imagePath": nullString(a.imagePath), "libraryId": a.libraryID, "addedAt": deref(created), "updatedAt": deref(updated), "numBooks": a.numBooks, "lastFirst": nullString(a.lastFirst)})
	}
	if !paginated {
		writeJSON(w, r, http.StatusOK, map[string]any{"authors": authors})
		return
	}
	payload := map[string]any{"results": authors, "total": len(data), "limit": limit, "page": page, "sortDesc": desc, "minified": r.URL.Query().Get("minified") == "1"}
	if sortBy != "" {
		payload["sortBy"] = sortBy
	}
	if v := r.URL.Query().Get("filter"); v != "" {
		payload["filterBy"] = v
	}
	if v := r.URL.Query().Get("include"); v != "" {
		payload["include"] = v
	}
	writeJSON(w, r, http.StatusOK, payload)
}

func authorPagination(r *http.Request) (int, int, bool) {
	rawLimit, rawPage := r.URL.Query().Get("limit"), r.URL.Query().Get("page")
	if rawLimit == "" || rawPage == "" {
		return 0, 0, false
	}
	limit, e1 := strconv.Atoi(rawLimit)
	page, e2 := strconv.Atoi(rawPage)
	if e1 != nil || e2 != nil {
		return 0, 0, false
	}
	if limit < 0 || page < 0 {
		return 0, 0, false
	}
	return limit, page, true
}

func (h *Handler) series(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var mediaType string
	var settingsRaw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType,settings FROM libraries WHERE id=?`, libraryID).Scan(&mediaType, &settingsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if mediaType != "book" {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	filter := r.URL.Query().Get("filter")
	settings := decodeObject(settingsRaw)
	hideSingle, _ := settings["hideSingleBookSeries"].(bool)
	permissionSQL, permissionArgs := userMediaClause(user, "book")
	hasPermissionFilter := permissionSQL != ""
	rows, err := h.db.QueryContext(r.Context(), `SELECT s.id,s.name,s.nameIgnorePrefix,s.description,s.createdAt,s.updatedAt,s.libraryId,(SELECT COUNT(*) FROM bookSeries bs WHERE bs.seriesId=s.id) AS bookCount,(SELECT COALESCE(SUM(b.duration),0) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS duration,(SELECT MAX(b.createdAt) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS recent FROM series s WHERE s.libraryId=?`, libraryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type seriesRow struct {
		id, libraryID                 string
		name, nameIgnore, description sql.NullString
		created, updated, recent      any
		count                         int64
		duration                      float64
	}
	all := make([]seriesRow, 0)
	for rows.Next() {
		var s seriesRow
		if err := rows.Scan(&s.id, &s.name, &s.nameIgnore, &s.description, &s.created, &s.updated, &s.libraryID, &s.count, &s.duration, &s.recent); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	rows.Close()
	if hideSingle || hasPermissionFilter {
		visibleSeries := all[:0]
		for _, s := range all {
			if hideSingle && s.count <= 1 {
				continue
			}
			if hasPermissionFilter {
				var visible int64
				query := `SELECT COUNT(*) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=?` + permissionSQL
				args := append([]any{s.id}, permissionArgs...)
				if err := h.db.QueryRowContext(r.Context(), query, args...).Scan(&visible); err != nil {
					writeError(w, http.StatusInternalServerError, "database read failed")
					return
				}
				if visible == 0 {
					continue
				}
			}
			visibleSeries = append(visibleSeries, s)
		}
		all = visibleSeries
	}
	if filter != "" {
		filtered := all[:0]
		for _, s := range all {
			ok, e := h.seriesMatchesFilter(r.Context(), s.id, filter, user)
			if e != nil {
				writeError(w, 500, "database read failed")
				return
			}
			if ok {
				filtered = append(filtered, s)
			}
		}
		all = filtered
	}
	sortBy := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("desc") == "1"
	ignorePrefix := false
	var serverSettings []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&serverSettings); err == nil {
		ignorePrefix, _ = decodeObject(serverSettings)["sortingIgnorePrefix"].(bool)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		var less bool
		switch sortBy {
		case "name":
			an, bn := a.name.String, b.name.String
			if ignorePrefix {
				an = a.nameIgnore.String
				bn = b.nameIgnore.String
			}
			less = naturalLess(an, bn)
		case "addedAt":
			less = dateSortKey(a.created) < dateSortKey(b.created)
		case "numBooks":
			less = a.count < b.count
		case "totalDuration":
			less = a.duration < b.duration
		case "lastBookAdded":
			less = dateSortKey(a.recent) < dateSortKey(b.recent)
		case "lastBookUpdated":
			return false
		case "random":
			return i < j
		default:
			return false
		}
		if desc {
			return !less
		}
		return less
	})
	total := len(all)
	page, limit, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if limit > 0 {
		start := page * limit
		if start > len(all) {
			all = nil
		} else {
			end := start + limit
			if end < len(all) {
				all = all[start:end]
			} else {
				all = all[start:]
			}
		}
	}
	results := make([]map[string]any, 0, len(all))
	for _, s := range all {
		created, e := dateMillis(s.created)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		updated, e := dateMillis(s.updated)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		entry := map[string]any{"id": s.id, "name": nullString(s.name), "nameIgnorePrefix": nullString(s.nameIgnore), "description": nullString(s.description), "addedAt": deref(created), "updatedAt": deref(updated), "libraryId": s.libraryID}
		if sortBy == "totalDuration" && s.duration != 0 {
			entry["totalDuration"] = s.duration
		}
		books, err := h.seriesBooks(r.Context(), s.id, user)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		entry["books"] = books
		if includeHas(r, "rssfeed") {
			if feed, ok, e := h.feedMinified(r.Context(), "series", s.id); e != nil {
				writeError(w, 500, "database read failed")
				return
			} else if ok {
				entry["rssFeed"] = feed
			}
		}
		results = append(results, entry)
	}
	payload := map[string]any{"results": results, "total": total, "limit": limit, "page": page, "sortDesc": desc, "minified": r.URL.Query().Get("minified") == "1", "include": normalizedInclude(r)}
	if filter != "" {
		payload["filterBy"] = filter
	}
	if sortBy != "" {
		payload["sortBy"] = sortBy
	}
	writeJSON(w, r, http.StatusOK, payload)
}

func (h *Handler) seriesBooks(ctx context.Context, seriesID string, user store.User) ([]map[string]any, error) {
	permission, args := userMediaClause(user, "book")
	queryArgs := append([]any{seriesID}, args...)
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,bs.sequence FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id WHERE bs.seriesId=?`+permission, queryArgs...)
	if err != nil {
		return nil, err
	}
	type bookRef struct{ id, sequence string }
	refs := make([]bookRef, 0)
	for rows.Next() {
		var ref bookRef
		var sequence sql.NullString
		if err := rows.Scan(&ref.id, &sequence); err != nil {
			rows.Close()
			return nil, err
		}
		ref.sequence = sequence.String
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].sequence == "" {
			return false
		}
		if refs[j].sequence == "" {
			return true
		}
		return naturalLess(refs[i].sequence, refs[j].sequence)
	})
	result := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		item, err := h.ItemMinifiedJSON(ctx, ref.id)
		if err != nil {
			return nil, err
		}
		if media, ok := item["media"].(map[string]any); ok {
			// Series associations serialize their book via Book.toOldJSONMinified,
			// not the expanded media shape used by ItemMinifiedJSON.
			media, err = h.bookJSON(ctx, stringValue(media["id"]), ref.id, true)
			if err != nil {
				return nil, err
			}
			delete(media, "libraryItemId")
			delete(media, "tracks")
			if metadata, ok := media["metadata"].(map[string]any); ok {
				delete(metadata, "descriptionPlain")
			}
			item["media"] = media
		}
		result = append(result, item)
	}
	return result, nil
}

func (h *Handler) seriesByID(w http.ResponseWriter, r *http.Request, user store.User, libraryID, seriesID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var name, nameIgnore, description sql.NullString
	var created, updated any
	var lib string
	err := h.db.QueryRowContext(r.Context(), `SELECT name,nameIgnorePrefix,description,createdAt,updatedAt,libraryId FROM series WHERE id=?`, seriesID).Scan(&name, &nameIgnore, &description, &created, &updated, &lib)
	if errors.Is(err, sql.ErrNoRows) || lib != libraryID {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	visibleSQL, args := userMediaClause(user, "book")
	var count int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=?`+visibleSQL, append([]any{seriesID}, args...)...).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "")
		return
	}
	createdMs, e := dateMillis(created)
	if e != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	updatedMs, e := dateMillis(updated)
	if e != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	payload := map[string]any{"id": seriesID, "name": nullString(name), "nameIgnorePrefix": nullString(nameIgnore), "description": nullString(description), "addedAt": deref(createdMs), "updatedAt": deref(updatedMs), "libraryId": lib}
	if includeHas(r, "rssfeed") {
		if feed, ok, e := h.feedMinified(r.Context(), "series", seriesID); e != nil {
			writeError(w, 500, "database read failed")
			return
		} else if ok {
			payload["rssFeed"] = feed
		}
	}
	if includeHas(r, "progress") {
		books, e := h.visibleSeriesItems(r.Context(), seriesID, user)
		if e != nil {
			writeError(w, 500, "database read failed")
			return
		}
		ids := make([]string, 0, len(books))
		finished := make([]string, 0)
		for _, book := range books {
			itemID := stringValue(book["id"])
			ids = append(ids, itemID)
			media := book["media"].(map[string]any)
			mediaID := stringValue(media["id"])
			var isFinished bool
			_ = h.db.QueryRowContext(r.Context(), `SELECT COALESCE(isFinished,0) FROM mediaProgresses WHERE userId=? AND mediaItemId=?`, user.ID, mediaID).Scan(&isFinished)
			if isFinished {
				finished = append(finished, itemID)
			}
		}
		payload["progress"] = map[string]any{"libraryItemIds": ids, "libraryItemIdsFinished": finished, "isFinished": len(finished) >= len(ids)}
	}
	writeJSON(w, r, http.StatusOK, payload)
}

func dateSortKey(v any) int64 {
	ms, _ := dateMillis(v)
	if ms == nil {
		return 0
	}
	return *ms
}

func (h *Handler) libraryFolders(ctx context.Context, libraryID string) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,path,libraryId,createdAt FROM libraryFolders WHERE libraryId=?`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id, path, parent string
		var created any
		if err := rows.Scan(&id, &path, &parent, &created); err != nil {
			return nil, err
		}
		ms, err := dateMillis(created)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "fullPath": path, "libraryId": parent, "addedAt": deref(ms)})
	}
	return result, rows.Err()
}

func (h *Handler) libraryItems(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	var mediaType string
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	page, limit, err := pagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.URL.Query().Get("collapseseries") == "1" {
		writeError(w, http.StatusNotImplemented, "collapsed series are not implemented")
		return
	}
	include := normalizedInclude(r)
	ignorePrefix := false
	var settingsJSON []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsJSON); err == nil {
		ignorePrefix, _ = decodeObject(settingsJSON)["sortingIgnorePrefix"].(bool)
	}
	filterValue := r.URL.Query().Get("filter")
	order, orderArgs, err := orderClauseForRequest(r.URL.Query().Get("sort"), r.URL.Query().Get("desc") == "1", ignorePrefix, mediaType, filterValue, user)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var total int
	mediaTable := "books"
	if mediaType == "podcast" {
		mediaTable = "podcasts"
	} else if mediaType != "book" {
		writeError(w, http.StatusNotImplemented, "unsupported library media type")
		return
	}
	filterSQL, filterArgs, err := filterClause(filterValue, mediaType, user)
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	permissionSQL, permissionArgs := userMediaClause(user, mediaType)
	whereSQL := filterSQL + permissionSQL
	countArgs := append([]any{libraryID, mediaType}, filterArgs...)
	countArgs = append(countArgs, permissionArgs...)
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraryItems li JOIN `+mediaTable+` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?`+whereSQL, countArgs...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	query := `SELECT li.id,li.ino,li.libraryId,li.libraryFolderId,li.path,li.relPath,li.isFile,li.mtime,li.ctime,li.birthtime,li.createdAt,li.updatedAt,li.lastScan,li.lastScanVersion,li.isMissing,li.isInvalid,li.mediaType,li.size,li.extraData,li.libraryFiles,li.mediaId
	FROM libraryItems li JOIN ` + mediaTable + ` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?` + whereSQL + order
	args := append([]any{libraryID, mediaType}, filterArgs...)
	args = append(args, permissionArgs...)
	args = append(args, orderArgs...)
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, page*limit)
	}
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	type libraryItemRow struct {
		item    map[string]any
		mediaID string
	}
	items := make([]libraryItemRow, 0)
	for rows.Next() {
		item, mediaID, err := scanItemRow(rows)
		if err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		items = append(items, libraryItemRow{item: item, mediaID: mediaID})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	results := make([]map[string]any, 0, len(items))
	for _, row := range items {
		delete(row.item, "lastScan")
		delete(row.item, "scanVersion")
		delete(row.item, "libraryFiles")
		var media map[string]any
		if mediaType == "book" {
			media, err = h.bookMinifiedJSON(r.Context(), row.mediaID, row.item["id"].(string))
		} else {
			media, err = h.podcastJSON(r.Context(), row.mediaID, row.item["id"].(string), true)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		row.item["media"] = media
		if includeHas(r, "rssfeed") {
			if feed, ok, feedErr := h.feedMinified(r.Context(), mediaType, row.mediaID); feedErr != nil {
				writeError(w, http.StatusInternalServerError, "database read failed")
				return
			} else if ok {
				row.item["rssFeed"] = feed
			}
		}
		if mediaType == "podcast" && includeHas(r, "numepisodesincomplete") {
			var totalEpisodes, finishedEpisodes int64
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?`, row.mediaID).Scan(&totalEpisodes); err != nil {
				writeError(w, http.StatusInternalServerError, "database read failed")
				return
			}
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM podcastEpisodes pe JOIN mediaProgresses mp ON mp.mediaItemId=pe.id AND mp.userId=? WHERE pe.podcastId=? AND mp.isFinished=1`, user.ID, row.mediaID).Scan(&finishedEpisodes); err != nil {
				writeError(w, http.StatusInternalServerError, "database read failed")
				return
			}
			row.item["numEpisodesIncomplete"] = totalEpisodes - finishedEpisodes
		}
		results = append(results, row.item)
	}
	payload := map[string]any{
		"results": results, "total": total, "limit": limit, "page": page,
		"sortDesc": r.URL.Query().Get("desc") == "1", "mediaType": mediaType,
		"minified": r.URL.Query().Get("minified") == "1", "collapseseries": false, "include": include,
		"offset": page * limit,
	}
	if v := r.URL.Query().Get("sort"); v != "" {
		payload["sortBy"] = v
	}
	if v := r.URL.Query().Get("filter"); v != "" {
		payload["filterBy"] = v
	}
	writeJSON(w, r, http.StatusOK, payload)
}

func (h *Handler) item(w http.ResponseWriter, r *http.Request, user store.User, itemID string) {
	item, err := h.ItemJSON(r.Context(), itemID, r.URL.Query().Get("expanded") == "1")
	if errors.Is(err, sql.ErrNoRows) {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, item["libraryId"].(string)) {
		writeStatus(w, http.StatusForbidden)
		return
	}
	if !userCanAccessItem(user, item) {
		writeStatus(w, http.StatusForbidden)
		return
	}
	writeJSON(w, r, http.StatusOK, item)
}

func userCanAccessItem(user store.User, item map[string]any) bool {
	media, _ := item["media"].(map[string]any)
	if media == nil {
		return false
	}
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	metadata, _ := media["metadata"].(map[string]any)
	if item["mediaType"] == "book" {
		if value, exists := metadata["explicit"]; exists {
			metadata = map[string]any{"explicit": value}
		}
	}
	if metadata == nil {
		return false
	}
	if explicit, _ := metadata["explicit"].(bool); explicit && permissions["accessExplicitContent"] != true {
		return false
	}
	if permissions["accessAllTags"] == true {
		return true
	}
	tags, _ := media["tags"].([]string)
	if tags == nil {
		if raw, ok := media["tags"].([]any); ok {
			for _, tag := range raw {
				if value, ok := tag.(string); ok {
					tags = append(tags, value)
				}
			}
		}
	}
	selected := make(map[string]bool)
	for _, tag := range stringValues(permissions["itemTagsSelected"]) {
		selected[tag] = true
	}
	if permissions["selectedTagsNotAccessible"] == true {
		for _, tag := range tags {
			if selected[tag] {
				return false
			}
		}
		return true
	}
	for _, tag := range tags {
		if selected[tag] {
			return true
		}
	}
	return false
}

func stringValues(value any) []string {
	var values []string
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		for _, item := range items {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	return values
}

// ItemJSON returns the upstream legacy full item representation, or the
// expanded minified superset when expanded is true. Callers enforce permissions.
func (h *Handler) ItemJSON(ctx context.Context, itemID string, expanded bool) (map[string]any, error) {
	item, mediaID, err := h.loadItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if item["mediaType"] == "book" {
		if expanded {
			return h.expandedBookItem(ctx, item, mediaID, itemID)
		}
		media, err := h.bookJSON(ctx, mediaID, itemID, false)
		if err != nil {
			return nil, err
		}
		item["media"] = media
		delete(item, "numFiles")
		delete(item, "size")
		return item, nil
	}
	if item["mediaType"] == "podcast" {
		if expanded {
			return h.expandedPodcastItem(ctx, item, mediaID, itemID)
		}
		media, err := h.podcastJSON(ctx, mediaID, itemID, false)
		if err != nil {
			return nil, err
		}
		item["media"] = media
		delete(item, "numFiles")
		delete(item, "size")
		return item, nil
	}
	return nil, fmt.Errorf("unsupported media type %q", item["mediaType"])
}

// ItemMinifiedJSON returns the upstream shelf/collection item shape.
func (h *Handler) ItemMinifiedJSON(ctx context.Context, itemID string) (map[string]any, error) {
	item, mediaID, err := h.loadItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	delete(item, "lastScan")
	delete(item, "scanVersion")
	delete(item, "libraryFiles")
	var media map[string]any
	if item["mediaType"] == "book" {
		media, err = h.bookJSON(ctx, mediaID, itemID, true)
	} else if item["mediaType"] == "podcast" {
		media, err = h.podcastJSON(ctx, mediaID, itemID, true)
	} else {
		return nil, fmt.Errorf("unsupported media type %q", item["mediaType"])
	}
	if err != nil {
		return nil, err
	}
	item["media"] = media
	return item, nil
}

func (h *Handler) loadItem(ctx context.Context, itemID string) (map[string]any, string, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT li.id,li.ino,li.libraryId,li.libraryFolderId,li.path,li.relPath,li.isFile,li.mtime,li.ctime,li.birthtime,li.createdAt,li.updatedAt,li.lastScan,li.lastScanVersion,li.isMissing,li.isInvalid,li.mediaType,li.size,li.extraData,li.libraryFiles,li.mediaId FROM libraryItems li WHERE li.id=?`, itemID)
	if err != nil {
		return nil, "", err
	}
	if !rows.Next() {
		rows.Close()
		return nil, "", sql.ErrNoRows
	}
	item, mediaID, err := scanItemRow(rows)
	if err != nil {
		rows.Close()
		return nil, "", err
	}
	if err := rows.Close(); err != nil {
		return nil, "", err
	}
	return item, mediaID, nil
}

func (h *Handler) mediaFile(w http.ResponseWriter, r *http.Request, user store.User, itemID, ino string, download bool) {
	var libraryID string
	var raw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeStatus(w, http.StatusForbidden)
		return
	}
	var target string
	for _, file := range decodeArray(raw) {
		if stringValue(file["ino"]) == ino {
			if meta, ok := file["metadata"].(map[string]any); ok {
				target = stringValue(meta["path"])
			}
			break
		}
	}
	if target == "" {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if download {
		var permissions map[string]any
		_ = json.Unmarshal(user.Permissions, &permissions)
		if !user.IsActive || permissions["download"] != true {
			writeStatus(w, http.StatusForbidden)
			return
		}
		if mimeType := audioMIMEType(filepath.Ext(target)); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}
	if !h.pathAllowed(r.Context(), target, libraryID, false) {
		if download {
			if _, statErr := os.Stat(target); statErr != nil {
				if os.IsNotExist(statErr) {
					sendDownloadText(w, http.StatusNotFound, "File not found")
				} else {
					sendDownloadText(w, http.StatusInternalServerError, "Download failed")
				}
				return
			}
		}
		writeStatus(w, http.StatusNotFound)
		return
	}
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(target)}))
	}
	h.servePath(w, r, target)
}

func (h *Handler) ebookFile(w http.ResponseWriter, r *http.Request, user store.User, itemID, fileID string) {
	var libraryID, mediaType, mediaID string
	var raw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId,libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &mediaID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	target := ""
	if fileID != "" {
		for _, file := range decodeArray(raw) {
			if stringValue(file["ino"]) == fileID && fileType(fileMetadataExt(file)) == "ebook" {
				target = filePath(file)
				break
			}
		}
		if target == "" {
			writeError(w, http.StatusBadRequest, "Invalid ebook file id")
			return
		}
	} else if mediaType == "book" {
		var ebookRaw []byte
		if err := h.db.QueryRowContext(r.Context(), `SELECT ebookFile FROM books WHERE id=?`, mediaID).Scan(&ebookRaw); err != nil {
			writeError(w, http.StatusNotFound, "")
			return
		}
		target = filePath(decodeObject(ebookRaw))
	}
	if target == "" {
		writeError(w, http.StatusNotFound, "")
		return
	}
	if !h.pathAllowed(r.Context(), target, libraryID, false) {
		writeError(w, http.StatusNotFound, "")
		return
	}
	h.servePath(w, r, target)
}

func (h *Handler) cover(w http.ResponseWriter, r *http.Request, itemID string) {
	var libraryID, mediaType, mediaID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if r.URL.Query().Get("ts") != "" {
		// LibraryItemController.getCover sets the cache header before any
		// existence checks when a cache-busting ts is present.
		w.Header().Set("Cache-Control", "private, max-age=86400")
	}
	if r.URL.Query().Get("raw") != "" {
		var target string
		if mediaType == "book" {
			err = h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM books WHERE id=?`, mediaID).Scan(&target)
		} else if mediaType == "podcast" {
			err = h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM podcasts WHERE id=?`, mediaID).Scan(&target)
		}
		if err != nil || target == "" {
			writeStatus(w, http.StatusNotFound)
			return
		}
		if !h.pathAllowed(r.Context(), target, libraryID, true) {
			writeStatus(w, http.StatusNotFound)
			return
		}
		h.coverRaw(w, r, target)
		return
	}
	h.handleCoverCache(w, r, itemID)
}

func (h *Handler) pathAllowed(ctx context.Context, target, libraryID string, metadataOK bool) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	roots := make([]string, 0)
	rows, err := h.db.QueryContext(ctx, `SELECT path FROM libraryFolders WHERE libraryId=?`, libraryID)
	if err == nil {
		for rows.Next() {
			var root string
			if rows.Scan(&root) == nil {
				roots = append(roots, root)
			}
		}
		rows.Close()
	}
	if metadataOK && h.cfg.MetadataPath != "" {
		roots = append(roots, h.cfg.MetadataPath)
	}
	for _, root := range roots {
		rootAbs, e := filepath.Abs(root)
		if e != nil {
			continue
		}
		rootResolved, e := filepath.EvalSymlinks(rootAbs)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(rootResolved, resolved)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (h *Handler) servePath(w http.ResponseWriter, r *http.Request, target string) {
	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "File not found")
		} else {
			writeError(w, http.StatusInternalServerError, "File read failed")
		}
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "File read failed")
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "")
		return
	}
	ext := strings.ToLower(filepath.Ext(target))
	media := map[string]string{".m4b": "audio/mp4", ".m4a": "audio/mp4", ".aac": "audio/aac", ".mp3": "audio/mpeg", ".flac": "audio/flac", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav"}[ext]
	if media == "" {
		media = mime.TypeByExtension(ext)
	}
	if media != "" {
		w.Header().Set("Content-Type", media)
	}
	http.ServeContent(w, r, filepath.Base(target), info.ModTime(), f)
}

func filePath(file map[string]any) string {
	meta, _ := file["metadata"].(map[string]any)
	return stringValue(meta["path"])
}
func fileMetadataExt(file map[string]any) any {
	meta, _ := file["metadata"].(map[string]any)
	return meta["ext"]
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request, user store.User, libraryID string) {
	if !userCanAccessLibrary(user, libraryID) {
		writeError(w, http.StatusForbidden, "")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, `Invalid request. Query param "q" must be a string`)
		return
	}
	limit := 12
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 0 {
			limit = n
		}
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType); err != nil {
		writeError(w, http.StatusNotFound, "Library not found")
		return
	}
	table := "books"
	if mediaType == "podcast" {
		table = "podcasts"
	} else if mediaType != "book" {
		writeError(w, http.StatusNotImplemented, "unsupported library media type")
		return
	}
	permissionSQL, permissionArgs := userMediaClause(user, mediaType)
	querySQL := `SELECT li.id FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId WHERE li.libraryId=? AND (lower(b.title) LIKE lower(?) OR `
	if mediaType == "book" {
		querySQL += `lower(b.subtitle) LIKE lower(?) OR lower(b.asin) LIKE lower(?) OR lower(b.isbn) LIKE lower(?))`
	} else {
		querySQL += `lower(b.author) LIKE lower(?) OR lower(b.itunesId) LIKE lower(?) OR lower(b.itunesArtistId) LIKE lower(?))`
	}
	querySQL += permissionSQL
	pattern := "%" + query + "%"
	args := []any{libraryID, pattern, pattern, pattern, pattern}
	args = append(args, permissionArgs...)
	rows, err := h.db.QueryContext(r.Context(), querySQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		ids = append(ids, id)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item, e := h.ItemJSON(r.Context(), id, true)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "database read failed")
			return
		}
		if mediaType == "podcast" {
			m := item["media"].(map[string]any)
			m["episodes"] = []any{}
			m["numEpisodes"] = 0
			m["size"] = 0
		}
		items = append(items, map[string]any{"libraryItem": item})
	}
	tags, err := h.searchJSONValues(r.Context(), libraryID, table, "tags", query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	genres, err := h.searchJSONValues(r.Context(), libraryID, table, "genres", query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	if mediaType == "podcast" {
		writeJSON(w, r, http.StatusOK, map[string]any{"podcast": items, "tags": tags, "genres": genres, "episodes": []any{}})
		return
	}
	narrators, err := h.searchJSONValues(r.Context(), libraryID, table, "narrators", query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	authors, err := h.searchAuthors(r.Context(), libraryID, query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	series, err := h.searchSeries(r.Context(), libraryID, query, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database read failed")
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"book": items, "narrators": narrators, "tags": tags, "genres": genres, "series": series, "authors": authors})
}

func (h *Handler) searchJSONValues(ctx context.Context, libraryID, table, column, query string, limit int) ([]map[string]any, error) {
	if (table != "books" && table != "podcasts") || (column != "tags" && column != "genres" && column != "narrators") {
		return nil, fmt.Errorf("unsupported search field")
	}
	if column == "narrators" && table != "books" {
		return []map[string]any{}, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT j.value,COUNT(*) FROM `+table+` b JOIN libraryItems li ON li.mediaId=b.id,json_each(b.`+column+`) j WHERE li.libraryId=? AND lower(CAST(j.value AS TEXT)) LIKE lower(?) GROUP BY j.value ORDER BY COUNT(*) DESC LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var value string
		var n int64
		if err := rows.Scan(&value, &n); err != nil {
			return nil, err
		}
		if column == "narrators" {
			out = append(out, map[string]any{"name": value, "numBooks": n})
		} else {
			out = append(out, map[string]any{"name": value, "numItems": n})
		}
	}
	return out, rows.Err()
}

func (h *Handler) searchAuthors(ctx context.Context, libraryID, query string, limit int) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,name,asin,description,imagePath,libraryId,createdAt,updatedAt,(SELECT COUNT(*) FROM bookAuthors ba WHERE ba.authorId=authors.id) FROM authors WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, library string
		var name, asin, description, image sql.NullString
		var created, updated any
		var count int64
		if err := rows.Scan(&id, &name, &asin, &description, &image, &library, &created, &updated, &count); err != nil {
			return nil, err
		}
		added, e := dateMillis(created)
		if e != nil {
			return nil, e
		}
		updatedMs, e := dateMillis(updated)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "name": nullString(name), "asin": nullString(asin), "description": nullString(description), "imagePath": nullString(image), "libraryId": library, "addedAt": deref(added), "updatedAt": deref(updatedMs), "numBooks": count})
	}
	return out, rows.Err()
}

func (h *Handler) searchSeries(ctx context.Context, libraryID, query string, limit int) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,name,nameIgnorePrefix,description,createdAt,updatedAt,libraryId FROM series WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, library string
		var name, nameIgnore, description sql.NullString
		var created, updated any
		if err := rows.Scan(&id, &name, &nameIgnore, &description, &created, &updated, &library); err != nil {
			return nil, err
		}
		added, e := dateMillis(created)
		if e != nil {
			return nil, e
		}
		updatedMs, e := dateMillis(updated)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"series": map[string]any{"id": id, "name": nullString(name), "nameIgnorePrefix": nullString(nameIgnore), "description": nullString(description), "addedAt": deref(added), "updatedAt": deref(updatedMs), "libraryId": library}, "books": []any{}})
	}
	return out, rows.Err()
}
