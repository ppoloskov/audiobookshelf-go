package scanner

import (
	"audiobookshelf-compat/internal/filekind"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
)

// The scan golden records, by value, the DB rows and events one library tree
// produces through the full scan, the watcher batch path and the single-item
// rescan. Regenerate with UPDATE_GOLDEN=1 only for an intended change.

var (
	goldenUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	goldenTime = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d+)?( [+-]\d\d:\d\d)?`)
	goldenMs   = regexp.MustCompile(`\d{12,}(\.\d+)?`)
	goldenIno  = regexp.MustCompile(`(\\?"ino\\?":\s*)(\\?")\d*(\\?")`)
)

func goldenNormalize(s, root, meta string) string {
	s = strings.ReplaceAll(s, root, "ROOT")
	s = strings.ReplaceAll(s, meta, "META")
	ids := map[string]string{}
	s = goldenUUID.ReplaceAllStringFunc(s, func(id string) string {
		if _, ok := ids[id]; !ok {
			ids[id] = "id" + string(rune('A'+len(ids)%26)) + string(rune('a'+len(ids)/26))
		}
		return ids[id]
	})
	s = regexp.MustCompile(`/file/\d+`).ReplaceAllString(s, "/file/I")
	s = goldenIno.ReplaceAllString(s, `${1}${2}I${3}`)
	s = goldenTime.ReplaceAllString(s, "T")
	s = goldenMs.ReplaceAllString(s, "0")
	s = regexp.MustCompile(`\(\d+ ms\)`).ReplaceAllString(s, "(N ms)")
	s = regexp.MustCompile(`"elapsed": \d+`).ReplaceAllString(s, `"elapsed": 0`)
	return s
}

// goldenCanonicalJSON re-encodes a JSON text column with sorted keys: key order
// is not part of the stored contract (ADR-002).
func goldenCanonicalJSON(v any) any {
	text, ok := v.(string)
	if !ok || !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
		return v
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var decoded any
	if dec.Decode(&decoded) != nil {
		return v
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return v
	}
	return string(out)
}

func goldenDump(t *testing.T, db *sql.DB, q string) []map[string]any {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			vals[i] = goldenCanonicalJSON(vals[i])
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out
}

// goldenHold keeps a hard link to every written file so a deleted file's inode
// is never reused by a later file (the scan matches moved items by inode).
var goldenHold string

func goldenWrite(t *testing.T, paths map[string]string) {
	t.Helper()
	for p, body := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(p, filepath.Join(goldenHold, strings.NewReplacer("/", "_", "\\", "_").Replace(p))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanGolden(t *testing.T) {
	root, meta := t.TempDir(), t.TempDir()
	goldenHold = t.TempDir()
	podRoot := t.TempDir()
	ffprobe := filepath.Join(t.TempDir(), "ffprobe")
	script := "#!/bin/sh\nfor a; do f=\"$a\"; done\nn=$(wc -c < \"$f\")\nprintf '{\"format\":{\"format_name\":\"mp3\",\"duration\":\"%s\",\"bit_rate\":\"64000\",\"tags\":{\"title\":\"T-%s\"}},\"streams\":[{\"codec_type\":\"audio\",\"codec_name\":\"mp3\",\"channels\":2}]}' \"$n\" \"$n\"\n"
	if err := os.WriteFile(ffprobe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", ffprobe)
	db := scannerTestDB(t, root)
	for _, q := range []string{
		`CREATE TABLE podcasts(id UUID PRIMARY KEY,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,createdAt,updatedAt)`,
		`CREATE TABLE podcastEpisodes(id UUID PRIMARY KEY,podcastId UUID,"index" INTEGER,season TEXT,episode TEXT,episodeType TEXT,title TEXT,subtitle TEXT,description TEXT,pubDate TEXT,enclosureURL TEXT,enclosureSize INTEGER,enclosureType TEXT,publishedAt DATETIME,audioFile JSON,chapters JSON,extraData JSON,createdAt DATETIME,updatedAt DATETIME)`,
		`INSERT INTO libraries(id,name,mediaType,settings,createdAt,updatedAt) VALUES('lib-2','Pods','podcast','{}','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
		`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('folder-2','` + podRoot + `','lib-2','2020-01-01 00:00:00.000 +00:00','2020-01-01 00:00:00.000 +00:00')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	emit, evs := captureEvents()
	s, err := newScannerForTest(db, config.Config{MetadataPath: meta}, emit)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	R := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	goldenWrite(t, map[string]string{
		R("Single.mp3"):                                "single-audio",
		R("Author A/Multi Book/01.mp3"):                "aaaa",
		R("Author A/Multi Book/02.mp3"):                "bbbbbb",
		R("Author A/Multi Book/cover.jpg"):             "img",
		R("Author A/Multi Book/desc.txt"):              "A description",
		R("Author A/Multi Book/metadata.json"):         `{"title":"Multi Title","authors":["Jane Doe"],"series":["Saga #2"],"genres":["Fantasy"]}`,
		R("Author B/CD Book/CD1/a.mp3"):                "cd-one",
		R("Author B/CD Book/CD2/b.mp3"):                "cd-two!",
		R("Author C/Series X/Vol 1 - Nested/ch.mp3"):   "nested",
		R("Author C/Series X/Vol 1 - Nested/book.opf"): `<package xmlns="http://www.idpf.org/2007/opf"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Opf Title</dc:title><dc:creator>Opf Author</dc:creator></metadata></package>`,
		R("Ignored Book/.ignore"):                      "",
		R("Ignored Book/x.mp3"):                        "ignored",
		R("Author D/Dot/.hidden/y.mp3"):                "hidden",
		R("@eaDir/z.mp3"):                              "syno",
		R("Ebook Book/book.epub"):                      "epub",
		R("Temp Book/part.mp3"):                        "partial",
		R("Temp Book/x.mp3.tmp"):                       "tmp",
		filepath.Join(podRoot, "loose.mp3"):            "dropped",
		filepath.Join(podRoot, "Show/ep1.mp3"):         "ep-one",
		filepath.Join(podRoot, "Show/ep2.mp3"):         "episode-two",
		filepath.Join(podRoot, "Show/cover.jpg"):       "pod-img",
	})
	type step struct {
		Name   string
		Result any
		Events []map[string]any
	}
	var steps []step
	record := func(name string, res any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		st := step{Name: name, Result: res}
		for _, e := range *evs {
			raw, _ := json.Marshal(e.payload)
			st.Events = append(st.Events, map[string]any{"scope": e.scope, "event": e.event, "payload": json.RawMessage(raw)})
		}
		*evs = nil
		steps = append(steps, st)
	}
	r, err := s.Scan(ctx, "lib-1", false)
	record("scan books", r, err)
	r, err = s.Scan(ctx, "lib-2", false)
	record("scan podcasts", r, err)
	r, err = s.Scan(ctx, "lib-1", false)
	record("rescan unchanged", r, err)

	// Rename a book directory, delete a file item, add a new file.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Rename(R("Author A/Multi Book"), R("Author A/Multi Renamed")))
	must(os.Remove(R("Single.mp3")))
	goldenWrite(t, map[string]string{R("Author B/CD Book/CD2/c.mp3"): "cd-three", filepath.Join(podRoot, "Show/ep3.mp3"): "ep3"})
	r, err = s.Scan(ctx, "lib-1", false)
	record("scan after rename delete add", r, err)
	r, err = s.Scan(ctx, "lib-2", false)
	record("scan podcast add episode", r, err)

	// Watcher batch: new item, deleted item, file moved up to the parent folder.
	goldenWrite(t, map[string]string{R("Author E/New Book/n.mp3"): "new-book", R("Author F/Moving/m.mp3"): "moving"})
	r, err = s.Scan(ctx, "lib-1", false)
	record("scan watcher fixtures", r, err)
	goldenWrite(t, map[string]string{R("Author E/Watched/w.mp3"): "watched", R("Author E/Watched/w.txt"): "note"})
	must(os.RemoveAll(R("Author E/New Book")))
	must(os.Rename(R("Author F/Moving/m.mp3"), R("Author F/m.mp3")))
	must(os.Remove(R("Author F/Moving")))
	var upd []fileUpdate
	for _, u := range []struct{ rel, kind string }{
		{"Author E/Watched/w.mp3", "added"}, {"Author E/Watched/w.txt", "added"},
		{"Author E/New Book/n.mp3", "deleted"}, {"Author F/Moving/m.mp3", "deleted"}, {"Author F/m.mp3", "added"},
	} {
		upd = append(upd, fileUpdate{path: R(u.rel), relPath: u.rel, folderID: "folder-1", libraryID: "lib-1", kind: u.kind})
	}
	res, err := s.scanFolderUpdates(ctx, "lib-1", "folder-1", upd)
	record("watcher batch", res, err)

	var itemID string
	must(db.QueryRow(`SELECT id FROM libraryItems WHERE path=?`, R("Author B/CD Book")).Scan(&itemID))
	goldenWrite(t, map[string]string{R("Author B/CD Book/notes.txt"): "n"})
	r, err = s.scanItemForce(ctx, itemID)
	record("scan item force", r, err)

	dump := map[string]any{"steps": steps}
	for name, q := range map[string]string{
		"libraryItems":    `SELECT path,relPath,isFile,isMissing,mediaType,size,libraryFiles,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst,mediaId,lastScanVersion,id FROM libraryItems ORDER BY path`,
		"books":           `SELECT * FROM books ORDER BY title,duration`,
		"authors":         `SELECT name,lastFirst,libraryId FROM authors ORDER BY name`,
		"series":          `SELECT name,nameIgnorePrefix FROM series ORDER BY name`,
		"bookAuthors":     `SELECT b.title,a.name FROM bookAuthors x JOIN books b ON b.id=x.bookId JOIN authors a ON a.id=x.authorId ORDER BY b.title,a.name`,
		"bookSeries":      `SELECT b.title,s.name,x.sequence FROM bookSeries x JOIN books b ON b.id=x.bookId JOIN series s ON s.id=x.seriesId ORDER BY b.title,s.name`,
		"podcasts":        `SELECT * FROM podcasts ORDER BY title`,
		"podcastEpisodes": `SELECT * FROM podcastEpisodes ORDER BY podcastId,"index"`,
		"libraries":       `SELECT id,lastScanVersion,extraData FROM libraries ORDER BY id`,
	} {
		dump[name] = goldenDump(t, db, q)
	}
	sidecars := map[string]string{}
	_ = filepath.WalkDir(meta, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			// Keyed by the owning item's path: item ids are random.
			var itemPath string
			_ = db.QueryRow(`SELECT path FROM libraryItems WHERE id=?`, filepath.Base(filepath.Dir(p))).Scan(&itemPath)
			sidecars[itemPath+"|"+filepath.Base(p)] = string(b)
		}
		return nil
	})
	dump["sidecars"] = sidecars
	raw, err := json.MarshalIndent(dump, "", " ")
	must(err)
	// Row order is by path/title; ids only need stable relative identity.
	got := goldenNormalize(string(raw), root, meta)
	got = strings.ReplaceAll(got, podRoot, "PODROOT")
	golden := filepath.Join("testdata", "scan_golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		must(os.WriteFile(golden, []byte(got), 0o644))
	}
	want, err := os.ReadFile(golden)
	must(err)
	var a, b any
	must(json.Unmarshal(want, &a))
	must(json.Unmarshal([]byte(got), &b))
	wa, _ := json.Marshal(a)
	wb, _ := json.Marshal(b)
	if string(wa) != string(wb) {
		must(os.WriteFile(golden+".got", []byte(got), 0o644))
		t.Fatalf("scan golden differs; compare %s and %s.got", golden, golden)
	}
}

// One walk serves the library scan, the item rescan and the watcher: ignored
// names, temporary extensions and .ignore directories are skipped everywhere.
func TestWalkFilesIgnoreRules(t *testing.T) {
	root := t.TempDir()
	goldenHold = t.TempDir()
	for _, rel := range []string{"a.mp3", "b.mp3.tmp", ".hidden/c.mp3", "@eaDir/d.mp3", "sub/e.mp3", "skip/.ignore", "skip/f.mp3", "skip/deep/g.mp3"} {
		goldenWrite(t, map[string]string{filepath.Join(root, filepath.FromSlash(rel)): "x"})
	}
	files, err := walkFiles(context.Background(), root, func(rel string) bool { return filekind.IsAudio(filepath.Ext(rel)) })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.relPath)
	}
	if strings.Join(got, ",") != "a.mp3,sub/e.mp3" {
		t.Fatalf("walked %v", got)
	}
}
