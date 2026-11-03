package contracttest

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/internal/contracttest/shape"
)

// journeyDumps are the journey.json entries that are not HTTP steps: the trial
// read them from the database and the install's files after the journey, in
// this order (dumpRows and disposableState in tools/parity/replacement-trial.cjs).
var journeyDumps = []string{
	"db.authors", "db.books.titles", "db.books.match", "db.series", "db.bookAuthors",
	"db.libraryItems.authorNames", "fs.metadata.authors", "fs.cache.images",
	"fs.items.files", "fs.items.metadata.json", "db.feeds", "db.feedEpisodes",
	"db.mediaItemShares", "db.devices.public", "fs.media.disposable",
}

// dumpQueries are the trial's SQL for the database entries.
var dumpQueries = map[string]string{
	"db.authors":                  `SELECT * FROM authors ORDER BY name, createdAt`,
	"db.books.titles":             `SELECT li.title AS itemTitle, li.titleIgnorePrefix AS itemIgnore, b.title AS bookTitle, b.titleIgnorePrefix AS bookIgnore, b.subtitle, b.narrators, b.genres, b.tags, b.explicit, b.publishedYear, b.description FROM libraryItems li JOIN books b ON b.id = li.mediaId ORDER BY li.createdAt, li.title`,
	"db.books.match":              `SELECT li.title AS itemTitle, b.coverPath, b.asin, b.isbn, b.publisher, b.language, b.abridged FROM libraryItems li JOIN books b ON b.id = li.mediaId ORDER BY li.createdAt, li.title`,
	"db.series":                   `SELECT * FROM series ORDER BY name, createdAt`,
	"db.bookAuthors":              `SELECT li.title AS title, a.name AS author FROM bookAuthors ba JOIN authors a ON a.id = ba.authorId JOIN libraryItems li ON li.mediaId = ba.bookId ORDER BY li.title, ba.createdAt, a.name`,
	"db.libraryItems.authorNames": `SELECT title, authorNamesFirstLast, authorNamesLastFirst FROM libraryItems ORDER BY title`,
	"db.feeds":                    `SELECT * FROM feeds ORDER BY slug`,
	"db.feedEpisodes":             `SELECT fe.*, f.slug AS feedSlug FROM feedEpisodes fe LEFT JOIN feeds f ON f.id = fe.feedId ORDER BY f.slug, fe.pubDate, fe.filePath`,
	"db.mediaItemShares":          `SELECT * FROM mediaItemShares ORDER BY slug`,
	"db.devices.public":           `SELECT * FROM devices WHERE userId IS NULL ORDER BY clientName, deviceName`,
}

// idKeyedDumps are keyed by item ids, so their recorded keys are the recording
// run's ids (see shapeSkip in journey_steps_playback_test.go).
var idKeyedDumps = map[string]bool{"fs.items.files": true, "fs.items.metadata.json": true}

// checkJourneyDumps checks the database and file entries after the steps, on the
// same install. Each one must be a status 200 response with its recorded shape.
func checkJourneyDumps(t *testing.T, in *Install, recorded map[string]recordedStep) {
	t.Helper()
	for _, id := range journeyDumps {
		t.Run(id, func(t *testing.T) {
			rec, ok := recorded[id]
			if !ok {
				t.Fatalf("no recorded shape for %s in %s", id, journeyShapesFile)
			}
			body := dumpBody(t, in, id)
			if rec.Status != 200 {
				t.Fatalf("recorded status %d, want 200", rec.Status)
			}
			if idKeyedDumps[id] {
				t.Logf("shape not checked: the recorded map is keyed by the recording run's item ids")
				return
			}
			for _, problem := range shape.Conforms(rec.Shape, body) {
				t.Error(problem)
			}
		})
	}
}

// dumpBody reads one database or file entry the way the trial did.
func dumpBody(t *testing.T, in *Install, id string) any {
	t.Helper()
	switch id {
	case "fs.metadata.authors":
		return listDir(t, filepath.Join(in.Dir, "metadata", "authors"))
	case "fs.cache.images":
		return listDir(t, filepath.Join(in.Dir, "metadata", "cache", "images"))
	case "fs.items.files":
		out := map[string]any{}
		for _, item := range itemDirs(t, in) {
			var files []any
			for _, f := range listDir(t, filepath.Join(in.Dir, "metadata", "items", item)).([]any) {
				if f.(map[string]any)["name"] != "metadata.json" {
					files = append(files, f)
				}
			}
			out[item] = files
		}
		return out
	case "fs.items.metadata.json":
		out := map[string]any{}
		for _, item := range itemDirs(t, in) {
			raw, err := os.ReadFile(filepath.Join(in.Dir, "metadata", "items", item, "metadata.json"))
			if err != nil {
				continue
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("%s: %v", item, err)
			}
			out[item] = v
		}
		return out
	case "fs.media.disposable":
		return disposableFiles(in.Dir)
	}
	return queryRows(t, in.DB, dumpQueries[id])
}

// itemDirs lists the library item folders under metadata/items, sorted.
func itemDirs(t *testing.T, in *Install) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(in.Dir, "metadata", "items"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// listDir lists the files of dir as {name, size}, sorted by name. A missing
// directory lists as empty.
func listDir(t *testing.T, dir string) any {
	t.Helper()
	out := []any{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		out = append(out, map[string]any{"name": e.Name(), "size": info.Size()})
	}
	return out
}

// disposableFiles lists the folder the hard delete removes, recursively and
// sorted. nil when it is gone.
func disposableFiles(dir string) any {
	root := filepath.Join(dir, "media", "audiobooks", "Zed Disposable")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	out := []any{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, rel)
		return nil
	})
	return out
}

// queryRows runs a dump query and returns each row as a column map. Values are
// the JSON types node:sqlite gives: numbers, strings and null. Booleans are 0
// or 1, and times are the text the column holds.
func queryRows(t *testing.T, db *sql.DB, query string) any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	out := []any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = sqlValue(vals[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func sqlValue(v any) any {
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case time.Time:
		return x.Format("2006-01-02 15:04:05.000 -07:00")
	case []byte:
		return string(x)
	}
	return v
}
