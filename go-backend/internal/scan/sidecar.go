package scan

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
)

// maxSidecar bounds a metadata file the scanner reads into memory.
const maxSidecar = 8 << 20

// SeriesEntry is one series of a book. v2.37.2 writes it into metadata.json as
// the string "Name #sequence"; older files hold {"name", "sequence"} objects.
type SeriesEntry struct{ Name, Sequence string }

// MarshalJSON writes the string form v2.37.2 reads.
func (s SeriesEntry) MarshalJSON() ([]byte, error) {
	text := s.Name
	if s.Sequence != "" {
		text += " #" + s.Sequence
	}
	return json.Marshal(text)
}

// UnmarshalJSON reads either form; any other value is an entry without a name,
// so one odd entry never discards the rest of the sidecar.
func (s *SeriesEntry) UnmarshalJSON(data []byte) error {
	*s = SeriesEntry{}
	var text string
	if json.Unmarshal(data, &text) == nil {
		s.Name, s.Sequence = splitSeries(text)
		return nil
	}
	var object struct{ Name, Sequence string }
	_ = json.Unmarshal(data, &object) // not an object either: leave the entry empty
	s.Name, s.Sequence = object.Name, object.Sequence
	return nil
}

// splitSeries splits "Name #1.5" at the last " #" unless the rest looks like
// part of the name.
func splitSeries(text string) (name, sequence string) {
	text = strings.TrimSpace(text)
	if i := strings.LastIndex(text, " #"); i > 0 && !strings.ContainsAny(text[i+2:], "# \t\r\n") {
		return strings.TrimSpace(text[:i]), text[i+2:]
	}
	return text, ""
}

// MetadataJSON is the book metadata.json document: what the scanner reads as
// the absMetadata source and what WriteMetadata writes. A nil scalar means the
// key is absent or null; a present scalar overwrites even with "".
type MetadataJSON struct {
	Tags          []string      `json:"tags"`
	Chapters      []db.Chapter  `json:"chapters"`
	Title         *string       `json:"title"`
	Subtitle      *string       `json:"subtitle"`
	Authors       []string      `json:"authors"`
	Narrators     []string      `json:"narrators"`
	Series        []SeriesEntry `json:"series"`
	Genres        []string      `json:"genres"`
	PublishedYear *string       `json:"publishedYear"`
	PublishedDate *string       `json:"publishedDate"`
	Publisher     *string       `json:"publisher"`
	Description   *string       `json:"description"`
	ISBN          *string       `json:"isbn"`
	ASIN          *string       `json:"asin"`
	Language      *string       `json:"language"`
	Explicit      bool          `json:"explicit"`
	Abridged      bool          `json:"abridged"`
	Nested        *MetadataJSON `json:"metadata,omitempty"` // files of older servers nest the fields here
}

// scalars are the pointers to the string fields, in the order of bookMeta.scalars.
func (j *MetadataJSON) scalars() [9]**string {
	return [9]**string{&j.Title, &j.Subtitle, &j.PublishedYear, &j.PublishedDate, &j.Publisher, &j.Description, &j.ISBN, &j.ASIN, &j.Language}
}

// ParseMetadataJSON decodes a sidecar. Fields of the wrong type are skipped
// (the rest still applies); text that is not JSON gives an empty document.
func ParseMetadataJSON(raw []byte) MetadataJSON {
	var doc MetadataJSON
	_ = json.Unmarshal(raw, &doc) // a mistyped field or malformed text leaves the field empty
	if n := doc.Nested; n != nil {
		own, from := doc.scalars(), n.scalars()
		for i := range own {
			if *own[i] == nil {
				*own[i] = *from[i]
			}
		}
		doc.Tags, doc.Chapters, doc.Authors = cmpOr(doc.Tags, n.Tags), cmpOr(doc.Chapters, n.Chapters), cmpOr(doc.Authors, n.Authors)
		doc.Narrators, doc.Series, doc.Genres = cmpOr(doc.Narrators, n.Narrators), cmpOr(doc.Series, n.Series), cmpOr(doc.Genres, n.Genres)
	}
	return doc
}

func cmpOr[T any](own, fallback []T) []T {
	if len(own) > 0 {
		return own
	}
	return fallback
}

// writeJSONFile writes the document to path, indented as v2.37.2 does.
func writeJSONFile(path string, doc any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// bookMeta is the metadata the sources are merged into.
type bookMeta struct {
	Title, Subtitle, PublishedYear, PublishedDate, Publisher, Description, ISBN, ASIN, Language string
	Authors, Narrators, Tags, Genres                                                            []string
	Series                                                                                      []SeriesEntry
	Chapters                                                                                    []db.Chapter
}

func (m *bookMeta) scalars() [9]*string {
	return [9]*string{&m.Title, &m.Subtitle, &m.PublishedYear, &m.PublishedDate, &m.Publisher, &m.Description, &m.ISBN, &m.ASIN, &m.Language}
}

// overlay copies every non-empty value of src onto m.
func (m *bookMeta) overlay(src bookMeta) {
	to := m.scalars()
	for i, from := range src.scalars() {
		if *from != "" {
			*to[i] = *from
		}
	}
	m.overlayLists(src)
}

func (m *bookMeta) overlayLists(src bookMeta) {
	m.Authors, m.Narrators, m.Tags = cmpOr(src.Authors, m.Authors), cmpOr(src.Narrators, m.Narrators), cmpOr(src.Tags, m.Tags)
	m.Genres, m.Series, m.Chapters = cmpOr(src.Genres, m.Genres), cmpOr(src.Series, m.Series), cmpOr(src.Chapters, m.Chapters)
}

// overlayJSON is overlay for metadata.json, which also overwrites with "".
func (m *bookMeta) overlayJSON(doc MetadataJSON) {
	to := m.scalars()
	for i, from := range doc.scalars() {
		if *from != nil {
			*to[i] = **from
		}
	}
	doc.Series = nonEmptySeries(doc.Series)
	m.overlayLists(bookMeta{Authors: doc.Authors, Narrators: doc.Narrators, Tags: doc.Tags, Genres: doc.Genres, Series: doc.Series, Chapters: doc.Chapters})
}

func nonEmptySeries(list []SeriesEntry) []SeriesEntry {
	out := list[:0:0]
	for _, s := range list {
		if strings.TrimSpace(s.Name) != "" {
			out = append(out, s)
		}
	}
	return out
}

// splitList splits "A; B" into trimmed non-empty names.
func splitList(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ";") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// readSmall reads a metadata file, refusing one over maxSidecar.
func readSmall(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSidecar {
		return nil, errors.New("metadata file too large")
	}
	return os.ReadFile(path)
}

// readSidecars applies the companion files of the item in the order of the
// library's metadata precedence (audio tags and folder names are handled by
// the caller). itemMetadata is the item's copy under the metadata directory,
// used for absMetadata only when the item folder has no readable metadata.json.
func readSidecars(source string, files []fileEntry, itemMetadata string, dst *bookMeta) {
	switch source {
	case "nfoFile":
		if f, ok := firstWithExt(files, ".nfo"); ok {
			if raw, err := readSmall(f.path); err == nil {
				dst.overlay(parseNFO(string(raw)))
			}
		}
	case "opfFile":
		if f, ok := firstWithExt(files, ".opf"); ok {
			if raw, err := readSmall(f.path); err == nil {
				dst.overlay(parseOPF(raw))
			}
		}
	case "txtFiles":
		for _, f := range files {
			name := strings.ToLower(filepath.Base(f.path))
			if name != "desc.txt" && name != "reader.txt" {
				continue
			}
			raw, err := readSmall(f.path)
			if err != nil {
				continue
			}
			if name == "desc.txt" {
				if text := strings.TrimSpace(string(raw)); text != "" {
					dst.Description = text
				}
			} else if first, _, _ := strings.Cut(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"); strings.TrimSpace(first) != "" {
				dst.Narrators = splitList(strings.TrimSpace(first))
			}
		}
	case "absMetadata":
		path := itemMetadata
		for _, f := range files {
			if strings.EqualFold(filepath.Base(f.path), "metadata.json") {
				path = f.path
				break
			}
		}
		if raw, err := readSmall(path); err == nil {
			dst.overlayJSON(ParseMetadataJSON(raw))
		}
	}
}

// firstWithExt is the first file with the extension, in any case.
func firstWithExt(files []fileEntry, ext string) (fileEntry, bool) {
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f.path), ext) {
			return f, true
		}
	}
	return fileEntry{}, false
}

// parseNFO reads "Key: value" lines.
func parseNFO(text string) bookMeta {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	m := bookMeta{Title: values["title"], Subtitle: values["subtitle"], PublishedYear: values["publishedyear"], Description: values["description"],
		Publisher: values["publisher"], ASIN: values["asin"], ISBN: values["isbn"], Language: values["language"]}
	m.Tags, m.Authors, m.Narrators = splitList(values["tag"]), splitList(values["author"]), splitList(values["narrator"])
	if name := values["series name"]; name != "" {
		m.Series = []SeriesEntry{{Name: name, Sequence: values["position in series"]}}
	}
	if genres := splitList(values["genre"]); len(genres) > 0 {
		m.Genres, m.Tags = genres, slices.Clone(genres) // genres also replace the tags, as the scanner always did
	}
	return m
}

// parseOPF reads the Dublin Core and calibre fields of an OPF package.
func parseOPF(raw []byte) bookMeta {
	type value struct {
		Text    string `xml:",chardata"`
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
	var m bookMeta
	if xml.Unmarshal(raw, &doc) != nil {
		return m
	}
	md := doc.Metadata
	first := func(list []value) string {
		if len(list) > 0 {
			return strings.TrimSpace(list[0].Text)
		}
		return ""
	}
	m.Title, m.Subtitle, m.Publisher, m.Description, m.Language = first(md.Titles), first(md.Subtitles), first(md.Publishers), first(md.Descriptions), first(md.Languages)
	if year := first(md.Dates); len(year) >= 4 {
		m.PublishedYear = year[:4]
	}
	for _, v := range md.Identifiers {
		switch text := strings.TrimSpace(v.Text); {
		case strings.EqualFold(v.Scheme, "ISBN") && m.ISBN == "":
			m.ISBN = text
		case strings.EqualFold(v.Scheme, "ASIN") && m.ASIN == "":
			m.ASIN = text
		}
	}
	for _, v := range md.Creators {
		if name := strings.TrimSpace(v.Text); name != "" {
			switch strings.ToLower(v.Role) {
			case "aut":
				m.Authors = append(m.Authors, name)
			case "nrt":
				m.Narrators = append(m.Narrators, name)
			}
		}
	}
	for _, v := range md.Subjects {
		if genre := strings.TrimSpace(v.Text); genre != "" {
			m.Genres = append(m.Genres, genre)
		}
	}
	index := ""
	for _, v := range md.Metas {
		content := strings.TrimSpace(v.Content)
		switch {
		case strings.EqualFold(v.Name, "tags") && content != "":
			m.Tags = splitList(v.Content)
		case strings.EqualFold(v.Name, "calibre:series") && content != "":
			index = ""
			m.Series = append(m.Series, SeriesEntry{Name: content})
		case strings.EqualFold(v.Name, "calibre:series_index") && content != "":
			index = content
		}
	}
	if len(m.Series) == 1 {
		m.Series[0].Sequence = index
	}
	return m
}
