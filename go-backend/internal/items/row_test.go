package items

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestItemRowValues(t *testing.T) {
	db := itemRowDB(t)
	_, err := db.Exec(`INSERT INTO libraryItems VALUES (?,NULL,'library',NULL,'/books/a','a',1,?,?,?,?,?,?,NULL,0,0,'book',0,?,?,'media-a')`,
		"item-a", "2024-01-02 03:04:05.678 +00:00", nil, nil,
		"2024-01-02 03:04:06.000 +00:00", nil, nil, `{"oldLibraryItemId":"old-a"}`,
		`[{"ino":"cover","metadata":{"ext":".EPUB"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), rowSelect+` WHERE li.id=?`, "item-a")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("item row not found")
	}
	record, err := ScanRecord(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if record.ID != "item-a" || record.MediaID != "media-a" || record.ID == record.MediaID {
		t.Fatalf("identity=%q/%q, want distinct item/media IDs", record.ID, record.MediaID)
	}
	wantAdded := time.Date(2024, 1, 2, 3, 4, 6, 0, time.UTC).UnixMilli()
	if record.AddedAt == nil || *record.AddedAt != wantAdded {
		t.Fatalf("AddedAt=%v, want %d", record.AddedAt, wantAdded)
	}
	if record.MTimeMS == nil || *record.MTimeMS != time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC).UnixMilli() {
		t.Fatalf("MTimeMS=%v", record.MTimeMS)
	}
	if record.CTimeMS != nil || record.BirthtimeMS != nil || record.UpdatedAt != nil || record.LastScan != nil {
		t.Fatalf("optional times=%+v", record)
	}
	if record.Size != int64(0) || record.OldLibraryItemID != "old-a" || !record.IsFile {
		t.Fatalf("common values=%+v", record)
	}
	if len(record.LibraryFiles) != 1 || record.LibraryFiles[0]["fileType"] != "ebook" {
		t.Fatalf("LibraryFiles=%#v", record.LibraryFiles)
	}
	if FileType(".MP3") != "audio" || FileType("unknown") != "unknown" {
		t.Fatal("file extension classification changed")
	}
}

func TestItemRowValuesMalformedDate(t *testing.T) {
	db := itemRowDB(t)
	_, err := db.Exec(`INSERT INTO libraryItems VALUES ('bad','i','l',NULL,'/bad','bad',0,'bad-date',NULL,NULL,NULL,NULL,NULL,NULL,0,0,'book',NULL,'{}','[]','media')`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(context.Background(), rowSelect+` WHERE li.id='bad'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("item row not found")
	}
	if _, err := ScanRecord(rows); err == nil {
		t.Fatal("malformed date was accepted")
	}
}

func TestItemLoadManyBoundariesAndMissingIDs(t *testing.T) {
	db := itemRowDB(t)
	for i := 0; i < 251; i++ {
		id := fmt.Sprintf("item-%03d", i)
		if _, err := db.Exec(`INSERT INTO libraryItems VALUES (?,NULL,'l',NULL,'/','',0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,0,0,'book',NULL,'{}','[]',?)`, id, "media-"+id); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name string
		ids  []string
		want int
	}{
		{name: "empty", ids: nil, want: 0},
		{name: "one", ids: []string{"item-000"}, want: 1},
		{name: "250 deduplicated", ids: append(itemIDs(250), "item-000"), want: 250},
		{name: "251 across chunks", ids: itemIDs(251), want: 251},
		{name: "missing omitted", ids: []string{"missing"}, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := LoadMany(context.Background(), db, test.ids)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.want {
				t.Fatalf("LoadMany returned %d records, want %d", len(got), test.want)
			}
		})
	}
}

func TestItemLoadManyCancellation(t *testing.T) {
	db := itemRowDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadMany(ctx, db, []string{"item"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadMany error=%v, want context.Canceled", err)
	}
}

func itemIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("item-%03d", i)
	}
	return ids
}

func itemRowDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE libraryItems (
		id TEXT, ino TEXT, libraryId TEXT, libraryFolderId TEXT, path TEXT, relPath TEXT,
		isFile INTEGER, mtime DATETIME, ctime DATETIME, birthtime DATETIME, createdAt DATETIME,
		updatedAt DATETIME, lastScan DATETIME, lastScanVersion TEXT, isMissing INTEGER, isInvalid INTEGER,
		mediaType TEXT, size BIGINT, extraData JSON, libraryFiles JSON, mediaId TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
