package items

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// RowColumns is the fixed SQL projection ordered for ScanRecord.
const RowColumns = `li.id,li.ino,li.libraryId,li.libraryFolderId,li.path,li.relPath,li.isFile,li.mtime,li.ctime,li.birthtime,li.createdAt,li.updatedAt,li.lastScan,li.lastScanVersion,li.isMissing,li.isInvalid,li.mediaType,li.size,li.extraData,li.libraryFiles,li.mediaId`

const rowSelect = `SELECT ` + RowColumns + ` FROM libraryItems li`

// Record is the common library-item row. Media-specific metadata remains owned
// by its book or podcast domain.
type Record struct {
	ID               string
	MediaID          string
	Ino              sql.NullString
	LibraryID        string
	FolderID         sql.NullString
	Path             string
	RelPath          string
	IsFile           bool
	MTimeMS          *int64
	CTimeMS          *int64
	BirthtimeMS      *int64
	AddedAt          *int64
	UpdatedAt        *int64
	LastScan         *int64
	ScanVersion      sql.NullString
	IsMissing        bool
	IsInvalid        bool
	MediaType        string
	Size             any
	OldLibraryItemID string
	LibraryFiles     []map[string]any
}

// ScanRecord reads the shared libraryItems projection used by catalog pages
// and item lookups. Timestamp parsing is strict, matching dbtime's database
// boundary behavior.
func ScanRecord(rows *sql.Rows) (Record, error) {
	var record Record
	var id, libraryID, path, relPath, mediaType, mediaID sql.NullString
	var isFile, missing, invalid int64
	var mtime, ctime, birthtime, created, updated, lastScan, size any
	var extra, libraryFiles []byte
	err := rows.Scan(&id, &record.Ino, &libraryID, &record.FolderID, &path, &relPath, &isFile, &mtime, &ctime, &birthtime, &created, &updated, &lastScan, &record.ScanVersion, &missing, &invalid, &mediaType, &size, &extra, &libraryFiles, &mediaID)
	if err != nil {
		return Record{}, err
	}
	record.ID = id.String
	record.LibraryID = libraryID.String
	record.Path = path.String
	record.RelPath = relPath.String
	record.MediaID = mediaID.String
	record.IsFile = isFile != 0
	record.IsMissing = missing != 0
	record.IsInvalid = invalid != 0
	record.MediaType = mediaType.String
	record.Size = size

	for _, field := range []struct {
		raw any
		out **int64
	}{
		{mtime, &record.MTimeMS},
		{ctime, &record.CTimeMS},
		{birthtime, &record.BirthtimeMS},
		{created, &record.AddedAt},
		{updated, &record.UpdatedAt},
		{lastScan, &record.LastScan},
	} {
		*field.out, err = dbtime.OptionalMillis(field.raw)
		if err != nil {
			return Record{}, err
		}
	}

	extraData := store.DecodeJSONObject(extra)
	if oldID, ok := extraData["oldLibraryItemId"].(string); ok {
		record.OldLibraryItemID = oldID
	}
	record.LibraryFiles = store.DecodeJSONSlice[map[string]any](libraryFiles)
	for _, file := range record.LibraryFiles {
		if metadata, ok := file["metadata"].(map[string]any); ok {
			file["fileType"] = FileType(metadata["ext"])
		}
	}
	return record, nil
}

// LoadMany reads common item rows in bounded SQL chunks. Results are keyed by
// library-item ID; IDs absent from the database are omitted.
func LoadMany(ctx context.Context, db *sql.DB, ids []string) (map[string]Record, error) {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	result := make(map[string]Record, len(unique))
	for chunk := range slices.Chunk(unique, 250) {
		marks := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		query := rowSelect + ` WHERE li.id IN (` + marks + `)`
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			record, err := ScanRecord(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[record.ID] = record
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// FileType classifies a file extension using the catalog's established rules.
func FileType(ext any) string {
	value, _ := ext.(string)
	switch strings.ToLower(strings.TrimPrefix(value, ".")) {
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
	default:
		return "unknown"
	}
}
