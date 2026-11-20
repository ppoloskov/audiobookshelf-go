package scan

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"monstrobook/internal/db"
	"monstrobook/internal/media"
)

// probeAudio is the audio entry of one file (AudioFile.js toJSON), or nil when
// the file has no audio stream. The entry takes the file's inode and metadata.
func probeAudio(ctx context.Context, file db.LibraryFile) (*db.AudioFile, error) {
	probed, err := media.ProbeAudio(ctx, file.Metadata.Path)
	if err != nil || probed == nil {
		return nil, err
	}
	chapters := make([]db.Chapter, len(probed.Chapters))
	for i, ch := range probed.Chapters {
		chapters[i] = db.Chapter{ID: ch.ID, Start: ch.Start, End: ch.End, Title: ch.Title}
	}
	now := time.Now().UnixMilli()
	duration := probed.Duration
	trackFromFile, discFromFile := filenameNumbers(file.Metadata.Filename, filepath.Dir(file.Metadata.Path))
	return &db.AudioFile{
		Index:                1,
		Ino:                  file.Ino,
		Metadata:             file.Metadata,
		AddedAt:              now,
		UpdatedAt:            now,
		TrackNumFromMeta:     probed.TrackNumFromMeta,
		DiscNumFromMeta:      probed.DiscNumFromMeta,
		TrackNumFromFilename: trackFromFile,
		DiscNumFromFilename:  discFromFile,
		Format:               probed.Format,
		Duration:             &duration,
		BitRate:              probed.BitRate,
		Language:             probed.Language,
		Codec:                probed.Codec,
		TimeBase:             probed.TimeBase,
		Channels:             probed.Channels,
		ChannelLayout:        probed.ChannelLayout,
		Chapters:             chapters,
		EmbeddedCoverArt:     probed.EmbeddedCoverArt,
		MetaTags:             db.AudioMetaTags(probed.MetaTags),
		MimeType:             media.AudioMime(file.Metadata.Ext),
	}, nil
}

var (
	discInName  = regexp.MustCompile(`(?i)\b(disc|cd) ?(\d\d?)\b`)
	cdDirectory = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*\d{1,3}$`)
	discPrefix  = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*`)
	trackNumber = regexp.MustCompile(`\d{1,4}`)
)

// filenameNumbers reads the track and disc number from an audio file's name
// and from a "CD 1" style parent directory.
func filenameNumbers(filename, dir string) (track, disc *int64) {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	if m := discInName.FindStringSubmatch(base); m != nil {
		disc = media.LeadingNumber(m[2])
		base = strings.Replace(base, m[0], "", 1)
	}
	if parent := filepath.Base(dir); cdDirectory.MatchString(parent) {
		n, _ := strconv.ParseInt(discPrefix.ReplaceAllString(parent, ""), 10, 64)
		disc = &n
	}
	return media.LeadingNumber(trackNumber.FindString(base)), disc
}

// applyTags sets the book metadata from the first audio file's tags
// (AudioFileScanner.setBookMetadataFromAudioMetaTags).
func (m *bookMeta) applyTags(t db.AudioMetaTags) {
	set := func(dst *string, values ...string) {
		for _, v := range values {
			if v != "" {
				*dst = v
				return
			}
		}
	}
	if t.Composer != "" {
		m.Narrators = splitList(t.Composer)
	}
	set(&m.Description, t.Description, t.Comment)
	set(&m.Publisher, t.Publisher)
	set(&m.PublishedYear, t.Date)
	set(&m.Subtitle, t.Subtitle)
	set(&m.Title, t.Album, t.Title)
	if artist := cmpOrString(t.Artist, t.AlbumArtist); artist != "" {
		m.Authors = splitList(artist)
	}
	if t.Genre != "" {
		m.Genres = splitList(strings.ReplaceAll(t.Genre, "/", ";"))
	}
	if series := cmpOrString(t.Series, t.Grouping); series != "" {
		if strings.ContainsAny(series, "#;") {
			for _, part := range strings.Split(series, ";") {
				name, sequence := strings.TrimSpace(part), ""
				if i := strings.LastIndex(name, " #"); i > 0 {
					name, sequence = strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+2:])
				}
				if name != "" {
					m.Series = append(m.Series, SeriesEntry{name, sequence}) // appended to the folder's series, as always
				}
			}
		} else {
			m.Series = []SeriesEntry{{series, t.SeriesPart}}
		}
	}
	set(&m.ISBN, t.ISBN)
	set(&m.Language, t.Language)
	set(&m.ASIN, t.ASIN)
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// orderAudio sorts the book's audio files by disc and track number and numbers
// them from 1 (AudioFileScanner.runSmartTrackOrder).
func orderAudio(audio []db.AudioFile) {
	type number func(*db.AudioFile) *int64
	// usable: every file has the number and the numbers have no gap.
	usable := func(get number) bool {
		var nums []int64
		for i := range audio {
			n := get(&audio[i])
			if n == nil {
				return false
			}
			nums = append(nums, *n)
		}
		slices.Sort(nums)
		for i := 1; i < len(nums); i++ {
			if nums[i]-nums[i-1] > 1 {
				return false
			}
		}
		return len(nums) > 0
	}
	trackMeta := func(a *db.AudioFile) *int64 { return a.TrackNumFromMeta }
	trackName := func(a *db.AudioFile) *int64 { return a.TrackNumFromFilename }
	discMeta := func(a *db.AudioFile) *int64 { return a.DiscNumFromMeta }
	discName := func(a *db.AudioFile) *int64 { return a.DiscNumFromFilename }
	var disc number
	if usable(discMeta) {
		disc = discMeta
	} else if usable(discName) {
		disc = discName
	}
	distinct := func(get number) int {
		seen := map[int64]bool{}
		for i := range audio {
			if n := get(&audio[i]); n != nil {
				seen[*n] = true
			}
		}
		return len(seen)
	}
	track := trackMeta
	if distinct(trackName) > distinct(trackMeta) {
		track = trackName
	}
	less := func(a, b *db.AudioFile) bool {
		if disc != nil {
			if x, y := db.Val(disc(a)), db.Val(disc(b)); x != y {
				return x < y
			}
		}
		x, y := track(a), track(b)
		switch {
		case x == nil || y == nil:
			return false
		case *x != *y:
			return *x < *y
		}
		return a.Metadata.Filename < b.Metadata.Filename
	}
	slices.SortStableFunc(audio, func(a, b db.AudioFile) int {
		switch {
		case less(&a, &b):
			return -1
		case less(&b, &a):
			return 1
		}
		return 0
	})
	for i := range audio {
		audio[i].Index = int64(i + 1)
	}
}

// buildChapters derives the book's chapters from its audio files: the file
// chapters when they exist, else one chapter per file.
func buildChapters(audio []db.AudioFile, bookTitle string) []db.Chapter {
	out := []db.Chapter{}
	if len(audio) == 0 {
		return out
	}
	first := audio[0].Chapters
	if len(first) > 0 {
		// Identical chapters in every file collapse into one set.
		same := len(audio) > 1 && len(first) == len(audio[1].Chapters)
		for i := 0; same && i < len(first); i++ {
			same = first[i].Title == audio[1].Chapters[i].Title && first[i].Start == audio[1].Chapters[i].Start
		}
		if len(audio) == 1 || same {
			return append(out, first...)
		}
		offset := 0.0
		for _, a := range audio {
			if db.Val(a.Duration) == 0 {
				continue
			}
			for _, c := range a.Chapters {
				if c.End-c.Start < .1 {
					continue
				}
				c.Start, c.End, c.ID = c.Start+offset, c.End+offset, int64(len(out))
				out = append(out, c)
			}
			offset += db.Val(a.Duration)
		}
		return out
	}
	if len(audio) == 1 {
		return out
	}
	// Tag titles name the chapters only when every file has its own.
	titles := map[string]bool{}
	useTags := true
	for _, a := range audio {
		if a.MetaTags.Title == "" || a.MetaTags.Title == bookTitle {
			useTags = false
		}
		titles[a.MetaTags.Title] = true
	}
	useTags = useTags && len(titles) == len(audio)
	offset := 0.0
	for _, a := range audio {
		duration := db.Val(a.Duration)
		if duration <= 0 {
			continue
		}
		title := strings.TrimSuffix(a.Metadata.Filename, filepath.Ext(a.Metadata.Filename))
		if useTags {
			title = a.MetaTags.Title
		}
		out = append(out, db.Chapter{ID: int64(len(out)), Start: offset, End: offset + duration, Title: title})
		offset += duration
	}
	return out
}
