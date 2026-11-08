package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filekind"
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

func (s *Scanner) loadStoredAudio(ctx context.Context, mediaID string) []map[string]any {
	var raw []byte
	if mediaID == "" || s.db.QueryRowContext(ctx, `SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&raw) != nil {
		return nil
	}
	return parseStoredObjects(raw)
}

// findStoredAudio matches a scanned file to a stored audio entry by path, then
// inode (BookScanner.js:97-101).
func findStoredAudio(entries []map[string]any, currentPath, currentIno string) map[string]any {
	for _, entry := range entries {
		if meta, ok := entry["metadata"].(map[string]any); ok && meta["path"] == currentPath {
			return entry
		}
	}
	for _, entry := range entries {
		if currentIno != "" && entry["ino"] == currentIno {
			return entry
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
func readABSMetadata(g *group, itemID, metadataPath string) (bookMetadata, bool) {
	var result bookMetadata
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Base(f.path), "metadata.json") {
			if raw, err := os.ReadFile(f.path); err == nil {
				parseAbsMetadata(raw, &result)
				return result, true
			}
		}
	}
	if itemID == "" || metadataPath == "" {
		return result, false
	}
	path := filepath.Join(metadataPath, "items", itemID, "metadata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return result, false
	}
	parseAbsMetadata(raw, &result)
	return result, true
}

var defaultMetadataSourceOrder = []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}

func defaultMetadataPrecedence() []string {
	return append([]string(nil), defaultMetadataSourceOrder...)
}

func mergeBookMetadataWithABS(title, author, series, sequence string, g *group, order []string, audioTags map[string]any, abs bookMetadata, hasABS bool) bookMetadata {
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
func applyNonemptyMetadata(src bookMetadata, dst *bookMetadata) {
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.Subtitle != "" {
		dst.Subtitle = src.Subtitle
	}
	if src.PublishedYear != "" {
		dst.PublishedYear = src.PublishedYear
	}
	if src.PublishedDate != "" {
		dst.PublishedDate = src.PublishedDate
	}
	if src.Publisher != "" {
		dst.Publisher = src.Publisher
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.ISBN != "" {
		dst.ISBN = src.ISBN
	}
	if src.ASIN != "" {
		dst.ASIN = src.ASIN
	}
	if src.Language != "" {
		dst.Language = src.Language
	}
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

func applyABSMetadata(src bookMetadata, dst *bookMetadata) {
	for key, value := range map[string]string{"title": src.Title, "subtitle": src.Subtitle, "publishedYear": src.PublishedYear, "publishedDate": src.PublishedDate, "publisher": src.Publisher, "description": src.Description, "isbn": src.ISBN, "asin": src.ASIN, "language": src.Language} {
		if src.absPresent[key] {
			switch key {
			case "title":
				dst.Title = value
			case "subtitle":
				dst.Subtitle = value
			case "publishedYear":
				dst.PublishedYear = value
			case "publishedDate":
				dst.PublishedDate = value
			case "publisher":
				dst.Publisher = value
			case "description":
				dst.Description = value
			case "isbn":
				dst.ISBN = value
			case "asin":
				dst.ASIN = value
			case "language":
				dst.Language = value
			}
		}
	}
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
	decodeString("title", &dst.Title)
	decodeString("subtitle", &dst.Subtitle)
	decodeString("publishedYear", &dst.PublishedYear)
	decodeString("publishedDate", &dst.PublishedDate)
	decodeString("publisher", &dst.Publisher)
	decodeString("description", &dst.Description)
	decodeString("isbn", &dst.ISBN)
	decodeString("asin", &dst.ASIN)
	decodeString("language", &dst.Language)
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
func makeFileMeta(root string, f file) map[string]any {
	rel := f.relPath
	if rel == "" {
		rel, _ = filepath.Rel(root, f.path)
	}
	ext := strings.ToLower(filepath.Ext(f.path))
	mtime, ctime, birthtime := statTimes(f.path, f.info)
	return map[string]any{"ino": inode(f.info), "metadata": map[string]any{"filename": filepath.Base(f.path), "ext": ext, "path": f.path, "relPath": filepath.ToSlash(rel), "size": f.info.Size(), "mtimeMs": mtime.UnixMilli(), "ctimeMs": ctime.UnixMilli(), "birthtimeMs": birthtime.UnixMilli()}, "isSupplementary": nil, "addedAt": mtime.UnixMilli(), "updatedAt": mtime.UnixMilli(), "fileType": filekind.Of(ext)}
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
func probeAudio(ctx context.Context, p string, meta map[string]any, fileIno string) (map[string]any, error) {
	res, err := mediatools.Probe(ctx, p)
	if err != nil {
		return nil, err
	}
	duration, _ := strconv.ParseFloat(res.Format.Duration, 64)
	format := res.Format.LongName
	if format == "" {
		format = res.Format.Name
	}
	ext, _ := meta["ext"].(string)
	filename, _ := meta["filename"].(string)
	var audio *mediatools.Stream
	var embedded any
	defaultAudio := false
	for i := range res.Streams {
		s := &res.Streams[i]
		// MediaProbeData.js:46 takes the first video stream's codec as the
		// embedded cover, attached picture or not (prober.js:253).
		if s.Type == "video" && embedded == nil {
			embedded = s.Codec
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
	bitRate, _ := strconv.ParseInt(res.Format.BitRate, 10, 64)
	var normalizedBitRate any
	if bitRate > 0 {
		normalizedBitRate = bitRate
	}
	tags := res.Format.Tags
	out := map[string]any{"index": 1, "ino": fileIno, "metadata": meta, "addedAt": time.Now().UnixMilli(), "updatedAt": time.Now().UnixMilli(), "trackNumFromMeta": nil, "discNumFromMeta": nil, "trackNumFromFilename": nil, "discNumFromFilename": nil, "manuallyVerified": false, "exclude": false, "error": nil, "format": format, "duration": duration, "bitRate": normalizedBitRate, "language": nil, "codec": nil, "timeBase": nil, "channels": nil, "channelLayout": nil, "chapters": []map[string]any{}, "embeddedCoverArt": embedded, "metaTags": map[string]any{}, "mimeType": filekind.AudioMime(ext)}
	out["codec"] = audio.Codec
	out["timeBase"] = audio.TimeBase
	if audio.Channels > 0 {
		out["channels"] = audio.Channels
	}
	if audio.ChannelLayout != "" {
		out["channelLayout"] = strings.Split(audio.ChannelLayout, "(")[0]
	}
	// Upstream language comes from the stream tags (prober.js:92).
	if language, ok := audio.Tags["language"]; ok && strings.TrimSpace(language) != "" {
		out["language"] = strings.TrimSpace(language)
	}
	if streamRate, e := strconv.ParseInt(audio.BitRate, 10, 64); e == nil && streamRate > 0 {
		out["bitRate"] = streamRate
	}
	if tags == nil {
		tags = audio.Tags
	}
	metaTags := parseAudioTags(tags)
	out["metaTags"] = metaTags
	if value, ok := metaTags["tagTrack"].(string); ok {
		out["trackNumFromMeta"] = firstTrackNumber(value)
	}
	if value, ok := metaTags["tagDisc"].(string); ok {
		out["discNumFromMeta"] = firstTrackNumber(value)
	}
	out["trackNumFromFilename"], out["discNumFromFilename"] = filenameNumbers(filename, filepath.Dir(p))
	chapters := make([]map[string]any, 0, len(res.Chapters))
	for i, ch := range res.Chapters {
		start, end := ch.Seconds()
		chapters = append(chapters, map[string]any{"start": start, "end": end, "title": strings.TrimSpace(ch.Tags["title"]), "id": i})
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i]["start"].(float64) < chapters[j]["start"].(float64) })
	for i := range chapters {
		chapters[i]["id"] = i
	}
	out["chapters"] = chapters
	return out, nil
}

func firstTrackNumber(value string) any {
	part := strings.Split(value, "/")[0]
	number, err := strconv.Atoi(strings.TrimSpace(part))
	if err != nil {
		return nil
	}
	return number
}
func audioFilename(af map[string]any) string {
	if meta, ok := af["metadata"].(map[string]any); ok {
		if filename, ok := meta["filename"].(string); ok {
			return strings.TrimSuffix(filename, filepath.Ext(filename))
		}
	}
	return ""
}

var (
	discInName    = regexp.MustCompile(`(?i)\b(disc|cd) ?(\d\d?)\b`)
	discDirPrefix = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*`)
	trackNumber   = regexp.MustCompile(`\d{1,4}`)
)

// filenameNumbers reads the track and disc number from an audio file's name
// and from a "CD 1" style parent directory.
func filenameNumbers(filename, dir string) (any, any) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	disc := any(nil)
	if m := discInName.FindStringSubmatch(base); len(m) > 2 {
		if n, e := strconv.Atoi(m[2]); e == nil {
			disc = n
		}
		base = strings.Replace(base, m[0], "", 1)
	}
	if parent := filepath.Base(dir); cdDirectory.MatchString(parent) {
		n, _ := strconv.Atoi(discDirPrefix.ReplaceAllString(parent, ""))
		disc = n
	}
	m := trackNumber.FindString(base)
	if m == "" {
		return nil, disc
	}
	n, _ := strconv.Atoi(m)
	return n, disc
}

func smartAudioOrder(audio []map[string]any) []map[string]any {
	selectKey := func(meta, filename string) string {
		all := func(key string) bool {
			for _, af := range audio {
				if af[key] == nil {
					return false
				}
			}
			return true
		}
		sequential := func(key string) bool {
			nums := []int{}
			for _, af := range audio {
				if n, ok := af[key].(int); ok {
					nums = append(nums, n)
				} else {
					return false
				}
			}
			sort.Ints(nums)
			for i := 1; i < len(nums); i++ {
				if nums[i]-nums[i-1] > 1 {
					return false
				}
			}
			return len(nums) > 0
		}
		if all(meta) && sequential(meta) {
			return meta
		}
		if all(filename) && sequential(filename) {
			return filename
		}
		return ""
	}
	disc := selectKey("discNumFromMeta", "discNumFromFilename")
	track := "trackNumFromMeta"
	fm, mm := map[int]bool{}, map[int]bool{}
	for _, a := range audio {
		if n, ok := a["trackNumFromFilename"].(int); ok {
			fm[n] = true
		}
		if n, ok := a["trackNumFromMeta"].(int); ok {
			mm[n] = true
		}
	}
	if len(fm) > len(mm) {
		track = "trackNumFromFilename"
	}
	less := func(a, b map[string]any) bool {
		if disc != "" {
			x, _ := a[disc].(int)
			y, _ := b[disc].(int)
			if x != y {
				return x < y
			}
		}
		x, xok := a[track].(int)
		y, yok := b[track].(int)
		if !xok || !yok {
			return false
		}
		if x != y {
			return x < y
		}
		am := a["metadata"].(map[string]any)
		bm := b["metadata"].(map[string]any)
		return am["filename"].(string) < bm["filename"].(string)
	}
	sort.SliceStable(audio, func(i, j int) bool { return less(audio[i], audio[j]) })
	for i, a := range audio {
		a["index"] = i + 1
	}
	return audio
}

func buildChapters(audio []map[string]any, bookTitle string) []map[string]any {
	out := []map[string]any{}
	if len(audio) == 0 {
		return out
	}
	chapters := func(a map[string]any) []map[string]any { c, _ := a["chapters"].([]map[string]any); return c }
	first := chapters(audio[0])
	same := len(audio) > 1 && len(first) > 0 && len(first) == len(chapters(audio[1]))
	if same {
		for i, c := range first {
			d := chapters(audio[1])[i]
			if c["title"] != d["title"] || c["start"] != d["start"] {
				same = false
				break
			}
		}
	}
	if len(first) > 0 {
		if len(audio) == 1 || same {
			for _, c := range first {
				out = append(out, c)
			}
			return out
		}
		offset := 0.0
		for _, a := range audio {
			d, _ := a["duration"].(float64)
			if d == 0 {
				continue
			}
			for _, c := range chapters(a) {
				st, _ := c["start"].(float64)
				en, _ := c["end"].(float64)
				if en-st < .1 {
					continue
				}
				cp := map[string]any{}
				for k, v := range c {
					cp[k] = v
				}
				cp["start"] = st + offset
				cp["end"] = en + offset
				cp["id"] = len(out)
				out = append(out, cp)
			}
			offset += d
		}
		return out
	}
	if len(audio) <= 1 {
		return out
	}
	uniqueTitles := map[string]bool{}
	useMetaTitles := len(audio) > 1
	for _, a := range audio {
		m, _ := a["metaTags"].(map[string]any)
		tag, _ := m["tagTitle"].(string)
		if tag == "" || tag == bookTitle {
			useMetaTitles = false
		}
		if tag != "" && tag != bookTitle {
			uniqueTitles[tag] = true
		}
	}
	if len(uniqueTitles) != len(audio) {
		useMetaTitles = false
	}
	offset := 0.0
	for _, a := range audio {
		d, _ := a["duration"].(float64)
		if d <= 0 {
			continue
		}
		m, _ := a["metadata"].(map[string]any)
		filename, _ := m["filename"].(string)
		title := strings.TrimSuffix(filename, filepath.Ext(filename))
		if useMetaTitles {
			m, _ := a["metaTags"].(map[string]any)
			title, _ = m["tagTitle"].(string)
		}
		out = append(out, map[string]any{"id": len(out), "start": offset, "end": offset + d, "title": title})
		offset += d
	}
	return out
}

var audioTagAliases = map[string][]string{"tagEncoder": {"encoder", "tsse", "tss"}, "tagEncodedBy": {"encoded_by", "tenc", "ten"}, "tagTitle": {"title", "tit2", "tt2"}, "tagTitleSort": {"title-sort", "tsot"}, "tagSubtitle": {"subtitle", "tit3", "tt3"}, "tagTrack": {"track", "trck", "trk"}, "tagDisc": {"discnumber", "disc", "disk", "tpos", "tpa"}, "tagAlbum": {"album", "talb", "tal"}, "tagAlbumSort": {"album-sort", "tsoa"}, "tagArtist": {"artist", "tpe1", "tp1"}, "tagArtistSort": {"artist-sort", "tsop"}, "tagAlbumArtist": {"albumartist", "album_artist", "tpe2"}, "tagDate": {"date", "tyer", "tye"}, "tagComposer": {"composer", "tcom", "tcm"}, "tagPublisher": {"publisher", "tpub", "tpb"}, "tagComment": {"comment", "comm", "com"}, "tagDescription": {"description", "desc"}, "tagGenre": {"genre", "tcon", "tco"}, "tagSeries": {"series", "show", "mvnm"}, "tagSeriesPart": {"series-part", "episode_id", "mvin", "part"}, "tagGrouping": {"grouping", "grp1"}, "tagIsbn": {"isbn"}, "tagLanguage": {"language", "lang"}, "tagASIN": {"asin", "audible_asin"}, "tagItunesId": {"itunes-id"}, "tagPodcastType": {"podcast-type"}, "tagEpisodeType": {"episode-type"}, "tagOriginalYear": {"originalyear"}, "tagReleaseCountry": {"musicbrainz album release country", "releasecountry"}, "tagReleaseStatus": {"musicbrainz album status", "releasestatus", "musicbrainz_albumstatus"}, "tagReleaseType": {"musicbrainz album type", "releasetype", "musicbrainz_albumtype"}, "tagISRC": {"tsrc", "isrc"}, "tagMusicBrainzTrackId": {"musicbrainz release track id", "musicbrainz_releasetrackid"}, "tagMusicBrainzAlbumId": {"musicbrainz album id", "musicbrainz_albumid"}, "tagMusicBrainzAlbumArtistId": {"musicbrainz album artist id", "musicbrainz_albumartistid"}, "tagMusicBrainzArtistId": {"musicbrainz artist id", "musicbrainz_artistid"}}

func parseAudioTags(raw map[string]string) map[string]any {
	out := map[string]any{}
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
func applyAudioMetadata(tags map[string]any, dst *bookMetadata) {
	str := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := tags[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
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
	re := regexp.MustCompile(`^ *\(?([0-9]{4})\)? * - *(.+)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return s, ""
	}
	return m[2], m[1]
}

func extractASIN(s string) (string, string) {
	pattern := regexp.MustCompile(`(?: |^)\[([A-Z0-9]{10})\](?: |$)`)
	m := pattern.FindStringSubmatchIndex(s)
	if m == nil {
		return s, ""
	}
	asin := s[m[2]:m[3]]
	return strings.TrimSpace(s[:m[0]] + s[m[1]:]), asin
}
func extractSequence(s string) (string, string) {
	re := regexp.MustCompile(`(?i)^(?:(?:vol\.? |volume |book ))?(\d{0,3}(?:\.\d{1,2})?)(\.?)(?: (.*))?$`)
	parts := strings.Split(s, " - ")
	for i, p := range parts {
		m := re.FindStringSubmatch(p)
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
