package media

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
)

// AudioMetaTags holds the metadata tags scraped from an audio file by ffprobe
// (server/objects/metadata/AudioMetaTags.js). Empty fields are omitted in JSON.
type AudioMetaTags struct {
	Album                  string `json:"tagAlbum,omitempty"`
	AlbumSort              string `json:"tagAlbumSort,omitempty"`
	Artist                 string `json:"tagArtist,omitempty"`
	ArtistSort             string `json:"tagArtistSort,omitempty"`
	Genre                  string `json:"tagGenre,omitempty"`
	Title                  string `json:"tagTitle,omitempty"`
	TitleSort              string `json:"tagTitleSort,omitempty"`
	Series                 string `json:"tagSeries,omitempty"`
	SeriesPart             string `json:"tagSeriesPart,omitempty"`
	Grouping               string `json:"tagGrouping,omitempty"`
	Track                  string `json:"tagTrack,omitempty"`
	Disc                   string `json:"tagDisc,omitempty"`
	Subtitle               string `json:"tagSubtitle,omitempty"`
	AlbumArtist            string `json:"tagAlbumArtist,omitempty"`
	Date                   string `json:"tagDate,omitempty"`
	Composer               string `json:"tagComposer,omitempty"`
	Publisher              string `json:"tagPublisher,omitempty"`
	Comment                string `json:"tagComment,omitempty"`
	Description            string `json:"tagDescription,omitempty"`
	Encoder                string `json:"tagEncoder,omitempty"`
	EncodedBy              string `json:"tagEncodedBy,omitempty"`
	ISBN                   string `json:"tagIsbn,omitempty"`
	Language               string `json:"tagLanguage,omitempty"`
	ASIN                   string `json:"tagASIN,omitempty"`
	ItunesID               string `json:"tagItunesId,omitempty"`
	PodcastType            string `json:"tagPodcastType,omitempty"`
	EpisodeType            string `json:"tagEpisodeType,omitempty"`
	OverdriveMediaMarker   string `json:"tagOverdriveMediaMarker,omitempty"`
	OriginalYear           string `json:"tagOriginalYear,omitempty"`
	ReleaseCountry         string `json:"tagReleaseCountry,omitempty"`
	ReleaseType            string `json:"tagReleaseType,omitempty"`
	ReleaseStatus          string `json:"tagReleaseStatus,omitempty"`
	ISRC                   string `json:"tagISRC,omitempty"`
	MusicBrainzTrackID     string `json:"tagMusicBrainzTrackId,omitempty"`
	MusicBrainzAlbumID     string `json:"tagMusicBrainzAlbumId,omitempty"`
	MusicBrainzAlbumArtist string `json:"tagMusicBrainzAlbumArtistId,omitempty"`
	MusicBrainzArtistID    string `json:"tagMusicBrainzArtistId,omitempty"`
}

// AudioChapter is a chapter extracted from an audio file, sorted by start time.
type AudioChapter struct {
	ID    int64
	Start float64
	End   float64
	Title string
}

// AudioProbe holds the audio stream metadata, chapters, and tags extracted
// from probing an audio file (prober.js / MediaProbeData.js).
type AudioProbe struct {
	Format           string
	Duration         float64
	BitRate          *int64
	Language         *string
	Codec            *string
	TimeBase         *string
	Channels         *int64
	ChannelLayout    *string
	EmbeddedCoverArt *string
	Chapters         []AudioChapter
	MetaTags         AudioMetaTags
	TrackNumFromMeta *int64
	DiscNumFromMeta  *int64
}

// ProbeAudio runs ffprobe on path and extracts its audio stream metadata,
// chapters, and tags. It returns (nil, nil) when the file has no audio stream.
func ProbeAudio(ctx context.Context, path string) (*AudioProbe, error) {
	info, err := Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	return ExtractAudio(info), nil
}

// ExtractAudio converts an ffprobe Info report into an AudioProbe, returning nil
// when the report contains no audio stream.
func ExtractAudio(info Info) *AudioProbe {
	var audio *Stream
	var embedded *string
	hasDefault := false
	for i := range info.Streams {
		s := &info.Streams[i]
		if s.Type == "video" && embedded == nil && s.Codec != "" {
			embedded = &s.Codec
		}
		if s.Type == "audio" && (audio == nil || (s.Disposition.Default == 1 && !hasDefault)) {
			audio, hasDefault = s, hasDefault || s.Disposition.Default == 1
		}
	}
	if audio == nil {
		return nil
	}

	duration, _ := strconv.ParseFloat(info.Format.Duration, 64) // missing or malformed counts as 0
	out := &AudioProbe{
		Format:           cmp.Or(info.Format.LongName, info.Format.Name),
		Duration:         duration,
		EmbeddedCoverArt: embedded,
		Chapters:         make([]AudioChapter, 0, len(info.Chapters)),
	}
	if audio.Codec != "" {
		out.Codec = &audio.Codec
	}
	if audio.TimeBase != "" {
		out.TimeBase = &audio.TimeBase
	}
	if rate, _ := strconv.ParseInt(audio.BitRate, 10, 64); rate > 0 { // stream bit rate takes precedence (MediaProbeData.js:54)
		out.BitRate = &rate
	} else if rate, _ := strconv.ParseInt(info.Format.BitRate, 10, 64); rate > 0 { // malformed or non-positive falls back to nil
		out.BitRate = &rate
	}
	if audio.Channels > 0 {
		ch := int64(audio.Channels)
		out.Channels = &ch
	}
	if audio.ChannelLayout != "" {
		layout := strings.Split(audio.ChannelLayout, "(")[0]
		out.ChannelLayout = &layout
	}
	if lang := tagLookup(audio.Tags, "language"); lang != "" {
		out.Language = &lang
	}

	tags := info.Format.Tags
	if len(tags) == 0 {
		tags = audio.Tags
	}
	out.MetaTags = ParseMetaTags(tags)
	out.TrackNumFromMeta = LeadingNumber(out.MetaTags.Track)
	out.DiscNumFromMeta = LeadingNumber(out.MetaTags.Disc)

	for _, ch := range info.Chapters {
		start, end := ch.Seconds()
		out.Chapters = append(out.Chapters, AudioChapter{
			Start: start,
			End:   end,
			Title: tagLookup(ch.Tags, "title"),
		})
	}
	slices.SortStableFunc(out.Chapters, func(a, b AudioChapter) int { return cmp.Compare(a.Start, b.Start) })
	for i := range out.Chapters {
		out.Chapters[i].ID = int64(i)
	}
	return out
}

// ParseMetaTags extracts AudioMetaTags from an ffprobe tag map using
// upstream's case-insensitive alias precedence (prober.js:172-227).
func ParseMetaTags(raw map[string]string) AudioMetaTags {
	if len(raw) == 0 {
		return AudioMetaTags{}
	}
	lower := make(map[string]string, len(raw))
	for key, value := range raw {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			lower[strings.ToLower(key)] = trimmed
		}
	}
	var tags AudioMetaTags
	mappings := []struct {
		dst     *string
		aliases []string
	}{
		{&tags.Encoder, []string{"encoder", "tsse", "tss"}},
		{&tags.EncodedBy, []string{"encoded_by", "tenc", "ten"}},
		{&tags.Title, []string{"title", "tit2", "tt2"}},
		{&tags.TitleSort, []string{"title-sort", "tsot"}},
		{&tags.Subtitle, []string{"subtitle", "tit3", "tt3"}},
		{&tags.Track, []string{"track", "trck", "trk"}},
		{&tags.Disc, []string{"discnumber", "disc", "disk", "tpos", "tpa"}},
		{&tags.Album, []string{"album", "talb", "tal"}},
		{&tags.AlbumSort, []string{"album-sort", "tsoa"}},
		{&tags.Artist, []string{"artist", "tpe1", "tp1"}},
		{&tags.ArtistSort, []string{"artist-sort", "tsop"}},
		{&tags.AlbumArtist, []string{"albumartist", "album_artist", "tpe2"}},
		{&tags.Date, []string{"date", "tyer", "tye"}},
		{&tags.Composer, []string{"composer", "tcom", "tcm"}},
		{&tags.Publisher, []string{"publisher", "tpub", "tpb"}},
		{&tags.Comment, []string{"comment", "comm", "com"}},
		{&tags.Description, []string{"description", "desc"}},
		{&tags.Genre, []string{"genre", "tcon", "tco"}},
		{&tags.Series, []string{"series", "show", "mvnm"}},
		{&tags.SeriesPart, []string{"series-part", "episode_id", "mvin", "part"}},
		{&tags.Grouping, []string{"grouping", "grp1"}},
		{&tags.ISBN, []string{"isbn"}},
		{&tags.Language, []string{"language", "lang"}},
		{&tags.ASIN, []string{"asin", "audible_asin"}},
		{&tags.ItunesID, []string{"itunes-id"}},
		{&tags.PodcastType, []string{"podcast-type"}},
		{&tags.EpisodeType, []string{"episode-type"}},
		{&tags.OverdriveMediaMarker, []string{"overdrive mediamarkers"}},
		{&tags.OriginalYear, []string{"originalyear"}},
		{&tags.ReleaseCountry, []string{"musicbrainz album release country", "releasecountry"}},
		{&tags.ReleaseStatus, []string{"musicbrainz album status", "releasestatus", "musicbrainz_albumstatus"}},
		{&tags.ReleaseType, []string{"musicbrainz album type", "releasetype", "musicbrainz_albumtype"}},
		{&tags.ISRC, []string{"tsrc", "isrc"}},
		{&tags.MusicBrainzTrackID, []string{"musicbrainz release track id", "musicbrainz_releasetrackid"}},
		{&tags.MusicBrainzAlbumID, []string{"musicbrainz album id", "musicbrainz_albumid"}},
		{&tags.MusicBrainzAlbumArtist, []string{"musicbrainz album artist id", "musicbrainz_albumartistid"}},
		{&tags.MusicBrainzArtistID, []string{"musicbrainz artist id", "musicbrainz_artistid"}},
	}
	for _, m := range mappings {
		for _, alias := range m.aliases {
			if value, ok := lower[alias]; ok {
				*m.dst = value
				break
			}
		}
	}
	return tags
}

// LeadingNumber parses the leading integer of "3" or "3/12"; missing or
// malformed values return nil.
func LeadingNumber(value string) *int64 {
	head, _, _ := strings.Cut(value, "/")
	n, err := strconv.ParseInt(strings.TrimSpace(head), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func tagLookup(tags map[string]string, name string) string {
	for key, value := range tags {
		if strings.EqualFold(key, name) {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
