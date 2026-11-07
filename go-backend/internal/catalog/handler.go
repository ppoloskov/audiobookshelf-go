// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	bookdomain "audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/filterdata"
	itemapi "audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

func recordToMap(record itemapi.Record) map[string]any {
	item := map[string]any{
		"id": record.ID, "ino": store.NullString(record.Ino), "oldLibraryItemId": valueOrNull(record.OldLibraryItemID),
		"libraryId": record.LibraryID, "folderId": store.NullString(record.FolderID), "path": record.Path, "relPath": record.RelPath,
		"isFile": record.IsFile, "addedAt": deref(record.AddedAt), "updatedAt": deref(record.UpdatedAt), "isMissing": record.IsMissing, "isInvalid": record.IsInvalid,
		"mediaType": record.MediaType, "scanVersion": store.NullString(record.ScanVersion), "libraryFiles": record.LibraryFiles, "numFiles": len(record.LibraryFiles),
	}
	for key, value := range map[string]*int64{"mtimeMs": record.MTimeMS, "ctimeMs": record.CTimeMS, "birthtimeMs": record.BirthtimeMS, "lastScan": record.LastScan} {
		if value != nil {
			item[key] = *value
		}
	}
	if record.Size != nil {
		item["size"] = record.Size
	}
	return item
}

// podcastBase builds the keys shared by every upstream Podcast serializer
// (server/models/Podcast.js:431 toOldJSON, :459 toOldJSONMinified). It carries
// no size, numEpisodes, episodes or libraryItemId; each caller adds the ones
// its upstream shape has.
func (h *Handler) podcastBase(ctx context.Context, podcastID string) (map[string]any, error) {
	var id, title, author, releaseDate, feedURL, imageURL, description, itunesPageURL, itunesID, itunesArtistID, language, podcastType, autoSchedule, coverPath sql.NullString
	var explicit, autoDownload int64
	var lastCheck any
	var maxKeep, maxNew sql.NullInt64
	var tagsRaw, genresRaw []byte
	err := h.db.QueryRowContext(ctx, `SELECT id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres FROM podcasts WHERE id=?`, podcastID).Scan(&id, &title, &author, &releaseDate, &feedURL, &imageURL, &description, &itunesPageURL, &itunesID, &itunesArtistID, &language, &podcastType, &explicit, &autoDownload, &autoSchedule, &lastCheck, &maxKeep, &maxNew, &coverPath, &tagsRaw, &genresRaw)
	if err != nil {
		return nil, err
	}
	check, err := dbtime.OptionalMillis(lastCheck)
	if err != nil {
		return nil, err
	}
	if check != nil && *check == 0 {
		check = nil
	}
	metadata := map[string]any{"title": store.NullString(title), "author": store.NullString(author), "description": store.NullString(description), "releaseDate": store.NullString(releaseDate), "genres": decodeStrings(genresRaw), "feedUrl": store.NullString(feedURL), "imageUrl": store.NullString(imageURL), "itunesPageUrl": store.NullString(itunesPageURL), "itunesId": store.NullString(itunesID), "itunesArtistId": store.NullString(itunesArtistID), "language": store.NullString(language), "explicit": explicit != 0, "type": store.NullString(podcastType)}
	return map[string]any{"id": id.String, "metadata": metadata, "coverPath": store.NullString(coverPath), "tags": decodeStrings(tagsRaw), "autoDownloadEpisodes": autoDownload != 0, "autoDownloadSchedule": store.NullString(autoSchedule), "lastEpisodeCheck": deref(check), "maxEpisodesToKeep": maxKeep.Int64, "maxNewEpisodesToDownload": maxNew.Int64}, nil
}

// podcastOldJSON is Podcast.toOldJSON (server/models/Podcast.js:431), served
// by GET /api/items/:id without expanded=1: episodes in their plain shape and
// neither size nor numEpisodes.
func (h *Handler) podcastOldJSON(ctx context.Context, podcastID, itemID string) (map[string]any, error) {
	media, err := h.podcastBase(ctx, podcastID)
	if err != nil {
		return nil, err
	}
	episodes, err := h.podcastEpisodes(ctx, podcastID, itemID, false)
	if err != nil {
		return nil, err
	}
	media["libraryItemId"] = itemID
	media["episodes"] = episodes
	return media, nil
}

// podcastMinifiedJSON is Podcast.toOldJSONMinified (server/models/Podcast.js:459)
// for a podcast loaded WITHOUT its episodes, which is how every shelf,
// playlist and list query upstream loads it: numEpisodes and size read 0
// unless the caller overrides them (LibraryItem.getByFilterAndSort does,
// server/models/LibraryItem.js:307-312).
func (h *Handler) podcastMinifiedJSON(ctx context.Context, podcastID string) (map[string]any, error) {
	media, err := h.podcastBase(ctx, podcastID)
	if err != nil {
		return nil, err
	}
	metadata := media["metadata"].(map[string]any)
	metadata["titleIgnorePrefix"] = h.titlePrefix(ctx, stringValue(metadata["title"]))
	media["numEpisodes"] = 0
	media["size"] = 0
	return media, nil
}

// numEpisodesIncomplete is podcasts.numEpisodes minus the user's finished
// progress rows tagged with the podcast
// (libraryItemsPodcastFilters.getFilteredLibraryItems,
// server/utils/queries/libraryItemsPodcastFilters.js:221-229). Callers set the
// key only when it is non-zero, as LibraryItem.getByFilterAndSort does
// (server/models/LibraryItem.js:313).
func (h *Handler) numEpisodesIncomplete(ctx context.Context, userID, podcastID string) (int64, error) {
	var numEpisodes, finished int64
	if err := h.db.QueryRowContext(ctx, `SELECT COALESCE(numEpisodes,0) FROM podcasts WHERE id=?`, podcastID).Scan(&numEpisodes); err != nil {
		return 0, err
	}
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mediaProgresses WHERE userId=? AND podcastId=? AND isFinished=1`, userID, podcastID).Scan(&finished); err != nil {
		return 0, err
	}
	return numEpisodes - finished, nil
}

// podcastListJSON is the media object of a library list row for a podcast:
// the minified shape with numEpisodes read from the podcasts column and size
// taken from the library item when the unloaded podcast reports 0
// (LibraryItem.getByFilterAndSort, server/models/LibraryItem.js:310-312).
func (h *Handler) podcastListJSON(ctx context.Context, podcastID string, item map[string]any) (map[string]any, error) {
	media, err := h.podcastMinifiedJSON(ctx, podcastID)
	if err != nil {
		return nil, err
	}
	var numEpisodes sql.NullInt64
	if err := h.db.QueryRowContext(ctx, `SELECT numEpisodes FROM podcasts WHERE id=?`, podcastID).Scan(&numEpisodes); err != nil {
		return nil, err
	}
	media["numEpisodes"] = numEpisodes.Int64
	if size := number(item["size"]); size != 0 {
		media["size"] = size
	}
	return media, nil
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
		pub, err := dbtime.OptionalMillis(e.published)
		if err != nil {
			return nil, err
		}
		if pub != nil && *pub == 0 {
			pub = nil
		}
		created, err := dbtime.OptionalMillis(e.created)
		if err != nil {
			return nil, err
		}
		updated, err := dbtime.OptionalMillis(e.updated)
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
			enclosure = map[string]any{"url": e.enclosureURL.String, "type": store.NullString(e.enclosureType), "length": length}
		}
		audio := decodeObject(e.audio)
		chapters := decodeArray(e.chapters)
		ep := map[string]any{"libraryItemId": itemID, "podcastId": podcastID, "id": e.id, "oldEpisodeId": valueOrNull(stringValue(extra["oldEpisodeId"])), "index": nullInt(e.index), "season": store.NullString(e.season), "episode": store.NullString(e.episode), "episodeType": store.NullString(e.episodeType), "title": store.NullString(e.title), "subtitle": store.NullString(e.subtitle), "description": store.NullString(e.description), "enclosure": enclosure, "guid": valueOrNull(stringValue(extra["guid"])), "pubDate": store.NullString(e.pubDate), "chapters": chapters, "audioFile": audio, "publishedAt": deref(pub), "addedAt": deref(created), "updatedAt": deref(updated)}
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

// expandedPodcastItem is Podcast.toOldJSONExpanded (server/models/Podcast.js:482):
// the minified keys with the loaded episodes' count and size, plus
// libraryItemId and the expanded episodes.
func (h *Handler) expandedPodcastItem(ctx context.Context, item map[string]any, podcastID, itemID string) (map[string]any, error) {
	media, err := h.podcastMinifiedJSON(ctx, podcastID)
	if err != nil {
		return nil, err
	}
	episodes, err := h.podcastEpisodes(ctx, podcastID, itemID, true)
	if err != nil {
		return nil, err
	}
	media["numEpisodes"] = len(episodes)
	media["size"] = episodesSize(episodes)
	media["libraryItemId"] = itemID
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
	book, err := bookdomain.Load(ctx, h.db, bookID)
	if err != nil {
		return nil, err
	}
	media, err := book.Expanded(itemID, h.bookTitlePrefix(ctx, book.Title()))
	if err != nil {
		return nil, err
	}
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

// podcastFilterGroups are the filter groups LibraryFilters splits at the
// first dot (server/utils/queries/libraryFilters.js:35). Every other filter
// string is taken whole as the group, so "explicit" only filters when sent
// without a value.
var podcastFilterGroups = []string{"genres", "tags", "series", "authors", "progress", "narrators", "publishers", "publishedDecades", "missing", "languages", "tracks", "ebooks"}

// podcastFilterClause mirrors libraryItemsPodcastFilters.getFilteredLibraryItems
// and getMediaGroupQuery (server/utils/queries/libraryItemsPodcastFilters.js:
// 40-62, 144-163): only issues, recent, feed-open, genres, tags, languages and
// explicit filter; any other group (progress, series, ...) matches everything.
func podcastFilterClause(raw string) (string, []any) {
	if raw == "" {
		return "", nil
	}
	group, value := raw, ""
	for _, g := range podcastFilterGroups {
		if strings.HasPrefix(raw, g+".") {
			group, value = g, decodeUpstreamFilterValue(strings.TrimPrefix(raw, g+"."))
			break
		}
	}
	switch group {
	case "issues":
		return ` AND (li.isMissing=1 OR li.isInvalid=1)`, nil
	case "recent":
		return ` AND li.createdAt>=?`, []any{dbtime.Format(time.Now().Add(-60 * 24 * time.Hour))}
	case "feed-open":
		return ` AND EXISTS (SELECT 1 FROM feeds f WHERE f.entityType='libraryItem' AND f.entityId=li.id)`, nil
	case "genres", "tags":
		return ` AND EXISTS (SELECT 1 FROM json_each(b.` + group + `) WHERE json_valid(b.` + group + `) AND json_each.value=?)`, []any{value}
	case "languages":
		return ` AND b.language=?`, []any{value}
	case "explicit":
		return ` AND b.explicit=1`, nil
	}
	return "", nil
}

// decodeUpstreamFilterValue is libraryFilters.decode: decodeURIComponent then
// base64 (server/utils/queries/libraryFilters.js:13). Node's base64 decoder is
// lenient about alphabet and padding, so all four Go encodings are tried.
func decodeUpstreamFilterValue(value string) string {
	if unescaped, err := url.PathUnescape(value); err == nil {
		value = unescaped
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(value); err == nil {
			return string(b)
		}
	}
	return value
}

// podcastOrderClause mirrors libraryItemsPodcastFilters.getOrder
// (server/utils/queries/libraryItemsPodcastFilters.js:67-95). Sort keys that
// are not listed (for example media.duration) add no ORDER BY upstream.
func podcastOrderClause(sortBy string, desc, ignorePrefix bool) string {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	switch sortBy {
	case "addedAt":
		return " ORDER BY li.createdAt " + dir
	case "size":
		return " ORDER BY li.size " + dir
	case "birthtimeMs":
		return " ORDER BY li.birthtime " + dir
	case "mtimeMs":
		return " ORDER BY li.mtime " + dir
	case "media.metadata.author":
		if desc {
			return " ORDER BY b.author COLLATE NOCASE DESC NULLS FIRST"
		}
		return " ORDER BY b.author COLLATE NOCASE ASC NULLS LAST"
	case "media.metadata.title":
		if ignorePrefix {
			return " ORDER BY li.titleIgnorePrefix COLLATE NOCASE " + dir
		}
		return " ORDER BY li.title COLLATE NOCASE " + dir
	case "media.numTracks":
		return " ORDER BY b.numEpisodes " + dir
	case "random":
		return " ORDER BY RANDOM()"
	}
	return ""
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
		// libraryItemsBookFilters.getMediaGroupQuery (server/utils/queries/libraryItemsBookFilters.js:109-183)
		// LEFT JOINs the user's single progress row; a missing row reads as
		// NULL, so "not finished"/"not started" include items without one.
		// An unknown value adds no condition.
		exists := func(cond string) string {
			return ` AND EXISTS (SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? AND (` + cond + `))`
		}
		notExists := func(cond string) string {
			return ` AND NOT EXISTS (SELECT 1 FROM mediaProgresses mp WHERE mp.mediaItemId=b.id AND mp.userId=? AND (` + cond + `))`
		}
		switch value {
		case "not-finished":
			clause = notExists(`mp.isFinished=1`)
		case "not-started":
			clause = notExists(`NOT ((mp.currentTime IS NULL OR mp.currentTime=0) AND (mp.isFinished IS NULL OR mp.isFinished=0))`)
		case "finished":
			clause = exists(`mp.isFinished=1`)
		case "in-progress":
			clause = exists(`(mp.currentTime>0 OR mp.ebookProgress>0) AND mp.isFinished=0`)
		case "audio-in-progress":
			clause = exists(`mp.currentTime>0 AND mp.isFinished=0`)
		case "ebook-in-progress":
			clause = ` AND json_array_length(b.audioFiles)=0` + exists(`mp.ebookProgress>0 AND mp.isFinished=0`)
		case "ebook-finished":
			clause = ` AND json_array_length(b.audioFiles)=0 AND b.ebookFile IS NOT NULL` + exists(`mp.isFinished=1`)
		default:
			return "", nil, nil
		}
		return clause, []any{user.ID}, nil
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
	return store.DecodeJSONObject(raw)
}
func decodeArray(raw []byte) []map[string]any {
	return store.DecodeJSONSlice[map[string]any](raw)
}
func decodeStrings(raw []byte) []string {
	return store.DecodeJSONSlice[string](raw)
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
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
		raw = nil
	}
	return catalogTitlePrefixAtEnd(title, sortingPrefixesFromJSON(raw))
}

func sortingPrefixesFromJSON(raw []byte) []string {
	prefixes := []string{"the", "a"}
	values, ok := decodeObject(raw)["sortingPrefixes"].([]any)
	if !ok {
		return prefixes
	}
	prefixes = make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			prefixes = append(prefixes, text)
		}
	}
	return prefixes
}

func (h *Handler) bookTitlePrefix(ctx context.Context, title *string) *string {
	if title == nil {
		return nil
	}
	prefix := h.titlePrefix(ctx, *title)
	return &prefix
}

// seriesPrefixFreeName is the nameIgnorePrefix of Series.toOldJSON
// (Series.js:172): getTitlePrefixAtEnd of the name, not the stored column,
// which a rename (PATCH /api/series/:id) leaves unchanged.
func (h *Handler) seriesPrefixFreeName(ctx context.Context, name sql.NullString) any {
	if !name.Valid {
		return nil
	}
	return h.titlePrefix(ctx, name.String)
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

// lastFirst is Author.getLastFirst for a non-empty name
// (server/models/Author.js:33-36, parseNameString.nameToLastFirst).
func lastFirst(name string) string { return nameparse.ToLastFirst(name) }
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

type emitFunc func(userID, event string, payload any)

// Handler reads library and media catalog data from an existing database.
// queries returns the generated query set on the handler's pool; sqlc.New only
// stores the handle.
func (h *Handler) queries() *sqlc.Queries { return sqlc.New(h.db) }

type Handler struct {
	db             *sql.DB
	cfg            config.Config
	emit           emitFunc
	libraryChanged func(libraryID string)
	itemChanged    func(context.Context, string) error
	filters        *filterdata.Cache
	downloads      itemapi.DownloadQueue
	authorFinder   AuthorFinder
	finder         BookFinder
	episodes       func(ctx context.Context, itemID string, overrideDetails bool) (int, error)
}

// Option configures optional collaborators of New.
type Option func(*Handler)

// WithDownloadQueue supplies the podcast download queue that
// GET /api/items/:id?include=downloads reports.
func WithDownloadQueue(queue itemapi.DownloadQueue) Option {
	return func(h *Handler) { h.downloads = queue }
}

// New constructs a catalog handler. The emit callback is used for events from
// catalog write routes.
func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any), options ...Option) *Handler {
	h := &Handler{db: db, cfg: cfg, emit: emit, filters: filterdata.NewCache()}
	for _, option := range options {
		option(h)
	}
	return h
}

// FilterCache exposes the library filter data cache to the scanner, which
// patches it the way upstream's scanner does.
func (h *Handler) FilterCache() *filterdata.Cache { return h.filters }

// EnsureFilterData loads a library's filter data when none is cached, as
// libraryFilters.getFilterData does at the start of every scan.
func (h *Handler) EnsureFilterData(ctx context.Context, libraryID, mediaType string) error {
	_, err := h.filterData(ctx, libraryID, mediaType)
	return err
}

// resetIssues is Database.resetLibraryIssuesFilterData: it recounts missing or
// invalid items into an existing cache entry and does nothing otherwise.
func (h *Handler) resetIssues(ctx context.Context, libraryID string) {
	if !h.filters.Has(libraryID) {
		return
	}
	var n int
	if h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraryItems WHERE libraryId=? AND (isMissing=1 OR isInvalid=1)`, libraryID).Scan(&n) == nil {
		h.filters.StoreNumIssues(libraryID, n)
	}
}

// SetLibraryChanged installs the scanner watcher refresh callback. It runs
// only after a catalog library mutation commits successfully.
func (h *Handler) SetLibraryChanged(callback func(libraryID string)) { h.libraryChanged = callback }

// SetItemChanged installs the source-compatible metadata sidecar callback for
// media mutations. It runs only after the catalog row has been saved.
func (h *Handler) SetItemChanged(callback func(context.Context, string) error) {
	h.itemChanged = callback
}

func (h *Handler) libraryFilterData(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, id) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, id).Scan(&mediaType); errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	} else if err != nil {
		return httputil.Internal("database read failed", err)
	}
	data, err := h.filterData(r.Context(), id, mediaType)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, data, httputil.ForRequest(r))
	return nil
}

func (h *Handler) libraries(w http.ResponseWriter, r *http.Request) error {
	user := requestUser(r)
	var rows *sql.Rows
	var err error
	includeStats := strings.Contains(","+r.URL.Query().Get("include")+",", ",stats,")
	rows, err = h.db.QueryContext(r.Context(), `SELECT id,name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries ORDER BY displayOrder ASC`)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
			return httputil.Internal("database read failed", err)
		}
		if userCanListLibrary(user, row.id.String) {
			selected = append(selected, row)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("database read failed", err)
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
			return httputil.Internal("database read failed", err)
		}
		last, err := dbtime.OptionalMillis(row.lastScan)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if last != nil && *last == 0 {
			last = nil // Library.toOldJSON emits lastScan?.valueOf() || null.
		}
		created, err := dbtime.OptionalMillis(row.createdAt)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		updated, err := dbtime.OptionalMillis(row.updatedAt)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		lib := libraryJSON{
			ID: row.id.String, Name: row.name.String, Folders: folders, DisplayOrder: row.order.Int64,
			Icon: store.NullString(row.icon), MediaType: store.NullString(row.mediaType), Provider: store.NullString(row.provider),
			Settings: decodeObject(row.settings), LastScan: last, LastScanVersion: store.NullString(row.version),
			CreatedAt: deref(created), LastUpdate: deref(updated),
		}
		if includeStats {
			stats, err := h.libraryStatsValue(r.Context(), row.id.String, row.mediaType.String)
			if err != nil {
				return httputil.Internal("database read failed", err)
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
		return httputil.Internal("database read failed", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

func (h *Handler) library(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, id) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var name, icon, mediaType, provider, version sql.NullString
	var order sql.NullInt64
	var lastScan, createdAt, updatedAt any
	var rawSettings []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries WHERE id=?`, id).
		Scan(&name, &order, &icon, &mediaType, &provider, &lastScan, &version, &rawSettings, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	folders, err := h.libraryFolders(r.Context(), id)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	last, err := dbtime.OptionalMillis(lastScan)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if last != nil && *last == 0 {
		last = nil
	}
	created, err := dbtime.OptionalMillis(createdAt)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	updated, err := dbtime.OptionalMillis(updatedAt)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	payload := map[string]any{
		"id": id, "name": name.String, "folders": folders, "displayOrder": order.Int64,
		"icon": store.NullString(icon), "mediaType": store.NullString(mediaType), "provider": store.NullString(provider),
		"settings": decodeObject(rawSettings), "lastScan": last, "lastScanVersion": store.NullString(version),
		"createdAt": deref(created), "lastUpdate": deref(updated),
	}
	if strings.Contains(","+r.URL.Query().Get("include")+",", ",filterdata,") {
		filterdata, err := h.filterData(r.Context(), id, mediaType.String)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		playlists, err := h.queries().CountUserLibraryPlaylists(r.Context(), sqlc.CountUserLibraryPlaylistsParams{UserID: user.ID, LibraryID: id})
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"filterdata": filterdata, "issues": filterdata["numIssues"], "numUserPlaylists": playlists, "library": payload}, httputil.ForRequest(r))
		return nil
	}
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
}

func (h *Handler) libraryStats(w http.ResponseWriter, r *http.Request) error {
	user, id := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, id) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var mediaType string
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, id).Scan(&mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	stats, err := h.libraryStatsValue(r.Context(), id, mediaType)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, stats, httputil.ForRequest(r))
	return nil
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
		// authorFilters.getAuthorsWithCount (server/utils/queries/authorFilters.js:27):
		// inner join from bookAuthors, grouped by authorId, so ties keep group order.
		authorRows, err := h.db.QueryContext(ctx, `SELECT ba.authorId,a.name,COUNT(ba.authorId) AS count FROM bookAuthors ba INNER JOIN authors a ON a.id=ba.authorId AND a.libraryId=? GROUP BY ba.authorId, a.id ORDER BY count DESC LIMIT 10`, id)
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
		// libraryItemsPodcastFilters.getPodcastLibraryStats (server/utils/queries/
		// libraryItemsPodcastFilters.js:566-580): the size is summed over the
		// library items alone, not over their joined episodes.
		err := h.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(json_extract(pe.audioFile,'$.duration')),0),COUNT(pe.id),COUNT(DISTINCT li.id) FROM libraryItems li, podcasts p LEFT OUTER JOIN podcastEpisodes pe ON pe.podcastId=p.id WHERE p.id=li.mediaId AND li.libraryId=?`, id).Scan(&duration, &audioCount, &total)
		if err != nil {
			return nil, err
		}
		err = h.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(li.size),0) FROM libraryItems li WHERE li.mediaType='podcast' AND li.libraryId=?`, id).Scan(&size)
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
		out = append(out, map[string]any{"id": itemID, "title": store.NullString(title), "duration": duration})
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
		out = append(out, map[string]any{"id": itemID, "title": store.NullString(title), "size": size})
	}
	return out, rows.Err()
}

// filterData serves the cached entry while it is fresh and otherwise reloads
// it (libraryFilters.js:449-466).
func (h *Handler) filterData(ctx context.Context, libraryID, mediaType string) (map[string]any, error) {
	if cached, ok := h.filters.Fresh(libraryID); ok {
		return cached.Map()
	}
	built, err := h.buildFilterData(ctx, libraryID, mediaType)
	if err != nil {
		return nil, err
	}
	if data, e := filterdata.FromMap(built); e == nil {
		h.filters.Store(libraryID, data)
	}
	return built, nil
}

func (h *Handler) buildFilterData(ctx context.Context, libraryID, mediaType string) (map[string]any, error) {
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
			authorValues = append(authorValues, map[string]any{"id": id, "name": store.NullString(name)})
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

func (h *Handler) authors(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var exists int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraries WHERE id=?`, libraryID).Scan(&exists); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if exists == 0 {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,asin,name,description,imagePath,libraryId,lastFirst,createdAt,updatedAt FROM authors WHERE libraryId=?`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
			return httputil.Internal("database read failed", err)
		}
		data = append(data, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
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
			return httputil.Internal("database read failed", err)
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
		created, e := dbtime.OptionalMillis(a.created)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		updated, e := dbtime.OptionalMillis(a.updated)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		authors = append(authors, map[string]any{"id": a.id, "asin": store.NullString(a.asin), "name": store.NullString(a.name), "description": store.NullString(a.description), "imagePath": store.NullString(a.imagePath), "libraryId": a.libraryID, "addedAt": deref(created), "updatedAt": deref(updated), "numBooks": a.numBooks, "lastFirst": store.NullString(a.lastFirst)})
	}
	if !paginated {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"authors": authors}, httputil.ForRequest(r))
		return nil
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
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
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

func (h *Handler) series(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var mediaType string
	var settingsRaw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType,settings FROM libraries WHERE id=?`, libraryID).Scan(&mediaType, &settingsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	// A podcast library has no series rows, so the shared query below yields
	// the empty list upstream returns (seriesFilters.getFilteredSeries).
	filter := r.URL.Query().Get("filter")
	settings := decodeObject(settingsRaw)
	hideSingle, _ := settings["hideSingleBookSeries"].(bool)
	permissionSQL, permissionArgs := userMediaClause(user, "book")
	hasPermissionFilter := permissionSQL != ""
	rows, err := h.db.QueryContext(r.Context(), `SELECT s.id,s.name,s.nameIgnorePrefix,s.description,s.createdAt,s.updatedAt,s.libraryId,(SELECT COUNT(*) FROM bookSeries bs WHERE bs.seriesId=s.id) AS bookCount,(SELECT COALESCE(SUM(b.duration),0) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS duration,(SELECT MAX(b.createdAt) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=s.id) AS recent FROM series s WHERE s.libraryId=?`, libraryID)
	if err != nil {
		return httputil.Internal("database read failed", err)
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
			return httputil.Internal("database read failed", err)
		}
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return httputil.Internal("database read failed", err)
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
					return httputil.Internal("database read failed", err)
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
				return httputil.Internal("database read failed", e)
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
		return httputil.Status(http.StatusBadRequest, err.Error())
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
		created, e := dbtime.OptionalMillis(s.created)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		updated, e := dbtime.OptionalMillis(s.updated)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		entry := map[string]any{"id": s.id, "name": store.NullString(s.name), "nameIgnorePrefix": h.seriesPrefixFreeName(r.Context(), s.name), "description": store.NullString(s.description), "addedAt": deref(created), "updatedAt": deref(updated), "libraryId": s.libraryID}
		if sortBy == "totalDuration" && s.duration != 0 {
			entry["totalDuration"] = s.duration
		}
		books, err := h.seriesBooks(r.Context(), s.id, user)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		entry["books"] = books
		if includeHas(r, "rssfeed") {
			if feed, ok, e := h.feedMinified(r.Context(), "series", s.id); e != nil {
				return httputil.Internal("database read failed", e)
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
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
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
		result = append(result, item)
	}
	return result, nil
}

func (h *Handler) seriesByID(w http.ResponseWriter, r *http.Request) error {
	user, libraryID, seriesID := requestUser(r), r.PathValue("id"), r.PathValue("seriesId")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var name, nameIgnore, description sql.NullString
	var created, updated any
	var lib string
	err := h.db.QueryRowContext(r.Context(), `SELECT name,nameIgnorePrefix,description,createdAt,updatedAt,libraryId FROM series WHERE id=?`, seriesID).Scan(&name, &nameIgnore, &description, &created, &updated, &lib)
	if errors.Is(err, sql.ErrNoRows) || lib != libraryID {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	visibleSQL, args := userMediaClause(user, "book")
	var count int64
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM bookSeries bs JOIN books b ON b.id=bs.bookId WHERE bs.seriesId=?`+visibleSQL, append([]any{seriesID}, args...)...).Scan(&count); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if count == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	createdMs, e := dbtime.OptionalMillis(created)
	if e != nil {
		return httputil.Internal("database read failed", e)
	}
	updatedMs, e := dbtime.OptionalMillis(updated)
	if e != nil {
		return httputil.Internal("database read failed", e)
	}
	payload := map[string]any{"id": seriesID, "name": store.NullString(name), "nameIgnorePrefix": h.seriesPrefixFreeName(r.Context(), name), "description": store.NullString(description), "addedAt": deref(createdMs), "updatedAt": deref(updatedMs), "libraryId": lib}
	if includeHas(r, "rssfeed") {
		// The web client reads rssFeed (BookShelfToolbar.vue); TS sends null
		// when no feed is open.
		payload["rssFeed"] = nil
		if feed, ok, e := h.feedMinified(r.Context(), "series", seriesID); e != nil {
			return httputil.Internal("database read failed", e)
		} else if ok {
			payload["rssFeed"] = feed
		}
	}
	if includeHas(r, "progress") {
		books, e := h.visibleSeriesItems(r.Context(), seriesID, user)
		if e != nil {
			return httputil.Internal("database read failed", e)
		}
		ids := make([]string, 0, len(books))
		finished := make([]string, 0)
		for _, book := range books {
			itemID := stringValue(book["id"])
			ids = append(ids, itemID)
			mediaID := ""
			if access, ok := itemapi.AccessMedia(book["media"]); ok {
				mediaID = access.ID
			}
			var isFinished bool
			_ = h.db.QueryRowContext(r.Context(), `SELECT COALESCE(isFinished,0) FROM mediaProgresses WHERE userId=? AND mediaItemId=?`, user.ID, mediaID).Scan(&isFinished)
			if isFinished {
				finished = append(finished, itemID)
			}
		}
		payload["progress"] = map[string]any{"libraryItemIds": ids, "libraryItemIdsFinished": finished, "isFinished": len(finished) >= len(ids)}
	}
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
}

func dateSortKey(v any) int64 {
	ms, _ := dbtime.OptionalMillis(v)
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
		ms, err := dbtime.OptionalMillis(created)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "fullPath": path, "libraryId": parent, "addedAt": deref(ms)})
	}
	return result, rows.Err()
}

func (h *Handler) libraryItems(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	var mediaType string
	err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	page, limit, err := pagination(r)
	if err != nil {
		return httputil.Status(http.StatusBadRequest, err.Error())
	}
	collapse := r.URL.Query().Get("collapseseries") == "1"
	include := normalizedInclude(r)
	batchBookPage := mediaType == "book" && !collapse && include == ""
	ignorePrefix := false
	var settingsJSON []byte
	if err := h.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsJSON); err == nil {
		ignorePrefix, _ = decodeObject(settingsJSON)["sortingIgnorePrefix"].(bool)
	}
	if seriesID, ok := collapseSubseriesFilter(r); ok {
		return h.collapseSubseries(w, r, user, libraryID, mediaType, seriesID, page, limit, ignorePrefix)
	}
	filterValue := r.URL.Query().Get("filter")
	var order string
	var orderArgs []any
	if mediaType == "podcast" {
		order = podcastOrderClause(r.URL.Query().Get("sort"), r.URL.Query().Get("desc") == "1", ignorePrefix)
	} else {
		order, orderArgs, err = orderClauseForRequest(r.URL.Query().Get("sort"), r.URL.Query().Get("desc") == "1", ignorePrefix, mediaType, filterValue, user)
		if err != nil {
			return httputil.Status(http.StatusBadRequest, err.Error())
		}
	}
	var total int
	mediaTable := "books"
	if mediaType == "podcast" {
		mediaTable = "podcasts"
	} else if mediaType != "book" {
		return httputil.Status(http.StatusNotImplemented, "unsupported library media type")
	}
	var filterSQL string
	var filterArgs []any
	if mediaType == "podcast" {
		filterSQL, filterArgs = podcastFilterClause(filterValue)
	} else if filterSQL, filterArgs, err = filterClause(filterValue, mediaType, user); err != nil {
		return httputil.Status(http.StatusNotImplemented, err.Error())
	}
	permissionSQL, permissionArgs := userMediaClause(user, mediaType)
	var collapsed collapsedSeries
	if collapse && mediaType == "book" {
		if collapsed, err = h.collapseSeries(r.Context(), libraryID, filterValue, filterSQL, filterArgs, permissionSQL, permissionArgs); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if len(collapsed.excludeBookIDs) > 0 {
			permissionSQL += ` AND b.id NOT IN (` + placeholders(len(collapsed.excludeBookIDs)) + `)`
			for _, id := range collapsed.excludeBookIDs {
				permissionArgs = append(permissionArgs, id)
			}
		}
		if r.URL.Query().Get("sort") == "media.metadata.title" {
			order, orderArgs = collapsedTitleOrder(collapsed, ignorePrefix, r.URL.Query().Get("desc") == "1")
		}
	}
	whereSQL := filterSQL + permissionSQL
	countArgs := append([]any{libraryID, mediaType}, filterArgs...)
	countArgs = append(countArgs, permissionArgs...)
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM libraryItems li JOIN `+mediaTable+` b ON b.id=li.mediaId WHERE li.libraryId=? AND li.mediaType=?`+whereSQL, countArgs...).Scan(&total); err != nil {
		return httputil.Internal("database read failed", err)
	}
	query := `SELECT ` + itemapi.RowColumns + `
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
		return httputil.Internal("database read failed", err)
	}
	type libraryItemRow struct {
		item    map[string]any
		mediaID string
	}
	items := make([]libraryItemRow, 0)
	for rows.Next() {
		record, err := itemapi.ScanRecord(rows)
		if err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		items = append(items, libraryItemRow{item: recordToMap(record), mediaID: record.MediaID})
	}
	if err := rows.Err(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	var pageBooks map[string]bookdomain.Book
	var pagePrefixes []string
	if batchBookPage {
		bookIDs := make([]string, 0, len(items))
		for _, row := range items {
			bookIDs = append(bookIDs, row.mediaID)
		}
		pageBooks, err = bookdomain.LoadMany(r.Context(), h.db, bookIDs)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		pagePrefixes = sortingPrefixesFromJSON(settingsJSON)
	}
	results := make([]map[string]any, 0, len(items))
	for _, row := range items {
		delete(row.item, "lastScan")
		delete(row.item, "scanVersion")
		delete(row.item, "libraryFiles")
		var media map[string]any
		if mediaType == "book" {
			var book bookdomain.Book
			if batchBookPage {
				var ok bool
				book, ok = pageBooks[row.mediaID]
				if !ok {
					err = sql.ErrNoRows
				}
			} else {
				book, err = bookdomain.Load(r.Context(), h.db, row.mediaID)
			}
			if err == nil {
				var titlePrefix *string
				if batchBookPage {
					if title := book.Title(); title != nil {
						value := catalogTitlePrefixAtEnd(*title, pagePrefixes)
						titlePrefix = &value
					}
				} else {
					titlePrefix = h.bookTitlePrefix(r.Context(), book.Title())
				}
				bookMedia, projectionErr := book.Minified(titlePrefix)
				err = projectionErr
				if err == nil {
					row.item["media"] = bookMedia
				}
			}
		} else {
			media, err = h.podcastListJSON(r.Context(), row.mediaID, row.item)
		}
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		if media != nil {
			row.item["media"] = media
		}
		if err := h.attachItemIncludes(r.Context(), requestItemIncludes(r, user), row.item["id"].(string), row.item["media"], func(k string, v any) { row.item[k] = v }); err != nil {
			return httputil.Internal("database read failed", err)
		}
		if mediaType == "podcast" && includeHas(r, "numepisodesincomplete") {
			incomplete, err := h.numEpisodesIncomplete(r.Context(), user.ID, row.mediaID)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			if incomplete != 0 {
				row.item["numEpisodesIncomplete"] = incomplete
			}
		}
		if collapse && mediaType == "book" {
			series, err := h.collapsedSeriesJSON(r.Context(), row.mediaID, collapsed)
			if err != nil {
				return httputil.Internal("database read failed", err)
			}
			if series != nil {
				row.item["collapsedSeries"] = series
			}
		}
		results = append(results, row.item)
	}
	payload := map[string]any{
		"results": results, "total": total, "limit": limit, "page": page,
		"sortDesc": r.URL.Query().Get("desc") == "1", "mediaType": mediaType,
		"minified": r.URL.Query().Get("minified") == "1", "collapseseries": collapse, "include": include,
		"offset": page * limit,
	}
	if v := r.URL.Query().Get("sort"); v != "" {
		payload["sortBy"] = v
	}
	if v := r.URL.Query().Get("filter"); v != "" {
		payload["filterBy"] = v
	}
	httputil.WriteJSON(w, http.StatusOK, payload, httputil.ForRequest(r))
	return nil
}

func (h *Handler) item(w http.ResponseWriter, r *http.Request) error {
	user, itemID := requestUser(r), r.PathValue("id")
	item, err := h.ItemJSON(r.Context(), itemID, r.URL.Query().Get("expanded") == "1")
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !userCanAccessLibrary(user, item["libraryId"].(string)) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if !userCanAccessItem(user, item) {
		return httputil.Status(http.StatusForbidden, "")
	}
	if r.URL.Query().Get("expanded") == "1" {
		if err := h.expandedItemIncludes(r, user, item); err != nil {
			return httputil.Internal("database read failed", err)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, item, httputil.ForRequest(r))
	return nil
}

// expandedItemIncludes applies the include= entities of upstream
// LibraryItemController.findOne (server/controllers/LibraryItemController.js:71-96).
// Upstream splits include on "," without trimming or lowercasing.
func (h *Handler) expandedItemIncludes(r *http.Request, user store.User, item map[string]any) error {
	includes := strings.Split(r.URL.Query().Get("include"), ",")
	has := func(v string) bool { return slices.Contains(includes, v) }
	itemID, _ := item["id"].(string)
	if has("progress") {
		progress, err := progressdto.ReadOne(r.Context(), h.db, user.ID, itemID, r.URL.Query().Get("episode"))
		if err != nil {
			return err
		}
		if progress == nil {
			item["userMediaProgress"] = nil
		} else {
			item["userMediaProgress"] = progress
		}
	}
	if has("rssfeed") {
		feed, ok, err := h.feedMinified(r.Context(), "", itemID)
		if err != nil {
			return err
		}
		if ok {
			item["rssFeed"] = feed
		} else {
			item["rssFeed"] = nil
		}
	}
	if item["mediaType"] == "podcast" && has("downloads") && h.downloads != nil {
		queued, current := h.downloads.EpisodeDownloads(itemID)
		item["episodeDownloadsQueued"] = queued
		if current != nil {
			item["episodesDownloading"] = []wire.EpisodeDownload{*current}
		}
	}
	// LibraryItemController.findOne: `item.mediaItemShare = ShareManager.
	// findByMediaItemId(item.media.id)`, null when no open share exists.
	if has("share") && isAdminOrUp(user) && item["mediaType"] == "book" {
		share, err := h.openMediaItemShare(r.Context(), itemMediaAccess(item["media"]))
		if err != nil {
			return err
		}
		item["mediaItemShare"] = share
	}
	return nil
}

func userCanAccessItem(user store.User, item map[string]any) bool {
	access, ok := itemapi.AccessMedia(item["media"])
	if !ok {
		return false
	}
	return userCanAccessMedia(user, access)
}

// userCanAccessMedia is User.checkCanAccessLibraryItem's explicit and tag
// checks (server/models/User.js:658-683) on a book's access fields.
func userCanAccessMedia(user store.User, access itemapi.MediaAccess) bool {
	var permissions map[string]any
	_ = json.Unmarshal(user.Permissions, &permissions)
	if access.Explicit && permissions["accessExplicitContent"] != true {
		return false
	}
	if permissions["accessAllTags"] == true {
		return true
	}
	tags := access.Tags
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
		book, err := bookdomain.Load(ctx, h.db, mediaID)
		if err != nil {
			return nil, err
		}
		media, err := book.Full(itemID)
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
		media, err := h.podcastOldJSON(ctx, mediaID, itemID)
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
	var media any
	if item["mediaType"] == "book" {
		book, loadErr := bookdomain.Load(ctx, h.db, mediaID)
		err = loadErr
		if loadErr == nil {
			media, err = book.Minified(h.bookTitlePrefix(ctx, book.Title()))
		}
	} else if item["mediaType"] == "podcast" {
		media, err = h.podcastMinifiedJSON(ctx, mediaID)
	} else {
		return nil, fmt.Errorf("unsupported media type %q", item["mediaType"])
	}
	if err != nil {
		return nil, err
	}
	if media != nil {
		item["media"] = media
	}
	return item, nil
}

func (h *Handler) loadItem(ctx context.Context, itemID string) (map[string]any, string, error) {
	records, err := itemapi.LoadMany(ctx, h.db, []string{itemID})
	if err != nil {
		return nil, "", err
	}
	record, ok := records[itemID]
	if !ok {
		return nil, "", sql.ErrNoRows
	}
	return recordToMap(record), record.MediaID, nil
}

func (h *Handler) mediaFile(w http.ResponseWriter, r *http.Request) error {
	download := strings.HasSuffix(r.Pattern, "/download")
	user, itemID, ino := requestUser(r), r.PathValue("id"), r.PathValue("ino")
	var libraryID string
	var raw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
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
		return httputil.Status(http.StatusNotFound, "")
	}
	if download {
		var permissions map[string]any
		_ = json.Unmarshal(user.Permissions, &permissions)
		if !user.IsActive || permissions["download"] != true {
			return httputil.Status(http.StatusForbidden, "")
		}
		if mimeType := audioMIMEType(filepath.Ext(target)); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
	}
	if !h.pathAllowed(r.Context(), target, libraryID, false) {
		if download {
			if _, statErr := os.Stat(target); statErr != nil {
				if os.IsNotExist(statErr) {
					return httputil.Status(http.StatusNotFound, "File not found")
				}
				return httputil.Internal("Download failed", statErr)
			}
		}
		return httputil.Status(http.StatusNotFound, "")
	}
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(target)}))
	}
	return h.servePath(w, r, target)
}

func (h *Handler) ebookFile(w http.ResponseWriter, r *http.Request) error {
	user, itemID, fileID := requestUser(r), r.PathValue("id"), r.PathValue("fileId")
	var libraryID, mediaType, mediaID string
	var raw []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId,libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &mediaID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	target := ""
	if fileID != "" {
		for _, file := range decodeArray(raw) {
			if stringValue(file["ino"]) == fileID && itemapi.FileType(fileMetadataExt(file)) == "ebook" {
				target = filePath(file)
				break
			}
		}
		if target == "" {
			return httputil.Status(http.StatusBadRequest, "Invalid ebook file id")
		}
	} else if mediaType == "book" {
		var ebookRaw []byte
		if err := h.db.QueryRowContext(r.Context(), `SELECT ebookFile FROM books WHERE id=?`, mediaID).Scan(&ebookRaw); err != nil {
			return httputil.Status(http.StatusNotFound, "")
		}
		target = filePath(decodeObject(ebookRaw))
	}
	if target == "" {
		return httputil.Status(http.StatusNotFound, "")
	}
	if !h.pathAllowed(r.Context(), target, libraryID, false) {
		return httputil.Status(http.StatusNotFound, "")
	}
	return h.servePath(w, r, target)
}

func (h *Handler) cover(w http.ResponseWriter, r *http.Request) error {
	itemID := r.PathValue("id")
	var libraryID, mediaType, mediaID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaType,mediaId FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &mediaType, &mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(http.StatusNotFound, "")
	}
	if err != nil {
		return httputil.Internal("database read failed", err)
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
			return httputil.Status(http.StatusNotFound, "")
		}
		if !h.pathAllowed(r.Context(), target, libraryID, true) {
			return httputil.Status(http.StatusNotFound, "")
		}
		return h.coverRaw(w, r, target)
	}
	return h.handleCoverCache(w, r, itemID)
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

func (h *Handler) servePath(w http.ResponseWriter, r *http.Request, target string) error {
	f, err := os.Open(target)
	if err != nil {
		if os.IsNotExist(err) {
			return httputil.Status(http.StatusNotFound, "File not found")
		}
		return httputil.Internal("File read failed", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return httputil.Internal("File read failed", err)
	}
	if !info.Mode().IsRegular() {
		return httputil.Status(http.StatusNotFound, "")
	}
	ext := strings.ToLower(filepath.Ext(target))
	media := map[string]string{".m4b": "audio/mp4", ".m4a": "audio/mp4", ".aac": "audio/aac", ".mp3": "audio/mpeg", ".flac": "audio/flac", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav"}[ext]
	if media == "" {
		media = mime.TypeByExtension(ext)
	}
	if media != "" {
		w.Header().Set("Content-Type", media)
	}
	// Express's etag helper uses stat.mtime.getTime(), which rounds the
	// filesystem timestamp to the nearest millisecond before hex encoding.
	fileETag := fmt.Sprintf(`W/"%x-%x"`, info.Size(), info.ModTime().Round(time.Millisecond).UnixMilli())
	w.Header().Set("ETag", fileETag)
	http.ServeContent(&sendFileRangeErrorWriter{
		ResponseWriter: w,
		method:         r.Method,
		etag:           fileETag,
		lastModified:   info.ModTime().UTC().Format(http.TimeFormat),
	}, r, filepath.Base(target), info.ModTime(), f)
	return nil
}

const expressRangeNotSatisfiableBody = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Range Not Satisfiable</pre>\n</body>\n</html>\n"

// sendFileRangeErrorWriter preserves streaming for successful responses while
// translating net/http's plain-text 416 into Express sendFile's finalhandler
// response. It buffers only the status until the 416 body arrives.
type sendFileRangeErrorWriter struct {
	http.ResponseWriter
	method       string
	etag         string
	lastModified string
	status       int
}

func (w *sendFileRangeErrorWriter) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.status = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *sendFileRangeErrorWriter) Write(p []byte) (int, error) {
	if w.status != http.StatusRequestedRangeNotSatisfiable {
		return w.ResponseWriter.Write(p)
	}
	body := []byte(expressRangeNotSatisfiableBody)
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("Accept-Ranges", "bytes")
	header.Set("ETag", w.etag)
	header.Set("Last-Modified", w.lastModified)
	w.ResponseWriter.WriteHeader(w.status)
	if w.method != http.MethodHead {
		if _, err := w.ResponseWriter.Write(body); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func filePath(file map[string]any) string {
	meta, _ := file["metadata"].(map[string]any)
	return stringValue(meta["path"])
}
func fileMetadataExt(file map[string]any) any {
	meta, _ := file["metadata"].(map[string]any)
	return meta["ext"]
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) error {
	user, libraryID := requestUser(r), r.PathValue("id")
	if !userCanAccessLibrary(user, libraryID) {
		return httputil.Status(http.StatusForbidden, "")
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		return httputil.Status(http.StatusBadRequest, `Invalid request. Query param "q" must be a string`)
	}
	limit := 12
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, e := strconv.Atoi(raw); e == nil && n >= 0 {
			limit = n
		}
	}
	var mediaType string
	if err := h.db.QueryRowContext(r.Context(), `SELECT mediaType FROM libraries WHERE id=?`, libraryID).Scan(&mediaType); err != nil {
		return httputil.Status(http.StatusNotFound, "Library not found")
	}
	table := "books"
	if mediaType == "podcast" {
		table = "podcasts"
	} else if mediaType != "book" {
		return httputil.Status(http.StatusNotImplemented, "unsupported library media type")
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
		return httputil.Internal("database read failed", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		ids = append(ids, id)
		if limit > 0 && len(ids) >= limit {
			break
		}
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item, e := h.ItemJSON(r.Context(), id, true)
		if e != nil {
			return httputil.Internal("database read failed", e)
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
		return httputil.Internal("database read failed", err)
	}
	genres, err := h.searchJSONValues(r.Context(), libraryID, table, "genres", query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if mediaType == "podcast" {
		episodes, err := h.searchEpisodes(r.Context(), libraryID, pattern, permissionSQL, permissionArgs, limit)
		if err != nil {
			return httputil.Internal("database read failed", err)
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"podcast": items, "tags": tags, "genres": genres, "episodes": episodes}, httputil.ForRequest(r))
		return nil
	}
	narrators, err := h.searchJSONValues(r.Context(), libraryID, table, "narrators", query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	authors, err := h.searchAuthors(r.Context(), libraryID, query, limit)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	series, err := h.searchSeries(r.Context(), libraryID, query, limit, user)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"book": items, "narrators": narrators, "tags": tags, "genres": genres, "series": series, "authors": authors}, httputil.ForRequest(r))
	return nil
}

// searchEpisodes is the episode half of libraryItemsPodcastFilters.search
// (server/utils/queries/libraryItemsPodcastFilters.js:411-440): episodes whose
// title matches, each with its podcast (loaded without episodes) and the
// expanded episode as recentEpisode.
func (h *Handler) searchEpisodes(ctx context.Context, libraryID, pattern, permissionSQL string, permissionArgs []any, limit int) ([]wire.ItemMatch, error) {
	query := `SELECT li.id, e.id FROM podcastEpisodes e JOIN podcasts b ON b.id=e.podcastId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='podcast' WHERE li.libraryId=? AND lower(e.title) LIKE lower(?)` + permissionSQL + ` ORDER BY e.rowid`
	args := append([]any{libraryID, pattern}, permissionArgs...)
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type ref struct{ itemID, episodeID string }
	refs := make([]ref, 0)
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.itemID, &x.episodeID); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, x)
		if limit > 0 && len(refs) >= limit {
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]wire.ItemMatch, 0, len(refs))
	for _, x := range refs {
		item, err := h.ItemJSON(ctx, x.itemID, true)
		if err != nil {
			return nil, err
		}
		media := item["media"].(map[string]any)
		var recent any
		for _, ep := range media["episodes"].([]map[string]any) {
			if ep["id"] == x.episodeID {
				recent = ep
			}
		}
		media["episodes"] = []any{}
		media["numEpisodes"] = 0
		media["size"] = 0
		item["recentEpisode"] = recent
		out = append(out, wire.ItemMatch{LibraryItem: item})
	}
	return out, nil
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
		added, e := dbtime.OptionalMillis(created)
		if e != nil {
			return nil, e
		}
		updatedMs, e := dbtime.OptionalMillis(updated)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "name": store.NullString(name), "asin": store.NullString(asin), "description": store.NullString(description), "imagePath": store.NullString(image), "libraryId": library, "addedAt": deref(added), "updatedAt": deref(updatedMs), "numBooks": count})
	}
	return out, rows.Err()
}

// searchSeries mirrors the series part of libraryItemsBookFilters.search
// (server/utils/queries/libraryItemsBookFilters.js:1227-1270): each match
// carries its user-visible books as full legacy items with authors and series
// emptied, and series without visible books are dropped after the limit.
func (h *Handler) searchSeries(ctx context.Context, libraryID, query string, limit int, user store.User) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id,name,description,createdAt,updatedAt,libraryId FROM series WHERE libraryId=? AND lower(name) LIKE lower(?) LIMIT ?`, libraryID, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	type seriesRow struct {
		id, library       string
		name, description sql.NullString
		added, updated    *int64
	}
	matches := make([]seriesRow, 0)
	for rows.Next() {
		var m seriesRow
		var created, updated any
		if err := rows.Scan(&m.id, &m.name, &m.description, &created, &updated, &m.library); err != nil {
			rows.Close()
			return nil, err
		}
		if m.added, err = dbtime.OptionalMillis(created); err != nil {
			rows.Close()
			return nil, err
		}
		if m.updated, err = dbtime.OptionalMillis(updated); err != nil {
			rows.Close()
			return nil, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	permission, permissionArgs := userMediaClause(user, "book")
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		itemIDs, err := h.queryStrings(ctx, `SELECT li.id FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE bs.seriesId=?`+permission+` ORDER BY bs.rowid`, append([]any{m.id}, permissionArgs...)...)
		if err != nil {
			return nil, err
		}
		books := make([]any, 0, len(itemIDs))
		for _, id := range itemIDs {
			item, err := h.ItemJSON(ctx, id, false)
			if err != nil {
				return nil, err
			}
			if media, ok := item["media"].(wire.BookFull); ok {
				media.Metadata.Authors = []wire.BookAuthor{}
				media.Metadata.Series = []wire.BookSeries{}
				item["media"] = media
			}
			books = append(books, item)
		}
		if len(books) == 0 {
			continue
		}
		var nameIgnore any
		if m.name.Valid {
			nameIgnore = h.titlePrefix(ctx, m.name.String)
		}
		out = append(out, map[string]any{"series": map[string]any{"id": m.id, "name": store.NullString(m.name), "nameIgnorePrefix": nameIgnore, "description": store.NullString(m.description), "addedAt": deref(m.added), "updatedAt": deref(m.updated), "libraryId": m.library}, "books": books})
	}
	return out, nil
}
