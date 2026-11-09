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
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT, updatedAt TEXT); INSERT INTO settings VALUES('server-settings','{"allowIframe":true,"sortingPrefixes":["the","a"],"nested":{"k":1}}',NULL)`); err != nil {
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

// A closed handle is forgotten; whatever then opens at the same address must
// read the row afresh instead of inheriting the old document.
func TestForgetDiscardsTheCachedDocument(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	if Load(ctx, db).String("language") != "" {
		t.Fatal("fixture already has a language")
	}
	if _, err := db.Exec(`UPDATE settings SET value='{"language":"fr"}'`); err != nil {
		t.Fatal(err)
	}
	Forget(db)
	if got := Load(ctx, db).String("language"); got != "fr" {
		t.Fatalf("after Forget the row was not read again, language %q", got)
	}
}

// Without a readable row the settings are the defaults (no keys, sorting
// prefixes "the" and "a"), Get reports the error, and a stored document's
// prefixes replace the defaults.
func TestUnreadableRowFallsBackToDefaults(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE settings SET value='{"sortingPrefixes":["der"],"language":"de"}'`); err != nil {
		t.Fatal(err)
	}
	Invalidate(db)
	if s := Load(ctx, db); !slices.Equal(s.SortingPrefixes(), []string{"der"}) || s.String("language") != "de" {
		t.Fatalf("stored document %v %v", s.Doc(), s.SortingPrefixes())
	}
	if _, err := db.Exec(`DELETE FROM settings`); err != nil {
		t.Fatal(err)
	}
	Invalidate(db)
	if _, err := Get(ctx, db); err == nil {
		t.Fatal("missing row must be an error")
	}
	if s := Load(ctx, db); len(s.Doc()) != 0 || !slices.Equal(s.SortingPrefixes(), []string{"the", "a"}) {
		t.Fatalf("defaults %v %v", s.Doc(), s.SortingPrefixes())
	}
}

func TestSaveInvalidates(t *testing.T) {
	db := open(t)
	ctx := context.Background()
	_ = Load(ctx, db)
	if err := Save(ctx, db, map[string]any{"language": "de", "sortingIgnorePrefix": true}); err != nil {
		t.Fatal(err)
	}
	if s := Load(ctx, db); s.String("language") != "de" || !s.IgnorePrefix() {
		t.Fatalf("read after Save: %v", s.Doc())
	}
	if err := Save(ctx, db, map[string]any{"language": "fr", "sortingPrefixes": []string{"der"}}); err != nil {
		t.Fatal(err)
	}
	if s := Load(ctx, db); s.String("language") != "fr" || s.IgnorePrefix() || !slices.Equal(s.SortingPrefixes(), []string{"der"}) {
		t.Fatalf("second Save not seen: %v", s.Doc())
	}
	if _, err := db.Exec(`DELETE FROM settings`); err != nil {
		t.Fatal(err)
	}
	if Save(ctx, db, map[string]any{}) == nil {
		t.Fatal("Save without a row must fail")
	}
}
