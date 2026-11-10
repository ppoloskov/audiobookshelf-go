package libitem

import (
	"net/url"
	"strings"

	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/jsonvalue"
)

// File is one stored file entry (libraryItems.libraryFiles, books.audioFiles,
// books.ebookFile). It stays a map so keys the server does not read round-trip
// unchanged; every read of the keys it does know goes through these methods.
type File map[string]any

// Metadata is the stored metadata object, nil when missing or malformed.
func (f File) Metadata() map[string]any {
	m, _ := f["metadata"].(map[string]any)
	return m
}

// Ino is the file's inode string, "" when missing.
func (f File) Ino() string { s, _ := f["ino"].(string); return s }

// Path is metadata.path, "" when missing.
func (f File) Path() string { s, _ := f.Metadata()["path"].(string); return s }

// Ext is metadata.ext as stored (with its dot), "" when missing.
func (f File) Ext() string { s, _ := f.Metadata()["ext"].(string); return s }

// Size is metadata.size, 0 when missing.
func (f File) Size() int64 { n, _ := f.Metadata()["size"].(float64); return int64(n) }

// Excluded is the audio file's exclude flag (JavaScript truthiness).
func (f File) Excluded() bool { return jsonvalue.Truthy(f["exclude"]) }

// Kind is LibraryFile.fileType (LibraryFile.js:42-49) of the file's extension.
func (f File) Kind() string { return filekind.Of("." + strings.TrimPrefix(f.Ext(), ".")) }

// ContentURL is the route that serves the file of the item.
func (f File) ContentURL(itemID string) string {
	return "/api/items/" + itemID + "/file/" + url.PathEscape(f.Ino())
}
