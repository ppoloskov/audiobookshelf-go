package library

import (
	"bytes"
	"encoding/json"

	"audiobookshelf-compat/internal/db"
)

// optional is a JSON field that tells absent (Set false), null (Null) and a
// value apart: the three nullable library settings are cleared by null.
type optional[T any] struct {
	Set, Null bool
	Value     T
}

// UnmarshalJSON records that the key was present and whether it was null.
func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(data, &o.Value)
}

// into sets *dst to the value, or to nil for null; absent leaves it alone.
func (o optional[T]) into(dst **T) {
	switch {
	case !o.Set:
	case o.Null:
		*dst = nil
	default:
		*dst = &o.Value
	}
}

// settingsPatch is the settings object of a create or update body: a nil
// pointer (or nil slice) is a key that was not sent. Keys that belong to the
// other media type are ignored, as upstream ignores keys outside the defaults.
type settingsPatch struct {
	CoverAspectRatio                   *float64          `json:"coverAspectRatio"`
	DisableWatcher                     *bool             `json:"disableWatcher"`
	AutoScanCronExpression             optional[string]  `json:"autoScanCronExpression"`
	SkipMatchingMediaWithAsin          *bool             `json:"skipMatchingMediaWithAsin"`
	SkipMatchingMediaWithIsbn          *bool             `json:"skipMatchingMediaWithIsbn"`
	AudiobooksOnly                     *bool             `json:"audiobooksOnly"`
	EpubsAllowScriptedContent          *bool             `json:"epubsAllowScriptedContent"`
	HideSingleBookSeries               *bool             `json:"hideSingleBookSeries"`
	OnlyShowLaterBooksInContinueSeries *bool             `json:"onlyShowLaterBooksInContinueSeries"`
	MetadataPrecedence                 []string          `json:"metadataPrecedence"`
	PodcastSearchRegion                *string           `json:"podcastSearchRegion"`
	MarkAsFinishedPercentComplete      optional[float64] `json:"markAsFinishedPercentComplete"`
	MarkAsFinishedTimeRemaining        optional[float64] `json:"markAsFinishedTimeRemaining"`
}

// apply writes the sent keys that exist for the media type onto s.
func (p settingsPatch) apply(s *db.LibrarySettings, podcast bool) {
	if p.CoverAspectRatio != nil {
		s.CoverAspectRatio = *p.CoverAspectRatio
	}
	if p.DisableWatcher != nil {
		s.DisableWatcher = *p.DisableWatcher
	}
	p.AutoScanCronExpression.into(&s.AutoScanCronExpression)
	p.MarkAsFinishedPercentComplete.into(&s.MarkAsFinishedPercentComplete)
	p.MarkAsFinishedTimeRemaining.into(&s.MarkAsFinishedTimeRemaining)
	if podcast {
		if p.PodcastSearchRegion != nil {
			s.PodcastSearchRegion = p.PodcastSearchRegion
		}
		return
	}
	for _, f := range []struct{ src, dst **bool }{
		{&p.SkipMatchingMediaWithAsin, &s.SkipMatchingMediaWithAsin}, {&p.SkipMatchingMediaWithIsbn, &s.SkipMatchingMediaWithIsbn},
		{&p.AudiobooksOnly, &s.AudiobooksOnly}, {&p.EpubsAllowScriptedContent, &s.EpubsAllowScriptedContent},
		{&p.HideSingleBookSeries, &s.HideSingleBookSeries}, {&p.OnlyShowLaterBooksInContinueSeries, &s.OnlyShowLaterBooksInContinueSeries},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if p.MetadataPrecedence != nil {
		s.MetadataPrecedence = p.MetadataPrecedence
	}
}

// defaultSettings is Library.getDefaultLibrarySettingsForMediaType; a new
// library stores it with the sent keys applied.
func defaultSettings(mediaType string) db.LibrarySettings {
	s := db.LibrarySettings{CoverAspectRatio: 1, MarkAsFinishedTimeRemaining: db.Ptr(10.0)}
	if mediaType == "podcast" {
		s.PodcastSearchRegion = db.Ptr("us")
		return s
	}
	off := func() *bool { return db.Ptr(false) }
	s.SkipMatchingMediaWithAsin, s.SkipMatchingMediaWithIsbn, s.AudiobooksOnly = off(), off(), off()
	s.EpubsAllowScriptedContent, s.HideSingleBookSeries, s.OnlyShowLaterBooksInContinueSeries = off(), off(), off()
	s.MetadataPrecedence = []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}
	return s
}
