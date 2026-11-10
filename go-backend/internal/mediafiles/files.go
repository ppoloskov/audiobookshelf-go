// Package mediafiles owns the stored file, audio-file and chapter
// representations shared by books, podcasts, items and the scanner.
package mediafiles

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

// FileMetadata is the stored FileMetadata of a library or audio file
// (server/objects/metadata/FileMetadata.js). Numbers are float64 so rows with
// fractional values still decode; whole numbers encode without a fraction.
type FileMetadata struct {
	Filename    string  `json:"filename"`
	Ext         string  `json:"ext"`
	Path        string  `json:"path"`
	RelPath     string  `json:"relPath"`
	Size        float64 `json:"size"`
	MtimeMs     float64 `json:"mtimeMs"`
	CtimeMs     float64 `json:"ctimeMs"`
	BirthtimeMs float64 `json:"birthtimeMs"`
}

// AudioChapter is one chapter of an audio file or a book
// (server/objects/files/AudioFile.js chapters, Book.chapters).
type AudioChapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// AudioFile is one entry of books.audioFiles (server/objects/files/AudioFile.js
// toJSON). A freshly probed file and a stored row are this one type; nullable
// upstream values are pointers so null round-trips as null.
type AudioFile struct {
	Index                int               `json:"index"`
	Ino                  string            `json:"ino"`
	Metadata             FileMetadata      `json:"metadata"`
	AddedAt              int64             `json:"addedAt"`
	UpdatedAt            int64             `json:"updatedAt"`
	TrackNumFromMeta     *int              `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int              `json:"discNumFromMeta"`
	TrackNumFromFilename *int              `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int              `json:"discNumFromFilename"`
	ManuallyVerified     bool              `json:"manuallyVerified"`
	Exclude              bool              `json:"exclude"`
	Error                *string           `json:"error"`
	Format               string            `json:"format"`
	Duration             float64           `json:"duration"`
	BitRate              *int64            `json:"bitRate"`
	Language             *string           `json:"language"`
	Codec                *string           `json:"codec"`
	TimeBase             *string           `json:"timeBase"`
	Channels             *int              `json:"channels"`
	ChannelLayout        *string           `json:"channelLayout"`
	Chapters             []AudioChapter    `json:"chapters"`
	EmbeddedCoverArt     *string           `json:"embeddedCoverArt"`
	MetaTags             map[string]string `json:"metaTags"`
	MimeType             string            `json:"mimeType"`
}
