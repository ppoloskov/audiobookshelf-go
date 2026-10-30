package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"audiobookshelf-compat/internal/config"
)

// WriteItemMetadata writes the pinned v2.37.2 metadata.json sidecar shape from
// an expanded legacy library-item DTO. settings accepts storeMetadataWithItem
// and metadataFileFormat from ServerSettings.
func WriteItemMetadata(ctx context.Context, db *sql.DB, cfg config.Config, itemID string, itemJSON map[string]any, settings map[string]any) error {
	if db == nil {
		return errors.New("scanner: metadata database is nil")
	}
	if itemID == "" {
		return errors.New("scanner: metadata item id is empty")
	}
	mediaType, _ := itemJSON["mediaType"].(string)
	media, _ := itemJSON["media"].(map[string]any)
	if media == nil {
		return errors.New("scanner: expanded item media is missing")
	}
	metadata, _ := media["metadata"].(map[string]any)
	if metadata == nil {
		return errors.New("scanner: expanded item metadata is missing")
	}
	format, _ := settings["metadataFileFormat"].(string)
	if format != "json" {
		format = "json"
	}
	withItem, _ := settings["storeMetadataWithItem"].(bool)
	var itemPath string
	var isFile int
	if err := db.QueryRowContext(ctx, `SELECT path,isFile FROM libraryItems WHERE id=?`, itemID).Scan(&itemPath, &isFile); err != nil {
		return fmt.Errorf("scanner: metadata item %q: %w", itemID, err)
	}
	metadataDir := filepath.Join(cfg.MetadataPath, "items", itemID)
	if withItem && (mediaType == "podcast" || isFile == 0) {
		metadataDir = itemPath
	} else {
		withItem = false
	}
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		return fmt.Errorf("scanner: create metadata directory: %w", err)
	}
	metadataPath := filepath.Join(metadataDir, "metadata."+format)
	var payload map[string]any
	if mediaType == "podcast" {
		payload = podcastMetadataObject(media, metadata)
	} else {
		payload = bookMetadataObject(media, metadata)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("scanner: encode metadata sidecar: %w", err)
	}
	if err := os.WriteFile(metadataPath, append(raw, '\n'), 0644); err != nil {
		return fmt.Errorf("scanner: write metadata sidecar: %w", err)
	}
	if withItem {
		if err := recordStoredSidecar(ctx, db, itemID, itemPath, metadataPath); err != nil {
			return err
		}
	}
	return nil
}

// WriteMetadata loads the actual library item and related media metadata from
// the existing database and persists its source-compatible sidecar.
func (s *Scanner) WriteMetadata(ctx context.Context, itemID string) error {
	var item map[string]any
	var itemPath, mediaType, mediaID string
	var isFile int
	if err := s.db.QueryRowContext(ctx, `SELECT path,mediaType,mediaId,isFile FROM libraryItems WHERE id=?`, itemID).Scan(&itemPath, &mediaType, &mediaID, &isFile); err != nil {
		return err
	}
	media := map[string]any{"metadata": map[string]any{}}
	metadata := media["metadata"].(map[string]any)
	if mediaType == "book" {
		var title, subtitle, year, publishedDate, publisher, description, isbn, asin, language sql.NullString
		var explicit, abridged int64
		var narrators, audio, ebook, chapters, tags, genres []byte
		if err := s.db.QueryRowContext(ctx, `SELECT title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,narrators,audioFiles,ebookFile,chapters,tags,genres FROM books WHERE id=?`, mediaID).Scan(&title, &subtitle, &year, &publishedDate, &publisher, &description, &isbn, &asin, &language, &explicit, &abridged, &narrators, &audio, &ebook, &chapters, &tags, &genres); err != nil {
			return err
		}
		metadata["title"] = nullDBString(title)
		metadata["subtitle"] = nullDBString(subtitle)
		metadata["publishedYear"] = nullDBString(year)
		metadata["publishedDate"] = nullDBString(publishedDate)
		metadata["publisher"] = nullDBString(publisher)
		metadata["description"] = nullDBString(description)
		metadata["isbn"] = nullDBString(isbn)
		metadata["asin"] = nullDBString(asin)
		metadata["language"] = nullDBString(language)
		metadata["explicit"] = explicit != 0
		metadata["abridged"] = abridged != 0
		metadata["narrators"] = decodeJSONValue(narrators)
		metadata["genres"] = decodeJSONValue(genres)
		media["tags"] = decodeJSONValue(tags)
		media["chapters"] = decodeJSONValue(chapters)
		authorRows, err := s.db.QueryContext(ctx, `SELECT a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=? ORDER BY ba.createdAt ASC`, mediaID)
		if err != nil {
			return err
		}
		var authors []map[string]any
		for authorRows.Next() {
			var name sql.NullString
			if err := authorRows.Scan(&name); err != nil {
				authorRows.Close()
				return err
			}
			authors = append(authors, map[string]any{"name": name.String})
		}
		if err := authorRows.Err(); err != nil {
			authorRows.Close()
			return err
		}
		if err := authorRows.Close(); err != nil {
			return err
		}
		metadata["authors"] = authors
		seriesRows, err := s.db.QueryContext(ctx, `SELECT s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=? ORDER BY bs.createdAt ASC`, mediaID)
		if err != nil {
			return err
		}
		var series []map[string]any
		for seriesRows.Next() {
			var name, sequence sql.NullString
			if err := seriesRows.Scan(&name, &sequence); err != nil {
				seriesRows.Close()
				return err
			}
			series = append(series, map[string]any{"name": name.String, "sequence": nullDBString(sequence)})
		}
		if err := seriesRows.Err(); err != nil {
			seriesRows.Close()
			return err
		}
		if err := seriesRows.Close(); err != nil {
			return err
		}
		metadata["series"] = series
	} else if mediaType == "podcast" {
		var title, author, releaseDate, feedURL, imageURL, description, itunesPageURL, itunesID, itunesArtistID, language, podcastType sql.NullString
		var explicit int64
		var tags, genres []byte
		if err := s.db.QueryRowContext(ctx, `SELECT title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,tags,genres FROM podcasts WHERE id=?`, mediaID).Scan(&title, &author, &releaseDate, &feedURL, &imageURL, &description, &itunesPageURL, &itunesID, &itunesArtistID, &language, &podcastType, &explicit, &tags, &genres); err != nil {
			return err
		}
		metadata["title"] = nullDBString(title)
		metadata["author"] = nullDBString(author)
		metadata["releaseDate"] = nullDBString(releaseDate)
		metadata["feedURL"] = nullDBString(feedURL)
		metadata["imageURL"] = nullDBString(imageURL)
		metadata["description"] = nullDBString(description)
		metadata["itunesPageURL"] = nullDBString(itunesPageURL)
		metadata["itunesId"] = nullDBString(itunesID)
		metadata["itunesArtistId"] = nullDBString(itunesArtistID)
		metadata["language"] = nullDBString(language)
		metadata["podcastType"] = nullDBString(podcastType)
		metadata["explicit"] = explicit != 0
		metadata["genres"] = decodeJSONValue(genres)
		media["tags"] = decodeJSONValue(tags)
	} else {
		return fmt.Errorf("scanner: unsupported metadata media type %q", mediaType)
	}
	item = map[string]any{"id": itemID, "path": itemPath, "mediaType": mediaType, "isFile": isFile != 0, "media": media}
	settings := map[string]any{"storeMetadataWithItem": false, "metadataFileFormat": "json"}
	var settingsRaw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsRaw); err == nil {
		_ = json.Unmarshal(settingsRaw, &settings)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return WriteItemMetadata(ctx, s.db, s.cfg, itemID, item, settings)
}

func nullDBString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
func decodeJSONValue(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return []any{}
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return []any{}
	}
	return value
}

func bookMetadataObject(media, metadata map[string]any) map[string]any {
	authors := []string{}
	if values := anySlice(metadata["authors"]); len(values) > 0 {
		for _, v := range values {
			if m, ok := v.(map[string]any); ok {
				if name, _ := m["name"].(string); name != "" {
					authors = append(authors, name)
				}
			} else if name, ok := v.(string); ok {
				authors = append(authors, name)
			}
		}
	}
	series := []string{}
	if values := anySlice(metadata["series"]); len(values) > 0 {
		for _, v := range values {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			sequence, _ := m["sequence"].(string)
			if name != "" {
				if sequence != "" {
					name += " #" + sequence
				}
				series = append(series, name)
			}
		}
	}
	return map[string]any{"tags": valueOrEmpty(media["tags"]), "chapters": valueOrEmpty(media["chapters"]), "title": metadata["title"], "subtitle": metadata["subtitle"], "authors": authors, "narrators": valueOrEmpty(metadata["narrators"]), "series": series, "genres": valueOrEmpty(metadata["genres"]), "publishedYear": metadata["publishedYear"], "publishedDate": metadata["publishedDate"], "publisher": metadata["publisher"], "description": metadata["description"], "isbn": metadata["isbn"], "asin": metadata["asin"], "language": metadata["language"], "explicit": truth(metadata["explicit"]), "abridged": truth(metadata["abridged"])}
}

func podcastMetadataObject(media, metadata map[string]any) map[string]any {
	return map[string]any{"tags": valueOrEmpty(media["tags"]), "title": metadata["title"], "author": metadata["author"], "description": metadata["description"], "releaseDate": metadata["releaseDate"], "genres": valueOrEmpty(metadata["genres"]), "feedURL": firstValue(metadata, "feedURL", "feedUrl"), "imageURL": firstValue(metadata, "imageURL", "imageUrl"), "itunesPageURL": firstValue(metadata, "itunesPageURL", "itunesPageUrl"), "itunesId": metadata["itunesId"], "itunesArtistId": metadata["itunesArtistId"], "asin": metadata["asin"], "language": metadata["language"], "explicit": truth(metadata["explicit"]), "podcastType": firstValue(metadata, "podcastType", "type")}
}

func valueOrEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}
func anySlice(v any) []any {
	switch values := v.(type) {
	case []any:
		return values
	case []map[string]any:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	case []string:
		out := make([]any, len(values))
		for i, value := range values {
			out[i] = value
		}
		return out
	default:
		return nil
	}
}
func truth(v any) bool { b, _ := v.(bool); return b }
func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := values[key]; ok {
			return v
		}
	}
	return nil
}

func recordStoredSidecar(ctx context.Context, db *sql.DB, itemID, itemPath, metadataPath string) error {
	info, err := os.Stat(metadataPath)
	if err != nil {
		return err
	}
	fileMeta := makeFileMeta(itemPath, file{path: metadataPath, info: info})
	if obj, ok := fileMeta["metadata"].(map[string]any); ok {
		obj["filename"] = "metadata.json"
		obj["relPath"] = "metadata.json"
	}
	fileMeta["fileType"] = "metadata"
	var raw []byte
	var libraryFiles string
	err = db.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryFiles)
	if err != nil {
		return err
	}
	var files []map[string]any
	if json.Unmarshal([]byte(libraryFiles), &files) != nil {
		files = []map[string]any{}
	}
	fileIndex := -1
	for i, file := range files {
		if meta, ok := file["metadata"].(map[string]any); ok {
			if path, _ := meta["path"].(string); path == metadataPath {
				fileIndex = i
				break
			}
		}
	}
	if fileIndex >= 0 {
		files[fileIndex] = fileMeta
	} else {
		files = append(files, fileMeta)
	}
	raw, err = json.Marshal(files)
	if err != nil {
		return err
	}
	size := int64(0)
	for _, file := range files {
		if meta, ok := file["metadata"].(map[string]any); ok {
			switch n := meta["size"].(type) {
			case int64:
				size += n
			case int:
				size += int64(n)
			case float64:
				size += int64(n)
			}
		}
	}
	now := dbtime.Now()
	dirInfo, err := os.Stat(itemPath)
	if err != nil {
		return err
	}
	mtime, ctime, birthtime := statTimes(dirInfo)
	_, err = db.ExecContext(ctx, `UPDATE libraryItems SET libraryFiles=?,size=?,mtime=?,ctime=?,birthtime=?,updatedAt=? WHERE id=?`, string(raw), size, timeString(mtime), timeString(ctime), timeString(birthtime), now, itemID)
	return err
}
