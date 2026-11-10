package items

import (
	"context"
	"database/sql"
	"slices"

	"audiobookshelf-compat/internal/books"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/podcasts/read"
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
	podcasts, err := podcastread.LoadMany(ctx, db, podcastIDs, opt)
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
	it.LibraryFiles = store.DecodeJSONSlice[mediafiles.File](libraryFiles)
	for _, file := range it.LibraryFiles {
		if file.Metadata() != nil {
			file["fileType"] = file.Kind()
		}
	}
	return it, nil
}
