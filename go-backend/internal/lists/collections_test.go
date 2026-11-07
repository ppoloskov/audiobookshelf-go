package lists

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

func TestCollectionReadValues(t *testing.T) {
	db, created, updated := collectionReadFixture(t)
	var checked []string
	canView := func(_ context.Context, itemID string) (bool, error) {
		checked = append(checked, itemID)
		return itemID != "item-book-denied", nil
	}

	got, libraryID, err := Collection(context.Background(), db, "collection-1", canView)
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
	books, _ := asJSON(t, got)["books"].([]any)
	if len(books) != 2 || books[0].(map[string]any)["id"] != "item-book-1" || books[1].(map[string]any)["id"] != "item-book-2" ||
		books[0].(map[string]any)["media"].(map[string]any)["audioFiles"] == nil {
		t.Fatalf("books=%#v, want the two visible expanded items in order", books)
	}

	empty, emptyLibrary, err := Collection(context.Background(), db, "collection-1", func(context.Context, string) (bool, error) { return false, nil })
	if err != nil || emptyLibrary != "library-1" {
		t.Fatalf("all-denied collection library=%q err=%v", emptyLibrary, err)
	}
	if books, ok := empty["books"].([]any); !ok || len(books) != 0 {
		t.Fatalf("all-denied books=%#v, want empty array", empty["books"])
	}

	wantErr := errors.New("visibility failed")
	var calls []string
	partial, partialLibrary, err := Collection(context.Background(), db, "collection-1", func(_ context.Context, itemID string) (bool, error) {
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

	missing, missingLibrary, err := Collection(context.Background(), db, "missing", func(context.Context, string) (bool, error) { return true, nil })
	if !errors.Is(err, sql.ErrNoRows) || missing != nil || missingLibrary != "" {
		t.Fatalf("missing collection result=%#v library=%q err=%v", missing, missingLibrary, err)
	}
}

func collectionReadFixture(t *testing.T) (*sql.DB, time.Time, time.Time) {
	t.Helper()
	db := schemaDB(t)
	created := time.Date(2024, 4, 5, 6, 7, 8, 0, time.UTC)
	updated := time.Date(2024, 4, 6, 7, 8, 9, 0, time.UTC)
	exec(t, db, `INSERT INTO collections (id,name,description,createdAt,updatedAt,libraryId) VALUES ('collection-1','Reading List',NULL,?,?, 'library-1')`, dbtime.Format(created), dbtime.Format(updated))
	for i, row := range [][2]string{{"item-book-1", "book-1"}, {"item-book-denied", "book-denied"}, {"item-book-2", "book-2"}} {
		addBook(t, db, row[0], row[1], row[1])
		exec(t, db, `INSERT INTO collectionBooks (id,"order",createdAt,bookId,collectionId) VALUES (?,?,?,?, 'collection-1')`, "entry-"+row[0], i+1, dbtime.Format(created), row[1])
	}
	return db, created, updated
}
