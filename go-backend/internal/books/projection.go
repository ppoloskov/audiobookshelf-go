package books

import (
	"encoding/json"
	"net/url"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// Full projects a book as the legacy full media value.
func (b Book) Full(itemID string) (wire.BookFull, error) {
	audioJSON, err := encodeJSON(b.audio)
	if err != nil {
		return wire.BookFull{}, err
	}
	chaptersJSON, err := encodeJSON(b.chapters)
	if err != nil {
		return wire.BookFull{}, err
	}
	ebookJSON, err := encodeJSON(b.ebook)
	if err != nil {
		return wire.BookFull{}, err
	}
	return wire.BookFull{
		ID:            b.row.id.String,
		LibraryItemID: itemID,
		Metadata:      b.fullMetadata(),
		CoverPath:     store.NullStringPtr(b.row.coverPath),
		Tags:          b.tags,
		AudioFiles:    audioJSON,
		Chapters:      chaptersJSON,
		EbookFile:     ebookJSON,
	}, nil
}

// Minified projects the shelf and collection book media value.
func (b Book) Minified(titlePrefix *string) (wire.BookMinified, error) {
	var ebookFormat json.RawMessage
	var err error
	if format, ok := b.ebook["ebookFormat"]; ok {
		ebookFormat, err = encodeJSON(format)
		if err != nil {
			return wire.BookMinified{}, err
		}
	}
	return wire.BookMinified{
		ID:            b.row.id.String,
		Metadata:      b.minifiedMetadata(titlePrefix),
		CoverPath:     store.NullStringPtr(b.row.coverPath),
		Tags:          b.tags,
		NumTracks:     includedAudioCount(b.audio),
		NumAudioFiles: len(b.audio),
		NumChapters:   len(b.chapters),
		Duration:      durationPointer(b.row.duration),
		Size:          audioSize(b.audio, b.ebook),
		EbookFormat:   ebookFormat,
	}, nil
}

// Expanded projects the item-detail and socket book media value.
func (b Book) Expanded(itemID string, titlePrefix *string) (wire.BookExpanded, error) {
	full, err := b.Full(itemID)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	minified, err := b.Minified(titlePrefix)
	if err != nil {
		return wire.BookExpanded{}, err
	}
	tracksJSON, err := encodeJSON(makeTracks(itemID, b.audio))
	if err != nil {
		return wire.BookExpanded{}, err
	}
	return wire.BookExpanded{
		ID:            full.ID,
		Metadata:      expandedMetadata(full.Metadata, minified.Metadata, b.row.description),
		CoverPath:     full.CoverPath,
		Tags:          full.Tags,
		NumTracks:     minified.NumTracks,
		NumAudioFiles: minified.NumAudioFiles,
		NumChapters:   minified.NumChapters,
		Duration:      minified.Duration,
		Size:          minified.Size,
		EbookFormat:   minified.EbookFormat,
		LibraryItemID: full.LibraryItemID,
		AudioFiles:    full.AudioFiles,
		Chapters:      full.Chapters,
		EbookFile:     full.EbookFile,
		Tracks:        tracksJSON,
	}, nil
}

func includedAudioCount(audio []map[string]any) int {
	n := 0
	for _, file := range audio {
		if file["exclude"] != true {
			n++
		}
	}
	return n
}

func audioSize(audio []map[string]any, ebook map[string]any) int64 {
	var size int64
	for _, file := range audio {
		if metadata, ok := file["metadata"].(map[string]any); ok {
			size += int64(floating(metadata["size"]))
		}
	}
	if metadata, ok := ebook["metadata"].(map[string]any); ok {
		size += int64(floating(metadata["size"]))
	}
	return size
}

func makeTracks(itemID string, audio []map[string]any) []map[string]any {
	tracks := make([]map[string]any, 0)
	var offset float64
	for _, file := range audio {
		if file["exclude"] == true {
			continue
		}
		track := make(map[string]any, len(file)+3)
		for key, value := range file {
			track[key] = value
		}
		metadata, _ := file["metadata"].(map[string]any)
		track["title"] = metadata["filename"]
		track["startOffset"] = offset
		track["contentUrl"] = "/api/items/" + itemID + "/file/" + url.PathEscape(stringValue(file["ino"]))
		offset += floating(file["duration"])
		tracks = append(tracks, track)
	}
	return tracks
}

func durationPointer(value any) *float64 {
	switch number := value.(type) {
	case float64:
		return &number
	case int64:
		converted := float64(number)
		return &converted
	case int:
		converted := float64(number)
		return &converted
	default:
		return nil
	}
}

func floating(value any) float64 {
	number, _ := value.(float64)
	return number
}

func stringValue(value any) string { text, _ := value.(string); return text }

func encodeJSON(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	return json.RawMessage(encoded), err
}
