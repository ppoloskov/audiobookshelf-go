package media

import (
	"testing"
)

func TestExtractAudioReturnsNilWithoutAudioStream(t *testing.T) {
	info := Info{
		Streams: []Stream{{Type: "video", Codec: "mjpeg"}},
	}
	if got := ExtractAudio(info); got != nil {
		t.Fatalf("ExtractAudio = %+v, want nil", got)
	}
}

func TestExtractAudioSelectsDefaultStreamAndMapsMetadata(t *testing.T) {
	info := Info{
		Format: Format{
			LongName: "MP2/3 (MPEG audio layer 2/3)",
			Duration: "12.5",
			BitRate:  "66336",
			Tags: map[string]string{
				"TITLE":        "  Episode 1 ",
				"ALBUM":        "My Pod",
				"track":        "99/100",
				"disc":         "2",
				"MVNM":         "My Series",
				"episode-type": "full",
			},
		},
		Streams: []Stream{
			{Type: "video", Codec: "mjpeg"},
			{Type: "audio", Codec: "aac", BitRate: "32000"},
			{
				Type:          "audio",
				Codec:         "mp3",
				TimeBase:      "1/14112000",
				BitRate:       "64000",
				Channels:      2,
				ChannelLayout: "stereo(side)",
				Tags:          map[string]string{"Language": " eng "},
				Disposition: struct {
					Default int `json:"default"`
				}{Default: 1},
			},
		},
		Chapters: []Chapter{
			{StartTime: "5.0", EndTime: "12.5", Tags: map[string]string{"Title": " Second "}},
			{StartTime: "0.0", EndTime: "5.0", Tags: map[string]string{"title": "First"}},
		},
	}

	got := ExtractAudio(info)
	if got == nil {
		t.Fatal("ExtractAudio = nil, want non-nil")
	}
	if got.Format != "MP2/3 (MPEG audio layer 2/3)" || got.Duration != 12.5 {
		t.Errorf("format/duration = %q / %v", got.Format, got.Duration)
	}
	if got.BitRate == nil || *got.BitRate != 64000 {
		t.Errorf("bitRate = %v, want 64000 (stream bit rate before format)", got.BitRate)
	}
	if got.Codec == nil || *got.Codec != "mp3" {
		t.Errorf("codec = %v, want mp3 (default stream)", got.Codec)
	}
	if got.ChannelLayout == nil || *got.ChannelLayout != "stereo" {
		t.Errorf("channelLayout = %v, want stereo", got.ChannelLayout)
	}
	if got.Channels == nil || *got.Channels != 2 {
		t.Errorf("channels = %v, want 2", got.Channels)
	}
	if got.Language == nil || *got.Language != "eng" {
		t.Errorf("language = %v, want eng", got.Language)
	}
	if got.EmbeddedCoverArt == nil || *got.EmbeddedCoverArt != "mjpeg" {
		t.Errorf("embeddedCoverArt = %v, want mjpeg", got.EmbeddedCoverArt)
	}
	if got.MetaTags.Title != "Episode 1" || got.MetaTags.Album != "My Pod" || got.MetaTags.Series != "My Series" || got.MetaTags.EpisodeType != "full" {
		t.Errorf("metaTags = %+v", got.MetaTags)
	}
	if got.TrackNumFromMeta == nil || *got.TrackNumFromMeta != 99 {
		t.Errorf("trackNumFromMeta = %v, want 99", got.TrackNumFromMeta)
	}
	if got.DiscNumFromMeta == nil || *got.DiscNumFromMeta != 2 {
		t.Errorf("discNumFromMeta = %v, want 2", got.DiscNumFromMeta)
	}
	if len(got.Chapters) != 2 || got.Chapters[0].ID != 0 || got.Chapters[0].Title != "First" || got.Chapters[1].ID != 1 || got.Chapters[1].Title != "Second" {
		t.Errorf("chapters = %+v", got.Chapters)
	}
}

func TestExtractAudioFallsBackToFormatBitRateAndStreamTags(t *testing.T) {
	info := Info{
		Format: Format{
			Name:    "mp3",
			BitRate: "128000",
		},
		Streams: []Stream{
			{
				Type:  "audio",
				Codec: "mp3",
				Tags:  map[string]string{"artist": "Stream Artist"},
			},
		},
	}
	got := ExtractAudio(info)
	if got == nil {
		t.Fatal("ExtractAudio = nil")
	}
	if got.BitRate == nil || *got.BitRate != 128000 {
		t.Errorf("bitRate = %v, want 128000", got.BitRate)
	}
	if got.MetaTags.Artist != "Stream Artist" {
		t.Errorf("artist = %q, want Stream Artist", got.MetaTags.Artist)
	}
	if got.ChannelLayout != nil || got.Channels != nil || got.Language != nil || got.EmbeddedCoverArt != nil {
		t.Errorf("expected nil optional fields, got %+v", got)
	}
	if got.Chapters == nil || len(got.Chapters) != 0 {
		t.Errorf("chapters = %#v, want empty non-nil slice", got.Chapters)
	}
}

func TestLeadingNumber(t *testing.T) {
	cases := []struct {
		in   string
		want *int64
	}{
		{"3", ptrInt64(3)},
		{" 3 / 12 ", ptrInt64(3)},
		{"", nil},
		{"abc", nil},
	}
	for _, tc := range cases {
		got := LeadingNumber(tc.in)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("LeadingNumber(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }
