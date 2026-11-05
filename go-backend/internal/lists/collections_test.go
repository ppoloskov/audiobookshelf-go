package lists

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	_ "modernc.org/sqlite"
)

func TestCollectionReadValues(t *testing.T) {
	db, created, updated := collectionReadFixture(t)
	reader := &playlistItemReader{}
	var checked []string
	canView := func(_ context.Context, itemID string) (bool, error) {
		checked = append(checked, itemID)
		return itemID != "item-book-denied", nil
	}

	got, libraryID, err := Collection(context.Background(), db, reader, "collection-1", canView)
	if err != nil {
		t.Fatal(err)
	}
	if libraryID != "library-1" || got["libraryId"] != "library-1" || got["id"] != "collection-1" || got["name"] != "Reading List" {
		t.Fatalf("collection identity=%#v libraryID=%q", got, libraryID)
	}
	if description, exists := got["description"]; !exists || description != nil {
		t.Fatalf("description=%#v present=%t; want explicit null", description, exists)
	}
	if got["createdAt"] != created.UnixMilli() || got["lastUpdate"] != updated.UnixMilli() {
		t.Fatalf("collection timestamps created=%v updated=%v", got["createdAt"], got["lastUpdate"])
	}
	wantChecked := []string{"item-book-1", "item-book-denied", "item-book-2"}
	if !reflect.DeepEqual(checked, wantChecked) {
		t.Fatalf("visibility calls=%v, want %v", checked, wantChecked)
	}
	wantReader := []string{"item-book-1", "item-book-2"}
	if !reflect.DeepEqual(reader.expanded, wantReader) {
		t.Fatalf("item reader calls=%v, want %v", reader.expanded, wantReader)
	}
	wantBooks := []any{
		map[string]any{"id": "item-book-1", "expanded": true},
		map[string]any{"id": "item-book-2", "expanded": true},
	}
	if books, ok := got["books"].([]any); !ok || !reflect.DeepEqual(books, wantBooks) {
		t.Fatalf("books=%#v, want %#v", got["books"], wantBooks)
	}

	emptyReader := &playlistItemReader{}
	empty, emptyLibrary, err := Collection(context.Background(), db, emptyReader, "collection-1", func(context.Context, string) (bool, error) { return false, nil })
	if err != nil || emptyLibrary != "library-1" {
		t.Fatalf("all-denied collection library=%q err=%v", emptyLibrary, err)
	}
	if books, ok := empty["books"].([]any); !ok || len(books) != 0 {
		t.Fatalf("all-denied books=%#v, want empty array", empty["books"])
	}
	if len(emptyReader.expanded) != 0 {
		t.Fatalf("all-denied item reader calls=%v, want none", emptyReader.expanded)
	}

	wantErr := errors.New("visibility failed")
	var calls []string
	partial, partialLibrary, err := Collection(context.Background(), db, &playlistItemReader{}, "collection-1", func(_ context.Context, itemID string) (bool, error) {
		calls = append(calls, itemID)
		if itemID == "item-book-denied" {
			return false, wantErr
		}
		return true, nil
	})
	if !errors.Is(err, wantErr) || partial != nil || partialLibrary != "" {
		t.Fatalf("callback error result=%#v library=%q err=%v", partial, partialLibrary, err)
	}
	if !reflect.DeepEqual(calls, []string{"item-book-1", "item-book-denied"}) {
		t.Fatalf("callback error calls=%v", calls)
	}

	missing, missingLibrary, err := Collection(context.Background(), db, &playlistItemReader{}, "missing", func(context.Context, string) (bool, error) { return true, nil })
	if !errors.Is(err, sql.ErrNoRows) || missing != nil || missingLibrary != "" {
		t.Fatalf("missing collection result=%#v library=%q err=%v", missing, missingLibrary, err)
	}
}

func collectionReadFixture(t *testing.T) (*sql.DB, time.Time, time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, query := range []string{
		`CREATE TABLE collections (id TEXT PRIMARY KEY,name TEXT,description TEXT,createdAt DATETIME,updatedAt DATETIME,libraryId TEXT)`,
		`CREATE TABLE collectionBooks (id TEXT PRIMARY KEY,"order" INTEGER,createdAt DATETIME,bookId TEXT,collectionId TEXT)`,
		`CREATE TABLE libraryItems (id TEXT PRIMARY KEY,mediaId TEXT,mediaType TEXT)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC)
	updated := time.Date(2024, 4, 6, 7, 8, 9, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO collections VALUES ('collection-1','Reading List',NULL,?,?, 'library-1')`, dbtime.Format(created), dbtime.Format(updated)); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, mediaID string
		order       int
	}{
		{"item-book-1", "book-1", 1},
		{"item-book-denied", "book-denied", 2},
		{"item-book-2", "book-2", 3},
	} {
		if _, err := db.Exec(`INSERT INTO libraryItems VALUES (?,?,'book')`, row.id, row.mediaID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO collectionBooks VALUES (?,?,? ,?, 'collection-1')`, "entry-"+row.id, row.order, dbtime.Format(created), row.mediaID); err != nil {
			t.Fatal(err)
		}
	}
	return db, created, updated
}
