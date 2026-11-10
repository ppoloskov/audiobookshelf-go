package scanner

import (
	"audiobookshelf-compat/internal/serversettings"
	"context"
	"slices"

	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/libitem"
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

// itemEvent loads an item as the realtime events carry it (expanded). Audio
// files that were scanned during this rescan and appended to the book are still
// AudioFile instances when upstream serializes them: structuredClone copies
// their own properties instead of calling toJSON, so the event carries every
// metaTags key (null when unset), the scan-only metadata.wasModified flag and
// no mimeType getter (Book.js:629,683; BookScanner.js:100-103). Later reads
// come from the database and have the plain shape.
func (s *Scanner) itemEvent(ctx context.Context, itemID string, scannedPaths ...string) (items.Expanded, error) {
	it, err := items.LoadOne(ctx, s.db, itemID, items.Options{Episodes: true})
	if err != nil {
		return items.Expanded{}, err
	}
	item := it.Expanded(serversettings.Load(ctx, s.db).SortingPrefixes())
	if media, ok := item.Media.(libitem.BookExpanded); ok && len(scannedPaths) > 0 {
		instanceShape(media.AudioFiles, scannedPaths)
		instanceShape(media.Tracks, scannedPaths)
	}
	return item, nil
}

func instanceShape[M ~map[string]any](entries []M, scanned []string) {
	for _, entry := range entries {
		meta, _ := entry["metadata"].(map[string]any)
		if path, _ := meta["path"].(string); meta == nil || !slices.Contains(scanned, path) {
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
}
