package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// WriteMetadata writes the item's pinned v2.37.2 metadata.json sidecar, built
// from the same expanded item the clients receive: beside the item when
// storeMetadataWithItem is on (a podcast folder or a book folder), otherwise
// under the metadata directory.
func (s *Scanner) WriteMetadata(ctx context.Context, itemID string) error {
	item, err := s.itemEvent(ctx, itemID)
	if err != nil {
		return fmt.Errorf("scanner: metadata item %q: %w", itemID, err)
	}
	mediaRaw, err := json.Marshal(item.Media)
	if err != nil {
		return fmt.Errorf("scanner: encode expanded item media: %w", err)
	}
	var media map[string]any
	if err := json.Unmarshal(mediaRaw, &media); err != nil {
		return fmt.Errorf("scanner: decode expanded item media: %w", err)
	}
	metadata, _ := media["metadata"].(map[string]any)
	if metadata == nil {
		return errors.New("scanner: expanded item metadata is missing")
	}
	withItem := serversettings.Load(ctx, s.db).Bool("storeMetadataWithItem") && (item.MediaType == "podcast" || !item.IsFile)
	metadataDir := filepath.Join(s.cfg.Dirs().Items, itemID)
	if withItem {
		metadataDir = item.Path
	} else if s.cfg.MetadataPath == "" {
		return errors.New("scanner: metadata path is empty")
	}
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		return fmt.Errorf("scanner: create metadata directory: %w", err)
	}
	metadataPath := filepath.Join(metadataDir, "metadata.json")
	payload := bookMetadataObject(media, metadata)
	if item.MediaType == "podcast" {
		payload = podcastMetadataObject(media, metadata)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("scanner: encode metadata sidecar: %w", err)
	}
	if err := os.WriteFile(metadataPath, append(raw, '\n'), 0644); err != nil {
		return fmt.Errorf("scanner: write metadata sidecar: %w", err)
	}
	if withItem {
		return recordStoredSidecar(ctx, s.db, itemID, item.Path, metadataPath)
	}
	return nil
}

func bookMetadataObject(media, metadata map[string]any) map[string]any {
	authors := []string{}
	if values, _ := metadata["authors"].([]any); len(values) > 0 {
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
	if values, _ := metadata["series"].([]any); len(values) > 0 {
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
	return map[string]any{"tags": valueOrEmpty(media["tags"]), "title": metadata["title"], "author": metadata["author"], "description": metadata["description"], "releaseDate": metadata["releaseDate"], "genres": valueOrEmpty(metadata["genres"]), "feedURL": firstValue(metadata, "feedURL", "feedUrl"), "imageURL": firstValue(metadata, "imageURL", "imageUrl"), "itunesPageURL": firstValue(metadata, "itunesPageURL", "itunesPageUrl"), "itunesId": metadata["itunesId"], "itunesArtistId": metadata["itunesArtistId"], "language": metadata["language"], "explicit": truth(metadata["explicit"]), "podcastType": firstValue(metadata, "podcastType", "type")}
}

func valueOrEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
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
	entry := newLibraryFile(itemPath, file{path: metadataPath, info: info})
	entry.Metadata.Filename, entry.Metadata.RelPath, entry.FileType = "metadata.json", "metadata.json", "metadata"
	var libraryFiles []byte
	if err = db.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&libraryFiles); err != nil {
		return err
	}
	files := parseStoredLibraryFiles(libraryFiles)
	if i := slices.IndexFunc(files, func(f libraryFile) bool { return f.Metadata.Path == metadataPath }); i >= 0 {
		files[i] = entry
	} else {
		files = append(files, entry)
	}
	raw, err := json.Marshal(files)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	dirInfo, err := os.Stat(itemPath)
	if err != nil {
		return err
	}
	mtime, ctime, birthtime := statTimes(itemPath, dirInfo)
	_, err = db.ExecContext(ctx, `UPDATE libraryItems SET libraryFiles=?,size=?,mtime=?,ctime=?,birthtime=?,updatedAt=? WHERE id=?`, string(raw), libraryFileSize(files), timeString(mtime), timeString(ctime), timeString(birthtime), now, itemID)
	return err
}
