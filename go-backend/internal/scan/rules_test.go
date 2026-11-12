package scan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filekind"
)

func i64(n int64) *int64 { return &n }

func f64(n float64) *float64 { return &n }

func TestFilenameNumbers(t *testing.T) {
	tests := []struct {
		name, filename, dir string
		track, disc         *int64
	}{
		{"old: disc and track in name", "Author - Book CD2 - 003 Chapter.mp3", "", i64(3), i64(2)},
		{"track only", "07 Chapter.mp3", "", i64(7), nil},
		{"disc with space", "Disc 3 - 12.mp3", "", i64(12), i64(3)},
		{"no numbers", "chapter.mp3", "", nil, nil},
		{"cd directory", "05.mp3", "/lib/Book/CD 2", i64(5), i64(2)},
		{"disc directory wins over name", "CD1 04.mp3", "/lib/Book/Disk3", i64(4), i64(3)},
		{"unrelated directory", "04.mp3", "/lib/Book/Part 2", i64(4), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			track, disc := filenameNumbers(tc.filename, tc.dir)
			if !reflect.DeepEqual(track, tc.track) || !reflect.DeepEqual(disc, tc.disc) {
				t.Fatalf("track/disc = %v/%v, want %v/%v", db.Val(track), db.Val(disc), db.Val(tc.track), db.Val(tc.disc))
			}
		})
	}
}

func TestLeadingNumber(t *testing.T) {
	tests := []struct {
		in   string
		want *int64
	}{{"3", i64(3)}, {"3/12", i64(3)}, {" 7 /9", i64(7)}, {"", nil}, {"x", nil}, {"/3", nil}}
	for _, tc := range tests {
		if got := leadingNumber(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("leadingNumber(%q) = %v, want %v", tc.in, db.Val(got), db.Val(tc.want))
		}
	}
}

func TestOrderAudio(t *testing.T) {
	file := func(name string, track, disc *int64) db.AudioFile {
		return db.AudioFile{TrackNumFromMeta: track, DiscNumFromMeta: disc, Metadata: db.FileMetadata{Filename: name}}
	}
	names := func(audio []db.AudioFile) string {
		var out []string
		for i, a := range audio {
			if a.Index != int64(i+1) {
				t.Fatalf("audio[%d].Index = %d", i, a.Index)
			}
			out = append(out, a.Metadata.Filename)
		}
		return strings.Join(out, ",")
	}
	t.Run("old: missing track stays last and stable, index is one-based", func(t *testing.T) {
		audio := []db.AudioFile{file("a.mp3", i64(1), nil), file("b.mp3", i64(2), nil), file("zz.mp3", nil, nil)}
		orderAudio(audio)
		if got := names(audio); got != "a.mp3,b.mp3,zz.mp3" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("sorted by track", func(t *testing.T) {
		audio := []db.AudioFile{file("c.mp3", i64(3), nil), file("a.mp3", i64(1), nil), file("b.mp3", i64(2), nil)}
		orderAudio(audio)
		if got := names(audio); got != "a.mp3,b.mp3,c.mp3" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("same track falls back to file name", func(t *testing.T) {
		audio := []db.AudioFile{file("b.mp3", i64(1), nil), file("a.mp3", i64(1), nil)}
		orderAudio(audio)
		if got := names(audio); got != "a.mp3,b.mp3" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("disc first, then track", func(t *testing.T) {
		audio := []db.AudioFile{file("d2t1.mp3", i64(1), i64(2)), file("d1t2.mp3", i64(2), i64(1)), file("d1t1.mp3", i64(1), i64(1)), file("d2t2.mp3", i64(2), i64(2))}
		orderAudio(audio)
		if got := names(audio); got != "d1t1.mp3,d1t2.mp3,d2t1.mp3,d2t2.mp3" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("filename tracks are used when they distinguish more files", func(t *testing.T) {
		mk := func(name string, track int64) db.AudioFile {
			return db.AudioFile{TrackNumFromFilename: i64(track), Metadata: db.FileMetadata{Filename: name}}
		}
		audio := []db.AudioFile{mk("z.mp3", 2), mk("y.mp3", 1)}
		orderAudio(audio)
		if got := names(audio); got != "y.mp3,z.mp3" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("empty", func(t *testing.T) { orderAudio(nil) })
}

func chapter(id int64, start, end float64, title string) db.Chapter {
	return db.Chapter{ID: id, Start: start, End: end, Title: title}
}

func TestBuildChapters(t *testing.T) {
	tagged := func(name, title string, duration float64) db.AudioFile {
		return db.AudioFile{Duration: f64(duration), Metadata: db.FileMetadata{Filename: name}, MetaTags: db.AudioMetaTags{Title: title}}
	}
	chaptered := func(duration float64, chapters ...db.Chapter) db.AudioFile {
		return db.AudioFile{Duration: f64(duration), Chapters: chapters}
	}
	tests := []struct {
		name  string
		audio []db.AudioFile
		book  string
		want  []db.Chapter
	}{
		{"no audio", nil, "Book", []db.Chapter{}},
		{"old: unique tag titles name the chapters", []db.AudioFile{tagged("a.mp3", "First", 2), tagged("b.mp3", "Second", 3)}, "Book",
			[]db.Chapter{chapter(0, 0, 2, "First"), chapter(1, 2, 5, "Second")}},
		{"old: tags equal to the book title use file names", []db.AudioFile{tagged("a.mp3", "Book", 2), tagged("b.mp3", "Book", 3)}, "Book",
			[]db.Chapter{chapter(0, 0, 2, "a"), chapter(1, 2, 5, "b")}},
		{"duplicate tag titles use file names", []db.AudioFile{tagged("a.mp3", "Same", 2), tagged("b.mp3", "Same", 3)}, "Book",
			[]db.Chapter{chapter(0, 0, 2, "a"), chapter(1, 2, 5, "b")}},
		{"one empty tag title uses file names", []db.AudioFile{tagged("a.mp3", "", 2), tagged("b.mp3", "Two", 3)}, "Book",
			[]db.Chapter{chapter(0, 0, 2, "a"), chapter(1, 2, 5, "b")}},
		{"old: single unchaptered file has none", []db.AudioFile{tagged("a.mp3", "Only", 2)}, "Book", []db.Chapter{}},
		{"file without duration is skipped and adds no offset", []db.AudioFile{tagged("a.mp3", "", 2), tagged("b.mp3", "", 0), tagged("c.mp3", "", 4)}, "Book",
			[]db.Chapter{chapter(0, 0, 2, "a"), chapter(1, 2, 6, "c")}},
		{"single chaptered file is kept as is", []db.AudioFile{chaptered(10, chapter(7, 0, 4, "A"), chapter(8, 4, 10, "B"))}, "Book",
			[]db.Chapter{chapter(7, 0, 4, "A"), chapter(8, 4, 10, "B")}},
		{"identical chapters in every file collapse", []db.AudioFile{
			chaptered(10, chapter(0, 0, 4, "A"), chapter(1, 4, 10, "B")),
			chaptered(10, chapter(0, 0, 4, "A"), chapter(1, 4, 10, "B"))}, "Book",
			[]db.Chapter{chapter(0, 0, 4, "A"), chapter(1, 4, 10, "B")}},
		{"different chapters accumulate offsets and renumber", []db.AudioFile{
			chaptered(10, chapter(0, 0, 4, "A"), chapter(1, 4, 10, "B")),
			chaptered(5, chapter(0, 0, 5, "C"))}, "Book",
			[]db.Chapter{chapter(0, 0, 4, "A"), chapter(1, 4, 10, "B"), chapter(2, 10, 15, "C")}},
		{"same titles but different starts do not collapse", []db.AudioFile{
			chaptered(10, chapter(0, 0, 10, "A")), chaptered(10, chapter(0, 1, 10, "A"))}, "Book",
			[]db.Chapter{chapter(0, 0, 10, "A"), chapter(1, 11, 20, "A")}},
		{"chapters shorter than 0.1 s are skipped", []db.AudioFile{
			chaptered(10, chapter(0, 0, 0.05, "Tiny"), chapter(1, 0.05, 10, "Real")),
			chaptered(5, chapter(0, 0, 5, "Other"))}, "Book",
			[]db.Chapter{chapter(0, 0.05, 10, "Real"), chapter(1, 10, 15, "Other")}},
		{"zero-duration chaptered file adds nothing", []db.AudioFile{
			chaptered(10, chapter(0, 0, 10, "A")), chaptered(0, chapter(0, 0, 3, "X")), chaptered(2, chapter(0, 0, 2, "C"))}, "Book",
			[]db.Chapter{chapter(0, 0, 10, "A"), chapter(1, 10, 12, "C")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildChapters(tc.audio, tc.book); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("chapters = %+v\nwant      %+v", got, tc.want)
			}
		})
	}
}

func TestParseOPF(t *testing.T) {
	t.Run("old: title, ISBN, author, series", func(t *testing.T) {
		got := parseOPF([]byte(`<package xmlns:dc="urn:dc" xmlns:opf="urn:opf"><metadata><dc:title>OPF Book</dc:title><dc:creator opf:role="aut">A. Writer</dc:creator><dc:identifier opf:scheme="ISBN">123</dc:identifier><meta name="calibre:series" content="Tales"/></metadata></package>`))
		if got.Title != "OPF Book" || got.ISBN != "123" || !slices.Equal(got.Authors, []string{"A. Writer"}) || len(got.Series) != 1 || got.Series[0].Name != "Tales" {
			t.Fatalf("OPF = %+v", got)
		}
	})
	t.Run("all fields", func(t *testing.T) {
		got := parseOPF([]byte(`<package xmlns:dc="urn:dc" xmlns:opf="urn:opf"><metadata>
<dc:title> T </dc:title><dc:subtitle>S</dc:subtitle><dc:publisher>P</dc:publisher><dc:description>D</dc:description><dc:language>en</dc:language>
<dc:date>2019-05-01</dc:date>
<dc:creator opf:role="aut">Au</dc:creator><dc:creator opf:role="nrt">Na</dc:creator><dc:creator opf:role="edt">Ed</dc:creator>
<dc:identifier opf:scheme="ASIN">B0123456789</dc:identifier><dc:identifier opf:scheme="isbn">9</dc:identifier>
<dc:subject>G1</dc:subject><dc:subject>G2</dc:subject>
<meta name="tags" content="x; y"/><meta name="calibre:series" content="Saga"/><meta name="calibre:series_index" content="2.5"/>
</metadata></package>`))
		want := bookMeta{Title: "T", Subtitle: "S", Publisher: "P", Description: "D", Language: "en", PublishedYear: "2019", ISBN: "9", ASIN: "B0123456789",
			Authors: []string{"Au"}, Narrators: []string{"Na"}, Genres: []string{"G1", "G2"}, Tags: []string{"x", "y"}, Series: []SeriesEntry{{"Saga", "2.5"}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("OPF = %+v\nwant  %+v", got, want)
		}
	})
	t.Run("series index is dropped with several series", func(t *testing.T) {
		got := parseOPF([]byte(`<package><metadata><meta name="calibre:series" content="A"/><meta name="calibre:series_index" content="1"/><meta name="calibre:series" content="B"/></metadata></package>`))
		if !reflect.DeepEqual(got.Series, []SeriesEntry{{"A", ""}, {"B", ""}}) {
			t.Fatalf("series = %+v", got.Series)
		}
	})
	t.Run("malformed XML gives nothing", func(t *testing.T) {
		if got := parseOPF([]byte(`<package><metadata><title>`)); got.Title != "" {
			t.Fatalf("OPF = %+v", got)
		}
	})
}

func TestParseNFO(t *testing.T) {
	got := parseNFO("Title: NFO Title\r\nSubtitle: Sub\nAuthor: A; B\nNarrator: N\nPublishedYear: 2001\nPublisher: Pub\nASIN: B000000000\nISBN: 1\nLanguage: fr\nDescription: Desc: with colon\nTag: t1;t2\nSeries Name: Saga\nPosition in Series: 3\nnot a field\n")
	want := bookMeta{Title: "NFO Title", Subtitle: "Sub", Authors: []string{"A", "B"}, Narrators: []string{"N"}, PublishedYear: "2001", Publisher: "Pub", ASIN: "B000000000", ISBN: "1",
		Language: "fr", Description: "Desc: with colon", Tags: []string{"t1", "t2"}, Series: []SeriesEntry{{"Saga", "3"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NFO = %+v\nwant  %+v", got, want)
	}
	t.Run("genres also replace tags", func(t *testing.T) {
		got := parseNFO("Tag: x\nGenre: Fantasy; Mystery\n")
		if !slices.Equal(got.Genres, []string{"Fantasy", "Mystery"}) || !slices.Equal(got.Tags, got.Genres) {
			t.Fatalf("genres/tags = %v / %v", got.Genres, got.Tags)
		}
	})
	t.Run("keys are case-insensitive, empty file is empty", func(t *testing.T) {
		if got := parseNFO("TITLE:  X \n"); got.Title != "X" {
			t.Fatalf("title = %q", got.Title)
		}
		if got := parseNFO(""); !reflect.DeepEqual(got, bookMeta{}) {
			t.Fatalf("empty = %+v", got)
		}
	})
}

// sidecars writes the named files into a directory and returns them as the
// scanner sees companion files.
func sidecars(t *testing.T, files map[string]string) (dir string, entries []fileEntry) {
	t.Helper()
	dir = t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, fileEntry{path: path, rel: name, info: info})
	}
	return dir, entries
}

func TestReadSidecarsTextFiles(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bookMeta
	}{
		{"old: desc.txt trimmed, reader.txt first line", map[string]string{"desc.txt": "  a description \n", "reader.txt": "First Reader\nIgnored Reader\n"},
			bookMeta{Description: "a description", Narrators: []string{"First Reader"}}},
		{"reader.txt with CRLF and several names", map[string]string{"reader.txt": "A; B\r\nC\r\n"}, bookMeta{Narrators: []string{"A", "B"}}},
		{"blank files change nothing", map[string]string{"desc.txt": " \n", "reader.txt": "\nName\n"}, bookMeta{}},
		{"names are matched in any case", map[string]string{"DESC.TXT": "x"}, bookMeta{Description: "x"}},
		{"other txt files are ignored", map[string]string{"notes.txt": "x"}, bookMeta{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, files := sidecars(t, tc.files)
			var got bookMeta
			readSidecars("txtFiles", files, "", &got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("meta = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadSidecarsSources(t *testing.T) {
	_, files := sidecars(t, map[string]string{"book.NFO": "title: N\n", "book.opf": `<package xmlns:dc="urn:dc"><metadata><dc:title>O</dc:title></metadata></package>`})
	var got bookMeta
	readSidecars("nfoFile", files, "", &got)
	if got.Title != "N" {
		t.Fatalf("nfo title = %q", got.Title)
	}
	readSidecars("opfFile", files, "", &got)
	if got.Title != "O" {
		t.Fatalf("opf title = %q", got.Title)
	}
	readSidecars("unknownSource", files, "", &got)
	readSidecars("nfoFile", nil, "", &got)
	if got.Title != "O" {
		t.Fatalf("title changed to %q", got.Title)
	}
}

func TestReadSidecarsAbsMetadata(t *testing.T) {
	t.Run("old: item copy is read only without a readable file in the item", func(t *testing.T) {
		external, _ := sidecars(t, map[string]string{"metadata.json": `{"title":"External title"}`})
		externalPath := filepath.Join(external, "metadata.json")
		var got bookMeta
		readSidecars("absMetadata", nil, externalPath, &got)
		if got.Title != "External title" {
			t.Fatalf("external title = %q", got.Title)
		}
		_, files := sidecars(t, map[string]string{"metadata.json": `{"title":"In-item title"}`})
		got = bookMeta{}
		readSidecars("absMetadata", files, externalPath, &got)
		if got.Title != "In-item title" {
			t.Fatalf("in-item title = %q", got.Title)
		}
	})
	t.Run("old: empty string overwrites, empty arrays do not", func(t *testing.T) {
		_, files := sidecars(t, map[string]string{"metadata.json": `{"title":"","authors":[],"genres":[],"tags":[],"chapters":[]}`})
		got := bookMeta{Title: "Existing", Authors: []string{"A"}, Genres: []string{"G"}}
		readSidecars("absMetadata", files, "", &got)
		if got.Title != "" || !slices.Equal(got.Authors, []string{"A"}) || !slices.Equal(got.Genres, []string{"G"}) {
			t.Fatalf("meta = %+v", got)
		}
	})
	t.Run("absent or null keys leave the value", func(t *testing.T) {
		_, files := sidecars(t, map[string]string{"metadata.json": `{"subtitle":null}`})
		got := bookMeta{Title: "Keep", Subtitle: "Keep too"}
		readSidecars("absMetadata", files, "", &got)
		if got.Title != "Keep" || got.Subtitle != "Keep too" {
			t.Fatalf("meta = %+v", got)
		}
	})
	t.Run("series entries without a name are dropped", func(t *testing.T) {
		_, files := sidecars(t, map[string]string{"metadata.json": `{"series":["", "Saga #2", {"name":" "}]}`})
		var got bookMeta
		readSidecars("absMetadata", files, "", &got)
		if !reflect.DeepEqual(got.Series, []SeriesEntry{{"Saga", "2"}}) {
			t.Fatalf("series = %+v", got.Series)
		}
	})
}

func TestParseMetadataJSON(t *testing.T) {
	t.Run("both series forms", func(t *testing.T) {
		doc := ParseMetadataJSON([]byte(`{"series":["Saga #2","Plain","Odd # name",{"name":"Old","sequence":"3"},{"name":"NoSeq"},7]}`))
		want := []SeriesEntry{{"Saga", "2"}, {"Plain", ""}, {"Odd # name", ""}, {"Old", "3"}, {"NoSeq", ""}, {}}
		if !reflect.DeepEqual(doc.Series, want) {
			t.Fatalf("series = %+v\nwant     %+v", doc.Series, want)
		}
	})
	t.Run("nested document of older servers fills gaps only", func(t *testing.T) {
		doc := ParseMetadataJSON([]byte(`{"title":"Top","authors":[],"metadata":{"title":"Nested","subtitle":"Sub","authors":["Au"],"series":["S #1"]}}`))
		if *doc.Title != "Top" || *doc.Subtitle != "Sub" || !slices.Equal(doc.Authors, []string{"Au"}) || !reflect.DeepEqual(doc.Series, []SeriesEntry{{"S", "1"}}) {
			t.Fatalf("doc = %+v", doc)
		}
	})
	t.Run("a mistyped field does not discard the rest", func(t *testing.T) {
		doc := ParseMetadataJSON([]byte(`{"title":"T","explicit":"yes","authors":["A"]}`))
		if doc.Title == nil || *doc.Title != "T" {
			t.Fatalf("title = %v", doc.Title)
		}
	})
	t.Run("not JSON gives an empty document", func(t *testing.T) {
		if doc := ParseMetadataJSON([]byte("nope")); doc.Title != nil || doc.Series != nil {
			t.Fatalf("doc = %+v", doc)
		}
	})
	t.Run("series round trip", func(t *testing.T) {
		raw, err := SeriesEntry{"Saga", "2"}.MarshalJSON()
		if err != nil || string(raw) != `"Saga #2"` {
			t.Fatalf("marshal = %s, %v", raw, err)
		}
	})
}

func TestApplyTags(t *testing.T) {
	t.Run("old: title, artist and slash-separated genres", func(t *testing.T) {
		m := bookMeta{Title: "Folder"}
		m.applyTags(audioTags(map[string]string{"TITLE": "Tagged", "artist": "A Writer", "genre": "Mystery/Fiction"}))
		if m.Title != "Tagged" || !slices.Equal(m.Authors, []string{"A Writer"}) || !slices.Equal(m.Genres, []string{"Mystery", "Fiction"}) {
			t.Fatalf("meta = %+v", m)
		}
	})
	t.Run("album beats title, album artist backs up artist, comment backs up description", func(t *testing.T) {
		var m bookMeta
		m.applyTags(audioTags(map[string]string{"album": "Album", "title": "Track", "albumartist": "AA", "comment": "C", "composer": "N1; N2"}))
		if m.Title != "Album" || !slices.Equal(m.Authors, []string{"AA"}) || m.Description != "C" || !slices.Equal(m.Narrators, []string{"N1", "N2"}) {
			t.Fatalf("meta = %+v", m)
		}
	})
	t.Run("series forms", func(t *testing.T) {
		tests := []struct {
			tags map[string]string
			want []SeriesEntry
		}{
			{map[string]string{"series": "Saga", "series-part": "4"}, []SeriesEntry{{"Saga", "4"}}},
			{map[string]string{"series": "A #1; B #2.5; C"}, []SeriesEntry{{"A", "1"}, {"B", "2.5"}, {"C", ""}}},
			{map[string]string{"grouping": "G #3"}, []SeriesEntry{{"G", "3"}}},
			{map[string]string{}, nil},
		}
		for _, tc := range tests {
			var m bookMeta
			m.applyTags(audioTags(tc.tags))
			if !reflect.DeepEqual(m.Series, tc.want) {
				t.Errorf("%v: series = %+v, want %+v", tc.tags, m.Series, tc.want)
			}
		}
	})
	t.Run("empty tags leave the folder values", func(t *testing.T) {
		m := bookMeta{Title: "Folder", Authors: []string{"F"}}
		m.applyTags(db.AudioMetaTags{})
		if m.Title != "Folder" || !slices.Equal(m.Authors, []string{"F"}) {
			t.Fatalf("meta = %+v", m)
		}
	})
}

func TestFolderMeta(t *testing.T) {
	tests := []struct {
		name, path                      string
		subtitle                        bool
		title, author, series, sequence string
	}{
		{"old: volume prefix", "Author/Series/Vol. 3 Title - Part", false, "Title - Part", "Author", "Series", "3"},
		{"old: year prefix", "Author/Series/(2020) - Title - Extra", false, "Title - Extra", "Author", "Series", ""},
		{"old: subtitle split", "Author/Series/Title - Subtitle", true, "Title", "Author", "Series", ""},
		{"title only", "Title", false, "Title", "", "", ""},
		{"author and title", "Author/Title", false, "Title", "Author", "", ""},
		{"sequence is not read without a series folder", "Author/1 - Title", false, "1 - Title", "Author", "", ""},
		{"book prefix", "A/S/Book 2 - Title", false, "Title", "A", "S", "2"},
		{"decimal sequence", "A/S/Book 1.5 - Title", false, "Title", "A", "S", "1.5"},
		{"leading zeros", "A/S/01 - Title", false, "Title", "A", "S", "1"},
		{"trailing dot sequence", "A/S/3. Title", false, "Title", "A", "S", "3"},
		{"ASIN removed", "A/Title [B0015T963C]", false, "Title", "A", "", ""},
		{"narrator braces removed", "A/Title {Reader Name}", false, "Title", "A", "", ""},
		{"year prefix without dash is kept", "A/2020 Title", false, "2020 Title", "A", "", ""},
		{"deeper folders use the last three", "X/Y/Author/Series/Title", false, "Title", "Author", "Series", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			title, author, series, sequence := folderMeta(filepath.FromSlash(tc.path), "", false, tc.subtitle)
			if title != tc.title || author != tc.author || series != tc.series || sequence != tc.sequence {
				t.Fatalf("folderMeta = %q/%q/%q/%q, want %q/%q/%q/%q", title, author, series, sequence, tc.title, tc.author, tc.series, tc.sequence)
			}
		})
	}
	t.Run("root is stripped and a single file is titled by its name", func(t *testing.T) {
		title, author, series, _ := folderMeta("/lib/Author/My Book.m4b", "/lib", true, false)
		if title != "My Book" || author != "Author" || series != "" {
			t.Fatalf("folderMeta = %q/%q/%q", title, author, series)
		}
	})
}

func TestRemoveASINAndExtractSequence(t *testing.T) {
	asins := []struct{ in, want string }{
		{"A [B0015T963C]", "A"}, {"[B0015T963C] A", "A"}, {"A [B0015T963C] B", "AB"}, // the pattern eats both spaces, as in the old scanner, {"A [short]", "A [short]"}, {"A", "A"},
	}
	for _, tc := range asins {
		if got := removeASIN(tc.in); got != tc.want {
			t.Errorf("removeASIN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	seqs := []struct{ in, title, seq string }{
		{"Book 2 - Title", "Title", "2"}, {"Vol 10 - Title", "Title", "10"}, {"Title", "Title", ""}, {"3", "", "3"}, {"Title - 4", "Title", "4"},
	}
	for _, tc := range seqs {
		if title, seq := extractSequence(tc.in); title != tc.title || seq != tc.seq {
			t.Errorf("extractSequence(%q) = %q,%q want %q,%q", tc.in, title, seq, tc.title, tc.seq)
		}
	}
}

func TestIgnoredPath(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{"a.mp3", false}, {"sub/e.mp3", false}, {".hidden/c.mp3", true}, {"sub/.hidden.mp3", true}, {"@eaDir/d.mp3", true},
		{"sub/@eaDir/d.mp3", true}, {"b.mp3.tmp", true}, {"x.PART", true}, {"x.crdownload", true}, {"x.bak", true}, {"x.mp3~", false},
	}
	for _, tc := range tests {
		if got := ignoredPath(tc.rel); got != tc.want {
			t.Errorf("ignoredPath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestWalkFilesIgnoreRules(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.mp3", "b.mp3.tmp", ".hidden/c.mp3", "@eaDir/d.mp3", "sub/e.mp3", "skip/.ignore", "skip/f.mp3", "skip/deep/g.mp3"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := walkFiles(context.Background(), root, func(rel string) bool { return filekind.IsAudio(filepath.Ext(rel)) })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	if strings.Join(got, ",") != "a.mp3,sub/e.mp3" {
		t.Fatalf("walked %v", got)
	}
}

func TestDiffFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) fileEntry {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return fileEntry{path: path, rel: name, info: info}
	}
	a, b, c, cover := write("a.mp3", "a"), write("b.mp3", "b"), write("c.epub", "c"), write("cover.jpg", "j")
	first := diffFiles(nil, []fileEntry{a, b}, 100)
	if len(first.added) != 2 || len(first.removed) != 0 || len(first.modified) != 0 || !first.changed() {
		t.Fatalf("first = %+v", first)
	}
	for _, f := range first.added {
		if f.AddedAt != 100 || f.UpdatedAt != 100 || f.FileType != filekind.Audio || f.IsSupplementary != nil {
			t.Fatalf("added entry = %+v", f)
		}
	}
	stored := slices.Clone(first.files)
	stored[0].AddedAt, stored[0].FileType = 5, "kept"
	t.Run("nothing changed keeps everything", func(t *testing.T) {
		d := diffFiles(stored, []fileEntry{b, a}, 200)
		if d.changed() || d.files[0].Metadata.Filename != "a.mp3" || d.files[1].Metadata.Filename != "b.mp3" || d.files[0].AddedAt != 5 || d.files[0].FileType != "kept" {
			t.Fatalf("diff = %+v", d)
		}
	})
	t.Run("added ebook is supplementary, image is not", func(t *testing.T) {
		d := diffFiles(stored, []fileEntry{a, b, c, cover}, 200)
		if len(d.added) != 2 || len(d.files) != 4 {
			t.Fatalf("diff = %+v", d)
		}
		if d.added[0].FileType != filekind.Ebook || d.added[0].IsSupplementary == nil || !*d.added[0].IsSupplementary {
			t.Fatalf("ebook = %+v", d.added[0])
		}
		if d.added[1].FileType != filekind.Image || d.added[1].IsSupplementary != nil || d.added[1].AddedAt != 200 {
			t.Fatalf("image = %+v", d.added[1])
		}
		if d.files[0].Metadata.Filename != "a.mp3" || d.files[2].Metadata.Filename != "c.epub" {
			t.Fatalf("order = %+v", d.files)
		}
	})
	t.Run("missing file is removed", func(t *testing.T) {
		d := diffFiles(stored, []fileEntry{a}, 200)
		if len(d.removed) != 1 || d.removed[0].Metadata.Filename != "b.mp3" || len(d.files) != 1 {
			t.Fatalf("diff = %+v", d)
		}
	})
	t.Run("changed size is modified and stamped, addedAt kept", func(t *testing.T) {
		a2 := write("a.mp3", "longer")
		d := diffFiles(stored, []fileEntry{a2, b}, 300)
		if len(d.modified) != 1 || d.modified[0].UpdatedAt != 300 || d.modified[0].AddedAt != 5 || d.oldPath[a.path] != a.path || !d.touched()[a.path] {
			t.Fatalf("diff = %+v", d)
		}
	})
}
