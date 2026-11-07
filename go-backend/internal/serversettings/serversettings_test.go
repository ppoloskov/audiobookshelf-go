package serversettings

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { Forget(db); _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT); INSERT INTO settings VALUES('server-settings','{"allowIframe":true,"sortingPrefixes":["the","a"],"nested":{"k":1}}')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func write(t *testing.T, db *sql.DB, doc string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE settings SET value=? WHERE key='server-settings'`, doc); err != nil {
		t.Fatal(err)
	}
	Invalidate(db)
}

func TestSnapshotsAreImmutable(t *testing.T) {
	db := open(t)
	s := Load(context.Background(), db)
	doc := s.Doc()
	doc["allowIframe"] = false
	doc["nested"].(map[string]any)["k"] = 2
	prefixes := s.SortingPrefixes()
	prefixes[0] = "changed"
	again := Load(context.Background(), db)
	if !again.Bool("allowIframe") || again.Doc()["nested"].(map[string]any)["k"] != float64(1) || again.SortingPrefixes()[0] != "the" {
		t.Fatalf("a caller's change leaked into the cache: %v %v", again.Doc(), again.SortingPrefixes())
	}
}

func TestInvalidateIsSeenByTheNextRead(t *testing.T) {
	db := open(t)
	_ = Load(context.Background(), db)
	write(t, db, `{"allowIframe":false}`)
	if Load(context.Background(), db).Bool("allowIframe") {
		t.Fatal("read after Invalidate returned the old document")
	}
}

// TestConcurrentWritesNeverServeOlderThanLastWrite races readers against
// writers; after the final write and Invalidate, every read sees it.
func TestConcurrentWritesNeverServeOlderThanLastWrite(t *testing.T) {
	db := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = Load(context.Background(), db)
			}
		}()
	}
	for v := 0; v < 50; v++ {
		write(t, db, fmt.Sprintf(`{"version":%d}`, v))
	}
	wg.Wait()
	if got := Load(context.Background(), db).Doc()["version"]; got != float64(49) {
		t.Fatalf("after the last write got version %v, want 49", got)
	}
}

func TestForgetDropsTheEntry(t *testing.T) {
	db := open(t)
	_ = Load(context.Background(), db)
	Forget(db)
	if _, ok := entries.Load(db); ok {
		t.Fatal("Forget kept the handle's entry")
	}
}

func TestCacheServesUntilInvalidated(t *testing.T) {
	db := open(t)
	db.Exec(`UPDATE settings SET value='{"sortingPrefixes":["der"],"language":"de"}'`)
	ctx := context.Background()
	if s := Load(ctx, db); !slices.Equal(s.SortingPrefixes(), []string{"der"}) || s.Doc()["language"] != "de" {
		t.Fatalf("first read %v %v", s.Doc(), s.SortingPrefixes())
	}
	db.Exec(`UPDATE settings SET value='{"language":"fr"}'`)
	if s := Load(ctx, db); s.Doc()["language"] != "de" {
		t.Fatalf("expected the cached value, got %v", s.Doc()["language"])
	}
	Invalidate(db)
	if s := Load(ctx, db); s.Doc()["language"] != "fr" || !slices.Equal(s.SortingPrefixes(), []string{"the", "a"}) {
		t.Fatalf("after invalidate %v %v", s.Doc(), s.SortingPrefixes())
	}
	db.Exec(`DELETE FROM settings`)
	Invalidate(db)
	if _, err := Get(ctx, db); err == nil {
		t.Fatal("missing row must be an error")
	}
	if s := Load(ctx, db); len(s.Doc()) != 0 || !slices.Equal(s.SortingPrefixes(), []string{"the", "a"}) {
		t.Fatalf("defaults %v %v", s.Doc(), s.SortingPrefixes())
	}
}
