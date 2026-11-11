package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

// tables lists the 26 tables of the pinned schema with their row struct and the
// column that identifies a row (id, or key for the two tables without an id).
var tables = []struct {
	name string
	row  any
	key  string
}{
	{"apiKeys", db.ApiKey{}, "id"},
	{"authors", db.Author{}, "id"},
	{"bookAuthors", db.BookAuthor{}, "id"},
	{"bookSeries", db.BookSeries{}, "id"},
	{"books", db.Book{}, "id"},
	{"collectionBooks", db.CollectionBook{}, "id"},
	{"collections", db.Collection{}, "id"},
	{"customMetadataProviders", db.CustomMetadataProvider{}, "id"},
	{"devices", db.Device{}, "id"},
	{"feedEpisodes", db.FeedEpisode{}, "id"},
	{"feeds", db.Feed{}, "id"},
	{"libraries", db.Library{}, "id"},
	{"libraryFolders", db.LibraryFolder{}, "id"},
	{"libraryItems", db.LibraryItem{}, "id"},
	{"mediaItemShares", db.MediaItemShare{}, "id"},
	{"mediaProgresses", db.MediaProgress{}, "id"},
	{"migrationsMeta", db.MigrationsMeta{}, "key"},
	{"playbackSessions", db.PlaybackSession{}, "id"},
	{"playlistMediaItems", db.PlaylistMediaItem{}, "id"},
	{"playlists", db.Playlist{}, "id"},
	{"podcastEpisodes", db.PodcastEpisode{}, "id"},
	{"podcasts", db.Podcast{}, "id"},
	{"series", db.Series{}, "id"},
	{"sessions", db.Session{}, "id"},
	{"settings", db.Setting{}, "key"},
	{"users", db.User{}, "id"},
}

// tagged lists the db column names of a row struct type, failing on an exported
// field without a db tag.
func tagged(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var names []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := field.Tag.Get("db")
		if name == "" {
			t.Errorf("%s.%s has no db tag", typ.Name(), field.Name)
			continue
		}
		names = append(names, name)
	}
	return names
}

func TestStructsMatchSchemaColumns(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()

	var schemaTables []string
	if err := d.Select(ctx, &schemaTables, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err != nil {
		t.Fatal(err)
	}
	var covered []string
	for _, table := range tables {
		covered = append(covered, table.name)
	}
	slices.Sort(covered)
	if !slices.Equal(schemaTables, covered) {
		t.Fatalf("schema tables %v, structs for %v", schemaTables, covered)
	}
	if len(tables) != 26 {
		t.Fatalf("%d tables, want 26", len(tables))
	}

	for _, table := range tables {
		var columns []string
		if err := d.Select(ctx, &columns, "SELECT name FROM pragma_table_info(?)", table.name); err != nil {
			t.Fatal(err)
		}
		fields := tagged(t, reflect.TypeOf(table.row))
		slices.Sort(columns)
		slices.Sort(fields)
		if !slices.Equal(columns, fields) {
			t.Errorf("%s: table columns %v, struct tags %v", table.name, columns, fields)
		}
	}
}

// fill sets every field of v to a non-zero value, recursing into documents.
func fill(v reflect.Value) {
	switch v.Type() {
	case reflect.TypeFor[db.Time]():
		v.Set(reflect.ValueOf(db.FromMillis(1700000000123)))
		return
	case reflect.TypeFor[json.RawMessage]():
		v.Set(reflect.ValueOf(json.RawMessage(`{"a":1}`)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("text")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	default:
		panic(fmt.Sprintf("fill: unsupported kind %s of %s", v.Kind(), v.Type()))
	}
}

// sampleRow returns a pointer to a row of the table with every column set.
func sampleRow(typ reflect.Type, key string) reflect.Value {
	row := reflect.New(typ)
	fill(row.Elem())
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Tag.Get("db") == key {
			row.Elem().Field(i).SetString("key-" + typ.Name())
		}
	}
	return row
}

func TestRowsRoundTripThroughInsert(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	// The rows reference each other only by invented ids.
	if _, err := d.Exec(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		typ := reflect.TypeOf(table.row)
		row := sampleRow(typ, table.key)
		if err := d.Insert(ctx, table.name, row.Interface()); err != nil {
			t.Errorf("%s: insert: %v", table.name, err)
			continue
		}
		got := reflect.New(typ)
		query := "SELECT * FROM `" + table.name + "` WHERE `" + table.key + "` = ?"
		if err := d.Get(ctx, got.Interface(), query, "key-"+typ.Name()); err != nil {
			t.Errorf("%s: select *: %v", table.name, err)
			continue
		}
		if !reflect.DeepEqual(got.Elem().Interface(), row.Elem().Interface()) {
			t.Errorf("%s: read back\n %+v\nwant\n %+v", table.name, got.Elem().Interface(), row.Elem().Interface())
		}
	}
}

// snapshot is a dump of all tables: per table, per row, per column the SQLite
// typeof() and quote() of the cell. JSON columns are compared by value, so the
// quote() is of the canonical re-encoding.
type snapshot map[string][][]string

// dump snapshots every table in rowid order.
func dump(t *testing.T, d *db.DB) snapshot {
	t.Helper()
	ctx := context.Background()
	out := snapshot{}
	for _, table := range tables {
		var columns []struct {
			Name string `db:"name"`
			Type string `db:"type"`
		}
		if err := d.Select(ctx, &columns, "SELECT name, type FROM pragma_table_info(?) ORDER BY cid", table.name); err != nil {
			t.Fatal(err)
		}
		var exprs []string
		for _, column := range columns {
			if column.Type == "JSON" {
				exprs = append(exprs, fmt.Sprintf("typeof(`%[1]s`), CAST(`%[1]s` AS TEXT)", column.Name))
			} else {
				exprs = append(exprs, fmt.Sprintf("typeof(`%[1]s`), quote(`%[1]s`)", column.Name))
			}
		}
		rows, err := d.SQL().QueryContext(ctx, "SELECT "+strings.Join(exprs, ", ")+" FROM `"+table.name+"` ORDER BY rowid")
		if err != nil {
			t.Fatalf("dump %s: %v", table.name, err)
		}
		for rows.Next() {
			cells := make([]sql.NullString, 2*len(columns))
			dest := make([]any, len(cells))
			for i := range cells {
				dest[i] = &cells[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			row := make([]string, len(columns))
			for i, column := range columns {
				value := cells[2*i+1].String
				if column.Type == "JSON" && cells[2*i+1].Valid {
					var doc any
					if err := json.Unmarshal([]byte(value), &doc); err == nil {
						canonical, _ := json.Marshal(doc) // sorted keys
						value = string(canonical)
					}
				}
				row[i] = cells[2*i].String + " " + value
			}
			out[table.name] = append(out[table.name], row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

func rowCount(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Get(context.Background(), &n, "SELECT COUNT(*) FROM `"+table+"`"); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFixtureDocumentsRoundTrip scans every row of the upstream-created fixture
// into its struct and checks that writing the row back would change nothing.
func TestFixtureDocumentsRoundTrip(t *testing.T) {
	d := dbtest.Fixture(t)
	ctx := context.Background()
	total := 0
	for _, table := range tables {
		typ := reflect.TypeOf(table.row)
		rows := reflect.New(reflect.SliceOf(typ))
		if err := d.Select(ctx, rows.Interface(), "SELECT * FROM `"+table.name+"` ORDER BY rowid"); err != nil {
			t.Errorf("%s: %v", table.name, err)
			continue
		}
		slice := rows.Elem()
		if slice.Len() != rowCount(t, d, table.name) {
			t.Errorf("%s: scanned %d rows", table.name, slice.Len())
		}
		total += slice.Len()
		for i := range typ.NumField() {
			field := typ.Field(i)
			column := field.Tag.Get("db")
			isTime := field.Type == reflect.TypeFor[db.Time]()
			isJSON := strings.HasPrefix(field.Type.Name(), "JSON[")
			if !isTime && !isJSON {
				continue
			}
			var stored []sql.NullString
			if err := d.Select(ctx, &stored, "SELECT CAST(`"+column+"` AS TEXT) FROM `"+table.name+"` ORDER BY rowid"); err != nil {
				t.Fatal(err)
			}
			for n := range stored {
				id := slice.Index(n).FieldByName("ID")
				where := fmt.Sprintf("%s.%s row %d", table.name, column, n)
				if id.IsValid() {
					where = fmt.Sprintf("%s.%s id %s", table.name, column, id)
				}
				value, err := slice.Index(n).Field(i).Interface().(driver.Valuer).Value()
				if err != nil {
					t.Errorf("%s: Value: %v", where, err)
					continue
				}
				if isTime {
					want := any(nil)
					if stored[n].Valid {
						want = stored[n].String
					}
					if value != want {
						t.Errorf("%s: DATETIME stored %v, written %v", where, want, value)
					}
					continue
				}
				if !stored[n].Valid || value == nil {
					if stored[n].Valid != (value != nil) {
						t.Errorf("%s: JSON stored %q (valid %v), re-marshalled %v", where, stored[n].String, stored[n].Valid, value)
					}
					continue
				}
				var want, got any
				if err := json.Unmarshal([]byte(stored[n].String), &want); err != nil {
					t.Errorf("%s: stored text is not JSON: %v", where, err)
					continue
				}
				if err := json.Unmarshal([]byte(value.(string)), &got); err != nil {
					t.Errorf("%s: re-marshalled text is not JSON: %v", where, err)
					continue
				}
				if !reflect.DeepEqual(want, got) {
					t.Errorf("%s: JSON differs\n stored: %s\n marshalled: %s", where, stored[n].String, value)
				}
			}
		}
	}
	if total == 0 {
		t.Fatal("the fixture database is empty")
	}
}

// TestFixtureRewriteChangesNothing writes every fixture row back with all its
// columns and compares a full dump (storage type and value of every cell) of
// all tables before and after.
func TestFixtureRewriteChangesNothing(t *testing.T) {
	d := dbtest.Fixture(t)
	ctx := context.Background()
	before := dump(t, d)

	for _, table := range tables {
		typ := reflect.TypeOf(table.row)
		rows := reflect.New(reflect.SliceOf(typ))
		if err := d.Select(ctx, rows.Interface(), "SELECT * FROM `"+table.name+"` ORDER BY rowid"); err != nil {
			t.Fatalf("%s: %v", table.name, err)
		}
		if table.key != "id" {
			// No id to update by: delete and insert again.
			if _, err := d.Exec(ctx, "DELETE FROM `"+table.name+"`"); err != nil {
				t.Fatal(err)
			}
		}
		for i := range rows.Elem().Len() {
			row := rows.Elem().Index(i).Interface()
			var err error
			if table.key == "id" {
				err = d.Update(ctx, table.name, row)
			} else {
				err = d.Insert(ctx, table.name, row)
			}
			if err != nil {
				t.Errorf("%s row %d: %v", table.name, i, err)
			}
		}
	}

	after := dump(t, d)
	for _, table := range tables {
		if reflect.DeepEqual(before[table.name], after[table.name]) {
			continue
		}
		if len(before[table.name]) != len(after[table.name]) {
			t.Errorf("%s: %d rows before, %d after", table.name, len(before[table.name]), len(after[table.name]))
			continue
		}
		for n := range before[table.name] {
			for c := range before[table.name][n] {
				if b, a := before[table.name][n][c], after[table.name][n][c]; b != a {
					t.Errorf("%s row %d column %d changed:\n before: %s\n after:  %s", table.name, n, c, b, a)
				}
			}
		}
	}
}
