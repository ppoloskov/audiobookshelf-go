package scanner

import (
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/mediatools"
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type file struct {
	path    string
	info    os.FileInfo
	relPath string
}
type group struct {
	path       string
	folder     folder
	files      []file
	companions []file
	isFile     bool
	// old is the stored item this group rescans; nil means a new item.
	old *existingItem
}

// bookMetadata mirrors the metadata keys supported by the v2.37.2 metadata.json
// generator. Empty values are omitted so a partial sidecar cannot erase tags.
type bookMetadata struct {
	Title, Subtitle, PublishedYear, PublishedDate, Publisher, Description string
	ISBN, ASIN, Language                                                  string
	Authors, Narrators, Tags, Genres                                      []string
	Series                                                                []seriesMetadata
	Chapters                                                              []map[string]any
	absPresent                                                            map[string]bool
}

type seriesMetadata struct {
	Name     string `json:"name"`
	Sequence string `json:"sequence"`
}

type imported struct {
	id             string
	added, updated bool
	// Per-scan bookkeeping the library scan finalizes once at the end
	// (LibraryItemScanner.finalizeAuthorsAndSeriesFromScan).
	authorsChanged, authorsRemoved, seriesRemoved []string
	// scannedAudio are audio files appended to an existing book by this scan.
	scannedAudio []string
}

var cdDirectory = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*\d{1,3}$`)

func shouldIgnore(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") || strings.Contains(part, "@eaDir") {
			return true
		}
	}
	if len(parts) > 0 && parts[len(parts)-1] == ".ignore" {
		return true
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".part", ".tmp", ".crdownload", ".download", ".bak", ".old", ".temp", ".tempfile", ".tempfile~":
		return true
	}
	return false
}

// sameDBTime reports whether a stored SQLite DATETIME and a freshly formatted
// one denote the same millisecond.
func sameDBTime(stored, fresh string) bool {
	if stored == fresh {
		return true
	}
	a, okA := dbtime.Parse(stored)
	b, okB := dbtime.Parse(fresh)
	return okA && okB && a.UnixMilli() == b.UnixMilli()
}

// nextCoverPath applies BookScanner's cover rules for a new or rescanned book:
// a removed cover file clears the cover, a renamed/moved one follows the file,
// and an unset cover takes the folder image named "cover" or the first image
// (BookScanner.js:106-161).
func nextCoverPath(current string, files []libraryFile, diff libraryFileDiff) (string, bool) {
	cover := current
	if cover != "" {
		for _, removed := range diff.removed {
			if removed.Metadata.Path == cover && removed.FileType == "image" {
				if _, err := os.Stat(cover); err != nil {
					cover = ""
				}
			}
		}
	}
	if cover != "" {
		for newPath, oldPath := range diff.oldPathOf {
			if oldPath == cover && newPath != cover && libraryFileHasPath(files, newPath) && filekind.Of(filepath.Ext(newPath)) == filekind.Image {
				cover = newPath
			}
		}
	}
	if cover == "" {
		cover = pickCoverPath(files)
	}
	return cover, cover != current
}

func (s *Scanner) loadStoredAudio(ctx context.Context, mediaID string) []libitem.AudioFile {
	var raw []byte
	if mediaID == "" || s.db.QueryRowContext(ctx, `SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&raw) != nil {
		return nil
	}
	var out []libitem.AudioFile
	_ = json.Unmarshal(raw, &out)
	return out
}

// findStoredAudio matches a scanned file to a stored audio entry by path, then
// inode (BookScanner.js:97-101).
func findStoredAudio(entries []libitem.AudioFile, currentPath, currentIno string) *libitem.AudioFile {
	for i := range entries {
		if entries[i].Metadata.Path == currentPath {
			return &entries[i]
		}
	}
	for i := range entries {
		if currentIno != "" && entries[i].Ino == currentIno {
			return &entries[i]
		}
	}
	return nil
}

// Group files are captured during discovery; directory reads here would lose
// upstream traversal order and attach files outside the claimed item path.

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// readABSMetadata prefers a readable in-item metadata.json and falls back to
// an existing item's external sidecar only when that file is unavailable.
func readABSMetadata(g *group, itemID string, cfg config.Config) (bookMetadata, bool) {
	var result bookMetadata
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Base(f.path), "metadata.json") {
			if raw, err := os.ReadFile(f.path); err == nil {
				parseAbsMetadata(raw, &result)
				return result, true
			}
		}
	}
	if itemID == "" || cfg.MetadataPath == "" {
		return result, false
	}
	path := filepath.Join(filepath.Join(cfg.Dirs().Items, itemID), "metadata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return result, false
	}
	parseAbsMetadata(raw, &result)
	return result, true
}

func mergeBookMetadataWithABS(title, author, series, sequence string, g *group, order []string, audioTags map[string]string, abs bookMetadata, hasABS bool) bookMetadata {
	m := bookMetadata{Title: title, Authors: splitMetadataList(author)}
	if series != "" {
		m.Series = []seriesMetadata{{Name: series, Sequence: sequence}}
	}
	for _, source := range order {
		switch source {
		case "audioMetatags":
			if len(g.files) > 0 {
				applyAudioMetadata(audioTags, &m)
			}
		case "nfoFile":
			applyNFO(g, &m)
		case "txtFiles":
			applyTextFiles(g, &m)
		case "opfFile":
			applyOPFFiles(g, &m)
		case "absMetadata":
			if hasABS {
				applyABSMetadata(abs, &m)
			}
		}
	}
	return m
}
func applyTextFiles(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		switch strings.ToLower(filepath.Base(f.path)) {
		case "desc.txt":
			if raw, e := os.ReadFile(f.path); e == nil && strings.TrimSpace(string(raw)) != "" {
				dst.Description = strings.TrimSpace(string(raw))
			}
		case "reader.txt":
			if raw, e := os.ReadFile(f.path); e == nil {
				lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
				if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
					dst.Narrators = splitMetadataList(strings.TrimSpace(lines[0]))
				}
			}
		}
	}
}
func applyNFO(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Ext(f.path), ".nfo") {
			raw, e := os.ReadFile(f.path)
			if e != nil {
				continue
			}
			values := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				if i := strings.Index(line, ":"); i >= 0 {
					values[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
				}
			}
			if v := values["title"]; v != "" {
				dst.Title = v
			}
			if v := values["subtitle"]; v != "" {
				dst.Subtitle = v
			}
			if v := values["publishedyear"]; v != "" {
				dst.PublishedYear = v
			}
			if v := values["description"]; v != "" {
				dst.Description = v
			}
			if v := values["tag"]; v != "" {
				dst.Tags = splitMetadataList(v)
			}
			if v := values["author"]; v != "" {
				dst.Authors = splitMetadataList(v)
			}
			if v := values["narrator"]; v != "" {
				dst.Narrators = splitMetadataList(v)
			}
			if v := values["series name"]; v != "" {
				dst.Series = []seriesMetadata{{Name: v, Sequence: values["position in series"]}}
			}
			if v := values["genre"]; v != "" {
				dst.Genres = splitMetadataList(v)
				dst.Tags = append([]string(nil), dst.Genres...)
			}
			if v := values["publisher"]; v != "" {
				dst.Publisher = v
			}
			if v := values["asin"]; v != "" {
				dst.ASIN = v
			}
			if v := values["isbn"]; v != "" {
				dst.ISBN = v
			}
			if v := values["language"]; v != "" {
				dst.Language = v
			}
			break
		}
	}
}
func applyOPFFiles(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Ext(f.path), ".opf") {
			if raw, e := os.ReadFile(f.path); e == nil {
				var parsed bookMetadata
				parseOPF(raw, &parsed)
				applyNonemptyMetadata(parsed, dst)
			}
			break
		}
	}
}

// scalars are the string fields of the metadata, keyed as metadata.json names
// them; every source that copies or reads all of them goes through this list.
func (m *bookMetadata) scalars() []scalarField {
	return []scalarField{{"title", &m.Title}, {"subtitle", &m.Subtitle}, {"publishedYear", &m.PublishedYear}, {"publishedDate", &m.PublishedDate},
		{"publisher", &m.Publisher}, {"description", &m.Description}, {"isbn", &m.ISBN}, {"asin", &m.ASIN}, {"language", &m.Language}}
}

type scalarField struct {
	key string
	v   *string
}

// applyNonemptyMetadata copies every value src has onto dst.
func applyNonemptyMetadata(src bookMetadata, dst *bookMetadata) {
	to := dst.scalars()
	for i, f := range src.scalars() {
		if *f.v != "" {
			*to[i].v = *f.v
		}
	}
	applyListMetadata(src, dst)
}

// applyABSMetadata is applyNonemptyMetadata except that a string the sidecar
// names overwrites even with "" (arrays and chapters only when not empty).
func applyABSMetadata(src bookMetadata, dst *bookMetadata) {
	to := dst.scalars()
	for i, f := range src.scalars() {
		if src.absPresent[f.key] {
			*to[i].v = *f.v
		}
	}
	applyListMetadata(src, dst)
}

func applyListMetadata(src bookMetadata, dst *bookMetadata) {
	if len(src.Authors) > 0 {
		dst.Authors = src.Authors
	}
	if len(src.Narrators) > 0 {
		dst.Narrators = src.Narrators
	}
	if len(src.Tags) > 0 {
		dst.Tags = src.Tags
	}
	if len(src.Genres) > 0 {
		dst.Genres = src.Genres
	}
	if len(src.Series) > 0 {
		dst.Series = src.Series
	}
	if len(src.Chapters) > 0 {
		dst.Chapters = src.Chapters
	}
}

func parseAbsMetadata(raw []byte, dst *bookMetadata) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	dst.absPresent = map[string]bool{}
	if nested := obj["metadata"]; len(nested) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(nested, &m) == nil {
			for k, v := range m {
				if _, ok := obj[k]; !ok {
					obj[k] = v
				}
			}
		}
	}
	decode := func(key string, out any) {
		if v := obj[key]; len(v) > 0 {
			_ = json.Unmarshal(v, out)
		}
	}
	decodeString := func(key string, out *string) {
		if v := obj[key]; len(v) > 0 && string(v) != "null" {
			dst.absPresent[key] = true
			_ = json.Unmarshal(v, out)
		}
	}
	decodeArray := func(key string, out *[]string) {
		var values []string
		if v := obj[key]; len(v) > 0 && json.Unmarshal(v, &values) == nil && len(values) > 0 {
			*out = values
		}
	}
	for _, f := range dst.scalars() {
		decodeString(f.key, f.v)
	}
	decodeArray("authors", &dst.Authors)
	decodeArray("narrators", &dst.Narrators)
	decodeArray("tags", &dst.Tags)
	decodeArray("genres", &dst.Genres)
	var chapters []map[string]any
	if v := obj["chapters"]; len(v) > 0 && json.Unmarshal(v, &chapters) == nil && len(chapters) > 0 {
		dst.Chapters = chapters
	}
	var names []string
	decode("series", &names)
	var objects []seriesMetadata
	decode("series", &objects)
	if len(objects) > 0 {
		dst.Series = objects
	} else if len(names) > 0 {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				parsed := name
				if i := strings.LastIndex(parsed, " #"); i > 0 && !strings.ContainsAny(parsed[i+2:], "# \t\r\n") {
					dst.Series = append(dst.Series, seriesMetadata{Name: strings.TrimSpace(parsed[:i]), Sequence: parsed[i+2:]})
				} else {
					dst.Series = append(dst.Series, seriesMetadata{Name: name})
				}
			}
		}
	}
}

func parseOPF(raw []byte, dst *bookMetadata) {
	type value struct {
		Text    string `xml:",chardata"`
		ID      string `xml:"id,attr"`
		Scheme  string `xml:"scheme,attr"`
		Name    string `xml:"name,attr"`
		Content string `xml:"content,attr"`
		Role    string `xml:"role,attr"`
	}
	var doc struct {
		Metadata struct {
			Titles       []value `xml:"title"`
			Subtitles    []value `xml:"subtitle"`
			Creators     []value `xml:"creator"`
			Dates        []value `xml:"date"`
			Publishers   []value `xml:"publisher"`
			Descriptions []value `xml:"description"`
			Identifiers  []value `xml:"identifier"`
			Languages    []value `xml:"language"`
			Subjects     []value `xml:"subject"`
			Metas        []value `xml:"meta"`
		} `xml:"metadata"`
	}
	if xml.Unmarshal(raw, &doc) != nil {
		return
	}
	m := doc.Metadata
	first := func(a []value) string {
		if len(a) > 0 {
			return strings.TrimSpace(a[0].Text)
		}
		return ""
	}
	if dst.Title == "" {
		dst.Title = first(m.Titles)
	}
	if dst.Subtitle == "" {
		dst.Subtitle = first(m.Subtitles)
	}
	if dst.Publisher == "" {
		dst.Publisher = first(m.Publishers)
	}
	if dst.Description == "" {
		dst.Description = first(m.Descriptions)
	}
	if dst.Language == "" {
		dst.Language = first(m.Languages)
	}
	if y := first(m.Dates); len(y) >= 4 && dst.PublishedYear == "" {
		dst.PublishedYear = y[:4]
	}
	for _, v := range m.Identifiers {
		if strings.EqualFold(v.Scheme, "ISBN") && dst.ISBN == "" {
			dst.ISBN = strings.TrimSpace(v.Text)
		}
		if strings.EqualFold(v.Scheme, "ASIN") && dst.ASIN == "" {
			dst.ASIN = strings.TrimSpace(v.Text)
		}
	}
	if len(dst.Authors) == 0 {
		for _, v := range m.Creators {
			role := strings.ToLower(v.Role)
			if role == "aut" {
				if strings.TrimSpace(v.Text) != "" {
					dst.Authors = append(dst.Authors, strings.TrimSpace(v.Text))
				}
			} else if role == "nrt" {
				dst.Narrators = append(dst.Narrators, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Narrators) == 0 {
		for _, v := range m.Creators {
			if strings.EqualFold(v.Role, "nrt") && strings.TrimSpace(v.Text) != "" {
				dst.Narrators = append(dst.Narrators, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Genres) == 0 {
		for _, v := range m.Subjects {
			if strings.TrimSpace(v.Text) != "" {
				dst.Genres = append(dst.Genres, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Tags) == 0 {
		for _, v := range m.Metas {
			if strings.EqualFold(v.Name, "tags") && strings.TrimSpace(v.Content) != "" {
				dst.Tags = splitMetadataList(v.Content)
			}
		}
	}
	if len(dst.Series) == 0 {
		seriesIndex := ""
		for _, v := range m.Metas {
			if strings.EqualFold(v.Name, "calibre:series") && strings.TrimSpace(v.Content) != "" {
				seriesIndex = ""
				dst.Series = append(dst.Series, seriesMetadata{Name: strings.TrimSpace(v.Content)})
			} else if strings.EqualFold(v.Name, "calibre:series_index") && strings.TrimSpace(v.Content) != "" {
				seriesIndex = strings.TrimSpace(v.Content)
			}
		}
		if len(dst.Series) == 1 {
			dst.Series[0].Sequence = seriesIndex
		}
	}
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return dbtime.Format(t)
}
func statTimes(path string, info os.FileInfo) (mtime, ctime, birthtime time.Time) {
	mtime = info.ModTime()
	ctime = statChangeTime(info)
	birthtime = statBirthTime(path, info)
	return
}

func inode(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(st.Ino, 10)
	}
	return ""
}

// probeAudio shapes the imported audio file per the upstream AudioFile object
// (server/objects/files/AudioFile.js:113-136): meta is the flat FileMetadata
// object; the inode belongs top-level only.
func probeAudio(ctx context.Context, p string, meta libitem.FileMetadata, fileIno string) (*libitem.AudioFile, error) {
	res, err := mediatools.Probe(ctx, p)
	if err != nil {
		return nil, err
	}
	duration, _ := strconv.ParseFloat(res.Format.Duration, 64)
	format := res.Format.LongName
	if format == "" {
		format = res.Format.Name
	}
	var audio *mediatools.Stream
	var embedded *string
	defaultAudio := false
	for i := range res.Streams {
		s := &res.Streams[i]
		// MediaProbeData.js:46 takes the first video stream's codec as the
		// embedded cover, attached picture or not (prober.js:253).
		if s.Type == "video" && embedded == nil {
			embedded = &s.Codec
		}
		if s.Type == "audio" && (audio == nil || (s.Disposition.Default == 1 && !defaultAudio)) {
			audio = s
			defaultAudio = defaultAudio || s.Disposition.Default == 1
		}
	}
	if audio == nil {
		// Upstream rejects files without an audio stream outright
		// (AudioFileScanner.js:156-179).
		return nil, nil
	}
	now := time.Now().UnixMilli()
	out := &libitem.AudioFile{Index: 1, Ino: fileIno, Metadata: meta, AddedAt: now, UpdatedAt: now, Format: format, Duration: duration,
		Codec: &audio.Codec, TimeBase: &audio.TimeBase, EmbeddedCoverArt: embedded, MimeType: filekind.AudioMime(meta.Ext)}
	if bitRate, _ := strconv.ParseInt(res.Format.BitRate, 10, 64); bitRate > 0 {
		out.BitRate = &bitRate
	}
	if audio.Channels > 0 {
		out.Channels = &audio.Channels
	}
	if audio.ChannelLayout != "" {
		layout := strings.Split(audio.ChannelLayout, "(")[0]
		out.ChannelLayout = &layout
	}
	// Upstream language comes from the stream tags (prober.js:92).
	if language := strings.TrimSpace(audio.Tags["language"]); language != "" {
		out.Language = &language
	}
	if streamRate, e := strconv.ParseInt(audio.BitRate, 10, 64); e == nil && streamRate > 0 {
		out.BitRate = &streamRate
	}
	tags := res.Format.Tags
	if tags == nil {
		tags = audio.Tags
	}
	out.MetaTags = parseAudioTags(tags)
	out.TrackNumFromMeta = firstTrackNumber(out.MetaTags["tagTrack"])
	out.DiscNumFromMeta = firstTrackNumber(out.MetaTags["tagDisc"])
	out.TrackNumFromFilename, out.DiscNumFromFilename = filenameNumbers(meta.Filename, filepath.Dir(p))
	out.Chapters = make([]libitem.AudioChapter, 0, len(res.Chapters))
	for _, ch := range res.Chapters {
		start, end := ch.Seconds()
		out.Chapters = append(out.Chapters, libitem.AudioChapter{Start: start, End: end, Title: strings.TrimSpace(ch.Tags["title"])})
	}
	sort.Slice(out.Chapters, func(i, j int) bool { return out.Chapters[i].Start < out.Chapters[j].Start })
	for i := range out.Chapters {
		out.Chapters[i].ID = i
	}
	return out, nil
}

// firstTrackNumber reads "3" or "3/12" (a missing or malformed value is nil).
func firstTrackNumber(value string) *int {
	number, err := strconv.Atoi(strings.TrimSpace(strings.Split(value, "/")[0]))
	if err != nil {
		return nil
	}
	return &number
}

func audioFilename(af libitem.AudioFile) string {
	return strings.TrimSuffix(af.Metadata.Filename, filepath.Ext(af.Metadata.Filename))
}

var (
	discInName    = regexp.MustCompile(`(?i)\b(disc|cd) ?(\d\d?)\b`)
	discDirPrefix = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*`)
	trackNumber   = regexp.MustCompile(`\d{1,4}`)

	publishedYearPrefix = regexp.MustCompile(`^ *\(?([0-9]{4})\)? * - *(.+)`)
	asinInName          = regexp.MustCompile(`(?: |^)\[([A-Z0-9]{10})\](?: |$)`)
	sequenceInName      = regexp.MustCompile(`(?i)^(?:(?:vol\.? |volume |book ))?(\d{0,3}(?:\.\d{1,2})?)(\.?)(?: (.*))?$`)
)

// filenameNumbers reads the track and disc number from an audio file's name
// and from a "CD 1" style parent directory.
func filenameNumbers(filename, dir string) (track, disc *int) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if m := discInName.FindStringSubmatch(base); len(m) > 2 {
		if n, e := strconv.Atoi(m[2]); e == nil {
			disc = &n
		}
		base = strings.Replace(base, m[0], "", 1)
	}
	if parent := filepath.Base(dir); cdDirectory.MatchString(parent) {
		n, _ := strconv.Atoi(discDirPrefix.ReplaceAllString(parent, ""))
		disc = &n
	}
	if m := trackNumber.FindString(base); m != "" {
		n, _ := strconv.Atoi(m)
		track = &n
	}
	return track, disc
}

// smartAudioOrder sorts the book's audio files by disc and track number and
// numbers them from 1 (AudioFileScanner.runSmartTrackOrder).
func smartAudioOrder(audio []libitem.AudioFile) {
	type number func(*libitem.AudioFile) *int
	trackMeta := func(a *libitem.AudioFile) *int { return a.TrackNumFromMeta }
	trackName := func(a *libitem.AudioFile) *int { return a.TrackNumFromFilename }
	discMeta := func(a *libitem.AudioFile) *int { return a.DiscNumFromMeta }
	discName := func(a *libitem.AudioFile) *int { return a.DiscNumFromFilename }
	// usable: every file has the number and the numbers have no gap.
	usable := func(get number) bool {
		nums := []int{}
		for i := range audio {
			n := get(&audio[i])
			if n == nil {
				return false
			}
			nums = append(nums, *n)
		}
		sort.Ints(nums)
		for i := 1; i < len(nums); i++ {
			if nums[i]-nums[i-1] > 1 {
				return false
			}
		}
		return len(nums) > 0
	}
	var disc number
	if usable(discMeta) {
		disc = discMeta
	} else if usable(discName) {
		disc = discName
	}
	fm, mm := map[int]bool{}, map[int]bool{}
	for i := range audio {
		if n := trackName(&audio[i]); n != nil {
			fm[*n] = true
		}
		if n := trackMeta(&audio[i]); n != nil {
			mm[*n] = true
		}
	}
	track := trackMeta
	if len(fm) > len(mm) {
		track = trackName
	}
	less := func(a, b *libitem.AudioFile) bool {
		if disc != nil {
			var x, y int
			if n := disc(a); n != nil {
				x = *n
			}
			if n := disc(b); n != nil {
				y = *n
			}
			if x != y {
				return x < y
			}
		}
		x, y := track(a), track(b)
		if x == nil || y == nil {
			return false
		}
		if *x != *y {
			return *x < *y
		}
		return a.Metadata.Filename < b.Metadata.Filename
	}
	sort.SliceStable(audio, func(i, j int) bool { return less(&audio[i], &audio[j]) })
	for i := range audio {
		audio[i].Index = i + 1
	}
}

func buildChapters(audio []libitem.AudioFile, bookTitle string) []libitem.AudioChapter {
	out := []libitem.AudioChapter{}
	if len(audio) == 0 {
		return out
	}
	first := audio[0].Chapters
	same := len(audio) > 1 && len(first) > 0 && len(first) == len(audio[1].Chapters)
	if same {
		for i, c := range first {
			if d := audio[1].Chapters[i]; c.Title != d.Title || c.Start != d.Start {
				same = false
				break
			}
		}
	}
	if len(first) > 0 {
		if len(audio) == 1 || same {
			return append(out, first...)
		}
		offset := 0.0
		for _, a := range audio {
			if a.Duration == 0 {
				continue
			}
			for _, c := range a.Chapters {
				if c.End-c.Start < .1 {
					continue
				}
				c.Start, c.End, c.ID = c.Start+offset, c.End+offset, len(out)
				out = append(out, c)
			}
			offset += a.Duration
		}
		return out
	}
	if len(audio) <= 1 {
		return out
	}
	uniqueTitles := map[string]bool{}
	useMetaTitles := len(audio) > 1
	for _, a := range audio {
		tag := a.MetaTags["tagTitle"]
		if tag == "" || tag == bookTitle {
			useMetaTitles = false
		} else {
			uniqueTitles[tag] = true
		}
	}
	if len(uniqueTitles) != len(audio) {
		useMetaTitles = false
	}
	offset := 0.0
	for _, a := range audio {
		if a.Duration <= 0 {
			continue
		}
		title := audioFilename(a)
		if useMetaTitles {
			title = a.MetaTags["tagTitle"]
		}
		out = append(out, libitem.AudioChapter{ID: len(out), Start: offset, End: offset + a.Duration, Title: title})
		offset += a.Duration
	}
	return out
}

var audioTagAliases = map[string][]string{"tagEncoder": {"encoder", "tsse", "tss"}, "tagEncodedBy": {"encoded_by", "tenc", "ten"}, "tagTitle": {"title", "tit2", "tt2"}, "tagTitleSort": {"title-sort", "tsot"}, "tagSubtitle": {"subtitle", "tit3", "tt3"}, "tagTrack": {"track", "trck", "trk"}, "tagDisc": {"discnumber", "disc", "disk", "tpos", "tpa"}, "tagAlbum": {"album", "talb", "tal"}, "tagAlbumSort": {"album-sort", "tsoa"}, "tagArtist": {"artist", "tpe1", "tp1"}, "tagArtistSort": {"artist-sort", "tsop"}, "tagAlbumArtist": {"albumartist", "album_artist", "tpe2"}, "tagDate": {"date", "tyer", "tye"}, "tagComposer": {"composer", "tcom", "tcm"}, "tagPublisher": {"publisher", "tpub", "tpb"}, "tagComment": {"comment", "comm", "com"}, "tagDescription": {"description", "desc"}, "tagGenre": {"genre", "tcon", "tco"}, "tagSeries": {"series", "show", "mvnm"}, "tagSeriesPart": {"series-part", "episode_id", "mvin", "part"}, "tagGrouping": {"grouping", "grp1"}, "tagIsbn": {"isbn"}, "tagLanguage": {"language", "lang"}, "tagASIN": {"asin", "audible_asin"}, "tagItunesId": {"itunes-id"}, "tagPodcastType": {"podcast-type"}, "tagEpisodeType": {"episode-type"}, "tagOriginalYear": {"originalyear"}, "tagReleaseCountry": {"musicbrainz album release country", "releasecountry"}, "tagReleaseStatus": {"musicbrainz album status", "releasestatus", "musicbrainz_albumstatus"}, "tagReleaseType": {"musicbrainz album type", "releasetype", "musicbrainz_albumtype"}, "tagISRC": {"tsrc", "isrc"}, "tagMusicBrainzTrackId": {"musicbrainz release track id", "musicbrainz_releasetrackid"}, "tagMusicBrainzAlbumId": {"musicbrainz album id", "musicbrainz_albumid"}, "tagMusicBrainzAlbumArtistId": {"musicbrainz album artist id", "musicbrainz_albumartistid"}, "tagMusicBrainzArtistId": {"musicbrainz artist id", "musicbrainz_artistid"}}

func parseAudioTags(raw map[string]string) map[string]string {
	out := map[string]string{}
	for target, aliases := range audioTagAliases {
		for key, value := range raw {
			for _, alias := range aliases {
				if strings.EqualFold(key, alias) && strings.TrimSpace(value) != "" {
					out[target] = strings.TrimSpace(value)
					goto nextTarget
				}
			}
		}
	nextTarget:
	}
	return out
}

// applyAudioMetadata mirrors AudioFileScanner.setBookMetadataFromAudioMetaTags
// (v2.37.2, AudioFileScanner.js:221-344); the caller supplies only file zero.
func applyAudioMetadata(tags map[string]string, dst *bookMetadata) {
	str := func(keys ...string) string {
		for _, key := range keys {
			if v := strings.TrimSpace(tags[key]); v != "" {
				return v
			}
		}
		return ""
	}
	if v := str("tagComposer"); v != "" {
		dst.Narrators = splitMetadataList(v)
	}
	if v := str("tagDescription", "tagComment"); v != "" {
		dst.Description = v
	}
	if v := str("tagPublisher"); v != "" {
		dst.Publisher = v
	}
	if v := str("tagDate"); v != "" {
		dst.PublishedYear = v
	}
	if v := str("tagSubtitle"); v != "" {
		dst.Subtitle = v
	}
	if v := str("tagAlbum", "tagTitle"); v != "" {
		dst.Title = v
	}
	if v := str("tagArtist", "tagAlbumArtist"); v != "" {
		dst.Authors = splitMetadataList(v)
	}
	if v := str("tagGenre"); v != "" {
		dst.Genres = parseGenreList(v)
	}
	if v := str("tagSeries", "tagGrouping"); v != "" {
		if strings.Contains(v, "#") || strings.Contains(v, ";") {
			for _, part := range strings.Split(v, ";") {
				name, seq := parseSeriesValue(strings.TrimSpace(part))
				if name != "" {
					dst.Series = append(dst.Series, seriesMetadata{Name: name, Sequence: seq})
				}
			}
		} else {
			dst.Series = []seriesMetadata{{Name: v, Sequence: str("tagSeriesPart")}}
		}
	}
	if v := str("tagIsbn"); v != "" {
		dst.ISBN = v
	}
	if v := str("tagLanguage"); v != "" {
		dst.Language = v
	}
	if v := str("tagASIN"); v != "" {
		dst.ASIN = v
	}
}
func parseGenreList(s string) []string {
	return splitMetadataList(strings.NewReplacer("/", ";").Replace(s))
}
func splitMetadataList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func parseSeriesValue(s string) (string, string) {
	if i := strings.LastIndex(s, " #"); i > 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
	}
	return s, ""
}

// mustJSON encodes a value for a JSON column; a nil slice is an empty list, not
// the JSON null that json.Marshal would produce.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}
func bookPathMetadata(p, root string, parseSubtitle bool) (title, author, series, seq string) {
	rel, _ := filepath.Rel(root, p)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	titleDir := filepath.Base(p)
	seriesName := ""
	if len(parts) > 2 {
		// Upstream getBookDataFromDir (scandir.js:153): after the title folder
		// is popped, a series folder only exists when more than one directory
		// remains, so two-component paths have an author but no series.
		seriesName = parts[len(parts)-2]
	}
	if len(parts) > 2 {
		// Three-deep paths: /Author/Series/Title/ per upstream getBookDataFromDir.
		author = parts[len(parts)-3]
	} else if len(parts) > 1 {
		author = parts[len(parts)-2]
	}
	title = titleDir
	title, _ = extractASIN(title)
	if i := strings.LastIndex(title, " {"); i >= 0 && strings.HasSuffix(title, "}") {
		title = title[:i]
	}
	if seriesName != "" {
		title, seq = extractSequence(title)
	}
	if seriesName != "" {
		series = seriesName
	}
	title, _ = extractPublishedYear(title)
	if parseSubtitle {
		title, _, _ = strings.Cut(title, " - ")
	}
	return
}
func extractPublishedYear(s string) (string, string) {
	m := publishedYearPrefix.FindStringSubmatch(s)
	if m == nil {
		return s, ""
	}
	return m[2], m[1]
}

func extractASIN(s string) (string, string) {
	m := asinInName.FindStringSubmatchIndex(s)
	if m == nil {
		return s, ""
	}
	asin := s[m[2]:m[3]]
	return strings.TrimSpace(s[:m[0]] + s[m[1]:]), asin
}
func extractSequence(s string) (string, string) {
	parts := strings.Split(s, " - ")
	for i, p := range parts {
		m := sequenceInName.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		labeled := strings.HasPrefix(strings.ToLower(p), "vol") || strings.HasPrefix(strings.ToLower(p), "volume") || strings.HasPrefix(strings.ToLower(p), "book ")
		if m[3] != "" && !labeled && m[2] == "" {
			continue
		}
		n := m[1]
		if n != "" {
			if f, e := strconv.ParseFloat(n, 64); e == nil {
				n = strconv.FormatFloat(f, 'f', -1, 64)
			}
		}
		if m[3] != "" {
			parts[i] = m[3]
		} else {
			parts = append(parts[:i], parts[i+1:]...)
		}
		return strings.Join(parts, " - "), n
	}
	return s, ""
}
