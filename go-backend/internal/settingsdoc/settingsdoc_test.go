package settingsdoc

import (
	"context"
	"database/sql"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"

	_ "modernc.org/sqlite"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(schemaassets.SchemaSQL); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLoadSaveRoundTripAndDefaults(t *testing.T) {
	db, ctx := newDB(t), context.Background()
	const key = "test-settings"
	defaults := map[string]any{"port": 465}
	got, err := Load(ctx, db, key, defaults)
	if err != nil || got["port"] != 465 {
		t.Fatalf("missing row must give the defaults: %v %v", got, err)
	}
	if err := Save(ctx, db, key, map[string]any{"host": "smtp.test", "port": 25}); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, db, key, map[string]any{"host": "smtp2.test", "port": 26}); err != nil {
		t.Fatal(err)
	}
	got, err = Load(ctx, db, key, defaults)
	if err != nil || got["host"] != "smtp2.test" || got["port"] != float64(26) {
		t.Fatalf("saved document = %v %v", got, err)
	}
	for _, bad := range []string{`[1]`, `"x"`} {
		db.Exec(`UPDATE settings SET value=? WHERE key=?`, bad, key)
		if _, err := Load(ctx, db, key, defaults); err == nil {
			t.Errorf("a row holding %s is not a settings document", bad)
		}
	}
	db.Exec(`UPDATE settings SET value='null' WHERE key=?`, key)
	if got, err := Load(ctx, db, key, defaults); err != nil || got["port"] != 465 {
		t.Fatalf("JSON null gives the defaults: %v %v", got, err)
	}
}
