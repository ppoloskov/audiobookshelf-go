package items

import (
	"context"
	"database/sql"
	"slices"
	"strconv"

	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
)

// Load reads library items and their media in a fixed number of queries
// (libraryItems, books, podcasts, authors, series, and episodes when
// opt.Episodes), each IN-list chunked at 500 ids. The result follows ids;
// ids that do not exist, or whose media row is missing, are omitted.
func Load(ctx context.Context, db *sql.DB, ids []string, opt Options) ([]Item, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ids)))
	items := make(map[string]*Item, len(unique))
	var bookIDs, podcastIDs []string
	err := store.QueryIn(ctx, db, `SELECT li.id,li.ino,li.libraryId,li.libraryFolderId,li.path,li.relPath,li.isFile,li.mtime,li.ctime,li.birthtime,li.createdAt,li.updatedAt,li.lastScan,li.lastScanVersion,li.isMissing,li.isInvalid,li.mediaType,li.size,li.extraData,li.libraryFiles,li.mediaId FROM libraryItems li WHERE li.id IN `, ``, unique, func(rows *sql.Rows) error {
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
	loadedBooks, err := books.LoadMany(ctx, db, bookIDs)
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
			if it.Book = loadedBooks[it.MediaID]; it.Book == nil {
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
	it.Ino, it.FolderID, it.ScanVersion = store.NullStringPtr(ino), store.NullStringPtr(folder), store.NullStringPtr(scanVersion)
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
	it.LibraryFiles = store.DecodeJSONSlice[libitem.File](libraryFiles)
	for _, file := range it.LibraryFiles {
		if file.Metadata() != nil {
			file["fileType"] = file.Kind()
		}
	}
	return it, nil
}

func loadPodcasts(ctx context.Context, db *sql.DB, ids []string, opt Options) (map[string]*libitem.Podcast, error) {
	podcasts := make(map[string]*libitem.Podcast, len(ids))
	if len(ids) == 0 {
		return podcasts, nil
	}
	err := store.QueryIn(ctx, db, `SELECT id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes FROM podcasts WHERE id IN `, ``, ids, func(rows *sql.Rows) error {
		var p libitem.Podcast
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
		p = libitem.Podcast{ID: id.String, Title: store.NullStringPtr(title), Author: store.NullStringPtr(author), Description: store.NullStringPtr(description), ReleaseDate: store.NullStringPtr(release),
			FeedURL: store.NullStringPtr(feed), ImageURL: store.NullStringPtr(image), ITunesPageURL: store.NullStringPtr(itunesPage), ITunesID: store.NullStringPtr(itunesID), ITunesArtistID: store.NullStringPtr(itunesArtist),
			Language: store.NullStringPtr(language), Type: store.NullStringPtr(ptype), Explicit: explicit.Int64 != 0, AutoDownloadEpisodes: autoDownload.Int64 != 0, AutoDownloadSchedule: store.NullStringPtr(schedule),
			LastEpisodeCheck: check, MaxEpisodesToKeep: maxKeep.Int64, MaxNewEpisodesToDownload: maxNew.Int64, CoverPath: store.NullStringPtr(cover),
			Tags: store.DecodeJSONSlice[string](tags), Genres: store.DecodeJSONSlice[string](genres), NumEpisodes: numEpisodes.Int64}
		if opt.Episodes {
			p.Episodes = []libitem.Episode{}
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
	err = store.QueryIn(ctx, db, `SELECT podcastId,id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt FROM podcastEpisodes WHERE `+column+` IN `, ` ORDER BY podcastId,createdAt ASC,rowid`, keys, func(rows *sql.Rows) error {
		var e libitem.Episode
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
		e.Season, e.Episode, e.EpisodeType, e.Title, e.Subtitle, e.Description, e.PubDate = store.NullStringPtr(season), store.NullStringPtr(episode), store.NullStringPtr(episodeType), store.NullStringPtr(title), store.NullStringPtr(subtitle), store.NullStringPtr(description), store.NullStringPtr(pubDate)
		if enclosureURL.Valid && enclosureURL.String != "" {
			e.Enclosure = &libitem.Enclosure{URL: enclosureURL.String, Type: store.NullStringPtr(enclosureType)}
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
