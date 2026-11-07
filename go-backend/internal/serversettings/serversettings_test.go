package serversettings

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCacheServesUntilInvalidated(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT); INSERT INTO settings VALUES('server-settings','{"sortingPrefixes":["der"],"language":"de"}')`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if s := Load(ctx, db); !slices.Equal(s.SortingPrefixes, []string{"der"}) || s.Doc["language"] != "de" {
		t.Fatalf("first read %+v", s)
	}
	db.Exec(`UPDATE settings SET value='{"language":"fr"}'`)
	if s := Load(ctx, db); s.Doc["language"] != "de" {
		t.Fatalf("expected the cached value, got %v", s.Doc["language"])
	}
	Invalidate(db)
	if s := Load(ctx, db); s.Doc["language"] != "fr" || !slices.Equal(s.SortingPrefixes, []string{"the", "a"}) {
		t.Fatalf("after invalidate %+v", s)
	}
	db.Exec(`DELETE FROM settings`)
	Invalidate(db)
	if _, err := Get(ctx, db); err == nil {
		t.Fatal("missing row must be an error")
	}
	if s := Load(ctx, db); len(s.Doc) != 0 || !slices.Equal(s.SortingPrefixes, []string{"the", "a"}) {
		t.Fatalf("defaults %+v", s)
	}
}
