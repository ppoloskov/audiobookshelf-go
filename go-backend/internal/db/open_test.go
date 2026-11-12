package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/version"
)

func newPath(t *testing.T) string { return filepath.Join(t.TempDir(), "absdatabase.sqlite") }

func defaultSetting(key, value string) db.Setting {
	return db.Setting{Key: key, Value: db.JSON[json.RawMessage]{V: json.RawMessage(value)}}
}

func TestCreateMakesThePinnedFreshDatabase(t *testing.T) {
	path := newPath(t)
	if err := db.Create(path, []db.Setting{defaultSetting("server-settings", `{"id":"server-settings"}`)}); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("a fresh database must validate: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	var tables int
	if err := d.Get(ctx, &tables, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'"); err != nil || tables != 26 {
		t.Fatalf("tables = %d, %v; want 26", tables, err)
	}
	var meta []db.MigrationsMeta
	if err := d.Select(ctx, &meta, "SELECT * FROM migrationsMeta ORDER BY key"); err != nil {
		t.Fatal(err)
	}
	want := []db.MigrationsMeta{{Key: "maxVersion", Value: version.Server}, {Key: "version", Value: version.Server}}
	if len(meta) != 2 || meta[0] != want[0] || meta[1] != want[1] {
		t.Fatalf("migrationsMeta = %v, want %v", meta, want)
	}
	// The settings row is JSON text with timestamps, never a BLOB.
	var stored struct {
		Value   string `db:"value"`
		Kind    string `db:"kind"`
		Created string `db:"created"`
	}
	if err := d.Get(ctx, &stored, "SELECT value, typeof(value) || typeof(createdAt) AS kind, createdAt AS created FROM settings WHERE key = 'server-settings'"); err != nil {
		t.Fatal(err)
	}
	if stored.Value != `{"id":"server-settings"}` || stored.Kind != "texttext" || stored.Created == "" {
		t.Fatalf("settings row = %+v", stored)
	}
	// The schema equals what dbtest.New builds from the same pinned text.
	var got, pinned []string
	const query = "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY name"
	if err := d.Select(ctx, &got, query); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.New(t).Select(ctx, &pinned, query); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) != len(pinned) {
		t.Fatalf("schema objects %d, want %d", len(got), len(pinned))
	}
	for i := range got {
		if got[i] != pinned[i] {
			t.Fatalf("schema object %d differs:\n%s\n%s", i, got[i], pinned[i])
		}
	}
}

func TestCreateNeverTouchesAnExistingFile(t *testing.T) {
	path := newPath(t)
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(path, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("Create over an existing file = %v, want os.ErrExist", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "not a database" {
		t.Fatal("Create modified an existing file")
	}
}

func TestCreateRemovesTheFileOnFailure(t *testing.T) {
	path := newPath(t)
	dup := []db.Setting{defaultSetting("a", `{}`), defaultSetting("a", `{}`)} // primary key clash
	if err := db.Create(path, dup); err == nil {
		t.Fatal("Create accepted duplicate settings keys")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed Create left the file behind: %v", err)
	}
}

func TestOpenDoesNotCreateMissingDatabase(t *testing.T) {
	path := newPath(t)
	if _, err := db.Open(path); err == nil {
		t.Fatal("Open of a missing file succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Open created the file: %v", err)
	}
}

// createdCopy makes a valid database file that a test may then damage.
func createdCopy(t *testing.T) string {
	t.Helper()
	path := newPath(t)
	if err := db.Create(path, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// exec runs statements on a raw connection to path.
func exec(t *testing.T, path string, statements ...string) {
	t.Helper()
	d, err := db.Connect(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, statement := range statements {
		if _, err := d.Exec(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

// A database from another server version is refused: Go cannot migrate, and
// upstream would have migrated it when it reconnected after a restore.
func TestOpenRefusesOtherMigrationState(t *testing.T) {
	for _, key := range []string{"version", "maxVersion"} {
		path := createdCopy(t)
		exec(t, path, "UPDATE migrationsMeta SET value = '2.36.0' WHERE key = '"+key+"'")
		if _, err := db.Open(path); !errors.Is(err, db.ErrSchema) {
			t.Fatalf("%s 2.36.0: Open = %v, want ErrSchema", key, err)
		}
		d, err := db.Connect(path)
		if err != nil {
			t.Fatalf("Connect does not validate: %v", err)
		}
		d.Close()
	}
}

func TestOpenRefusesAMissingColumnButAcceptsExtraOnes(t *testing.T) {
	path := createdCopy(t)
	exec(t, path, "ALTER TABLE users ADD COLUMN futureColumn TEXT")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("an extra column must be accepted: %v", err)
	}
	d.Close()

	exec(t, path, "ALTER TABLE libraryFolders DROP COLUMN path")
	if _, err := db.Open(path); !errors.Is(err, db.ErrSchema) {
		t.Fatalf("missing column: Open = %v, want ErrSchema", err)
	}
	if _, err := db.Open(path); err == nil || !bytes.Contains([]byte(err.Error()), []byte("libraryFolders is missing column path")) {
		t.Fatalf("the error must name the column: %v", err)
	}
}

func TestOpenRefusesAMissingTable(t *testing.T) {
	path := createdCopy(t)
	exec(t, path, "DROP TABLE mediaItemShares")
	if _, err := db.Open(path); !errors.Is(err, db.ErrSchema) {
		t.Fatalf("missing table: Open = %v, want ErrSchema", err)
	}
}

func TestOpenAcceptsTheUpstreamFixture(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "contracttest", "testdata", "install", "config", "absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	path := newPath(t)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("a database written by upstream must validate: %v", err)
	}
	d.Close()
}

func TestOpenOrCreateConcurrentFirstBoot(t *testing.T) {
	path := newPath(t)
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := db.OpenOrCreate(path, []db.Setting{defaultSetting("server-settings", `{}`)})
			if err == nil {
				err = d.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent OpenOrCreate: %v", err)
		}
	}
}

func TestOpenOrCreateLeavesAnExistingDatabaseAlone(t *testing.T) {
	path := createdCopy(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.OpenOrCreate(path, []db.Setting{defaultSetting("server-settings", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("OpenOrCreate rewrote an existing database")
	}
}
