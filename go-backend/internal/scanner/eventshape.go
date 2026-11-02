package scanner

import (
	"context"
	"encoding/json"

	"audiobookshelf-compat/internal/wire"
)

// audioMetaTagKeys are the AudioMetaTags own properties
// (server/objects/metadata/AudioMetaTags.js:2-37).
var audioMetaTagKeys = []string{
	"tagAlbum", "tagAlbumSort", "tagArtist", "tagArtistSort", "tagGenre", "tagTitle", "tagTitleSort",
	"tagSeries", "tagSeriesPart", "tagGrouping", "tagTrack", "tagDisc", "tagSubtitle", "tagAlbumArtist",
	"tagDate", "tagComposer", "tagPublisher", "tagComment", "tagDescription", "tagEncoder", "tagEncodedBy",
	"tagIsbn", "tagLanguage", "tagASIN", "tagItunesId", "tagPodcastType", "tagEpisodeType",
	"tagOverdriveMediaMarker", "tagOriginalYear", "tagReleaseCountry", "tagReleaseType", "tagReleaseStatus",
	"tagISRC", "tagMusicBrainzTrackId", "tagMusicBrainzAlbumId", "tagMusicBrainzAlbumArtistId", "tagMusicBrainzArtistId",
}

// rescanItemJSON serializes a rescanned item for a realtime event. Audio files
// that were scanned during this rescan and appended to the book are still
// AudioFile instances when upstream serializes them: structuredClone copies
// their own properties instead of calling toJSON, so the event carries every
// metaTags key (null when unset), the scan-only metadata.wasModified flag and
// no mimeType getter (Book.js:629,683; BookScanner.js:100-103). Later reads
// come from the database and have the plain shape.
func (s *Scanner) rescanItemJSON(ctx context.Context, itemID string, scannedPaths []string) (map[string]any, error) {
	item, err := s.items.ItemJSON(ctx, itemID, true)
	if err != nil || len(scannedPaths) == 0 {
		return item, err
	}
	media, ok := item["media"].(wire.BookExpanded)
	if !ok {
		return item, nil
	}
	scanned := make(map[string]bool, len(scannedPaths))
	for _, p := range scannedPaths {
		scanned[p] = true
	}
	media.AudioFiles = instanceShape(media.AudioFiles, scanned)
	media.Tracks = instanceShape(media.Tracks, scanned)
	item["media"] = media
	return item, nil
}

func instanceShape(raw json.RawMessage, scanned map[string]bool) json.RawMessage {
	var entries []map[string]any
	if json.Unmarshal(raw, &entries) != nil {
		return raw
	}
	for _, entry := range entries {
		meta, _ := entry["metadata"].(map[string]any)
		if path, _ := meta["path"].(string); !scanned[path] {
			continue
		}
		meta["wasModified"] = false
		delete(entry, "mimeType")
		tags, _ := entry["metaTags"].(map[string]any)
		if tags == nil {
			tags = map[string]any{}
			entry["metaTags"] = tags
		}
		for _, key := range audioMetaTagKeys {
			if _, ok := tags[key]; !ok {
				tags[key] = nil
			}
		}
	}
	out, err := json.Marshal(entries)
	if err != nil {
		return raw
	}
	return out
}
