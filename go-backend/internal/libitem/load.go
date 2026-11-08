package libitem

import (
	"audiobookshelf-compat/internal/jsonvalue"
	"context"
	"database/sql"
	"slices"
	"strconv"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

const chunkSize = 500

// Load reads library items and their media in a fixed number of queries
// (libraryItems, books, podcasts, authors, series, and episodes when
// opt.Episodes), each IN-list chunked at 500 ids. The result follows ids;
// ids that do not exist, or whose media row is missing, are omitted.
func Load(ctx context.Context, db *sql.DB, ids []string, opt Options) ([]Item, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ids)))
	items := make(map[string]*Item, len(unique))
	var bookIDs, podcastIDs []string
	err := queryIn(ctx, db, `SELECT li.id,li.ino,li.libraryId,li.libraryFolderId,li.path,li.relPath,li.isFile,li.mtime,li.ctime,li.birthtime,li.createdAt,li.updatedAt,li.lastScan,li.lastScanVersion,li.isMissing,li.isInvalid,li.mediaType,li.size,li.extraData,li.libraryFiles,li.mediaId FROM libraryItems li WHERE li.id IN `, ``, unique, func(rows *sql.Rows) error {
		it, err := scanItem(rows)
		if err != nil {
			return err
		}
		items[it.ID] = &it
		switch it.MediaType {
		case "book":
			bookIDs = append(bookIDs, it.MediaID)
		case "podcast":
			podcastIDs = append(podcastIDs, it.MediaID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	books, err := loadBooks(ctx, db, bookIDs)
	if err != nil {
		return nil, err
	}
	podcasts, err := loadPodcasts(ctx, db, podcastIDs, opt)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		found, ok := items[id]
		if !ok {
			continue
		}
		it := *found
		if it.MediaType == "book" {
			if it.Book = books[it.MediaID]; it.Book == nil {
				continue
			}
		} else if it.MediaType == "podcast" {
			if it.Podcast = podcasts[it.MediaID]; it.Podcast == nil {
				continue
			}
		} else {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

// LoadAccess reads only the permission inputs of one item (library, media
// explicit flag and tags) in a single query. It returns sql.ErrNoRows when the
// item or its media row is missing, as LoadOne does.
func LoadAccess(ctx context.Context, db *sql.DB, id string) (Access, error) {
	var a Access
	var explicit sql.NullInt64
	var tags []byte
	var mediaID sql.NullString
	err := db.QueryRowContext(ctx, `SELECT li.libraryId, COALESCE(b.id,p.id), COALESCE(b.explicit,p.explicit), COALESCE(b.tags,p.tags)
FROM libraryItems li
LEFT JOIN books b ON li.mediaType='book' AND b.id=li.mediaId
LEFT JOIN podcasts p ON li.mediaType='podcast' AND p.id=li.mediaId
WHERE li.id=?`, id).Scan(&a.LibraryID, &mediaID, &explicit, &tags)
	if err != nil {
		return Access{}, err
	}
	if !mediaID.Valid {
		return Access{}, sql.ErrNoRows
	}
	a.Explicit, a.Tags = explicit.Int64 != 0, store.DecodeJSONSlice[string](tags)
	return a, nil
}

// LoadOne is Load for a single id; it returns sql.ErrNoRows when the item or
// its media row is missing.
func LoadOne(ctx context.Context, db *sql.DB, id string, opt Options) (Item, error) {
	items, err := Load(ctx, db, []string{id}, opt)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, sql.ErrNoRows
	}
	return items[0], nil
}

func queryIn(ctx context.Context, db *sql.DB, prefix, suffix string, ids []string, fn func(*sql.Rows) error) error {
	for chunk := range slices.Chunk(ids, chunkSize) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, prefix+"("+store.Placeholders(len(chunk))+")"+suffix, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = fn(rows); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func scanItem(rows *sql.Rows) (Item, error) {
	var it Item
	var id, libraryID, path, relPath, mediaType, mediaID, ino, folder, scanVersion sql.NullString
	var isFile, missing, invalid sql.NullInt64
	var mtime, ctime, birthtime, created, updated, lastScan, size any
	var extra, libraryFiles []byte
	if err := rows.Scan(&id, &ino, &libraryID, &folder, &path, &relPath, &isFile, &mtime, &ctime, &birthtime, &created, &updated, &lastScan, &scanVersion, &missing, &invalid, &mediaType, &size, &extra, &libraryFiles, &mediaID); err != nil {
		return Item{}, err
	}
	it.ID, it.LibraryID, it.Path, it.RelPath, it.MediaType, it.MediaID = id.String, libraryID.String, path.String, relPath.String, mediaType.String, mediaID.String
	it.Ino, it.FolderID, it.ScanVersion = nullStr(ino), nullStr(folder), nullStr(scanVersion)
	it.IsFile, it.IsMissing, it.IsInvalid = isFile.Int64 != 0, missing.Int64 != 0, invalid.Int64 != 0
	switch n := size.(type) {
	case int64:
		it.Size = &n
	case float64:
		v := int64(n)
		it.Size = &v
	}
	var err error
	for _, f := range []struct {
		raw any
		out **int64
	}{{mtime, &it.MTimeMS}, {ctime, &it.CTimeMS}, {birthtime, &it.BirthtimeMS}, {created, &it.AddedAt}, {updated, &it.UpdatedAt}, {lastScan, &it.LastScan}} {
		if *f.out, err = dbtime.OptionalMillis(f.raw); err != nil {
			return Item{}, err
		}
	}
	it.OldLibraryItemID, _ = store.DecodeJSONObject(extra)["oldLibraryItemId"].(string)
	it.LibraryFiles = store.DecodeJSONSlice[File](libraryFiles)
	for _, file := range it.LibraryFiles {
		if file.metadata() != nil {
			file["fileType"] = file.Kind()
		}
	}
	return it, nil
}

func loadBooks(ctx context.Context, db *sql.DB, ids []string) (map[string]*Book, error) {
	books := make(map[string]*Book, len(ids))
	if len(ids) == 0 {
		return books, nil
	}
	err := queryIn(ctx, db, `SELECT id,title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,updatedAt FROM books WHERE id IN `, ``, ids, func(rows *sql.Rows) error {
		var b Book
		var id, title, subtitle, year, date, publisher, description, isbn, asin, language, cover sql.NullString
		var explicit, abridged sql.NullInt64
		var duration, updated any
		var narrators, audio, ebook, chapters, tags, genres []byte
		if err := rows.Scan(&id, &title, &subtitle, &year, &date, &publisher, &description, &isbn, &asin, &language, &explicit, &abridged, &cover, &duration, &narrators, &audio, &ebook, &chapters, &tags, &genres, &updated); err != nil {
			return err
		}
		var err error
		if b.UpdatedAt, err = dbtime.OptionalMillis(updated); err != nil {
			return err
		}
		b.ID, b.Title, b.Subtitle, b.PublishedYear, b.PublishedDate, b.Publisher = id.String, nullStr(title), nullStr(subtitle), nullStr(year), nullStr(date), nullStr(publisher)
		b.Description, b.ISBN, b.ASIN, b.Language, b.CoverPath = nullStr(description), nullStr(isbn), nullStr(asin), nullStr(language), nullStr(cover)
		b.Explicit, b.Abridged = explicit.Int64 != 0, abridged.Int64 != 0
		if n, ok := jsonvalue.Number(duration); ok {
			b.Duration = &n
		}
		b.Narrators, b.Genres, b.Tags = store.DecodeJSONSlice[string](narrators), store.DecodeJSONSlice[string](genres), store.DecodeJSONSlice[string](tags)
		b.AudioFiles, b.Chapters = store.DecodeJSONSlice[File](audio), store.DecodeJSONSlice[map[string]any](chapters)
		if b.Ebook = File(store.DecodeJSONObject(ebook)); len(b.Ebook) == 0 {
			b.Ebook = nil
		}
		b.Authors, b.Series = []Author{}, []SeriesRef{}
		books[b.ID] = &b
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = queryIn(ctx, db, `SELECT ba.bookId,a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId IN `, ` ORDER BY ba.bookId,ba.createdAt ASC,ba.rowid`, ids, func(rows *sql.Rows) error {
		var bookID string
		var a Author
		var name sql.NullString
		if err := rows.Scan(&bookID, &a.ID, &name); err != nil {
			return err
		}
		a.Name = name.String
		if b := books[bookID]; b != nil {
			b.Authors = append(b.Authors, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = queryIn(ctx, db, `SELECT bs.bookId,s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId IN `, ` ORDER BY bs.bookId,bs.createdAt ASC,bs.rowid`, ids, func(rows *sql.Rows) error {
		var bookID string
		var s SeriesRef
		var name, sequence sql.NullString
		if err := rows.Scan(&bookID, &s.ID, &name, &sequence); err != nil {
			return err
		}
		s.Name, s.Sequence = name.String, nullStr(sequence)
		if b := books[bookID]; b != nil {
			b.Series = append(b.Series, s)
		}
		return nil
	})
	return books, err
}

func loadPodcasts(ctx context.Context, db *sql.DB, ids []string, opt Options) (map[string]*Podcast, error) {
	podcasts := make(map[string]*Podcast, len(ids))
	if len(ids) == 0 {
		return podcasts, nil
	}
	err := queryIn(ctx, db, `SELECT id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes FROM podcasts WHERE id IN `, ``, ids, func(rows *sql.Rows) error {
		var p Podcast
		var id, title, author, release, feed, image, description, itunesPage, itunesID, itunesArtist, language, ptype, schedule, cover sql.NullString
		var explicit, autoDownload sql.NullInt64
		var lastCheck any
		var maxKeep, maxNew, numEpisodes sql.NullInt64
		var tags, genres []byte
		if err := rows.Scan(&id, &title, &author, &release, &feed, &image, &description, &itunesPage, &itunesID, &itunesArtist, &language, &ptype, &explicit, &autoDownload, &schedule, &lastCheck, &maxKeep, &maxNew, &cover, &tags, &genres, &numEpisodes); err != nil {
			return err
		}
		check, err := dbtime.OptionalMillis(lastCheck)
		if err != nil {
			return err
		}
		if check != nil && *check == 0 {
			check = nil
		}
		p = Podcast{ID: id.String, Title: nullStr(title), Author: nullStr(author), Description: nullStr(description), ReleaseDate: nullStr(release),
			FeedURL: nullStr(feed), ImageURL: nullStr(image), ITunesPageURL: nullStr(itunesPage), ITunesID: nullStr(itunesID), ITunesArtistID: nullStr(itunesArtist),
			Language: nullStr(language), Type: nullStr(ptype), Explicit: explicit.Int64 != 0, AutoDownloadEpisodes: autoDownload.Int64 != 0, AutoDownloadSchedule: nullStr(schedule),
			LastEpisodeCheck: check, MaxEpisodesToKeep: maxKeep.Int64, MaxNewEpisodesToDownload: maxNew.Int64, CoverPath: nullStr(cover),
			Tags: store.DecodeJSONSlice[string](tags), Genres: store.DecodeJSONSlice[string](genres), NumEpisodes: numEpisodes.Int64}
		if opt.Episodes {
			p.Episodes = []Episode{}
		}
		podcasts[p.ID] = &p
		return nil
	})
	if err != nil || !opt.Episodes {
		return podcasts, err
	}
	column, keys := "podcastId", ids
	if opt.EpisodeIDs != nil {
		column, keys = "id", opt.EpisodeIDs
	}
	err = queryIn(ctx, db, `SELECT podcastId,id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt FROM podcastEpisodes WHERE `+column+` IN `, ` ORDER BY podcastId,createdAt ASC,rowid`, keys, func(rows *sql.Rows) error {
		var e Episode
		var index, enclosureSize sql.NullInt64
		var season, episode, episodeType, title, subtitle, description, pubDate, enclosureURL, enclosureType sql.NullString
		var published, created, updated any
		var audio, chapters, extra []byte
		if err := rows.Scan(&e.PodcastID, &e.ID, &index, &season, &episode, &episodeType, &title, &subtitle, &description, &pubDate, &enclosureURL, &enclosureSize, &enclosureType, &published, &audio, &chapters, &extra, &created, &updated); err != nil {
			return err
		}
		var err error
		if e.PublishedAt, err = dbtime.OptionalMillis(published); err != nil {
			return err
		}
		if e.PublishedAt != nil && *e.PublishedAt == 0 {
			e.PublishedAt = nil
		}
		if e.AddedAt, err = dbtime.OptionalMillis(created); err != nil {
			return err
		}
		if e.UpdatedAt, err = dbtime.OptionalMillis(updated); err != nil {
			return err
		}
		if index.Valid {
			e.Index = &index.Int64
		}
		extras := store.DecodeJSONObject(extra)
		e.OldEpisodeID, _ = extras["oldEpisodeId"].(string)
		e.GUID, _ = extras["guid"].(string)
		e.Season, e.Episode, e.EpisodeType, e.Title, e.Subtitle, e.Description, e.PubDate = nullStr(season), nullStr(episode), nullStr(episodeType), nullStr(title), nullStr(subtitle), nullStr(description), nullStr(pubDate)
		if enclosureURL.Valid && enclosureURL.String != "" {
			e.Enclosure = &Enclosure{URL: enclosureURL.String, Type: nullStr(enclosureType)}
			if enclosureSize.Valid {
				length := strconv.FormatInt(enclosureSize.Int64, 10)
				e.Enclosure.Length = &length
			}
		}
		e.AudioFile, e.Chapters = store.DecodeJSONObject(audio), store.DecodeJSONSlice[map[string]any](chapters)
		if p := podcasts[e.PodcastID]; p != nil {
			p.Episodes = append(p.Episodes, e)
		}
		return nil
	})
	return podcasts, err
}
